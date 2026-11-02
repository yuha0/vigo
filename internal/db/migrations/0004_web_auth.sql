-- The browser listener's password and sessions (docs/design.md
-- "Authentication").

-- One row at most: the argon2id hash as a PHC string, carrying its own
-- parameters and salt.
CREATE TABLE web_password (
    id         smallint    PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    hash       text        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- Only the SHA-256 of each session token is stored.
CREATE TABLE web_sessions (
    token_hash   bytea       PRIMARY KEY,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_used_at timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL
);
CREATE INDEX web_sessions_expires_at ON web_sessions (expires_at);
