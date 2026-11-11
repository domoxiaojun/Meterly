package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/domoxiaojun/Meterly/internal/proto"
	"time"
)

var ErrConflict = errors.New("配置已被其他操作更新，请刷新后重试")

func (s *Store) HostConfig(ctx context.Context, id int64) (proto.DesiredConfig, error) {
	c := proto.DesiredConfig{}
	var raw string
	e := s.R.QueryRowContext(ctx, `SELECT version,policy FROM host_config WHERE host_id=?`, id).Scan(&c.Version, &raw)
	if errors.Is(e, sql.ErrNoRows) {
		return c, nil
	}
	if e != nil {
		return c, e
	}
	e = json.Unmarshal([]byte(raw), &c.Policy)
	return c, e
}
func (s *Store) SetHostConfig(ctx context.Context, id, expected int64, p proto.Policy) (proto.DesiredConfig, error) {
	out := proto.DesiredConfig{Version: expected + 1, Policy: p}
	if e := p.Validate(); e != nil {
		return out, e
	}
	raw, e := json.Marshal(p)
	if e != nil {
		return out, e
	}
	tx, e := s.W.BeginTx(ctx, nil)
	if e != nil {
		return out, e
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `INSERT OR IGNORE INTO host_config(host_id) VALUES(?)`, id); e != nil {
		return out, e
	}
	r, e := tx.ExecContext(ctx, `UPDATE host_config SET version=?,policy=? WHERE host_id=? AND version=?`, out.Version, string(raw), id, expected)
	if e != nil {
		return out, e
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return out, ErrConflict
	}
	return out, tx.Commit()
}
func (s *Store) CreateUpgrade(ctx context.Context, id int64, u proto.UpgradeTask) (int64, error) {
	r, e := s.W.ExecContext(ctx, `INSERT INTO upgrade_tasks(host_id,version,url,sha256,status,created_at,updated_at) VALUES(?,?,?,?,'pending',?,?)`, id, u.Version, u.URL, u.SHA256, u.CreatedAt, u.CreatedAt)
	if e != nil {
		return 0, e
	}
	return r.LastInsertId()
}
func (s *Store) NextUpgrade(ctx context.Context, id int64) (*proto.UpgradeTask, error) {
	var u proto.UpgradeTask
	e := s.R.QueryRowContext(ctx, `SELECT id,version,url,sha256,status,message,created_at FROM upgrade_tasks WHERE host_id=? AND status IN ('pending','running') ORDER BY id LIMIT 1`, id).Scan(&u.ID, &u.Version, &u.URL, &u.SHA256, &u.Status, &u.Message, &u.CreatedAt)
	if errors.Is(e, sql.ErrNoRows) {
		return nil, nil
	}
	return &u, e
}
func (s *Store) UpgradeStatus(ctx context.Context, host, id int64, status, msg string) error {
	switch status {
	case "running", "succeeded", "failed", "rolled_back":
	default:
		return errors.New("无效任务状态")
	}
	r, e := s.W.ExecContext(ctx, `UPDATE upgrade_tasks SET status=?,message=?,updated_at=? WHERE host_id=? AND id=? AND status IN ('pending','running')`, status, msg, time.Now().Unix(), host, id)
	if e != nil {
		return e
	}
	n, _ := r.RowsAffected()
	if n == 0 {
		var current string
		if e := s.R.QueryRowContext(ctx, "SELECT status FROM upgrade_tasks WHERE host_id=? AND id=?", host, id).Scan(&current); e == nil && current == status {
			return nil
		}
		return ErrConflict
	}
	return nil
}
