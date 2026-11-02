package library

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Sync states as stored; the names of corev1.SyncState without prefix.
const (
	stateInSync        = "IN_SYNC"
	statePendingUpload = "PENDING_UPLOAD"
	stateUploading     = "UPLOADING"
	statePendingDelete = "PENDING_DELETE"
	stateDeleting      = "DELETING"
	stateError         = "ERROR"
	stateTVOnly        = "TV_ONLY"
)

type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func artworkID(hash string) string    { return "sha256:" + hash }
func tvArtworkID(cid string) string   { return "tv:" + cid }
func mtime(t time.Time) time.Time     { return t.UTC().Truncate(time.Microsecond) }
func isTVArtwork(id string) bool      { return strings.HasPrefix(id, "tv:") }
func hashOf(id string) (string, bool) { return strings.CutPrefix(id, "sha256:") }

type item struct {
	sourceID string
	path     string
	size     int64
	mtime    time.Time
	hash     string
	width    int
	height   int
}

func loadItems(ctx context.Context, q querier, sourceID string) (map[string]item, error) {
	rows, err := q.Query(ctx, `SELECT path, size, mtime, hash, width, height FROM source_items WHERE source_id = $1`, sourceID)
	if err != nil {
		return nil, err
	}
	out := map[string]item{}
	var it item
	_, err = pgx.ForEachRow(rows, []any{&it.path, &it.size, &it.mtime, &it.hash, &it.width, &it.height}, func() error {
		it.sourceID = sourceID
		out[it.path] = it
		return nil
	})
	return out, err
}

func upsertItem(ctx context.Context, q querier, it item) error {
	_, err := q.Exec(ctx, `
		INSERT INTO source_items (source_id, path, size, mtime, hash, width, height)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (source_id, path) DO UPDATE
		SET size = $3, mtime = $4, hash = $5, width = $6, height = $7`,
		it.sourceID, it.path, it.size, mtime(it.mtime), it.hash, it.width, it.height)
	return err
}

// ensureArtwork creates the artwork for source content if it is new.
func ensureArtwork(ctx context.Context, q querier, it item) error {
	_, err := q.Exec(ctx, `
		INSERT INTO artworks (id, hash, sync_state, width, height, size_bytes)
		VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT (id) DO NOTHING`,
		artworkID(it.hash), it.hash, statePendingUpload, it.width, it.height, it.size)
	return err
}

func setState(ctx context.Context, q querier, id, state, reason string) error {
	_, err := q.Exec(ctx, `UPDATE artworks SET sync_state = $2, sync_error = $3, updated_at = now() WHERE id = $1`, id, state, reason)
	return err
}

type artworkRow struct {
	id          string
	hash        *string
	contentID   *string
	linked      bool
	tvDeleted   bool
	state       string
	syncError   string
	width       int
	height      int
	size        int64
	contentType *string
	categories  []string
}

// settle recomputes the derived state of artworks from the facts: their
// source files, their TV copy and whether it was deleted on the TV. It sets
// the sync state, system tags and search text, and drops artworks that no
// longer exist anywhere. Every change to those facts ends in settle.
func settle(ctx context.Context, q querier, ids []string) error {
	slices.Sort(ids)
	for _, id := range slices.Compact(ids) {
		if err := settleOne(ctx, q, id); err != nil {
			return fmt.Errorf("settle %s: %w", id, err)
		}
	}
	return nil
}

func settleOne(ctx context.Context, q querier, id string) error {
	var a artworkRow
	err := q.QueryRow(ctx, `
		SELECT a.id, a.hash, a.tv_content_id, a.linked, a.tv_deleted_at IS NOT NULL, a.sync_state, a.sync_error,
		       a.width, a.height, a.size_bytes, t.content_type, COALESCE(t.category_ids, '{}')
		FROM artworks a LEFT JOIN tv_items t ON t.content_id = a.tv_content_id AND t.gone_at IS NULL
		WHERE a.id = $1`, id).
		Scan(&a.id, &a.hash, &a.contentID, &a.linked, &a.tvDeleted, &a.state, &a.syncError,
			&a.width, &a.height, &a.size, &a.contentType, &a.categories)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	drop := func() error {
		_, err := q.Exec(ctx, `DELETE FROM artworks WHERE id = $1`, id)
		return err
	}

	f := facts{categories: a.categories}
	if a.contentType != nil {
		f.contentType = *a.contentType
		f.onTV = true
	}
	state, reason := a.state, a.syncError
	cid := ""
	if a.contentID != nil {
		cid = *a.contentID
	}

	if a.hash == nil {
		if !f.onTV {
			return drop()
		}
		f.tvOnly = true
		state, reason = stateTVOnly, ""
	} else {
		rows, err := q.Query(ctx, `SELECT source_id, path, size, width, height FROM source_items WHERE hash = $1 ORDER BY source_id, path`, *a.hash)
		if err != nil {
			return err
		}
		var r ref
		var size int64
		var w, h int
		_, err = pgx.ForEachRow(rows, []any{&r.sourceID, &r.path, &size, &w, &h}, func() error {
			if len(f.refs) == 0 {
				a.size, a.width, a.height = size, w, h
			}
			f.refs = append(f.refs, r)
			return nil
		})
		if err != nil {
			return err
		}
		f.width, f.height = a.width, a.height
		hasRefs := len(f.refs) > 0
		switch {
		case a.tvDeleted && !hasRefs:
			return drop()
		case a.tvDeleted:
			if state != stateError {
				state, reason = statePendingDelete, ""
			}
		case hasRefs && a.linked:
			state, reason = stateInSync, ""
			// The content came back after vigo queued its deletion.
			if _, err := q.Exec(ctx, `DELETE FROM tv_deletions WHERE content_id = $1`, cid); err != nil {
				return err
			}
		case hasRefs:
			if state != stateUploading && state != stateError {
				state, reason = statePendingUpload, ""
			}
		case a.linked:
			if state != stateDeleting && state != stateError {
				state, reason = statePendingDelete, ""
			}
		default:
			return drop()
		}
	}

	tags := systemTags(f)
	if _, err := q.Exec(ctx, `DELETE FROM artwork_tags WHERE artwork_id = $1 AND origin = 'system' AND tag <> ALL(COALESCE($2::text[], ARRAY[]::text[]))`, id, tags); err != nil {
		return err
	}
	if _, err := q.Exec(ctx, `
		INSERT INTO artwork_tags (artwork_id, tag, origin) SELECT $1, unnest($2::text[]), 'system'
		ON CONFLICT (artwork_id, tag) DO NOTHING`, id, tags); err != nil {
		return err
	}
	var userTags []string
	rows, err := q.Query(ctx, `SELECT tag FROM artwork_tags WHERE artwork_id = $1 AND origin = 'user' ORDER BY tag`, id)
	if err != nil {
		return err
	}
	if userTags, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return err
	}
	_, err = q.Exec(ctx, `
		UPDATE artworks SET sync_state = $2, sync_error = $3, width = $4, height = $5, size_bytes = $6,
		       search_text = $7, updated_at = now()
		WHERE id = $1`,
		id, state, reason, a.width, a.height, a.size, searchText(f, append(tags, userTags...), cid))
	return err
}

// sourcesOf lists the sources holding an artwork's content.
func sourcesOf(ctx context.Context, q querier, hash string) ([]string, error) {
	rows, err := q.Query(ctx, `SELECT DISTINCT source_id FROM source_items WHERE hash = $1 ORDER BY source_id`, hash)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}
