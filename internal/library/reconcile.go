package library

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/yuha0/vigo/internal/errs"
	"github.com/yuha0/vigo/internal/imageprep"
	"github.com/yuha0/vigo/internal/source"
	"github.com/yuha0/vigo/internal/tv"
)

const maxFileBytes = 256 << 20

var (
	errTVUnavailable = errors.New("the TV is unavailable")
)

// readFile reads a source file and returns it with its hash.
func readFile(ctx context.Context, src source.Source, p string) ([]byte, string, error) {
	rd, ok := src.(source.Reader)
	if !ok {
		return nil, "", errs.New(ErrUnsupported, "source %s cannot be read", src.ID())
	}
	rc, err := rd.Open(ctx, p)
	if err != nil {
		return nil, "", err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, maxFileBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(data) > maxFileBytes {
		return nil, "", fmt.Errorf("%s is larger than %d bytes", p, maxFileBytes)
	}
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]), nil
}

// probeItem builds the index record of a file from its content.
func probeItem(sourceID string, e source.Entry, data []byte, hash string) (item, error) {
	w, h, _, err := imageprep.Probe(data)
	if err != nil {
		return item{}, fmt.Errorf("%s: %w", e.Path, err)
	}
	return item{sourceID: sourceID, path: e.Path, size: e.Size, mtime: e.ModTime, hash: hash, width: w, height: h}, nil
}

// pass reconciles one source with the TV.
func (l *Library) pass(ctx context.Context, src source.Source) {
	sid := src.ID()
	l.setSyncing(sid, true)
	defer l.setSyncing(sid, false)
	start := time.Now()
	defer func() { l.o.Metrics.SourceSyncDuration.WithLabelValues(sid).Observe(time.Since(start).Seconds()) }()

	// Best effort: an unreachable TV still lets the source side update.
	l.refreshMirror(ctx)

	mu := l.srcMu[sid]
	mu.Lock()
	err := l.scan(ctx, src)
	mu.Unlock()
	if err != nil {
		l.passFailed(ctx, sid, err)
		return
	}
	if err := l.tvOps(ctx, src); err != nil && !errors.Is(err, errTVUnavailable) {
		l.passFailed(ctx, sid, err)
		return
	}
	if _, err := l.pool.Exec(ctx, `UPDATE sources SET last_synced_at = now(), last_error = '' WHERE id = $1`, sid); err != nil {
		l.log.Error("record source sync", "source", sid, "err", err)
	}
}

func (l *Library) passFailed(ctx context.Context, sid string, err error) {
	if ctx.Err() != nil {
		return
	}
	l.log.Error("source sync failed", "source", sid, "err", err)
	l.o.Metrics.SourceSyncErrors.WithLabelValues(sid).Inc()
	l.pool.Exec(context.WithoutCancel(ctx), `UPDATE sources SET last_error = $2 WHERE id = $1`, sid, err.Error())
	l.changed(nil, []string{sid})
}

// scan reads the source, applies the changes and deletes the files of
// artworks proven deleted on the TV. A failed scan changes nothing: an
// unreadable source is an error, never an empty one.
func (l *Library) scan(ctx context.Context, src source.Source) error {
	sid := src.ID()
	scanner, ok := src.(source.Scanner)
	if !ok {
		return errs.New(ErrUnsupported, "source %s cannot be listed", sid)
	}
	entries, err := scanner.Scan(ctx)
	if err != nil {
		return err
	}
	prev, err := loadItems(ctx, l.pool, sid)
	if err != nil {
		return err
	}
	next := map[string]item{}
	var unreadable []string
	thumbs := map[string]renderedThumb{}
	for _, e := range entries {
		e.ModTime = mtime(e.ModTime)
		if p, ok := prev[e.Path]; ok && p.size == e.Size && p.mtime.Equal(e.ModTime) {
			next[e.Path] = p
			continue
		}
		// A file being written by something else can be empty or cut short
		// for a moment. Skip it and keep what is known; the next pass sees
		// the finished file. Never treat it as removed.
		keep := func() {
			if p, ok := prev[e.Path]; ok {
				next[e.Path] = p
			}
		}
		if e.Size == 0 {
			l.log.Info("skipping empty source file until a later pass", "source", sid, "path", e.Path)
			keep()
			continue
		}
		data, hash, err := readFile(ctx, src, e.Path)
		var it item
		if err == nil {
			it, err = probeItem(sid, e, data, hash)
			if err == nil {
				l.queueThumb(ctx, thumbs, hash, data)
			}
			if errors.Is(err, imageprep.ErrUnsupported) {
				l.log.Info("skipping undecodable source file until a later pass", "source", sid, "path", e.Path)
				keep()
				continue
			}
		}
		if err != nil {
			unreadable = append(unreadable, e.Path)
			l.log.Warn("skipping unreadable source file", "source", sid, "path", e.Path, "err", err)
			keep()
			continue
		}
		next[e.Path] = it
	}

	var changed []string
	err = pgx.BeginFunc(ctx, l.pool, func(tx pgx.Tx) error {
		paths := slices.Collect(maps.Keys(next))
		if _, err := tx.Exec(ctx, `DELETE FROM source_items WHERE source_id = $1 AND path <> ALL(COALESCE($2::text[], ARRAY[]::text[]))`, sid, paths); err != nil {
			return err
		}
		for _, it := range next {
			if p, ok := prev[it.path]; ok && p == it {
				continue
			}
			if err := upsertItem(ctx, tx, it); err != nil {
				return err
			}
			if err := ensureArtwork(ctx, tx, it); err != nil {
				return err
			}
		}
		for _, it := range prev {
			if n, ok := next[it.path]; !ok || n.hash != it.hash {
				changed = append(changed, artworkID(it.hash))
			}
		}
		for _, it := range next {
			if p, ok := prev[it.path]; !ok || p.hash != it.hash {
				changed = append(changed, artworkID(it.hash))
			}
		}
		return settle(ctx, tx, changed)
	})
	if err != nil {
		return err
	}
	l.storeThumbs(ctx, thumbs)
	l.changed(changed, nil)

	if err := l.deleteFilesDeletedOnTV(ctx, src); err != nil {
		return err
	}
	if len(unreadable) > 0 {
		return fmt.Errorf("%d files could not be read, e.g. %s", len(unreadable), unreadable[0])
	}
	return nil
}

// deleteFilesDeletedOnTV removes the source files of linked artworks proven
// deleted on the TV.
func (l *Library) deleteFilesDeletedOnTV(ctx context.Context, src source.Source) error {
	sid := src.ID()
	rows, err := l.pool.Query(ctx, `
		SELECT a.id, s.path FROM artworks a JOIN source_items s ON s.hash = a.hash
		WHERE a.tv_deleted_at IS NOT NULL AND s.source_id = $1 ORDER BY a.id, s.path`, sid)
	if err != nil {
		return err
	}
	type victim struct{ id, path string }
	var victims []victim
	var ids []string
	var v victim
	if _, err := pgx.ForEachRow(rows, []any{&v.id, &v.path}, func() error {
		ids = append(ids, v.id)
		victims = append(victims, v)
		return nil
	}); err != nil {
		return err
	}
	if len(victims) == 0 {
		return nil
	}
	w, ok := src.(source.Writer)
	if !ok {
		for _, id := range ids {
			setState(ctx, l.pool, id, stateError, "deleted on the TV, but source "+sid+" is read-only; delete the file to finish")
		}
		l.changed(ids, nil)
		return nil
	}
	for _, v := range victims {
		if err := w.Remove(ctx, v.path); err != nil {
			return fmt.Errorf("delete %s: %w", v.path, err)
		}
		l.log.Info("deleted source file of an artwork deleted on the TV", "source", sid, "path", v.path, "artwork", v.id)
		if _, err := l.pool.Exec(ctx, `DELETE FROM source_items WHERE source_id = $1 AND path = $2`, sid, v.path); err != nil {
			return err
		}
	}
	if err := settle(ctx, l.pool, ids); err != nil {
		return err
	}
	l.changed(ids, nil)
	return nil
}

// tvOps uploads the source's pending artworks, then deletes TV copies whose
// content is gone from every source, one transfer at a time. It stops at
// the first sign that the TV is unavailable and leaves the rest pending.
func (l *Library) tvOps(ctx context.Context, src source.Source) error {
	l.tvMu.Lock()
	defer l.tvMu.Unlock()
	if err := l.uploads(ctx, src); err != nil {
		return err
	}
	return l.deletions(ctx)
}

func (l *Library) uploads(ctx context.Context, src source.Source) error {
	sid := src.ID()
	rows, err := l.pool.Query(ctx, `
		SELECT a.id, a.hash, (SELECT min(s.path) FROM source_items s WHERE s.hash = a.hash AND s.source_id = $1)
		FROM artworks a
		WHERE NOT a.linked AND a.tv_deleted_at IS NULL AND a.hash IS NOT NULL
		  AND a.sync_state IN ($2, $3, $4)
		  AND EXISTS (SELECT 1 FROM source_items s WHERE s.hash = a.hash AND s.source_id = $1)
		ORDER BY a.added_at, a.id`, sid, statePendingUpload, stateUploading, stateError)
	if err != nil {
		return err
	}
	type pending struct{ id, hash, path string }
	var todo []pending
	var p pending
	if _, err := pgx.ForEachRow(rows, []any{&p.id, &p.hash, &p.path}, func() error {
		todo = append(todo, p)
		return nil
	}); err != nil {
		return err
	}
	for _, p := range todo {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		setState(ctx, l.pool, p.id, stateUploading, "")
		l.changed([]string{p.id}, nil)
		data, hash, err := readFile(ctx, src, p.path)
		if err == nil && hash != p.hash {
			// Changed since the scan; the next pass indexes the new content.
			setState(ctx, l.pool, p.id, statePendingUpload, "")
			continue
		}
		if err != nil {
			setState(ctx, l.pool, p.id, stateError, "read "+p.path+": "+err.Error())
			l.changed([]string{p.id}, nil)
			continue
		}
		art, err := l.o.TV.Upload(ctx, bytes.NewReader(data), imageprep.FitPad, "")
		switch {
		case tvUnavailable(err):
			setState(ctx, l.pool, p.id, statePendingUpload, "")
			l.changed([]string{p.id}, nil)
			return fmt.Errorf("%w: %w", errTVUnavailable, err)
		case err != nil:
			setState(ctx, l.pool, p.id, stateError, "upload: "+err.Error())
			l.changed([]string{p.id}, nil)
			continue
		}
		if err := l.link(ctx, p.id, art); err != nil {
			return err
		}
		l.log.Info("uploaded artwork", "artwork", p.id, "content_id", art.ContentId, "source", sid, "path", p.path)
		l.changed([]string{p.id}, nil)
	}
	return nil
}

// deletions deletes from the TV the copies of linked artworks whose
// content is in no source, and the TV-only uploads recorded for deletion.
// Only uploads are ever deleted, and each deletion is recorded before it is
// sent. The whole batch goes in one request; if the TV refuses it, each
// item is tried on its own so one bad item cannot block the rest.
func (l *Library) deletions(ctx context.Context) error {
	rows, err := l.pool.Query(ctx, `
		SELECT a.id, a.tv_content_id, COALESCE(t.content_type, '')
		FROM artworks a LEFT JOIN tv_items t ON t.content_id = a.tv_content_id
		WHERE a.tv_content_id IS NOT NULL AND (
		      (a.linked AND a.hash IS NOT NULL AND a.tv_deleted_at IS NULL
		       AND NOT EXISTS (SELECT 1 FROM source_items s WHERE s.hash = a.hash))
		   OR (a.hash IS NULL AND EXISTS (SELECT 1 FROM tv_deletions d WHERE d.content_id = a.tv_content_id)))
		ORDER BY a.updated_at, a.id`)
	if err != nil {
		return err
	}
	type victim struct{ id, cid, ctype string }
	var todo []victim
	var v victim
	if _, err := pgx.ForEachRow(rows, []any{&v.id, &v.cid, &v.ctype}, func() error {
		todo = append(todo, v)
		return nil
	}); err != nil {
		return err
	}
	var batch []victim
	for _, v := range todo {
		if v.ctype != "" && v.ctype != "mobile" {
			setState(ctx, l.pool, v.id, stateError, "refusing to delete "+v.cid+": it is not an upload")
			continue
		}
		if _, err := l.pool.Exec(ctx, `INSERT INTO tv_deletions (content_id, artwork_id) VALUES ($1, $2) ON CONFLICT (content_id) DO NOTHING`, v.cid, v.id); err != nil {
			return err
		}
		setState(ctx, l.pool, v.id, stateDeleting, "")
		batch = append(batch, v)
	}
	if len(batch) == 0 {
		return nil
	}
	ids, cids := make([]string, len(batch)), make([]string, len(batch))
	for i, v := range batch {
		ids[i], cids[i] = v.id, v.cid
	}
	l.changed(ids, nil)

	deleted, err := l.o.TV.DeleteArt(ctx, cids)
	if tvUnavailable(err) {
		for _, v := range batch {
			setState(ctx, l.pool, v.id, statePendingDelete, "")
		}
		l.changed(ids, nil)
		return fmt.Errorf("%w: %w", errTVUnavailable, err)
	}
	done := map[string]bool{}
	if err == nil {
		for _, cid := range deleted {
			done[cid] = true
			l.contentDeleted(ctx, cid, "vigo")
		}
		l.log.Info("deleted artworks from the TV", "count", len(deleted))
	} else {
		l.log.Warn("batch deletion refused; deleting one at a time", "count", len(batch), "err", err)
	}
	for _, v := range batch {
		if done[v.cid] {
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := l.deleteFromTV(ctx, v.id, v.cid, true); err != nil {
			return err
		}
	}
	return nil
}

// deleteFromTV records the deletion, sends it and applies the result. For
// the reconciler, failures are recorded on the artwork and retried by later
// passes. For an immediate request, a failure withdraws the recorded
// deletion and is returned, so nothing has changed.
func (l *Library) deleteFromTV(ctx context.Context, id, cid string, fromReconciler bool) error {
	if _, err := l.pool.Exec(ctx, `INSERT INTO tv_deletions (content_id, artwork_id) VALUES ($1, $2) ON CONFLICT (content_id) DO NOTHING`, cid, id); err != nil {
		return err
	}
	if fromReconciler {
		setState(ctx, l.pool, id, stateDeleting, "")
		l.changed([]string{id}, nil)
	}
	_, err := l.o.TV.DeleteArt(ctx, []string{cid})
	switch {
	case err == nil, errors.Is(err, tv.ErrNotFound):
		l.contentDeleted(ctx, cid, "vigo")
		l.log.Info("deleted artwork from the TV", "artwork", id, "content_id", cid)
		return nil
	case !fromReconciler:
		l.pool.Exec(context.WithoutCancel(ctx), `DELETE FROM tv_deletions WHERE content_id = $1`, cid)
		return err
	case tvUnavailable(err):
		setState(ctx, l.pool, id, statePendingDelete, "")
		l.changed([]string{id}, nil)
		return fmt.Errorf("%w: %w", errTVUnavailable, err)
	default:
		setState(ctx, l.pool, id, stateError, "delete from the TV: "+err.Error())
		l.changed([]string{id}, nil)
		return nil
	}
}
