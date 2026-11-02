package library

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/yuha0/vigo/gen/go/vigo/core/v1"
	"github.com/yuha0/vigo/internal/errs"
	"github.com/yuha0/vigo/internal/imageprep"
	"github.com/yuha0/vigo/internal/source"
)

const (
	defaultPageSize = 100
	maxPageSize     = 500
)

const artworkColumns = `a.id, a.tv_content_id, COALESCE(t.content_type, ''), a.width, a.height, a.size_bytes,
	a.sync_state, a.sync_error, a.updated_at, a.added_at`

const artworkFrom = `FROM artworks a LEFT JOIN tv_items t ON t.content_id = a.tv_content_id AND t.gone_at IS NULL`

func scanArtwork(row pgx.Row) (*corev1.LibraryArtwork, time.Time, error) {
	var (
		a       corev1.LibraryArtwork
		cid     *string
		ctype   string
		state   string
		updated time.Time
		added   time.Time
	)
	if err := row.Scan(&a.Id, &cid, &ctype, &a.Width, &a.Height, &a.SizeBytes, &state, &a.SyncError, &updated, &added); err != nil {
		return nil, time.Time{}, err
	}
	if cid != nil {
		a.TvContentId = *cid
	}
	a.ContentType = corev1.ContentType(corev1.ContentType_value["CONTENT_TYPE_"+strings.ToUpper(ctype)])
	a.SyncState = corev1.SyncState(corev1.SyncState_value["SYNC_STATE_"+state])
	a.UpdatedAt = timestamppb.New(updated)
	return &a, added, nil
}

// decorate fills in tags and source refs.
func (l *Library) decorate(ctx context.Context, arts []*corev1.LibraryArtwork) error {
	if len(arts) == 0 {
		return nil
	}
	byID := map[string]*corev1.LibraryArtwork{}
	ids := make([]string, len(arts))
	for i, a := range arts {
		byID[a.Id] = a
		ids[i] = a.Id
	}
	rows, err := l.pool.Query(ctx, `SELECT artwork_id, tag, origin FROM artwork_tags WHERE artwork_id = ANY($1) ORDER BY origin, tag`, ids)
	if err != nil {
		return err
	}
	var id, tag, origin string
	if _, err := pgx.ForEachRow(rows, []any{&id, &tag, &origin}, func() error {
		o := corev1.TagOrigin_TAG_ORIGIN_SYSTEM
		if origin == "user" {
			o = corev1.TagOrigin_TAG_ORIGIN_USER
		}
		byID[id].Tags = append(byID[id].Tags, &corev1.Tag{Name: tag, Origin: o})
		return nil
	}); err != nil {
		return err
	}
	rows, err = l.pool.Query(ctx, `
		SELECT a.id, s.source_id, s.path FROM artworks a JOIN source_items s ON s.hash = a.hash
		WHERE a.id = ANY($1) ORDER BY s.source_id, s.path`, ids)
	if err != nil {
		return err
	}
	var sid, p string
	_, err = pgx.ForEachRow(rows, []any{&id, &sid, &p}, func() error {
		byID[id].Sources = append(byID[id].Sources, &corev1.SourceRef{SourceId: sid, Path: p})
		return nil
	})
	return err
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

type pageToken struct {
	added time.Time
	id    string
}

func encodeToken(t pageToken) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(t.added.UnixMicro(), 10) + "|" + t.id))
}

func decodeToken(s string) (pageToken, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return pageToken{}, errs.New(ErrInvalid, "page_token is invalid")
	}
	us, id, ok := strings.Cut(string(b), "|")
	n, err := strconv.ParseInt(us, 10, 64)
	if !ok || err != nil || id == "" {
		return pageToken{}, errs.New(ErrInvalid, "page_token is invalid")
	}
	return pageToken{added: time.UnixMicro(n), id: id}, nil
}

// List returns artworks newest first, filtered by tags (a tag ending in "/"
// matches as a prefix) and a case-insensitive substring query.
func (l *Library) List(ctx context.Context, req *corev1.ListArtworksRequest) (*corev1.ListArtworksResponse, error) {
	var (
		where []string
		args  []any
	)
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if !req.IncludeDeleting {
		where = append(where, fmt.Sprintf("a.sync_state NOT IN (%s, %s)", arg(statePendingDelete), arg(stateDeleting)))
	}
	for _, tag := range req.Tags {
		if tag == "" {
			continue
		}
		if strings.HasSuffix(tag, "/") {
			where = append(where, "EXISTS (SELECT 1 FROM artwork_tags g WHERE g.artwork_id = a.id AND g.tag LIKE "+arg(escapeLike(tag)+"%")+")")
		} else {
			where = append(where, "EXISTS (SELECT 1 FROM artwork_tags g WHERE g.artwork_id = a.id AND g.tag = "+arg(tag)+")")
		}
	}
	if q := strings.TrimSpace(req.Query); q != "" {
		where = append(where, "a.search_text LIKE "+arg("%"+escapeLike(strings.ToLower(q))+"%"))
	}
	filter := ""
	if len(where) > 0 {
		filter = " WHERE " + strings.Join(where, " AND ")
	}
	var total int32
	if err := l.pool.QueryRow(ctx, "SELECT count(*) FROM artworks a"+filter, args...).Scan(&total); err != nil {
		return nil, err
	}

	size := int(req.PageSize)
	if size <= 0 {
		size = defaultPageSize
	}
	size = min(size, maxPageSize)
	page := filter
	if req.PageToken != "" {
		tok, err := decodeToken(req.PageToken)
		if err != nil {
			return nil, err
		}
		cond := fmt.Sprintf("(a.added_at < %s OR (a.added_at = %s AND a.id > %s))", arg(tok.added), arg(tok.added), arg(tok.id))
		if page == "" {
			page = " WHERE " + cond
		} else {
			page += " AND " + cond
		}
	}
	rows, err := l.pool.Query(ctx, "SELECT "+artworkColumns+" "+artworkFrom+page+
		" ORDER BY a.added_at DESC, a.id ASC LIMIT "+arg(size+1), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var arts []*corev1.LibraryArtwork
	var added []time.Time
	for rows.Next() {
		a, t, err := scanArtwork(rows)
		if err != nil {
			return nil, err
		}
		arts, added = append(arts, a), append(added, t)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	resp := &corev1.ListArtworksResponse{Total: total}
	if len(arts) > size {
		arts = arts[:size]
		resp.NextPageToken = encodeToken(pageToken{added: added[size-1], id: arts[size-1].Id})
	}
	if err := l.decorate(ctx, arts); err != nil {
		return nil, err
	}
	resp.Artworks = arts
	return resp, nil
}

func (l *Library) Get(ctx context.Context, id string) (*corev1.LibraryArtwork, error) {
	a, _, err := scanArtwork(l.pool.QueryRow(ctx, "SELECT "+artworkColumns+" "+artworkFrom+" WHERE a.id = $1", id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errs.New(ErrNotFound, "no artwork %s", id)
	}
	if err != nil {
		return nil, err
	}
	if err := l.decorate(ctx, []*corev1.LibraryArtwork{a}); err != nil {
		return nil, err
	}
	return a, nil
}

// ListTags counts tags over artworks that are not being deleted.
func (l *Library) ListTags(ctx context.Context, prefix string) ([]*corev1.TagCount, error) {
	rows, err := l.pool.Query(ctx, `
		SELECT g.tag, g.origin, count(*) FROM artwork_tags g JOIN artworks a ON a.id = g.artwork_id
		WHERE a.sync_state NOT IN ($1, $2) AND g.tag LIKE $3
		GROUP BY g.tag, g.origin ORDER BY g.tag`, statePendingDelete, stateDeleting, escapeLike(prefix)+"%")
	if err != nil {
		return nil, err
	}
	var out []*corev1.TagCount
	var tag, origin string
	var n int32
	_, err = pgx.ForEachRow(rows, []any{&tag, &origin, &n}, func() error {
		o := corev1.TagOrigin_TAG_ORIGIN_SYSTEM
		if origin == "user" {
			o = corev1.TagOrigin_TAG_ORIGIN_USER
		}
		out = append(out, &corev1.TagCount{Tag: &corev1.Tag{Name: tag, Origin: o}, Count: n})
		return nil
	})
	return out, err
}

func (l *Library) UpdateTags(ctx context.Context, id string, add, remove []string) (*corev1.LibraryArtwork, error) {
	var adds, removes []string
	for _, t := range add {
		c, err := checkUserTag(t)
		if err != nil {
			return nil, err
		}
		adds = append(adds, c)
	}
	for _, t := range remove {
		c, err := checkUserTag(t)
		if err != nil {
			return nil, err
		}
		removes = append(removes, c)
	}
	err := pgx.BeginFunc(ctx, l.pool, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM artworks WHERE id = $1)`, id).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return errs.New(ErrNotFound, "no artwork %s", id)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM artwork_tags WHERE artwork_id = $1 AND origin = 'user' AND tag = ANY($2)`, id, removes); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO artwork_tags (artwork_id, tag, origin) SELECT $1, unnest($2::text[]), 'user'
			ON CONFLICT (artwork_id, tag) DO NOTHING`, id, adds); err != nil {
			return err
		}
		return settle(ctx, tx, []string{id})
	})
	if err != nil {
		return nil, err
	}
	l.changed([]string{id}, nil)
	return l.Get(ctx, id)
}

func (l *Library) requireRunning() error {
	if !l.isRunning() {
		return ErrNotRunning
	}
	return nil
}

// Delete deletes an artwork everywhere: its files from every source, then
// through the reconciler its TV copy. A TV-only artwork is deleted from the
// TV directly, only if it is an upload.
func (l *Library) Delete(ctx context.Context, id string) (*corev1.LibraryArtwork, error) {
	if err := l.requireRunning(); err != nil {
		return nil, err
	}
	a, err := l.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if isTVArtwork(id) {
		if a.ContentType != corev1.ContentType_CONTENT_TYPE_MOBILE {
			return nil, errs.New(ErrInvalid, "only uploads can be deleted from the TV")
		}
		l.tvMu.Lock()
		err := l.deleteFromTV(ctx, id, a.TvContentId)
		l.tvMu.Unlock()
		if err != nil {
			return nil, err
		}
		a.SyncState = corev1.SyncState_SYNC_STATE_DELETING
		return a, nil
	}
	if a.SyncState == corev1.SyncState_SYNC_STATE_DELETING {
		return a, nil
	}
	writers := map[string]source.Writer{}
	for _, r := range a.Sources {
		s, ok := l.o.Sources.Get(r.SourceId)
		if !ok {
			return nil, errs.New(ErrUnsupported, "source %s is not configured", r.SourceId)
		}
		w, ok := s.(source.Writer)
		if !ok {
			return nil, errs.New(ErrUnsupported, "source %s is read-only", r.SourceId)
		}
		writers[r.SourceId] = w
	}
	for _, r := range a.Sources {
		mu := l.srcMu[r.SourceId]
		mu.Lock()
		err := writers[r.SourceId].Remove(ctx, r.Path)
		if err == nil {
			_, err = l.pool.Exec(ctx, `DELETE FROM source_items WHERE source_id = $1 AND path = $2`, r.SourceId, r.Path)
		}
		mu.Unlock()
		if err != nil {
			return nil, err
		}
		l.log.Info("deleted artwork file", "artwork", id, "source", r.SourceId, "path", r.Path)
	}
	if err := settle(ctx, l.pool, []string{id}); err != nil {
		return nil, err
	}
	l.changed([]string{id}, nil)
	for _, r := range a.Sources {
		l.kick(r.SourceId)
	}
	if after, err := l.Get(ctx, id); err == nil {
		return after, nil
	}
	a.Sources = nil
	return a, nil
}

// Crop replaces the file of an artwork in place with the cropped image and
// returns the new artwork. Only the first copy in an editable source is
// edited; other copies keep the old content.
func (l *Library) Crop(ctx context.Context, id string, rect *corev1.Rect) (*corev1.LibraryArtwork, error) {
	if err := l.requireRunning(); err != nil {
		return nil, err
	}
	if rect == nil {
		return nil, errs.New(ErrInvalid, "rect is required")
	}
	a, err := l.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	hash, ok := hashOf(id)
	if !ok {
		return nil, errs.New(ErrUnsupported, "TV-only artworks cannot be edited")
	}
	var src source.Source
	var ed source.Editor
	var p string
	for _, r := range a.Sources {
		if s, ok := l.o.Sources.Get(r.SourceId); ok {
			if e, ok := s.(source.Editor); ok {
				src, ed, p = s, e, r.Path
				break
			}
		}
	}
	if ed == nil {
		return nil, errs.New(ErrUnsupported, "no source of this artwork supports editing")
	}
	mu := l.srcMu[src.ID()]
	mu.Lock()
	defer mu.Unlock()
	data, got, err := readFile(ctx, src, p)
	if err != nil {
		return nil, err
	}
	if got != hash {
		return nil, errs.New(ErrInvalid, "%s changed since it was indexed; try again after the next sync", p)
	}
	r := image.Rect(int(rect.X), int(rect.Y), int(rect.X+rect.Width), int(rect.Y+rect.Height))
	out, err := imageprep.Crop(data, r)
	if err != nil {
		return nil, errs.Wrap(ErrInvalid, err)
	}
	e, err := ed.Replace(ctx, p, bytes.NewReader(out))
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(out)
	newID, err := l.index(ctx, src.ID(), e, out, hex.EncodeToString(sum[:]))
	if err != nil {
		return nil, err
	}
	l.log.Info("cropped artwork", "artwork", id, "new_artwork", newID, "source", src.ID(), "path", p)
	l.changed([]string{id, newID}, nil)
	l.kick(src.ID())
	return l.Get(ctx, newID)
}

// index records one file just written by vigo and settles the artworks
// involved. Callers hold the source's srcMu.
func (l *Library) index(ctx context.Context, sid string, e source.Entry, data []byte, hash string) (string, error) {
	e.ModTime = mtime(e.ModTime)
	it, err := probeItem(sid, e, data, hash)
	if err != nil {
		return "", err
	}
	var affected []string
	err = pgx.BeginFunc(ctx, l.pool, func(tx pgx.Tx) error {
		var old string
		err := tx.QueryRow(ctx, `SELECT hash FROM source_items WHERE source_id = $1 AND path = $2`, sid, e.Path).Scan(&old)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if old != "" {
			affected = append(affected, artworkID(old))
		}
		if err := upsertItem(ctx, tx, it); err != nil {
			return err
		}
		if err := ensureArtwork(ctx, tx, it); err != nil {
			return err
		}
		affected = append(affected, artworkID(hash))
		return settle(ctx, tx, affected)
	})
	return artworkID(hash), err
}

// AddFile writes a browser upload into a source and queues its upload to
// the TV. dir is relative to the source root; the name is sanitized and
// its extension matched to the content.
func (l *Library) AddFile(ctx context.Context, sourceID, dir, name string, data []byte) (*corev1.LibraryArtwork, error) {
	if err := l.requireRunning(); err != nil {
		return nil, err
	}
	s, ok := l.o.Sources.Get(sourceID)
	if !ok {
		return nil, errs.New(ErrNotFound, "no source %s", sourceID)
	}
	w, ok := s.(source.Writer)
	if !ok {
		return nil, errs.New(ErrUnsupported, "source %s is read-only", sourceID)
	}
	_, _, format, err := imageprep.Probe(data)
	if err != nil {
		return nil, errs.New(ErrInvalid, "only JPEG and PNG files are accepted")
	}
	cleanDir, err := source.CleanPath(strings.TrimLeft(dir, "/"))
	if err != nil {
		return nil, errs.New(ErrInvalid, "path %q must be a directory relative to the source root, without \"..\" or hidden components", dir)
	}
	rel := path.Join(cleanDir, fileName(name, format))
	mu := l.srcMu[sourceID]
	mu.Lock()
	defer mu.Unlock()
	e, err := w.Create(ctx, rel, bytes.NewReader(data), false)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	id, err := l.index(ctx, sourceID, e, data, hex.EncodeToString(sum[:]))
	if err != nil {
		return nil, err
	}
	l.log.Info("stored browser upload", "source", sourceID, "path", e.Path, "artwork", id)
	l.changed([]string{id}, nil)
	l.kick(sourceID)
	return l.Get(ctx, id)
}

func (l *Library) ListSources(ctx context.Context) ([]*corev1.Source, error) {
	var out []*corev1.Source
	for _, s := range l.o.Sources.All() {
		p := &corev1.Source{
			Id:       s.ID(),
			Kind:     s.Kind(),
			Display:  s.Display(),
			Writable: source.CanWrite(s),
			Editable: source.CanEdit(s),
			Syncing:  l.isSyncing(s.ID()),
		}
		var synced *time.Time
		err := l.pool.QueryRow(ctx, `
			SELECT last_synced_at, last_error,
			       (SELECT count(DISTINCT hash) FROM source_items WHERE source_id = $1)
			FROM sources WHERE id = $1`, s.ID()).Scan(&synced, &p.LastError, &p.ArtworkCount)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		if synced != nil {
			p.LastSyncedAt = timestamppb.New(*synced)
		}
		out = append(out, p)
	}
	return out, nil
}

// Sync queues a pass of the source.
func (l *Library) Sync(ctx context.Context, sourceID string) error {
	if err := l.requireRunning(); err != nil {
		return err
	}
	if _, ok := l.o.Sources.Get(sourceID); !ok {
		return errs.New(ErrNotFound, "no source %s", sourceID)
	}
	l.kick(sourceID)
	return nil
}
