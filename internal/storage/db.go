// Package storage owns the SQLite database and its lifecycle.
//
// The split against UCI is deliberate. UCI holds what an operator edits by
// hand, wants visible to the uci command line, and expects to survive a
// firmware backup: the service switch, the server address, the tunnels. This
// holds what is structured, grows without bound, and needs querying: DNS
// provider credentials, certificate orders, issuance history.
//
// The driver is modernc.org/sqlite, and that is a constraint rather than a
// preference. It is SQLite transpiled to Go, so it needs no CGO. The common
// alternative, mattn/go-sqlite3, is a CGO binding and would require a C
// cross-toolchain per target architecture — which would destroy the property
// the whole project rests on, that one CGO_ENABLED=0 binary runs on every
// Linux distribution and every OpenWrt arch.
package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zoefix/openfrp/internal/storage/migrate"
)

// ErrUnsupported is returned on architectures with no SQLite driver. Callers
// should present it as "this feature is unavailable here" rather than as a
// failure, and keep going: the tunnel does not need storage.
var ErrUnsupported = errors.New(
	"storage: SQLite is not available on this architecture, so DNS and " +
		"certificate management are disabled")

// DefaultPath is where the database lives on a router.
const DefaultPath = "/etc/openfrp/openfrp.db"

// DB is an open database, migrated to the current schema.
type DB struct {
	*sql.DB
	path string
}

// Open opens or creates the database and brings its schema up to date.
//
// The file is created 0600 and re-checked on every open: it holds cloud
// provider access keys and certificate private keys, which is the highest
// value data this project stores. A permissive mode left behind by an older
// version, a restore, or a careless chmod is corrected rather than tolerated.
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

	// Create it ourselves rather than letting the driver do it, so the mode is
	// right from the first byte and there is no window in which the file
	// exists world-readable.
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

	// One writer at a time. SQLite serialises writes anyway, and letting the
	// pool open several connections only converts that into SQLITE_BUSY.
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

// dsn builds the connection string.
//
// WAL matters here: the renewal scheduler writes in the background while the
// LuCI pages read, and without it a write would block every read for its
// duration. busy_timeout turns the residual contention into a wait rather than
// an immediate SQLITE_BUSY error, and foreign_keys is off by default in
// SQLite, so a cascade would silently not happen.
func dsn(path string) string {
	return path +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=foreign_keys(ON)" +
		"&_pragma=synchronous(NORMAL)"
}

// Path reports the file backing this database.
func (db *DB) Path() string { return db.path }
