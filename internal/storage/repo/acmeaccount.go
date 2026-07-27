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

// FindEAB returns any stored binding credentials for an authority.
//
// Bindings are stored per account, but they do not belong to one: an EAB pair
// identifies the operator's account *at the CA*, and one operator has one such
// account. Keying the lookup strictly on (ca, email) would make changing the
// contact address look like the credentials had been lost, and send the
// operator back to the CA's dashboard to mint a pair they already have.
//
// The newest is preferred, so re-entering a pair supersedes an older one.
func (r *ACMEAccounts) FindEAB(ctx context.Context, ca string) (keyID, hmac string, err error) {
	row := r.db.QueryRowContext(ctx, `
		SELECT eab_key_id, eab_hmac FROM acme_account
		WHERE ca = ? AND eab_key_id != ''
		ORDER BY id DESC LIMIT 1`, ca)

	switch err := row.Scan(&keyID, &hmac); {
	case errors.Is(err, sql.ErrNoRows):
		return "", "", fmt.Errorf("repo: no account binding for %s: %w", ca, ErrNotFound)
	case err != nil:
		return "", "", fmt.Errorf("repo: find account binding for %s: %w", ca, err)
	}
	return keyID, hmac, nil
}

// Save inserts or updates the account for its (ca, email) pair.
//
// Upsert rather than insert: registration data arrives after the key is
// created, and an issuance that failed part way must not leave a row that
// blocks the retry.
//
// An empty key or registration never overwrites a stored one. Storing EAB
// credentials against an already-registered account passes neither, and
// clobbering the key would lose the ability to revoke every certificate issued
// under it — silently, since the next issuance would simply register again.
func (r *ACMEAccounts) Save(ctx context.Context, account ACMEAccount) (ACMEAccount, error) {
	row := r.db.QueryRowContext(ctx, `
		INSERT INTO acme_account
			(ca, email, private_key, registration, eab_key_id, eab_hmac, created_at)
		VALUES (?, ?, ?, ?, ?, ?, unixepoch())
		ON CONFLICT (ca, email) DO UPDATE SET
			private_key = CASE
				WHEN length(coalesce(excluded.private_key, '')) > 0
				THEN excluded.private_key ELSE acme_account.private_key END,
			registration = CASE
				WHEN length(coalesce(excluded.registration, '')) > 0
				THEN excluded.registration ELSE acme_account.registration END,
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
