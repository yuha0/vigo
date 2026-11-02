-- The art library: the TV mirror, sources and the artwork index. See
-- docs/design.md "Art library and sources".

-- pg_trgm is a trusted extension, so the database owner may create it. The
-- lock serializes concurrent migrations of separate schemas in one database
-- (tests), which would otherwise race on the database-wide extension.
SELECT pg_advisory_xact_lock(1986, 1);
CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;

CREATE TABLE tv_items (
    content_id    text        PRIMARY KEY,
    content_type  text        NOT NULL,
    category_ids  text[]      NOT NULL DEFAULT '{}',
    width         integer     NOT NULL DEFAULT 0,
    height        integer     NOT NULL DEFAULT 0,
    first_seen    timestamptz NOT NULL DEFAULT now(),
    last_seen     timestamptz NOT NULL DEFAULT now(),
    -- Consecutive complete content listings the item was absent from.
    missing_count integer     NOT NULL DEFAULT 0,
    -- Proof that the item was deleted on the TV: an image_deleted event or
    -- absence from two consecutive complete listings.
    gone_at       timestamptz,
    gone_via      text
);

CREATE TABLE sources (
    id             text        PRIMARY KEY,
    kind           text        NOT NULL,
    display        text        NOT NULL,
    last_synced_at timestamptz,
    last_error     text        NOT NULL DEFAULT '',
    -- Set by the mass-deletion guard; cleared by ResumeSource.
    halted_reason  text        NOT NULL DEFAULT '',
    -- Set by ResumeSource: the next pass applies its deletions even past
    -- the guard, once.
    resumed        boolean     NOT NULL DEFAULT false
);

CREATE TABLE source_items (
    source_id text        NOT NULL REFERENCES sources (id) ON DELETE CASCADE,
    path      text        NOT NULL,
    size      bigint      NOT NULL,
    mtime     timestamptz NOT NULL,
    -- Hex SHA-256 of the file bytes.
    hash      text        NOT NULL,
    -- Upright (EXIF-applied) dimensions.
    width     integer     NOT NULL,
    height    integer     NOT NULL,
    PRIMARY KEY (source_id, path)
);
CREATE INDEX source_items_hash ON source_items (hash);

CREATE TABLE artworks (
    id            text        PRIMARY KEY,
    -- Hex SHA-256 for source content; NULL for TV-only artworks.
    hash          text        UNIQUE,
    tv_content_id text        UNIQUE,
    -- tv_content_id was uploaded by vigo from a source.
    linked        boolean     NOT NULL DEFAULT false,
    -- The linked TV copy was proven deleted on the TV; the source files follow.
    tv_deleted_at timestamptz,
    sync_state    text        NOT NULL,
    sync_error    text        NOT NULL DEFAULT '',
    width         integer     NOT NULL DEFAULT 0,
    height        integer     NOT NULL DEFAULT 0,
    size_bytes    bigint      NOT NULL DEFAULT 0,
    -- Lowercased file names and tags, for substring search.
    search_text   text        NOT NULL DEFAULT '',
    added_at      timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX artworks_sync_state ON artworks (sync_state);
CREATE INDEX artworks_order ON artworks (added_at DESC, id);
CREATE INDEX artworks_search ON artworks USING gin (search_text public.gin_trgm_ops);

CREATE TABLE artwork_tags (
    artwork_id text NOT NULL REFERENCES artworks (id) ON DELETE CASCADE,
    tag        text NOT NULL,
    origin     text NOT NULL CHECK (origin IN ('system', 'user')),
    PRIMARY KEY (artwork_id, tag)
);
-- text_pattern_ops serves both exact matches and prefix LIKE 'folder/%'.
CREATE INDEX artwork_tags_tag ON artwork_tags (tag text_pattern_ops);

-- TV deletions vigo started, recorded before the request is sent so the
-- item's disappearance is never mistaken for a deletion on the TV.
CREATE TABLE tv_deletions (
    content_id   text        PRIMARY KEY,
    artwork_id   text        NOT NULL,
    requested_at timestamptz NOT NULL DEFAULT now()
);
