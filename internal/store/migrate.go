package store

import (
	"database/sql"
	"fmt"
	"strconv"
)

// CurrentSchema 当前库版本。M1/M2 库没有这个键,打开时会升到 3。
const CurrentSchema = 3

// migrate 在已有库上安全补齐新表/索引,并记录 schema_version。可重复执行。
func migrate(w *sql.DB) error {
	// Schema 常量里已有 CREATE IF NOT EXISTS;这里再跑一遍以防旧二进制写成的库缺表。
	if _, err := w.Exec(Schema); err != nil {
		return err
	}
	var ver int
	var s string
	err := w.QueryRow(`SELECT v FROM settings WHERE k='schema_version'`).Scan(&s)
	if err == sql.ErrNoRows {
		ver = 0
	} else if err != nil {
		return err
	} else {
		ver, _ = strconv.Atoi(s)
	}
	if ver < CurrentSchema {
		if _, err := w.Exec(`INSERT INTO settings(k,v) VALUES('schema_version',?)
			ON CONFLICT(k) DO UPDATE SET v=excluded.v`, strconv.Itoa(CurrentSchema)); err != nil {
			return fmt.Errorf("写入 schema_version: %w", err)
		}
	}
	return nil
}

// SchemaVersion 返回库中记录的版本号(测试用)。
func (s *Store) SchemaVersion() (int, error) {
	var v string
	err := s.R.QueryRow(`SELECT v FROM settings WHERE k='schema_version'`).Scan(&v)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(v)
	return n, err
}
