// Package migrate brings a database up to the current schema.
//
// Migrations are .sql files embedded in the binary and applied in filename
// order, so upgrading is something the daemon does to itself at startup. There
// is no external migration tool to install on a router, and no step an
// operator can forget.
package migrate

import (
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

//go:embed sql/*.sql
var files embed.FS

// Run applies every migration the database has not seen.
func Run(db *sql.DB) error {
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS schema_version (
			version    INTEGER PRIMARY KEY,
			applied_at INTEGER NOT NULL
		)`); err != nil {
		return fmt.Errorf("migrate: create version table: %w", err)
	}

	applied, err := appliedVersions(db)
	if err != nil {
		return err
	}

	pending, err := load()
	if err != nil {
		return err
	}

	for _, m := range pending {
		if applied[m.version] {
			continue
		}
		if err := apply(db, m); err != nil {
			return err
		}
	}

	return nil
}

type migration struct {
	version int
	name    string
	body    string
}

// load reads the embedded migrations, sorted by version.
func load() ([]migration, error) {
	entries, err := fs.ReadDir(files, "sql")
	if err != nil {
		return nil, fmt.Errorf("migrate: read embedded migrations: %w", err)
	}

	var out []migration
	for _, entry := range entries {
		name := entry.Name()

		// Files are named <version>_<description>.sql. The version is what
		// orders them and what is recorded, so a mis-named file is a hard
		// error rather than a migration that silently never runs.
		digits, _, found := strings.Cut(name, "_")
		if !found {
			return nil, fmt.Errorf("migrate: %s is not named <version>_<name>.sql", name)
		}
		version, err := strconv.Atoi(digits)
		if err != nil {
			return nil, fmt.Errorf("migrate: %s has no numeric version: %w", name, err)
		}

		body, err := files.ReadFile("sql/" + name)
		if err != nil {
			return nil, err
		}

		out = append(out, migration{version: version, name: name, body: string(body)})
	}

	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })

	for i := 1; i < len(out); i++ {
		if out[i].version == out[i-1].version {
			return nil, fmt.Errorf("migrate: %s and %s share version %d",
				out[i-1].name, out[i].name, out[i].version)
		}
	}

	return out, nil
}

func appliedVersions(db *sql.DB) (map[int]bool, error) {
	rows, err := db.Query(`SELECT version FROM schema_version`)
	if err != nil {
		return nil, fmt.Errorf("migrate: read applied versions: %w", err)
	}
	defer rows.Close()

	applied := map[int]bool{}
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return nil, err
		}
		applied[version] = true
	}
	return applied, rows.Err()
}

// apply runs one migration and records it in the same transaction, so a
// failure part way through leaves neither the change nor the version behind.
func apply(db *sql.DB, m migration) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("migrate: begin %s: %w", m.name, err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(m.body); err != nil {
		return fmt.Errorf("migrate: apply %s: %w", m.name, err)
	}

	if _, err := tx.Exec(
		`INSERT INTO schema_version (version, applied_at) VALUES (?, unixepoch())`,
		m.version,
	); err != nil {
		return fmt.Errorf("migrate: record %s: %w", m.name, err)
	}

	return tx.Commit()
}
