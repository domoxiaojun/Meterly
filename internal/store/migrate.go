package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
)

const CurrentSchema = 4

// Check before any DDL: old databases are never changed or silently migrated.
func checkSchema(w *sql.DB) error {
	var n int
	if err := w.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'`).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	var v string
	if err := w.QueryRow(`SELECT v FROM settings WHERE k='schema_version'`).Scan(&v); err != nil || v != strconv.Itoa(CurrentSchema) {
		return errors.New("数据库格式不兼容：请先归档旧库，配置一个新的 db_path，并成套升级中心与 agent；不会自动迁移或删除旧数据")
	}
	return nil
}
func migrate(w *sql.DB) error {
	if _, err := w.Exec(Schema + AnalyticsSchema + ControlSchema); err != nil {
		return err
	}
	_, err := w.Exec(`INSERT INTO settings(k,v) VALUES('schema_version',?) ON CONFLICT(k) DO NOTHING`, strconv.Itoa(CurrentSchema))
	if err != nil {
		return fmt.Errorf("写入 schema_version: %w", err)
	}
	return nil
}
func (s *Store) SchemaVersion() (int, error) {
	var v string
	err := s.R.QueryRow(`SELECT v FROM settings WHERE k='schema_version'`).Scan(&v)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(v)
}
