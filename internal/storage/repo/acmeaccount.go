package repo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ACMEAccount is a registration with one certificate authority.
type ACMEAccount struct {
	ID    int64
	CA    string
	Email string

	PrivateKey   []byte
	Registration []byte

	EABKeyID string
	EABHMAC  string

	CreatedAt int64
}

// ACMEAccounts stores ACME account keys.
type ACMEAccounts struct {
	db *sql.DB
}

// NewACMEAccounts returns a repository over db.
func NewACMEAccounts(db *sql.DB) *ACMEAccounts { return &ACMEAccounts{db: db} }

const acmeColumns = `id, ca, email, private_key, registration, eab_key_id, eab_hmac, created_at`

// Find returns the account for a CA and email, if one exists.
func (r *ACMEAccounts) Find(ctx context.Context, ca, email string) (ACMEAccount, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+acmeColumns+` FROM acme_account WHERE ca = ? AND email = ?`, ca, email)

	account, err := scanACME(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ACMEAccount{}, fmt.Errorf("repo: acme account for %s/%s: %w", ca, email, ErrNotFound)
	}
	return account, err
}

// Save inserts or updates the account for its (ca, email) pair.
//
// Upsert rather than insert: registration data arrives after the key is
// created, and an issuance that failed part way must not leave a row that
// blocks the retry.
func (r *ACMEAccounts) Save(ctx context.Context, account ACMEAccount) (ACMEAccount, error) {
	row := r.db.QueryRowContext(ctx, `
		INSERT INTO acme_account
			(ca, email, private_key, registration, eab_key_id, eab_hmac, created_at)
		VALUES (?, ?, ?, ?, ?, ?, unixepoch())
		ON CONFLICT (ca, email) DO UPDATE SET
			private_key  = excluded.private_key,
			registration = excluded.registration,
			eab_key_id   = excluded.eab_key_id,
			eab_hmac     = excluded.eab_hmac
		RETURNING id, created_at`,
		account.CA, account.Email, account.PrivateKey, account.Registration,
		account.EABKeyID, account.EABHMAC)

	if err := row.Scan(&account.ID, &account.CreatedAt); err != nil {
		return ACMEAccount{}, fmt.Errorf("repo: save acme account: %w", err)
	}
	return account, nil
}

func scanACME(src scanner) (ACMEAccount, error) {
	var (
		account      ACMEAccount
		registration []byte
	)

	if err := src.Scan(&account.ID, &account.CA, &account.Email,
		&account.PrivateKey, &registration,
		&account.EABKeyID, &account.EABHMAC, &account.CreatedAt); err != nil {
		return ACMEAccount{}, err
	}

	account.Registration = registration
	return account, nil
}
