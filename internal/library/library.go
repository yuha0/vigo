// Package library is vigo's artwork index: a mirror of the TV's content,
// the files in configured sources, tags, and the reconciler that keeps
// sources and the TV in sync. See docs/design.md "Art library and sources",
// whose safety rules this package implements.
package library

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/singleflight"

	corev1 "github.com/yuha0/vigo/gen/go/vigo/core/v1"
	"github.com/yuha0/vigo/internal/db"
	"github.com/yuha0/vigo/internal/imageprep"
	"github.com/yuha0/vigo/internal/metrics"
	"github.com/yuha0/vigo/internal/samsung"
	"github.com/yuha0/vigo/internal/source"
	"github.com/yuha0/vigo/internal/tv"
)

var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("invalid argument")
	// ErrUnsupported: the source lacks the capability, e.g. it is read-only.
	ErrUnsupported = errors.New("not supported by the source")
	// ErrNotRunning: this replica is not the leader.
	ErrNotRunning = errors.New("the library runs on the leader replica only")
)

// TV is what the library needs from internal/tv.
type TV interface {
	ListArt(ctx context.Context, category string) ([]*corev1.Artwork, []string, error)
	Upload(ctx context.Context, r io.Reader, fit imageprep.Fit, matte string) (*corev1.Artwork, error)
	DeleteArt(ctx context.Context, ids []string) ([]string, error)
	Thumbnail(ctx context.Context, contentID string) ([]byte, string, error)
	SetWarning(code, msg string)
	ClearWarning(code string)
	EmitLibraryChanged(artworkIDs, sourceIDs []string)
}

var _ TV = (*tv.Service)(nil)

type Options struct {
	DB      *db.DB
	TV      TV
	Sources *source.Registry
	// CacheDir holds generated thumbnails and previews, per source. It is
	// emptied by New.
	CacheDir string
	Metrics  *metrics.Metrics
	Log      *slog.Logger
	// SyncInterval spaces reconciliation passes of each source.
	SyncInterval time.Duration
	// MirrorInterval spaces full re-reads of the TV's content list.
	MirrorInterval time.Duration
}

type Library struct {
	o    Options
	pool *pgxpool.Pool
	log  *slog.Logger

	mu      sync.Mutex
	running bool
	syncing map[string]bool
	kicks   map[string]chan struct{}

	// srcMu serializes the passes of one source with the API calls that
	// change its files, so a pass never mistakes vigo's own edits for
	// outside changes.
	srcMu map[string]*sync.Mutex
	// mirrorMu serializes changes to tv_items and artwork links.
	mirrorMu sync.Mutex
	// tvMu keeps uploads and deletions one at a time.
	tvMu sync.Mutex

	mirrorKick chan struct{}
	thumbKick  chan struct{}
	deleteKick chan struct{}
	events     chan tvEvent
	media      singleflight.Group
}

type tvEvent struct {
	contentID string
	deleted   bool
}

func New(o Options) (*Library, error) {
	if o.DB == nil || o.TV == nil || o.Sources == nil {
		return nil, errors.New("library: DB, TV and Sources are required")
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Metrics == nil {
		o.Metrics = metrics.New()
	}
	if o.SyncInterval <= 0 {
		o.SyncInterval = 10 * time.Minute
	}
	if o.MirrorInterval <= 0 {
		o.MirrorInterval = 5 * time.Minute
	}
	if o.CacheDir == "" {
		o.CacheDir = filepath.Join(os.TempDir(), "vigo-core-cache")
	}
	if err := emptyDir(o.CacheDir); err != nil {
		return nil, err
	}
	l := &Library{
		o:          o,
		pool:       o.DB.Pool(),
		log:        o.Log,
		syncing:    map[string]bool{},
		kicks:      map[string]chan struct{}{},
		srcMu:      map[string]*sync.Mutex{},
		mirrorKick: make(chan struct{}, 1),
		thumbKick:  make(chan struct{}, 1),
		deleteKick: make(chan struct{}, 1),
		events:     make(chan tvEvent, 256),
	}
	for _, s := range o.Sources.All() {
		l.kicks[s.ID()] = make(chan struct{}, 1)
		l.srcMu[s.ID()] = &sync.Mutex{}
	}
	return l, nil
}

// emptyDir removes the contents of dir, creating it if needed. The cache is
// disposable; the database is the source of truth.
func emptyDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// Run makes the library active on the leader: it records the configured
// sources, mirrors the TV and reconciles each source until ctx ends.
func (l *Library) Run(ctx context.Context) error {
	if err := l.prepare(ctx); err != nil {
		return err
	}
	defer func() {
		l.mu.Lock()
		l.running = false
		l.mu.Unlock()
	}()
	var wg sync.WaitGroup
	wg.Go(func() { l.mirrorLoop(ctx) })
	wg.Go(func() { l.thumbLoop(ctx) })
	for _, s := range l.o.Sources.All() {
		wg.Go(func() { l.sourceLoop(ctx, s) })
	}
	wg.Wait()
	return nil
}

func (l *Library) prepare(ctx context.Context) error {
	for _, s := range l.o.Sources.All() {
		_, err := l.pool.Exec(ctx, `
			INSERT INTO sources (id, kind, display) VALUES ($1, $2, $3)
			ON CONFLICT (id) DO UPDATE SET kind = $2, display = $3`, s.ID(), s.Kind(), s.Display())
		if err != nil {
			return err
		}
	}
	// Transfers interrupted by a restart are retried; recorded deletions
	// stay recorded.
	if _, err := l.pool.Exec(ctx, `
		UPDATE artworks SET sync_state = CASE sync_state WHEN 'UPLOADING' THEN 'PENDING_UPLOAD' ELSE 'PENDING_DELETE' END
		WHERE sync_state IN ('UPLOADING', 'DELETING')`); err != nil {
		return err
	}
	// Mirror the TV once before serving, so a fresh database does not
	// briefly show no TV artworks. An unavailable TV fails fast and is left
	// to the mirror loop.
	l.refreshMirror(ctx)
	l.mu.Lock()
	l.running = true
	l.mu.Unlock()
	l.updateCounts(ctx)
	return nil
}

func (l *Library) isRunning() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.running
}

func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// TVContentAdded implements tv.LibraryHook.
func (l *Library) TVContentAdded(contentID string) {
	select {
	case l.events <- tvEvent{contentID: contentID}:
	default:
		signal(l.mirrorKick)
	}
}

// TVContentDeleted implements tv.LibraryHook. The event is proof of
// deletion; if it cannot be queued, two complete listings will prove it.
func (l *Library) TVContentDeleted(contentID string) {
	select {
	case l.events <- tvEvent{contentID: contentID, deleted: true}:
	default:
		l.log.Warn("library event queue full; relying on listings", "content_id", contentID)
		signal(l.mirrorKick)
	}
}

// TVRefreshed implements tv.LibraryHook.
func (l *Library) TVRefreshed() { signal(l.mirrorKick) }

func (l *Library) mirrorLoop(ctx context.Context) {
	t := time.NewTicker(l.o.MirrorInterval)
	defer t.Stop()
	l.refreshMirror(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			l.refreshMirror(ctx)
		case <-l.mirrorKick:
			l.refreshMirror(ctx)
		case <-l.deleteKick:
			l.tvMu.Lock()
			l.deletions(ctx)
			l.tvMu.Unlock()
		case ev := <-l.events:
			if ev.deleted {
				l.contentDeleted(ctx, ev.contentID, "event")
			} else {
				l.refreshMirror(ctx)
			}
		}
	}
}

func (l *Library) sourceLoop(ctx context.Context, s source.Source) {
	t := time.NewTicker(l.o.SyncInterval)
	defer t.Stop()
	kick := l.kicks[s.ID()]
	l.pass(ctx, s)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-kick:
		}
		l.pass(ctx, s)
	}
}

// kick queues a pass of the source.
func (l *Library) kick(sourceID string) {
	if ch, ok := l.kicks[sourceID]; ok {
		signal(ch)
	}
}

func (l *Library) setSyncing(id string, on bool) {
	l.mu.Lock()
	l.syncing[id] = on
	l.mu.Unlock()
	l.changed(nil, []string{id})
}

func (l *Library) isSyncing(id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.syncing[id]
}

// changed announces changes on WatchEvents and refreshes the gauges.
func (l *Library) changed(artworkIDs, sourceIDs []string) {
	if len(artworkIDs) == 0 && len(sourceIDs) == 0 {
		return
	}
	l.o.TV.EmitLibraryChanged(artworkIDs, sourceIDs)
	l.updateCounts(context.Background())
}

var allStates = []string{"in_sync", "pending_upload", "uploading", "pending_delete", "deleting", "error", "tv_only"}

func (l *Library) updateCounts(ctx context.Context) {
	rows, err := l.pool.Query(ctx, `SELECT sync_state, count(*) FROM artworks GROUP BY sync_state`)
	if err != nil {
		return
	}
	counts := map[string]float64{}
	for rows.Next() {
		var st string
		var n int64
		if rows.Scan(&st, &n) == nil {
			counts[strings.ToLower(st)] = float64(n)
		}
	}
	rows.Close()
	for _, st := range allStates {
		l.o.Metrics.LibraryArtworks.WithLabelValues(st).Set(counts[st])
	}
}

// tvUnavailable reports errors meaning the TV cannot be asked right now.
// Work is left pending for a later pass rather than marked failed.
func tvUnavailable(err error) bool {
	for _, e := range []error{
		tv.ErrUnreachable, tv.ErrNotConnected, tv.ErrNoArtHost, tv.ErrNotPaired,
		tv.ErrStandbyReplica, tv.ErrPoweredOff, tv.ErrNoHost, samsung.ErrUnauthorized,
	} {
		if errors.Is(err, e) {
			return true
		}
	}
	return false
}
