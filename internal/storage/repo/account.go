// Package repo holds one repository per aggregate. SQL lives here and nowhere
// else: domain packages take a narrow interface and never see a query.
package repo

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// Account is a set of credentials for one DNS provider.
type Account struct {
	ID       int64
	Name     string
	Provider string

	// Credentials is provider-specific, shaped by that provider's schema.Form.
	Credentials map[string]string

	CreatedAt int64
	UpdatedAt int64
}

// Accounts stores DNS provider credentials.
//
// This is the one place cloud access keys are read or written, which is the
// point: there is a single file to audit when the question is "where can an
// AK/SK escape from".
type Accounts struct {
	db *sql.DB
}

// NewAccounts returns a repository over db.
func NewAccounts(db *sql.DB) *Accounts { return &Accounts{db: db} }

// List returns every account, credentials included.
//
// Callers that render an account to a user must redact first — see
// schema.Form.Redact. This deliberately does not redact on their behalf,
// because the renewal scheduler needs the real values and a repository that
// sometimes lies about its contents is worse than one that never does.
func (r *Accounts) List(ctx context.Context) ([]Account, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name, provider, credentials, created_at, updated_at
		FROM dns_account
		ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("repo: list accounts: %w", err)
	}
	defer rows.Close()

	var out []Account
	for rows.Next() {
		account, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, account)
	}
	return out, rows.Err()
}

// Get returns one account by id.
func (r *Accounts) Get(ctx context.Context, id int64) (Account, error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT id, name, provider, credentials, created_at, updated_at
		FROM dns_account WHERE id = ?`, id)

	account, err := scanAccount(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, fmt.Errorf("repo: account %d: %w", id, ErrNotFound)
	}
	return account, err
}

// Create stores a new account and returns it with its assigned id.
func (r *Accounts) Create(ctx context.Context, account Account) (Account, error) {
	credentials, err := json.Marshal(account.Credentials)
	if err != nil {
		return Account{}, fmt.Errorf("repo: encode credentials: %w", err)
	}

	row := r.db.QueryRowContext(ctx, `
		INSERT INTO dns_account (name, provider, credentials, created_at, updated_at)
		VALUES (?, ?, ?, unixepoch(), unixepoch())
		RETURNING id, created_at, updated_at`,
		account.Name, account.Provider, string(credentials))

	if err := row.Scan(&account.ID, &account.CreatedAt, &account.UpdatedAt); err != nil {
		return Account{}, fmt.Errorf("repo: create account %q: %w", account.Name, err)
	}
	return account, nil
}

// Update replaces an account's name, provider and credentials.
//
// A secret the operator left blank keeps its stored value: the edit form shows
// a placeholder rather than the real key, so submitting the form unchanged
// must not overwrite a working credential with an empty string. Merging is the
// caller's job, since only they know which fields are secret.
func (r *Accounts) Update(ctx context.Context, account Account) error {
	credentials, err := json.Marshal(account.Credentials)
	if err != nil {
		return fmt.Errorf("repo: encode credentials: %w", err)
	}

	result, err := r.db.ExecContext(ctx, `
		UPDATE dns_account
		SET name = ?, provider = ?, credentials = ?, updated_at = unixepoch()
		WHERE id = ?`,
		account.Name, account.Provider, string(credentials), account.ID)
	if err != nil {
		return fmt.Errorf("repo: update account %d: %w", account.ID, err)
	}

	return affectedOne(result, "account", account.ID)
}

// Delete removes an account. Certificate orders that referenced it survive
// with a null account, so a mis-click cannot take a live certificate with it.
func (r *Accounts) Delete(ctx context.Context, id int64) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM dns_account WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("repo: delete account %d: %w", id, err)
	}
	return affectedOne(result, "account", id)
}

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanAccount(src scanner) (Account, error) {
	var (
		account     Account
		credentials string
	)

	if err := src.Scan(&account.ID, &account.Name, &account.Provider,
		&credentials, &account.CreatedAt, &account.UpdatedAt); err != nil {
		return Account{}, err
	}

	if err := json.Unmarshal([]byte(credentials), &account.Credentials); err != nil {
		return Account{}, fmt.Errorf("repo: account %d has unreadable credentials: %w",
			account.ID, err)
	}
	if account.Credentials == nil {
		account.Credentials = map[string]string{}
	}

	return account, nil
}

// affectedOne turns "updated nothing" into ErrNotFound. Without it an update
// against a deleted row reports success.
func affectedOne(result sql.Result, kind string, id int64) error {
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return fmt.Errorf("repo: %s %d: %w", kind, id, ErrNotFound)
	}
	return nil
}
