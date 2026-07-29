package repo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

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

type ACMEAccounts struct {
	db *sql.DB
}

func NewACMEAccounts(db *sql.DB) *ACMEAccounts { return &ACMEAccounts{db: db} }

const acmeColumns = `id, ca, email, private_key, registration, eab_key_id, eab_hmac, created_at`

func (r *ACMEAccounts) Find(ctx context.Context, ca, email string) (ACMEAccount, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+acmeColumns+` FROM acme_account WHERE ca = ? AND email = ?`, ca, email)

	account, err := scanACME(row)
	if errors.Is(err, sql.ErrNoRows) {
		return ACMEAccount{}, fmt.Errorf("repo: acme account for %s/%s: %w", ca, email, ErrNotFound)
	}
	return account, err
}

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
