-- ACME account keys.
--
-- The account key is what proves ownership to the CA. Regenerating it per
-- issuance would register a fresh account every time, burning the CA's
-- "new account" rate limit and losing the ability to revoke anything issued
-- under the previous one. It is keyed by (ca, email) because that pair is what
-- identifies an account to the directory.

CREATE TABLE acme_account (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    ca           TEXT    NOT NULL,
    email        TEXT    NOT NULL,

    private_key  BLOB    NOT NULL,
    registration BLOB,

    -- External account binding, for CAs that will not issue without it.
    eab_key_id   TEXT    NOT NULL DEFAULT '',
    eab_hmac     TEXT    NOT NULL DEFAULT '',

    created_at   INTEGER NOT NULL,

    UNIQUE (ca, email)
);

-- Which ACME account a renewal should use. Without this a scheduled renewal
-- would have to guess, and guessing wrong means registering yet another
-- account rather than reusing the one that issued the certificate.
ALTER TABLE cert_order ADD COLUMN email TEXT NOT NULL DEFAULT '';

-- Off for a certificate the operator wants to manage by hand.
ALTER TABLE cert_order ADD COLUMN auto_renew INTEGER NOT NULL DEFAULT 1;
