// Package store 中心存储(SQLite WAL,纯 Go 驱动)。只做简单分层,不做多数据库抽象。
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Schema 统一事实表 + 小时/天聚合表;新增采集项只是新 kind,不改库。
const Schema = `
CREATE TABLE IF NOT EXISTS hosts(
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  name          TEXT NOT NULL UNIQUE,
  token_hash    TEXT NOT NULL UNIQUE,          -- sha256(token) 十六进制;明文 token 不入库
  created_at    INTEGER NOT NULL,
  revoked_at    INTEGER NOT NULL DEFAULT 0,    -- >0 表示已吊销
  tz            TEXT NOT NULL DEFAULT 'Asia/Singapore',
  reset_day     INTEGER NOT NULL DEFAULT 9,    -- 计费周期重置日
  billing_mode  TEXT NOT NULL DEFAULT 'both',  -- both|tx|rx|max
  billing_iface TEXT NOT NULL DEFAULT '',
  settings_version INTEGER NOT NULL DEFAULT 0,
  quota_bytes   INTEGER NOT NULL DEFAULT 0,    -- 0 = 未设置
  note          TEXT NOT NULL DEFAULT '',
  last_seen     INTEGER NOT NULL DEFAULT 0,
  last_ip       TEXT NOT NULL DEFAULT '',
  agent_version TEXT NOT NULL DEFAULT '',
  meta_json     TEXT NOT NULL DEFAULT '{}'
);
-- 幂等:每 (host, agent 实例) 的 seq 高水位
CREATE TABLE IF NOT EXISTS ingest_hwm(
  host_id  INTEGER NOT NULL,
  instance TEXT NOT NULL,
  seq      INTEGER NOT NULL,
  ts       INTEGER NOT NULL,
  updated  INTEGER NOT NULL,
  PRIMARY KEY(host_id, instance)
) WITHOUT ROWID;
-- 事实表:ts = 桶起点(UTC epoch 秒)。分钟表保留 N 天;小时表保留 ~1 年;天表长期(ts = host 时区当日 00:00)。
CREATE TABLE IF NOT EXISTS fact_minute(
  host_id INTEGER NOT NULL, ts INTEGER NOT NULL, kind TEXT NOT NULL, dim1 TEXT NOT NULL, dim2 TEXT NOT NULL,
  rx INTEGER NOT NULL, tx INTEGER NOT NULL, PRIMARY KEY(host_id, ts, kind, dim1, dim2)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS fact_hour(
  host_id INTEGER NOT NULL, ts INTEGER NOT NULL, kind TEXT NOT NULL, dim1 TEXT NOT NULL, dim2 TEXT NOT NULL,
  rx INTEGER NOT NULL, tx INTEGER NOT NULL, PRIMARY KEY(host_id, ts, kind, dim1, dim2)) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS fact_day(
  host_id INTEGER NOT NULL, ts INTEGER NOT NULL, kind TEXT NOT NULL, dim1 TEXT NOT NULL, dim2 TEXT NOT NULL,
  rx INTEGER NOT NULL, tx INTEGER NOT NULL, PRIMARY KEY(host_id, ts, kind, dim1, dim2)) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS fact_hour_kind ON fact_hour(kind, host_id, ts);
CREATE INDEX IF NOT EXISTS fact_day_kind  ON fact_day(kind, host_id, ts);
CREATE TABLE IF NOT EXISTS settings(k TEXT PRIMARY KEY, v TEXT NOT NULL);
-- 服务商后台显示的用量(手工录入),每 host 每计费周期一条;cycle_start = 周期起始本地日期 YYYY-MM-DD
CREATE TABLE IF NOT EXISTS provider_readings(
  host_id     INTEGER NOT NULL,
  cycle_start TEXT NOT NULL,
  bytes       INTEGER NOT NULL,
  note        TEXT NOT NULL DEFAULT '',
  updated_at  INTEGER NOT NULL,
  PRIMARY KEY(host_id, cycle_start)) WITHOUT ROWID;
-- M3:一次性注册码(只存哈希)。既有手工 token 主机不受影响。
CREATE TABLE IF NOT EXISTS enroll_codes(
  id           INTEGER PRIMARY KEY AUTOINCREMENT,
  code_hash    TEXT NOT NULL UNIQUE,
  host_name    TEXT NOT NULL,
  created_at   INTEGER NOT NULL,
  expires_at   INTEGER NOT NULL,
  max_uses     INTEGER NOT NULL DEFAULT 1,
  uses         INTEGER NOT NULL DEFAULT 0,
  reissue      INTEGER NOT NULL DEFAULT 0,
  last_host_id INTEGER NOT NULL DEFAULT 0,
  note         TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS enroll_host ON enroll_codes(host_name);
`

type Store struct {
	W         *sql.DB // 写连接(单连接串行化)
	R         *sql.DB // 读连接池
	Retention Retention
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	// Inspect existing files read-only before any WAL or schema write.
	if fi, err := os.Stat(path); err == nil && fi.Size() > 0 {
		ro, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
		if err != nil {
			return nil, err
		}
		err = checkSchema(ro)
		ro.Close()
		if err != nil {
			return nil, err
		}
	}
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)"
	w, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)
	if err := checkSchema(w); err != nil {
		w.Close()
		return nil, fmt.Errorf("初始化数据库失败: %w", err)
	}
	if err := migrate(w); err != nil {
		w.Close()
		return nil, fmt.Errorf("数据库迁移失败: %w", err)
	}
	_ = os.Chmod(path, 0o600)
	r, err := sql.Open("sqlite", dsn+"&_pragma=query_only(1)")
	if err != nil {
		w.Close()
		return nil, err
	}
	r.SetMaxOpenConns(8)
	ret := Retention{}
	ret.Defaults()
	return &Store{W: w, R: r, Retention: ret}, nil
}

func (s *Store) Close() error {
	s.R.Close()
	return s.W.Close()
}

// ---- hosts / token ----

type Host struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	CreatedAt       int64  `json:"created_at"`
	RevokedAt       int64  `json:"revoked_at"`
	TZ              string `json:"tz"`
	ResetDay        int    `json:"reset_day"`
	BillingMode     string `json:"billing_mode"`
	BillingIface    string `json:"billing_iface"`
	QuotaBytes      int64  `json:"quota_bytes"`
	Note            string `json:"note"`
	LastSeen        int64  `json:"last_seen"`
	LastIP          string `json:"-"`
	AgentVersion    string `json:"agent_version"`
	MetaJSON        string `json:"-"`
	SettingsVersion int64  `json:"settings_version"`
}

const hostCols = `id,name,created_at,revoked_at,tz,reset_day,billing_mode,billing_iface,quota_bytes,note,last_seen,last_ip,agent_version,meta_json,settings_version`

func scanHost(sc interface{ Scan(...any) error }) (*Host, error) {
	h := &Host{}
	err := sc.Scan(&h.ID, &h.Name, &h.CreatedAt, &h.RevokedAt, &h.TZ, &h.ResetDay, &h.BillingMode, &h.BillingIface,
		&h.QuotaBytes, &h.Note, &h.LastSeen, &h.LastIP, &h.AgentVersion, &h.MetaJSON, &h.SettingsVersion)
	return h, err
}

var ErrNotFound = errors.New("未找到")

// NewToken 生成 host token(前缀 tsa_ + 32 字节随机 base64url)。
func NewToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "tsa_" + base64.RawURLEncoding.EncodeToString(b), nil
}

// HashToken 返回 token 的 sha256 十六进制,库里只存这个。
func HashToken(t string) string {
	h := sha256.Sum256([]byte(t))
	return hex.EncodeToString(h[:])
}

type HostOpts struct {
	TZ           string
	ResetDay     int
	BillingMode  string
	BillingIface string
	QuotaBytes   int64
	Note         string
}

func (o *HostOpts) Validate() error {
	if o.TZ != "" {
		if _, err := time.LoadLocation(o.TZ); err != nil {
			return fmt.Errorf("时区无效: %s", o.TZ)
		}
	}
	if o.ResetDay != 0 && (o.ResetDay < 1 || o.ResetDay > 31) {
		return errors.New("重置日需在 1~31")
	}
	switch o.BillingMode {
	case "", "both", "tx", "rx", "max":
	default:
		return errors.New("计费口径只能是 both|tx|rx|max")
	}
	if o.QuotaBytes < 0 {
		return errors.New("配额不能为负")
	}
	return nil
}

func ValidName(n string) bool {
	if n == "" || len(n) > 64 {
		return false
	}
	for _, r := range n {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}

// CreateHost 新建主机并返回明文 token(只此一次)。
func (s *Store) CreateHost(ctx context.Context, name string, o HostOpts) (string, error) {
	if !ValidName(name) {
		return "", errors.New("主机名只能包含字母数字 - _ .,最长 64")
	}
	if err := o.Validate(); err != nil {
		return "", err
	}
	tok, err := NewToken()
	if err != nil {
		return "", err
	}
	o.fill()
	_, err = s.W.ExecContext(ctx, `INSERT INTO hosts(name,token_hash,created_at,tz,reset_day,billing_mode,billing_iface,quota_bytes,note)
		VALUES(?,?,?,?,?,?,?,?,?)`, name, HashToken(tok), time.Now().Unix(), o.TZ, o.ResetDay, o.BillingMode, o.BillingIface, o.QuotaBytes, o.Note)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return "", errors.New("主机已存在(如需新 token 请用 rotate)")
		}
		return "", err
	}
	return tok, nil
}

func (o *HostOpts) fill() {
	if o.TZ == "" {
		o.TZ = "Asia/Singapore"
	}
	if o.ResetDay == 0 {
		o.ResetDay = 9
	}
	if o.BillingMode == "" {
		o.BillingMode = "both"
	}

}

// RotateToken 生成新 token(旧 token 立即失效),同时解除吊销。
func (s *Store) RotateToken(ctx context.Context, name string) (string, error) {
	tok, err := NewToken()
	if err != nil {
		return "", err
	}
	res, err := s.W.ExecContext(ctx, `UPDATE hosts SET token_hash=?, revoked_at=0 WHERE name=?`, HashToken(tok), name)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", ErrNotFound
	}
	return tok, nil
}

// RevokeHost 吊销主机 token(历史数据保留)。
func (s *Store) RevokeHost(ctx context.Context, name string) error {
	res, err := s.W.ExecContext(ctx, `UPDATE hosts SET revoked_at=? WHERE name=? AND revoked_at=0`, time.Now().Unix(), name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UpdateHost(ctx context.Context, name string, o HostOpts) error {
	if err := o.Validate(); err != nil {
		return err
	}
	cur, err := s.HostByName(ctx, name)
	if err != nil {
		return err
	}
	if o.TZ == "" {
		o.TZ = cur.TZ
	}
	if o.ResetDay == 0 {
		o.ResetDay = cur.ResetDay
	}
	if o.BillingMode == "" {
		o.BillingMode = cur.BillingMode
	}
	if o.BillingIface == "" {
		o.BillingIface = cur.BillingIface
	}
	if o.QuotaBytes == 0 {
		o.QuotaBytes = cur.QuotaBytes
	}
	if o.Note == "" {
		o.Note = cur.Note
	}
	_, err = s.W.ExecContext(ctx, `UPDATE hosts SET tz=?,reset_day=?,billing_mode=?,billing_iface=?,quota_bytes=?,note=? WHERE id=?`,
		o.TZ, o.ResetDay, o.BillingMode, o.BillingIface, o.QuotaBytes, o.Note, cur.ID)
	return err
}

func (s *Store) HostByName(ctx context.Context, name string) (*Host, error) {
	h, err := scanHost(s.R.QueryRowContext(ctx, `SELECT `+hostCols+` FROM hosts WHERE name=?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return h, err
}

// HostByToken 按 token 哈希查找未吊销主机。
func (s *Store) HostByToken(ctx context.Context, token string) (*Host, error) {
	h, err := scanHost(s.R.QueryRowContext(ctx, `SELECT `+hostCols+` FROM hosts WHERE token_hash=? AND revoked_at=0`, HashToken(token)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return h, err
}

func (s *Store) Hosts(ctx context.Context) ([]*Host, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT `+hostCols+` FROM hosts ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Host
	for rows.Next() {
		h, err := scanHost(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}
