package db

import (
	"database/sql"

	_ "modernc.org/sqlite"
)

func Open(path string) (*sql.DB, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}

	conn.SetMaxOpenConns(8)
	conn.SetMaxIdleConns(4)

	if err := conn.Ping(); err != nil {
		_ = conn.Close()
		return nil, err
	}

	return conn, nil
}
