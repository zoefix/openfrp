-- Initial schema: DNS provider accounts and certificate orders.
--
-- Times are unix seconds rather than SQLite datetime strings, so comparisons
-- are integer comparisons and no timezone ever enters the picture. The
-- renewal scheduler asks "what expires before now + 7 days" on every tick.

CREATE TABLE dns_account (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,

    -- Operator-chosen label. Unique so the UI can refer to an account by a
    -- name a human picked rather than an id they never see.
    name        TEXT    NOT NULL UNIQUE,

    -- Registry key of the provider implementation: aliyun, cloudflare, ...
    provider    TEXT    NOT NULL,

    -- Provider-specific credentials as a JSON object, shaped by that
    -- provider's schema.Form. Kept opaque here on purpose: adding a provider
    -- must not require a migration.
    --
    -- This column is the most sensitive data in the project. It is protected
    -- by the database file being 0600, and nothing else — anyone with root on
    -- the router can read it, which is stated plainly in the UI.
    credentials TEXT    NOT NULL,

    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

CREATE TABLE cert_order (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,

    -- JSON array of the names the certificate covers. A wildcard entry
    -- forces DNS-01, which is why an order is tied to a DNS account.
    domains     TEXT    NOT NULL,

    -- rsa2048 | rsa3072 | ec256 | ec384
    key_type    TEXT    NOT NULL,

    -- ACME directory URL, so a custom CA needs no schema change.
    ca          TEXT    NOT NULL,

    -- Deleting an account must not silently delete certificates that are
    -- currently serving traffic. The order is kept and will fail its next
    -- renewal with a message that names the missing account.
    account_id  INTEGER REFERENCES dns_account(id) ON DELETE SET NULL,

    -- pending | issuing | issued | failed
    state       TEXT    NOT NULL DEFAULT 'pending',
    last_error  TEXT    NOT NULL DEFAULT '',

    certificate BLOB,
    private_key BLOB,

    issued_at   INTEGER,
    expires_at  INTEGER,

    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

-- The scheduler's only query: orders due for renewal, soonest first.
CREATE INDEX cert_order_expiry ON cert_order (expires_at)
    WHERE expires_at IS NOT NULL;

CREATE TABLE cert_event (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    order_id   INTEGER NOT NULL REFERENCES cert_order(id) ON DELETE CASCADE,

    -- requested | issued | renewed | failed | deployed
    kind       TEXT    NOT NULL,
    detail     TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
);

CREATE INDEX cert_event_order ON cert_event (order_id, id DESC);
