-- Gallery thumbnails, one per artwork: rendered from the source file for
-- sha256: artworks, downloaded from the TV for tv: ones (docs/design.md
-- "Thumbnails"). Rows go with their artwork.
CREATE TABLE thumbnails (
    artwork_id      text        PRIMARY KEY REFERENCES artworks (id) ON DELETE CASCADE,
    data            bytea,
    etag            text        NOT NULL DEFAULT '',
    -- ok: data is current and never refetched automatically. missing: the
    -- TV has no thumbnail; rechecked weekly. error: retried with backoff.
    status          text        NOT NULL CHECK (status IN ('ok', 'missing', 'error')),
    error           text        NOT NULL DEFAULT '',
    fetched_at      timestamptz,
    attempts        integer     NOT NULL DEFAULT 0,
    next_attempt_at timestamptz,
    -- Set by RefreshThumbnails; the next job downloads it again.
    requested_at    timestamptz
);
-- JPEGs are already compressed: store out of line without trying to
-- compress them again.
ALTER TABLE thumbnails ALTER COLUMN data SET STORAGE EXTERNAL;
CREATE INDEX thumbnails_due ON thumbnails (next_attempt_at) WHERE status <> 'ok';
