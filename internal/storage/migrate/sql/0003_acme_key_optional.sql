-- The ACME account key arrives later than the row does.
--
-- External account binding credentials are stored before the first issuance,
-- and the account key is only generated when lego registers with the CA. So a
-- row legitimately exists with no key, and NOT NULL made storing EAB
-- credentials fail outright with a constraint error naming a column the
-- operator has never heard of.
--
-- SQLite cannot drop NOT NULL in place, so the table is rebuilt. Foreign keys
-- are deferred for the swap because nothing references this table; if anything
-- ever does, this needs PRAGMA legacy_alter_table handling.

CREATE TABLE acme_account_rebuilt (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    ca           TEXT    NOT NULL,
    email        TEXT    NOT NULL,

    -- Empty or absent until the account is registered.
    private_key  BLOB,
    registration BLOB,

    eab_key_id   TEXT    NOT NULL DEFAULT '',
    eab_hmac     TEXT    NOT NULL DEFAULT '',

    created_at   INTEGER NOT NULL,

    UNIQUE (ca, email)
);

INSERT INTO acme_account_rebuilt
    (id, ca, email, private_key, registration, eab_key_id, eab_hmac, created_at)
SELECT id, ca, email, private_key, registration, eab_key_id, eab_hmac, created_at
FROM acme_account;

DROP TABLE acme_account;

ALTER TABLE acme_account_rebuilt RENAME TO acme_account;
