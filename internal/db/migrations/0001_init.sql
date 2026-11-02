-- Encrypted columns (key_id, nonce, ciphertext) are written by
-- internal/secretbox with additional data binding them to table, column and
-- row key.

CREATE TABLE tv_credentials (
    id               smallint    PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    key_id           text        NOT NULL,
    nonce            bytea       NOT NULL,
    ciphertext       bytea       NOT NULL,
    client_name      text        NOT NULL,
    created_at       timestamptz NOT NULL DEFAULT now(),
    last_verified_at timestamptz,
    invalidated_at   timestamptz
);

CREATE TABLE config (
    id          smallint    PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    tv_host     text        NOT NULL DEFAULT '',
    tv_port     integer     NOT NULL DEFAULT 0,
    client_name text        NOT NULL DEFAULT '',
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE selection (
    id         smallint    PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    record     jsonb       NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE uploads (
    content_id  text        PRIMARY KEY,
    bytes       bigint      NOT NULL CHECK (bytes >= 0),
    uploaded_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE model_history (
    model           text        PRIMARY KEY,
    first_seen      timestamptz NOT NULL DEFAULT now(),
    last_seen       timestamptz NOT NULL DEFAULT now(),
    acknowledged_at timestamptz
);

-- backend_pid is the session holding the leader advisory lock; a row is
-- current only while that session still holds it (see pg_locks).
CREATE TABLE leader (
    id          smallint    PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    identity    text        NOT NULL,
    address     text        NOT NULL,
    backend_pid integer     NOT NULL,
    since       timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE integrations (
    kind        text        PRIMARY KEY,
    instance    text        NOT NULL DEFAULT '',
    identity    text        NOT NULL,
    endpoint    text        NOT NULL DEFAULT '',
    lease_hash  bytea,
    backend_pid integer,
    last_seen   timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE integration_state (
    kind       text        NOT NULL,
    key        text        NOT NULL,
    key_id     text        NOT NULL,
    nonce      bytea       NOT NULL,
    ciphertext bytea       NOT NULL,
    version    bigint      NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (kind, key)
);
