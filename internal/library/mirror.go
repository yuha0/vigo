package library

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"

	corev1 "github.com/yuha0/vigo/gen/go/vigo/core/v1"
	"github.com/yuha0/vigo/internal/samsung"
)

// missingListings is how many consecutive complete content listings an
// item must be absent from before that counts as proof of deletion.
const missingListings = 2

func contentTypeName(t corev1.ContentType) string {
	return strings.ToLower(strings.TrimPrefix(t.String(), "CONTENT_TYPE_"))
}

// refreshMirror reads the TV's full content list and applies it. A failed
// listing changes nothing: it never counts as absence.
func (l *Library) refreshMirror(ctx context.Context) error {
	arts, _, err := l.o.TV.ListArt(ctx, "")
	if err != nil {
		if !tvUnavailable(err) {
			l.log.Warn("read the TV content list", "err", err)
		}
		return err
	}
	return l.applyListing(ctx, arts)
}

// applyListing records a complete listing: present items are refreshed,
// absent ones counted, and items absent from two consecutive listings are
// proven deleted.
func (l *Library) applyListing(ctx context.Context, arts []*corev1.Artwork) error {
	l.mirrorMu.Lock()
	defer l.mirrorMu.Unlock()
	var changed, sources []string
	err := pgx.BeginFunc(ctx, l.pool, func(tx pgx.Tx) error {
		present := make([]string, 0, len(arts))
		for _, a := range arts {
			present = append(present, a.ContentId)
			if err := upsertTVItem(ctx, tx, a); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE tv_items SET missing_count = missing_count + 1
			WHERE content_id <> ALL(COALESCE($1::text[], ARRAY[]::text[])) AND gone_at IS NULL`, present); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE tv_items SET gone_at = now(), gone_via = 'listings'
			WHERE missing_count >= $1 AND gone_at IS NULL`, missingListings); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			INSERT INTO artworks (id, tv_content_id, sync_state, width, height)
			SELECT 'tv:' || t.content_id, t.content_id, $1, t.width, t.height
			FROM tv_items t
			WHERE t.gone_at IS NULL AND NOT EXISTS (SELECT 1 FROM artworks a WHERE a.tv_content_id = t.content_id)
			ON CONFLICT DO NOTHING RETURNING id`, stateTVOnly)
		if err != nil {
			return err
		}
		if changed, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
			return err
		}
		gone, gsrc, err := l.processGone(ctx, tx)
		if err != nil {
			return err
		}
		changed, sources = append(changed, gone...), gsrc
		rows, err = tx.Query(ctx, `SELECT id FROM artworks WHERE tv_content_id = ANY($1)`, present)
		if err != nil {
			return err
		}
		onTV, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		return settle(ctx, tx, append(onTV, changed...))
	})
	if err != nil {
		l.log.Error("apply the TV content list", "err", err)
		return err
	}
	l.changed(changed, nil)
	for _, s := range sources {
		l.kick(s)
	}
	return nil
}

func upsertTVItem(ctx context.Context, q querier, a *corev1.Artwork) error {
	_, err := q.Exec(ctx, `
		INSERT INTO tv_items (content_id, content_type, category_ids, width, height)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (content_id) DO UPDATE
		SET content_type = $2, category_ids = $3, width = $4, height = $5, last_seen = now(), missing_count = 0
		WHERE tv_items.gone_at IS NULL`,
		a.ContentId, contentTypeName(a.ContentType), a.CategoryIds, a.Width, a.Height)
	return err
}

// contentDeleted records proof that the TV no longer has contentID.
func (l *Library) contentDeleted(ctx context.Context, contentID, via string) {
	l.mirrorMu.Lock()
	defer l.mirrorMu.Unlock()
	var changed, sources []string
	err := pgx.BeginFunc(ctx, l.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO tv_items (content_id, content_type, gone_at, gone_via) VALUES ($1, '', now(), $2)
			ON CONFLICT (content_id) DO UPDATE
			SET gone_at = COALESCE(tv_items.gone_at, now()), gone_via = COALESCE(tv_items.gone_via, $2)`,
			contentID, via); err != nil {
			return err
		}
		var err error
		changed, sources, err = l.processGone(ctx, tx)
		return err
	})
	if err != nil {
		l.log.Error("record a TV deletion", "content_id", contentID, "err", err)
		return
	}
	l.changed(changed, nil)
	for _, s := range sources {
		l.kick(s)
	}
}

// processGone applies every proven TV deletion. A deletion vigo recorded
// beforehand only unlinks the artwork. Any other deletion of a linked
// artwork is a deletion on the TV: it is marked so that its source files
// follow in the source's next pass.
func (l *Library) processGone(ctx context.Context, tx pgx.Tx) (changed, sources []string, err error) {
	rows, err := tx.Query(ctx, `
		SELECT t.content_id, COALESCE(t.gone_via, ''), d.content_id IS NOT NULL, COALESCE(a.id, ''), COALESCE(a.hash, ''), COALESCE(a.linked, false)
		FROM tv_items t
		LEFT JOIN tv_deletions d ON d.content_id = t.content_id
		LEFT JOIN artworks a ON a.tv_content_id = t.content_id
		WHERE t.gone_at IS NOT NULL`)
	if err != nil {
		return nil, nil, err
	}
	type goneItem struct {
		cid, via, id, hash string
		ours, linked       bool
	}
	var gone []goneItem
	var g goneItem
	_, err = pgx.ForEachRow(rows, []any{&g.cid, &g.via, &g.ours, &g.id, &g.hash, &g.linked}, func() error {
		gone = append(gone, g)
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	for _, g := range gone {
		switch {
		case g.id == "":
		case isTVArtwork(g.id):
			if _, err := tx.Exec(ctx, `DELETE FROM artworks WHERE id = $1`, g.id); err != nil {
				return nil, nil, err
			}
			changed = append(changed, g.id)
		case g.ours || !g.linked:
			if _, err := tx.Exec(ctx, `UPDATE artworks SET tv_content_id = NULL, linked = false WHERE id = $1`, g.id); err != nil {
				return nil, nil, err
			}
			changed = append(changed, g.id)
		default:
			l.log.Info("artwork deleted on the TV; its source files will follow", "artwork", g.id, "content_id", g.cid, "proof", g.via)
			if _, err := tx.Exec(ctx, `UPDATE artworks SET tv_content_id = NULL, linked = false, tv_deleted_at = now() WHERE id = $1`, g.id); err != nil {
				return nil, nil, err
			}
			changed = append(changed, g.id)
			srcs, err := sourcesOf(ctx, tx, g.hash)
			if err != nil {
				return nil, nil, err
			}
			sources = append(sources, srcs...)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM tv_deletions WHERE content_id = $1`, g.cid); err != nil {
			return nil, nil, err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM tv_items WHERE content_id = $1`, g.cid); err != nil {
			return nil, nil, err
		}
	}
	return changed, sources, settle(ctx, tx, changed)
}

// link records that artworkID was uploaded as art.
func (l *Library) link(ctx context.Context, artworkID string, art *corev1.Artwork) error {
	l.mirrorMu.Lock()
	defer l.mirrorMu.Unlock()
	return pgx.BeginFunc(ctx, l.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM artworks WHERE id = $1`, tvArtworkID(art.ContentId)); err != nil {
			return err
		}
		if err := upsertTVItem(ctx, tx, art); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE artworks SET tv_content_id = $2, linked = true, sync_error = '' WHERE id = $1`,
			artworkID, art.ContentId); err != nil {
			return err
		}
		return settle(ctx, tx, []string{artworkID})
	})
}

// LoadTVContent returns the mirrored TV library, one entry per content id
// and category. Development mode restores the fake TV from it.
func LoadTVContent(ctx context.Context, q querier) ([]samsung.Content, error) {
	rows, err := q.Query(ctx, `SELECT content_id, content_type, category_ids, width, height FROM tv_items WHERE gone_at IS NULL ORDER BY content_id`)
	if err != nil {
		return nil, err
	}
	var out []samsung.Content
	var c samsung.Content
	var cats []string
	_, err = pgx.ForEachRow(rows, []any{&c.ContentID, &c.ContentType, &cats, &c.Width, &c.Height}, func() error {
		if len(cats) == 0 {
			cats = []string{""}
		}
		for _, cat := range cats {
			item := c
			item.CategoryID = cat
			out = append(out, item)
		}
		return nil
	})
	return out, err
}
