package library

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/yuha0/vigo/internal/errs"
	"github.com/yuha0/vigo/internal/imageprep"
	"github.com/yuha0/vigo/internal/tv"
)

// Thumbnails live in Postgres, one row per artwork, so every replica serves
// them and they survive restarts. Source thumbnails are rendered when a file
// is indexed; content addressing makes them immutable. TV-only thumbnails
// are downloaded in the background, one at a time and behind interactive TV
// calls. A stored thumbnail is never downloaded again unless
// RefreshThumbnails asks for it: TV artwork does not change.

const (
	thumbOK      = "ok"
	thumbMissing = "missing"
	thumbError   = "error"

	missingRecheck = 7 * 24 * time.Hour
	backoffMin     = time.Minute
	backoffMax     = 6 * time.Hour
	thumbBatch     = 20
	// thumbErrorsPerRun stops a run that keeps failing, e.g. a TV that
	// refuses every request; the rest wait for the next run.
	thumbErrorsPerRun = 5
)

func thumbOrigin(id string) string {
	if isTVArtwork(id) {
		return "tv"
	}
	return "source"
}

func thumbETag(data []byte) string {
	sum := sha256.Sum256(data)
	return `"` + hex.EncodeToString(sum[:12]) + `"`
}

func renderThumb(data []byte) ([]byte, error) {
	return imageprep.Scaled(data, thumbSize, thumbSize, thumbQuality)
}

// backoff is 1 minute after the first failure, doubling up to 6 hours.
func backoff(attempts int) time.Duration {
	d := backoffMin
	for i := 1; i < attempts && d < backoffMax; i++ {
		d *= 2
	}
	return min(d, backoffMax)
}

// ignoreGone treats a write for an artwork deleted meanwhile as done.
func ignoreGone(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23503" {
		return nil
	}
	return err
}

func (l *Library) storeThumbOK(ctx context.Context, id string, data []byte) (string, error) {
	etag := thumbETag(data)
	_, err := l.pool.Exec(ctx, `
		INSERT INTO thumbnails (artwork_id, data, etag, status, fetched_at, attempts, next_attempt_at, error, requested_at)
		VALUES ($1, $2, $3, 'ok', now(), 0, NULL, '', NULL)
		ON CONFLICT (artwork_id) DO UPDATE
		SET data = $2, etag = $3, status = 'ok', fetched_at = now(), attempts = 0, next_attempt_at = NULL, error = '', requested_at = NULL`,
		id, data, etag)
	return etag, ignoreGone(err)
}

func (l *Library) storeThumbMissing(ctx context.Context, id string) error {
	_, err := l.pool.Exec(ctx, `
		INSERT INTO thumbnails (artwork_id, status, fetched_at, attempts, next_attempt_at, requested_at)
		VALUES ($1, 'missing', now(), 0, now() + $2::interval, NULL)
		ON CONFLICT (artwork_id) DO UPDATE
		SET data = NULL, etag = '', status = 'missing', fetched_at = now(), attempts = 0,
		    next_attempt_at = now() + $2::interval, error = '', requested_at = NULL`,
		id, missingRecheck.String())
	return ignoreGone(err)
}

// storeThumbError records a failure without losing stored data: a failed
// refresh keeps serving the previous thumbnail.
func (l *Library) storeThumbError(ctx context.Context, id string, cause error) error {
	var attempts int
	err := l.pool.QueryRow(ctx, `SELECT attempts FROM thumbnails WHERE artwork_id = $1`, id).Scan(&attempts)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	attempts++
	_, err = l.pool.Exec(ctx, `
		INSERT INTO thumbnails (artwork_id, status, error, attempts, next_attempt_at)
		VALUES ($1, 'error', $2, $3, now() + $4::interval)
		ON CONFLICT (artwork_id) DO UPDATE
		SET status = CASE WHEN thumbnails.data IS NOT NULL THEN 'ok' ELSE 'error' END,
		    error = $2, attempts = $3, next_attempt_at = now() + $4::interval, requested_at = NULL`,
		id, cause.Error(), attempts, backoff(attempts).String())
	return ignoreGone(err)
}

// StoredThumbnail returns a stored thumbnail without touching the TV or the
// source, so any replica can serve it. status is "" when nothing is stored.
func (l *Library) StoredThumbnail(ctx context.Context, id string) (data []byte, etag, status string, err error) {
	err = l.pool.QueryRow(ctx, `SELECT data, etag, status FROM thumbnails WHERE artwork_id = $1`, id).Scan(&data, &etag, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", "", nil
	}
	if status == thumbOK && data == nil {
		status = thumbError
	}
	return data, etag, status, err
}

// ArtworkForContent maps a TV content id to its artwork: a vigo upload's
// content id maps to its source artwork.
func (l *Library) ArtworkForContent(ctx context.Context, contentID string) (string, error) {
	var id string
	err := l.pool.QueryRow(ctx, `SELECT id FROM artworks WHERE tv_content_id = $1`, contentID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errs.New(ErrNotFound, "no artwork for content %s", contentID)
	}
	return id, err
}

// Thumbnail returns an artwork's thumbnail: stored, else rendered or
// downloaded now and stored.
func (l *Library) Thumbnail(ctx context.Context, id string) ([]byte, string, error) {
	data, etag, status, err := l.StoredThumbnail(ctx, id)
	switch {
	case err != nil:
		return nil, "", err
	case status == thumbOK:
		return data, etag, nil
	case status == thumbMissing:
		return nil, "", errs.New(ErrNotFound, "the TV has no thumbnail for %s", id)
	}
	if err := l.requireRunning(); err != nil {
		return nil, "", err
	}
	a, err := l.Get(ctx, id)
	if err != nil {
		return nil, "", err
	}
	if _, ok := hashOf(id); ok && len(a.Sources) > 0 {
		data, err := l.renderSource(ctx, id)
		if err == nil {
			etag, err := l.storeThumbOK(ctx, id, data)
			return data, etag, err
		}
		l.storeThumbError(ctx, id, err)
		if a.TvContentId == "" {
			return nil, "", err
		}
		l.log.Warn("thumbnail from the source failed; using the TV's", "artwork", id, "err", err)
		data, etag, err := l.o.TV.Thumbnail(ctx, a.TvContentId)
		return data, etag, err
	}
	if a.TvContentId == "" {
		return nil, "", errs.New(ErrNotFound, "no thumbnail for %s", id)
	}
	data, _, err = l.o.TV.Thumbnail(ctx, a.TvContentId)
	switch {
	case errors.Is(err, tv.ErrNotFound):
		l.storeThumbMissing(ctx, id)
		return nil, "", err
	case err != nil:
		return nil, "", err
	}
	etag, err = l.storeThumbOK(ctx, id, data)
	return data, etag, err
}

// renderSource renders a source artwork's thumbnail from any of its files.
func (l *Library) renderSource(ctx context.Context, id string) ([]byte, error) {
	hash, _ := hashOf(id)
	rows, err := l.pool.Query(ctx, `SELECT source_id, path FROM source_items WHERE hash = $1 ORDER BY source_id, path`, hash)
	if err != nil {
		return nil, err
	}
	var refs []ref
	var r ref
	if _, err := pgx.ForEachRow(rows, []any{&r.sourceID, &r.path}, func() error {
		refs = append(refs, r)
		return nil
	}); err != nil {
		return nil, err
	}
	var last error = errs.New(ErrNotFound, "%s is in no configured source", id)
	for _, r := range refs {
		src, ok := l.o.Sources.Get(r.sourceID)
		if !ok {
			continue
		}
		data, got, err := readFile(ctx, src, r.path)
		if err == nil && got != hash {
			err = fmt.Errorf("%s changed since it was indexed", r.path)
		}
		if err == nil {
			return renderThumb(data)
		}
		last = err
	}
	return nil, last
}

func (l *Library) hasThumb(ctx context.Context, id string) bool {
	var ok bool
	l.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM thumbnails WHERE artwork_id = $1 AND status = 'ok')`, id).Scan(&ok)
	return ok
}

type thumbJob struct {
	id, contentID string
	requested     bool
	etag          string
}

func (l *Library) dueThumbJobs(ctx context.Context, skip []string) ([]thumbJob, error) {
	rows, err := l.pool.Query(ctx, `
		SELECT a.id, COALESCE(a.tv_content_id, ''), t.requested_at IS NOT NULL, COALESCE(t.etag, '')
		FROM artworks a LEFT JOIN thumbnails t ON t.artwork_id = a.id
		WHERE NOT (a.id = ANY(COALESCE($1::text[], ARRAY[]::text[])))
		  AND (a.hash IS NULL OR EXISTS (SELECT 1 FROM source_items s WHERE s.hash = a.hash))
		  AND (t.artwork_id IS NULL OR t.requested_at IS NOT NULL
		       OR (t.status <> 'ok' AND t.next_attempt_at <= now()))
		ORDER BY t.requested_at NULLS LAST, a.added_at DESC, a.id
		LIMIT $2`, skip, thumbBatch)
	if err != nil {
		return nil, err
	}
	var out []thumbJob
	var j thumbJob
	_, err = pgx.ForEachRow(rows, []any{&j.id, &j.contentID, &j.requested, &j.etag}, func() error {
		out = append(out, j)
		return nil
	})
	return out, err
}

func (l *Library) thumbLoop(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	l.runThumbJobs(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-l.thumbKick:
		}
		l.runThumbJobs(ctx)
	}
}

// runThumbJobs works through due thumbnails: explicit refreshes first, then
// artworks without one, then failed ones due for a retry.
func (l *Library) runThumbJobs(ctx context.Context) {
	defer l.updateThumbCounts(ctx)
	var tried []string
	errorsLeft := thumbErrorsPerRun
	for ctx.Err() == nil {
		jobs, err := l.dueThumbJobs(ctx, tried)
		if err != nil {
			l.log.Error("list thumbnail jobs", "err", err)
			return
		}
		if len(jobs) == 0 {
			return
		}
		for _, j := range jobs {
			tried = append(tried, j.id)
			outcome, err := l.runThumbJob(ctx, j)
			if errors.Is(err, errTVUnavailable) || ctx.Err() != nil {
				return
			}
			if outcome == thumbError {
				if errorsLeft--; errorsLeft == 0 {
					return
				}
			}
		}
	}
}

func (l *Library) runThumbJob(ctx context.Context, j thumbJob) (string, error) {
	origin := thumbOrigin(j.id)
	var data []byte
	var err error
	if origin == "source" {
		data, err = l.renderSource(ctx, j.id)
	} else {
		data, _, err = l.o.TV.Thumbnail(tv.Background(ctx), j.contentID)
		switch {
		case tvUnavailable(err):
			return "", fmt.Errorf("%w: %w", errTVUnavailable, err)
		case errors.Is(err, tv.ErrNotFound):
			l.o.Metrics.ThumbnailJobs.WithLabelValues(origin, thumbMissing).Inc()
			return thumbMissing, l.storeThumbMissing(ctx, j.id)
		}
	}
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		l.log.Warn("thumbnail job failed", "artwork", j.id, "err", err)
		l.o.Metrics.ThumbnailJobs.WithLabelValues(origin, thumbError).Inc()
		return thumbError, l.storeThumbError(ctx, j.id, err)
	}
	etag, err := l.storeThumbOK(ctx, j.id, data)
	if err != nil {
		return "", err
	}
	l.o.Metrics.ThumbnailJobs.WithLabelValues(origin, thumbOK).Inc()
	if j.requested && etag != j.etag {
		l.changed([]string{j.id}, nil)
	}
	return thumbOK, nil
}

var thumbStatuses = []string{thumbOK, thumbMissing, thumbError, "pending"}

func (l *Library) updateThumbCounts(ctx context.Context) {
	rows, err := l.pool.Query(context.WithoutCancel(ctx), `
		SELECT CASE WHEN a.hash IS NULL THEN 'tv' ELSE 'source' END, COALESCE(t.status, 'pending'), count(*)
		FROM artworks a LEFT JOIN thumbnails t ON t.artwork_id = a.id GROUP BY 1, 2`)
	if err != nil {
		return
	}
	counts := map[string]float64{}
	var origin, status string
	var n int64
	pgx.ForEachRow(rows, []any{&origin, &status, &n}, func() error {
		counts[origin+"/"+status] = float64(n)
		return nil
	})
	for _, o := range []string{"source", "tv"} {
		for _, st := range thumbStatuses {
			l.o.Metrics.Thumbnails.WithLabelValues(o, st).Set(counts[o+"/"+st])
		}
	}
}

// RefreshThumbnails queues a new download of TV-only artworks' thumbnails,
// ahead of background prefetching. It returns once queued; a LibraryChanged
// event follows when each new thumbnail is stored. errs[i] is the outcome
// for ids[i].
func (l *Library) RefreshThumbnails(ctx context.Context, ids []string) ([]error, error) {
	if err := checkBatch(ids); err != nil {
		return nil, err
	}
	if err := l.requireRunning(); err != nil {
		return nil, err
	}
	out := make([]error, len(ids))
	queued := false
	for i, id := range ids {
		if !isTVArtwork(id) {
			out[i] = errs.New(ErrInvalid, "%s is not a TV-only artwork; its thumbnail comes from the source file", id)
			continue
		}
		tag, err := l.pool.Exec(ctx, `
			INSERT INTO thumbnails (artwork_id, status, requested_at, next_attempt_at)
			SELECT id, 'error', now(), now() FROM artworks WHERE id = $1
			ON CONFLICT (artwork_id) DO UPDATE SET requested_at = now()`, id)
		switch {
		case err != nil:
			out[i] = err
		case tag.RowsAffected() == 0:
			out[i] = errs.New(ErrNotFound, "no artwork %s", id)
		default:
			queued = true
		}
	}
	if queued {
		signal(l.thumbKick)
	}
	return out, nil
}

const maxBatch = 500

func checkBatch(ids []string) error {
	switch {
	case len(ids) == 0:
		return errs.New(ErrInvalid, "ids is required")
	case len(ids) > maxBatch:
		return errs.New(ErrInvalid, "at most %d ids per request", maxBatch)
	}
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			return errs.New(ErrInvalid, "ids must not be empty")
		}
	}
	return nil
}

type renderedThumb struct {
	data []byte
	err  error
}

// queueThumb renders the thumbnail of newly read content unless one is
// already stored; content addressing means it never changes.
func (l *Library) queueThumb(ctx context.Context, thumbs map[string]renderedThumb, hash string, data []byte) {
	if _, done := thumbs[hash]; done || l.hasThumb(ctx, artworkID(hash)) {
		return
	}
	t, err := renderThumb(data)
	thumbs[hash] = renderedThumb{data: t, err: err}
}

// storeThumbs stores thumbnails rendered during a scan, once their
// artworks exist.
func (l *Library) storeThumbs(ctx context.Context, thumbs map[string]renderedThumb) {
	for hash, t := range thumbs {
		id := artworkID(hash)
		var err error
		if t.err != nil {
			l.o.Metrics.ThumbnailJobs.WithLabelValues("source", thumbError).Inc()
			err = l.storeThumbError(ctx, id, t.err)
		} else {
			l.o.Metrics.ThumbnailJobs.WithLabelValues("source", thumbOK).Inc()
			_, err = l.storeThumbOK(ctx, id, t.data)
		}
		if err != nil {
			l.log.Error("store thumbnail", "artwork", id, "err", err)
		}
	}
	if len(thumbs) > 0 {
		l.updateThumbCounts(ctx)
	}
}
