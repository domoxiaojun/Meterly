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
	"time"
)

// EnrollCode 注册码元数据(不含明文)。
type EnrollCode struct {
	ID         int64  `json:"id"`
	HostName   string `json:"host_name"`
	CreatedAt  int64  `json:"created_at"`
	ExpiresAt  int64  `json:"expires_at"`
	MaxUses    int    `json:"max_uses"`
	Uses       int    `json:"uses"`
	Reissue    bool   `json:"reissue"`
	LastHostID int64  `json:"last_host_id,omitempty"`
	Note       string `json:"note,omitempty"`
	Expired    bool   `json:"expired"`
	Exhausted  bool   `json:"exhausted"`
}

// EnrollOpts 生成注册码的参数。
type EnrollOpts struct {
	TTL     time.Duration // 默认 24h,最长 7 天
	MaxUses int           // 默认 1
	Reissue bool          // 允许给已存在的主机换发 token
	Note    string
}

var (
	ErrEnrollInvalid = errors.New("注册码无效或已过期")
	ErrEnrollExists  = errors.New("主机已注册(如需换发请用 reissue 注册码)")
	ErrEnrollName    = errors.New("主机名无效")
)

// NewEnrollCode 生成明文注册码(前缀 tse_ + 24 字节随机)。
func NewEnrollCode() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "tse_" + base64.RawURLEncoding.EncodeToString(b), nil
}

// HashEnroll 返回注册码的 sha256 十六进制,库里只存这个。
func HashEnroll(code string) string {
	h := sha256.Sum256([]byte(code))
	return hex.EncodeToString(h[:])
}

// CreateEnroll 生成一条短时注册码,明文只返回这一次。
func (s *Store) CreateEnroll(ctx context.Context, hostName string, o EnrollOpts, now time.Time) (string, *EnrollCode, error) {
	if !ValidName(hostName) {
		return "", nil, ErrEnrollName
	}
	if o.TTL == 0 {
		o.TTL = 24 * time.Hour
	}
	if o.TTL > 7*24*time.Hour {
		return "", nil, errors.New("注册码有效期最长 7 天")
	}
	if o.TTL < time.Minute {
		return "", nil, errors.New("注册码有效期至少 1 分钟")
	}
	if o.MaxUses == 0 {
		o.MaxUses = 1
	}
	if o.MaxUses < 1 || o.MaxUses > 100 {
		return "", nil, errors.New("注册码使用次数需在 1~100")
	}
	if len(o.Note) > 200 {
		return "", nil, errors.New("备注过长")
	}
	code, err := NewEnrollCode()
	if err != nil {
		return "", nil, err
	}
	rec := &EnrollCode{
		HostName:  hostName,
		CreatedAt: now.Unix(),
		ExpiresAt: now.Add(o.TTL).Unix(),
		MaxUses:   o.MaxUses,
		Reissue:   o.Reissue,
		Note:      o.Note,
	}
	res, err := s.W.ExecContext(ctx, `INSERT INTO enroll_codes(code_hash,host_name,created_at,expires_at,max_uses,uses,reissue,note)
		VALUES(?,?,?,?,?,0,?,?)`, HashEnroll(code), rec.HostName, rec.CreatedAt, rec.ExpiresAt, rec.MaxUses, boolInt(rec.Reissue), rec.Note)
	if err != nil {
		return "", nil, err
	}
	rec.ID, _ = res.LastInsertId()
	return code, rec, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// RedeemEnroll 校验并核销注册码,返回该主机的明文 token(只此一次)。
// 主机不存在时创建;已存在且注册码未标 reissue 则拒绝(避免踢掉正在跑的 agent)。
func (s *Store) RedeemEnroll(ctx context.Context, code, hostName string, now time.Time) (token string, host *Host, err error) {
	if code == "" {
		return "", nil, ErrEnrollInvalid
	}
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return "", nil, err
	}
	defer tx.Rollback()

	var rec EnrollCode
	var reissue int
	err = tx.QueryRowContext(ctx, `SELECT id,host_name,created_at,expires_at,max_uses,uses,reissue,last_host_id,note
		FROM enroll_codes WHERE code_hash=?`, HashEnroll(code)).
		Scan(&rec.ID, &rec.HostName, &rec.CreatedAt, &rec.ExpiresAt, &rec.MaxUses, &rec.Uses, &reissue, &rec.LastHostID, &rec.Note)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil, ErrEnrollInvalid
	}
	if err != nil {
		return "", nil, err
	}
	rec.Reissue = reissue != 0
	if now.Unix() > rec.ExpiresAt || rec.Uses >= rec.MaxUses {
		return "", nil, ErrEnrollInvalid
	}
	// 调用方可声明主机名,但必须与注册码绑定的名字一致(大小写不敏感比较后仍要求完全相等,避免歧义)。
	if hostName != "" && hostName != rec.HostName {
		return "", nil, fmt.Errorf("注册码绑定主机 %s,与请求的 %s 不符", rec.HostName, hostName)
	}
	if !ValidName(rec.HostName) {
		return "", nil, ErrEnrollName
	}

	var existing Host
	err = tx.QueryRowContext(ctx, `SELECT `+hostCols+` FROM hosts WHERE name=?`, rec.HostName).
		Scan(&existing.ID, &existing.Name, &existing.CreatedAt, &existing.RevokedAt, &existing.TZ, &existing.ResetDay,
			&existing.BillingMode, &existing.BillingIface, &existing.QuotaBytes, &existing.Note, &existing.LastSeen,
			&existing.LastIP, &existing.AgentVersion, &existing.MetaJSON, &existing.SettingsVersion)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", nil, err
	}

	tok, err := NewToken()
	if err != nil {
		return "", nil, err
	}
	if exists {
		if !rec.Reissue {
			return "", nil, ErrEnrollExists
		}
		if _, err := tx.ExecContext(ctx, `UPDATE hosts SET token_hash=?, revoked_at=0 WHERE id=?`, HashToken(tok), existing.ID); err != nil {
			return "", nil, err
		}
		existing.RevokedAt = 0
		host = &existing
	} else {
		o := HostOpts{}
		o.fill()
		res, err := tx.ExecContext(ctx, `INSERT INTO hosts(name,token_hash,created_at,tz,reset_day,billing_mode,billing_iface,quota_bytes,note)
			VALUES(?,?,?,?,?,?,?,?,?)`, rec.HostName, HashToken(tok), now.Unix(), o.TZ, o.ResetDay, o.BillingMode, o.BillingIface, o.QuotaBytes, o.Note)
		if err != nil {
			return "", nil, err
		}
		id, _ := res.LastInsertId()
		host = &Host{ID: id, Name: rec.HostName, CreatedAt: now.Unix(), TZ: o.TZ, ResetDay: o.ResetDay,
			BillingMode: o.BillingMode, BillingIface: o.BillingIface}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE enroll_codes SET uses=uses+1, last_host_id=? WHERE id=?`, host.ID, rec.ID); err != nil {
		return "", nil, err
	}
	return tok, host, tx.Commit()
}

// ListEnroll 列出注册码(无明文)。
func (s *Store) ListEnroll(ctx context.Context, now time.Time) ([]*EnrollCode, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT id,host_name,created_at,expires_at,max_uses,uses,reissue,last_host_id,note
		FROM enroll_codes ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*EnrollCode
	for rows.Next() {
		var c EnrollCode
		var reissue int
		if err := rows.Scan(&c.ID, &c.HostName, &c.CreatedAt, &c.ExpiresAt, &c.MaxUses, &c.Uses, &reissue, &c.LastHostID, &c.Note); err != nil {
			return nil, err
		}
		c.Reissue = reissue != 0
		c.Expired = now.Unix() > c.ExpiresAt
		c.Exhausted = c.Uses >= c.MaxUses
		out = append(out, &c)
	}
	return out, rows.Err()
}

// DeleteEnroll 删除一条注册码(已核销的历史也删,不影响已发出的 host token)。
func (s *Store) DeleteEnroll(ctx context.Context, id int64) error {
	res, err := s.W.ExecContext(ctx, `DELETE FROM enroll_codes WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
