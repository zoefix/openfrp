package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zoefix/openfrp/internal/storage/migrate"
)

var ErrUnsupported = errors.New(
	"storage: SQLite is not available on this architecture, so DNS and " +
		"certificate management are disabled")

const DefaultPath = "/etc/openfrp/openfrp.db"

type DB struct {
	*sql.DB
	path string
}

func Open(path string) (*DB, error) {
	if !driverAvailable {
		return nil, ErrUnsupported
	}

	if path == "" {
		path = DefaultPath
	}

	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("storage: create %s: %w", dir, err)
		}
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, fmt.Errorf("storage: create %s: %w", path, err)
		}
		file.Close()
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("storage: secure %s: %w", path, err)
	}

	handle, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, fmt.Errorf("storage: open %s: %w", path, err)
	}

	handle.SetMaxOpenConns(1)

	if err := handle.Ping(); err != nil {
		handle.Close()
		return nil, fmt.Errorf("storage: open %s: %w", path, err)
	}

	if err := migrate.Run(handle); err != nil {
		handle.Close()
		return nil, err
	}

	return &DB{DB: handle, path: path}, nil
}

func dsn(path string) string {
	return path +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(ON)" +
		"&_pragma=synchronous(NORMAL)"
}

func (db *DB) Path() string { return db.path }
