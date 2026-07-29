package repo

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

var ErrNotFound = errors.New("not found")

type Account struct {
	ID       int64
	Name     string
	Provider string

	Credentials map[string]string

	CreatedAt int64
	UpdatedAt int64
}

type Accounts struct {
	db *sql.DB
}

func NewAccounts(db *sql.DB) *Accounts { return &Accounts{db: db} }

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

func (r *Accounts) Delete(ctx context.Context, id int64) error {
	result, err := r.db.ExecContext(ctx, `DELETE FROM dns_account WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("repo: delete account %d: %w", id, err)
	}
	return affectedOne(result, "account", id)
}

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
