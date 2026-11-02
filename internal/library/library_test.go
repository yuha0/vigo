package library

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	corev1 "github.com/yuha0/vigo/gen/go/vigo/core/v1"
	"github.com/yuha0/vigo/internal/db/dbtest"
	"github.com/yuha0/vigo/internal/imageprep"
	"github.com/yuha0/vigo/internal/metrics"
	"github.com/yuha0/vigo/internal/samsung/fake"
	"github.com/yuha0/vigo/internal/source"
	_ "github.com/yuha0/vigo/internal/source/file"
	"github.com/yuha0/vigo/internal/tv"
)

type env struct {
	t    *testing.T
	ctx  context.Context
	lib  *Library
	tv   *fake.TV
	svc  *tv.Service
	root string
	src  source.Source
	m    *metrics.Metrics
}

// smallPrepare runs the upload pipeline on a 64x36 canvas: the sync logic
// does not depend on the size, and 3840x2160 under -race costs seconds per
// upload. TestAdd keeps the real size end to end.
func smallPrepare(r io.Reader, fit imageprep.Fit) ([]byte, error) {
	return imageprep.PrepareTo(r, fit, 64, 36)
}

func newEnv(t *testing.T, sourceQuery string) *env {
	t.Helper()
	return newEnvWith(t, sourceQuery, smallPrepare)
}

func newEnvWith(t *testing.T, sourceQuery string, prepare func(io.Reader, imageprep.Fit) ([]byte, error)) *env {
	t.Helper()
	e := dbtest.Open(t)
	ctx := context.Background()
	st := tv.NewMemoryStore()
	st.SetToken(ctx, fake.Token, "vigo")
	ftv := fake.New()
	m := metrics.New()
	svc, err := tv.New(tv.Options{
		Defaults:       tv.Config{Host: "tv.test", Port: 8002, ClientName: "vigo"},
		Store:          st,
		NewClient:      ftv.Client,
		Metrics:        m,
		PollInterval:   time.Hour,
		RequestTimeout: 2 * time.Second,
		ReconnectMin:   5 * time.Millisecond,
		ReconnectMax:   20 * time.Millisecond,
		Prepare:        prepare,
	})
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { svc.Run(runCtx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	root := t.TempDir()
	reg, err := source.NewRegistry([]string{"file://" + root + "?id=art" + sourceQuery})
	if err != nil {
		t.Fatal(err)
	}
	lib, err := New(Options{DB: e.DB, TV: svc, Sources: reg, CacheDir: t.TempDir(), Metrics: m})
	if err != nil {
		t.Fatal(err)
	}
	svc.SetLibraryHook(lib)
	if err := lib.prepare(ctx); err != nil {
		t.Fatal(err)
	}
	src, _ := reg.Get("art")
	env := &env{t: t, ctx: ctx, lib: lib, tv: ftv, svc: svc, root: root, src: src, m: m}
	env.waitConnected()
	return env
}

func (e *env) setReachable(ok bool) {
	e.t.Helper()
	e.tv.SetReachable(ok)
	if ok {
		e.waitConnected()
		return
	}
	deadline := time.Now().Add(5 * time.Second)
	for e.svc.Status().Connection == corev1.ConnectionState_CONNECTION_STATE_CONNECTED {
		if time.Now().After(deadline) {
			e.t.Fatal("TV never disconnected")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (e *env) waitConnected() {
	e.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if e.svc.Status().Connection == corev1.ConnectionState_CONNECTION_STATE_CONNECTED {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	e.t.Fatal("TV never connected")
}

// drain applies queued TV events, as the mirror loop would.
func (e *env) drain() {
	for {
		select {
		case ev := <-e.lib.events:
			if ev.deleted {
				e.lib.contentDeleted(e.ctx, ev.contentID, "event")
			} else {
				e.lib.refreshMirror(e.ctx)
			}
		case <-e.lib.mirrorKick:
		default:
			return
		}
	}
}

func (e *env) pass() {
	e.t.Helper()
	e.drain()
	e.lib.pass(e.ctx, e.src)
	e.drain()
}

// writeImage writes a distinct small JPEG; seed varies the content.
func (e *env) writeImage(rel string, w, h, seed int) {
	e.t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(seed * 40), uint8(x), uint8(y), 255})
		}
	}
	var buf bytes.Buffer
	jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90})
	p := filepath.Join(e.root, rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		e.t.Fatal(err)
	}
	// Distinct mtimes even within one clock tick.
	when := time.Now().Add(time.Duration(seed) * time.Second)
	os.Chtimes(p, when, when)
}

func (e *env) artworks() map[string]*corev1.LibraryArtwork {
	e.t.Helper()
	resp, err := e.lib.List(e.ctx, &corev1.ListArtworksRequest{IncludeDeleting: true, PageSize: 500})
	if err != nil {
		e.t.Fatal(err)
	}
	out := map[string]*corev1.LibraryArtwork{}
	for _, a := range resp.Artworks {
		out[a.Id] = a
	}
	return out
}

// byPath finds the source artwork stored at rel.
func (e *env) byPath(rel string) *corev1.LibraryArtwork {
	for _, a := range e.artworks() {
		for _, r := range a.Sources {
			if r.Path == rel {
				return a
			}
		}
	}
	return nil
}

func (e *env) fileExists(rel string) bool {
	_, err := os.Stat(filepath.Join(e.root, rel))
	return err == nil
}

func hasTag(a *corev1.LibraryArtwork, tag string) bool {
	return slices.ContainsFunc(a.Tags, func(t *corev1.Tag) bool { return t.Name == tag })
}

func hasWarning(svc *tv.Service, code string) bool {
	return slices.ContainsFunc(svc.Status().Warnings, func(w *corev1.Warning) bool { return w.Code == code })
}

// seeded creates six linked artworks, a.jpg through f.jpg.
func seeded(t *testing.T) *env {
	t.Helper()
	e := newEnv(t, "")
	for i, n := range []string{"a", "b", "c", "d", "e", "f"} {
		e.writeImage(n+".jpg", 64, 36, i+1)
	}
	e.pass()
	for _, n := range []string{"a", "b", "c", "d", "e", "f"} {
		a := e.byPath(n + ".jpg")
		if a == nil || a.SyncState != corev1.SyncState_SYNC_STATE_IN_SYNC || a.TvContentId == "" {
			t.Fatalf("%s.jpg not in sync after the first pass: %v", n, a)
		}
	}
	return e
}

// TestAdd runs the real upload pipeline, 3840x2160, end to end.
func TestAdd(t *testing.T) {
	var prepared []int
	var mu sync.Mutex
	e := newEnvWith(t, "", func(r io.Reader, fit imageprep.Fit) ([]byte, error) {
		out, err := imageprep.Prepare(r, fit)
		if err == nil {
			w, h, _, _ := imageprep.Probe(out)
			mu.Lock()
			prepared = append(prepared, w, h)
			mu.Unlock()
		}
		return out, err
	})
	defer func() {
		mu.Lock()
		defer mu.Unlock()
		if len(prepared) == 0 || prepared[0] != imageprep.Width || prepared[1] != imageprep.Height {
			t.Errorf("uploads were prepared at %v, want 3840x2160", prepared)
		}
	}()
	e.writeImage("2024/winter.jpg", 64, 36, 1)
	e.writeImage("portrait.jpg", 36, 64, 2)
	e.writeImage(".hidden.jpg", 64, 36, 3)
	e.pass()

	a := e.byPath("2024/winter.jpg")
	if a == nil {
		t.Fatal("not indexed")
	}
	if !strings.HasPrefix(a.Id, "sha256:") || a.SyncState != corev1.SyncState_SYNC_STATE_IN_SYNC || !e.tv.Has(a.TvContentId) {
		t.Errorf("artwork = %v", a)
	}
	for _, tag := range []string{"source/art", "folder/2024", "status/on-tv", "issue/low-resolution", "tv-category/MY-C0002"} {
		if !hasTag(a, tag) {
			t.Errorf("missing tag %s in %v", tag, a.Tags)
		}
	}
	if hasTag(a, "issue/aspect-ratio") {
		t.Error("16:9 image tagged aspect-ratio")
	}
	if p := e.byPath("portrait.jpg"); p == nil || !hasTag(p, "issue/aspect-ratio") || hasTag(p, "folder/.") {
		t.Errorf("portrait = %v", p)
	}
	if e.byPath(".hidden.jpg") != nil {
		t.Error("hidden file indexed")
	}
	tvOnly := e.artworks()["tv:SAM-S2701"]
	if tvOnly == nil || tvOnly.SyncState != corev1.SyncState_SYNC_STATE_TV_ONLY || !hasTag(tvOnly, "source/art-store") {
		t.Errorf("Art Store item = %v", tvOnly)
	}
	if up := e.artworks()["tv:MY_F0001"]; up == nil || !hasTag(up, "source/upload") {
		t.Errorf("outside upload = %v", up)
	}
	if _, ok := e.artworks()["tv:"+a.TvContentId]; ok {
		t.Error("vigo's upload also listed as TV-only")
	}
	before := len(e.tv.Calls())
	e.pass()
	for _, c := range e.tv.Calls()[before:] {
		if c == "send_image" {
			t.Error("unchanged file uploaded again")
		}
	}
}

func TestChange(t *testing.T) {
	e := seeded(t)
	old := e.byPath("a.jpg")
	e.writeImage("a.jpg", 64, 36, 9)
	e.pass()
	now := e.byPath("a.jpg")
	if now == nil || now.Id == old.Id || now.SyncState != corev1.SyncState_SYNC_STATE_IN_SYNC {
		t.Fatalf("after change: %v", now)
	}
	if e.tv.Has(old.TvContentId) {
		t.Error("old TV copy not deleted")
	}
	if _, ok := e.artworks()[old.Id]; ok {
		t.Error("old artwork still listed")
	}
}

func TestRemoveFromSource(t *testing.T) {
	e := seeded(t)
	a := e.byPath("b.jpg")
	os.Remove(filepath.Join(e.root, "b.jpg"))
	e.pass()
	if e.tv.Has(a.TvContentId) {
		t.Error("TV copy not deleted")
	}
	if _, ok := e.artworks()[a.Id]; ok {
		t.Error("artwork still listed")
	}
	var pending int
	e.lib.pool.QueryRow(e.ctx, `SELECT count(*) FROM tv_deletions`).Scan(&pending)
	if pending != 0 {
		t.Errorf("%d deletion records left", pending)
	}
	if !e.fileExists("a.jpg") {
		t.Error("an unrelated file was touched")
	}
}

func TestDeletedOnTVByEvent(t *testing.T) {
	e := seeded(t)
	a := e.byPath("c.jpg")
	// Someone deletes the upload on the TV; the art channel reports it.
	if _, err := e.svc.DeleteArt(e.ctx, []string{a.TvContentId}); err != nil {
		t.Fatal(err)
	}
	e.drain()
	if got := e.artworks()[a.Id]; got == nil || got.SyncState != corev1.SyncState_SYNC_STATE_PENDING_DELETE {
		t.Fatalf("after the event: %v", got)
	}
	e.pass()
	if e.fileExists("c.jpg") {
		t.Error("source file not deleted")
	}
	if _, ok := e.artworks()[a.Id]; ok {
		t.Error("artwork still listed")
	}
	if !e.fileExists("d.jpg") {
		t.Error("an unrelated file was deleted")
	}
}

func TestDeletedOnTVByTwoListings(t *testing.T) {
	e := seeded(t)
	a := e.byPath("d.jpg")
	e.tv.RemoveSilently(a.TvContentId)

	if err := e.lib.refreshMirror(e.ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.artworks()[a.Id]; got.SyncState != corev1.SyncState_SYNC_STATE_IN_SYNC {
		t.Fatalf("one listing already counted as proof: %v", got)
	}
	// Failed listings never count, whatever the reason.
	e.tv.FailNext("get_content_list", errors.New("truncated reply"))
	if err := e.lib.refreshMirror(e.ctx); err == nil {
		t.Fatal("failed listing reported success")
	}
	e.setReachable(false)
	e.lib.refreshMirror(e.ctx)
	e.lib.refreshMirror(e.ctx)
	e.setReachable(true)
	if got := e.artworks()[a.Id]; got.SyncState != corev1.SyncState_SYNC_STATE_IN_SYNC {
		t.Fatalf("failed listings counted: %v", got)
	}
	if err := e.lib.refreshMirror(e.ctx); err != nil {
		t.Fatal(err)
	}
	if got := e.artworks()[a.Id]; got == nil || got.SyncState != corev1.SyncState_SYNC_STATE_PENDING_DELETE {
		t.Fatalf("after the second complete listing: %v", got)
	}
	e.pass()
	if e.fileExists("d.jpg") {
		t.Error("source file not deleted")
	}
}

func TestVigoDeletionIsNotMirroredBack(t *testing.T) {
	e := seeded(t)
	a := e.byPath("e.jpg")
	// vigo deletes the TV copy while the file is still in the source, as
	// happens when a file reappears after vigo queued its deletion. The
	// recorded deletion keeps the TV's image_deleted from deleting the file.
	e.lib.tvMu.Lock()
	err := e.lib.deleteFromTV(e.ctx, a.Id, a.TvContentId, true)
	e.lib.tvMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	e.drain()
	e.pass()
	if !e.fileExists("e.jpg") {
		t.Fatal("vigo's own TV deletion deleted the source file")
	}
	got := e.byPath("e.jpg")
	if got.SyncState != corev1.SyncState_SYNC_STATE_IN_SYNC || got.TvContentId == a.TvContentId {
		t.Errorf("expected a fresh upload, got %v", got)
	}
}

// No deletion threshold: deleting many artworks is applied like deleting
// one (docs/design.md "No deletion threshold").
func TestMassDeletion(t *testing.T) {
	t.Run("files removed from the source", func(t *testing.T) {
		e := seeded(t)
		var gone []*corev1.LibraryArtwork
		for _, n := range []string{"a", "b", "c", "d", "e", "f"} {
			gone = append(gone, e.byPath(n+".jpg"))
			os.Remove(filepath.Join(e.root, n+".jpg"))
		}
		e.pass()
		for _, a := range gone {
			if e.tv.Has(a.TvContentId) {
				t.Errorf("%s still on the TV", a.TvContentId)
			}
		}
		if hasWarning(e.svc, "sync_halted") {
			t.Error("sync_halted warning raised")
		}
	})
	t.Run("artworks deleted on the TV", func(t *testing.T) {
		e := seeded(t)
		for _, n := range []string{"a", "b", "c"} {
			if _, err := e.svc.DeleteArt(e.ctx, []string{e.byPath(n + ".jpg").TvContentId}); err != nil {
				t.Fatal(err)
			}
		}
		e.pass()
		for _, n := range []string{"a", "b", "c"} {
			if e.fileExists(n + ".jpg") {
				t.Errorf("%s.jpg not deleted", n)
			}
		}
		if !e.fileExists("d.jpg") {
			t.Error("an unrelated file was deleted")
		}
	})
}

// An unmounted share must never read as "every file was deleted".
func TestUnreadableRootIsAnError(t *testing.T) {
	e := seeded(t)
	hidden := e.root + ".unmounted"
	os.Rename(e.root, hidden)
	defer os.Rename(hidden, e.root)
	e.pass()
	var lastErr string
	e.lib.pool.QueryRow(e.ctx, `SELECT last_error FROM sources WHERE id = 'art'`).Scan(&lastErr)
	if lastErr == "" {
		t.Error("no error recorded")
	}
	for _, n := range []string{"a", "b", "c", "d", "e", "f"} {
		a := e.byPath(n + ".jpg")
		if a == nil || a.SyncState != corev1.SyncState_SYNC_STATE_IN_SYNC || !e.tv.Has(a.TvContentId) {
			t.Errorf("%s.jpg changed by a failed scan: %v", n, a)
		}
	}
	if testutil.ToFloat64(e.m.SourceSyncErrors.WithLabelValues("art")) != 1 {
		t.Error("error not counted")
	}
}

func TestTVUnavailableKeepsPending(t *testing.T) {
	e := seeded(t)
	e.setReachable(false)
	e.writeImage("later.jpg", 64, 36, 8)
	removed := e.byPath("f.jpg")
	os.Remove(filepath.Join(e.root, "f.jpg"))
	e.pass()
	if a := e.byPath("later.jpg"); a == nil || a.SyncState != corev1.SyncState_SYNC_STATE_PENDING_UPLOAD || a.SyncError != "" {
		t.Fatalf("new file while the TV is away: %v", a)
	}
	if a := e.artworks()[removed.Id]; a == nil || a.SyncState != corev1.SyncState_SYNC_STATE_PENDING_DELETE || a.SyncError != "" {
		t.Fatalf("removed file while the TV is away: %v", a)
	}
	var lastErr string
	e.lib.pool.QueryRow(e.ctx, `SELECT last_error FROM sources WHERE id = 'art'`).Scan(&lastErr)
	if lastErr != "" {
		t.Errorf("an absent TV counted as a source error: %q", lastErr)
	}

	e.setReachable(true)
	e.pass()
	if a := e.byPath("later.jpg"); a.SyncState != corev1.SyncState_SYNC_STATE_IN_SYNC {
		t.Errorf("after the TV returned: %v", a)
	}
	if e.tv.Has(removed.TvContentId) {
		t.Error("pending deletion not sent after the TV returned")
	}
}

func TestDeleteArtwork(t *testing.T) {
	e := seeded(t)
	a := e.byPath("a.jpg")
	got, err := e.lib.Delete(e.ctx, a.Id)
	if err != nil {
		t.Fatal(err)
	}
	if got.SyncState != corev1.SyncState_SYNC_STATE_PENDING_DELETE || e.fileExists("a.jpg") {
		t.Errorf("after delete: %v, file exists %v", got, e.fileExists("a.jpg"))
	}
	e.pass()
	if e.tv.Has(a.TvContentId) {
		t.Error("TV copy not deleted")
	}

	if _, err := e.lib.Delete(e.ctx, "tv:SAM-S2701"); !errors.Is(err, ErrInvalid) {
		t.Errorf("deleting Art Store content: %v", err)
	}
	if _, err := e.lib.Delete(e.ctx, "tv:MY_F0001"); err != nil {
		t.Errorf("deleting an outside upload: %v", err)
	}
	if e.tv.Has("MY_F0001") {
		t.Error("outside upload still on the TV")
	}
	e.drain()
	if _, ok := e.artworks()["tv:MY_F0001"]; ok {
		t.Error("deleted TV-only artwork still listed")
	}
}

func TestReadOnlySource(t *testing.T) {
	e := newEnv(t, "&readonly=true")
	for i, n := range []string{"a", "b", "c", "d", "e", "f"} {
		e.writeImage(n+".jpg", 64, 36, i+1)
	}
	e.pass()
	a := e.byPath("a.jpg")
	if _, err := e.lib.Delete(e.ctx, a.Id); !errors.Is(err, ErrUnsupported) {
		t.Errorf("delete in a read-only source: %v", err)
	}
	e.svc.DeleteArt(e.ctx, []string{a.TvContentId})
	e.pass()
	got := e.byPath("a.jpg")
	if !e.fileExists("a.jpg") || got.SyncState != corev1.SyncState_SYNC_STATE_ERROR || !strings.Contains(got.SyncError, "read-only") {
		t.Errorf("after a TV deletion in a read-only source: %v", got)
	}
	e.pass()
	if got := e.byPath("a.jpg"); got.TvContentId != "" {
		t.Error("re-uploaded an artwork deleted on the TV")
	}
}

func TestAddFileAndCrop(t *testing.T) {
	e := seeded(t)
	img := func(shade uint8) []byte {
		m := image.NewRGBA(image.Rect(0, 0, 400, 300))
		for i := range m.Pix {
			m.Pix[i] = shade
		}
		var buf bytes.Buffer
		jpeg.Encode(&buf, m, nil)
		return buf.Bytes()
	}

	tests := []struct {
		dir, name string
		data      []byte
		wantPath  string
		wantErr   error
	}{
		{dir: "phone/2026", name: "IMG_1.JPG", data: img(10), wantPath: "phone/2026/IMG_1.JPG"},
		{dir: "", name: "../../etc/x.jpeg", data: img(20), wantPath: "x.jpeg"},
		{dir: "/", name: ".profile", data: img(30), wantPath: "profile.jpg"},
		{dir: "../up", name: "a.jpg", data: img(40), wantErr: ErrInvalid},
		{dir: "x/.git", name: "a.jpg", data: img(40), wantErr: ErrInvalid},
		{dir: "", name: "a.jpg", data: img(40), wantErr: source.ErrExists},
		{dir: "", name: "notes.txt", data: []byte("hello"), wantErr: ErrInvalid},
	}
	for _, tt := range tests {
		a, err := e.lib.AddFile(e.ctx, "art", tt.dir, tt.name, tt.data)
		if !errors.Is(err, tt.wantErr) {
			t.Errorf("AddFile(%q, %q): %v, want %v", tt.dir, tt.name, err, tt.wantErr)
			continue
		}
		if err != nil {
			continue
		}
		if len(a.Sources) == 0 || !slices.ContainsFunc(a.Sources, func(r *corev1.SourceRef) bool { return r.Path == tt.wantPath }) {
			t.Errorf("AddFile(%q, %q) stored at %v, want %s", tt.dir, tt.name, a.Sources, tt.wantPath)
		}
		if a.SyncState != corev1.SyncState_SYNC_STATE_PENDING_UPLOAD || a.Width != 400 || a.Height != 300 {
			t.Errorf("new upload = %v", a)
		}
	}
	e.pass()
	up := e.byPath("phone/2026/IMG_1.JPG")
	if up.SyncState != corev1.SyncState_SYNC_STATE_IN_SYNC || !hasTag(up, "issue/aspect-ratio") {
		t.Fatalf("after pass: %v", up)
	}

	cropped, err := e.lib.Crop(e.ctx, up.Id, &corev1.Rect{X: 0, Y: 37, Width: 400, Height: 225})
	if err != nil {
		t.Fatal(err)
	}
	if cropped.Id == up.Id || cropped.Width != 400 || cropped.Height != 225 || hasTag(cropped, "issue/aspect-ratio") {
		t.Errorf("cropped = %v", cropped)
	}
	if _, err := e.lib.Crop(e.ctx, cropped.Id, &corev1.Rect{X: 300, Y: 0, Width: 400, Height: 225}); !errors.Is(err, ErrInvalid) {
		t.Errorf("crop outside the image: %v", err)
	}
	e.pass()
	if e.tv.Has(up.TvContentId) {
		t.Error("old TV copy not deleted")
	}
	if got := e.byPath("phone/2026/IMG_1.JPG"); got.Id != cropped.Id || got.SyncState != corev1.SyncState_SYNC_STATE_IN_SYNC {
		t.Errorf("after the crop synced: %v", got)
	}

	thumb, etag, err := e.lib.Thumbnail(e.ctx, cropped.Id)
	if err != nil || etag == "" {
		t.Fatal(err)
	}
	if img, err := jpeg.Decode(bytes.NewReader(thumb)); err != nil || img.Bounds().Dx() > thumbSize {
		t.Errorf("thumbnail: %v", err)
	}
	if _, _, err := e.lib.Preview(e.ctx, "tv:SAM-S2701"); !errors.Is(err, ErrNotFound) {
		t.Errorf("preview of a TV-only artwork: %v", err)
	}
	if b, _, err := e.lib.Thumbnail(e.ctx, "tv:SAM-S2701"); err != nil || len(b) == 0 {
		t.Errorf("TV thumbnail: %v", err)
	}
}

func TestQueries(t *testing.T) {
	e := newEnv(t, "")
	names := []string{"2023/fog.jpg", "2024/snow_day.jpg", "2024/sun.jpg", "2024/dec/eve.jpg", "100%.jpg", "misc/x.jpg", "misc/y.jpg"}
	for i, n := range names {
		e.writeImage(n, 64, 36, i+1)
	}
	e.setReachable(false)
	e.pass()
	e.setReachable(true)
	e.lib.refreshMirror(e.ctx)

	snow := e.byPath("2024/snow_day.jpg")
	if _, err := e.lib.UpdateTags(e.ctx, snow.Id, []string{"favourite", " Winter "}, nil); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][2][]string{{{"folder/x"}, nil}, {nil, {"status/on-tv"}}, {{""}, nil}} {
		if _, err := e.lib.UpdateTags(e.ctx, snow.Id, bad[0], bad[1]); !errors.Is(err, ErrInvalid) {
			t.Errorf("UpdateTags(%v): %v", bad, err)
		}
	}

	count := func(req *corev1.ListArtworksRequest) int {
		t.Helper()
		resp, err := e.lib.List(e.ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		return int(resp.Total)
	}
	tests := []struct {
		name string
		req  *corev1.ListArtworksRequest
		want int
	}{
		{"all source files", &corev1.ListArtworksRequest{Tags: []string{"source/art"}}, 7},
		{"exact folder", &corev1.ListArtworksRequest{Tags: []string{"folder/2024"}}, 2},
		{"folder prefix", &corev1.ListArtworksRequest{Tags: []string{"folder/2024/"}}, 1},
		{"any folder", &corev1.ListArtworksRequest{Tags: []string{"folder/"}}, 6},
		{"two tags", &corev1.ListArtworksRequest{Tags: []string{"folder/2024", "favourite"}}, 1},
		{"user tag", &corev1.ListArtworksRequest{Tags: []string{"Winter"}}, 1},
		{"search file name", &corev1.ListArtworksRequest{Query: "SNOW"}, 1},
		{"search underscore literally", &corev1.ListArtworksRequest{Query: "w_d"}, 1},
		{"search percent literally", &corev1.ListArtworksRequest{Query: "100%"}, 1},
		{"search tag", &corev1.ListArtworksRequest{Query: "favour"}, 1},
		{"search content id", &corev1.ListArtworksRequest{Query: "sam-s270"}, 7},
		{"art store", &corev1.ListArtworksRequest{Tags: []string{"source/art-store"}}, 8},
		{"pending uploads have no TV tag", &corev1.ListArtworksRequest{Tags: []string{"source/art", "status/on-tv"}}, 0},
		{"nothing", &corev1.ListArtworksRequest{Query: "zzz"}, 0},
		{"origin sources", &corev1.ListArtworksRequest{Origin: corev1.ArtworkOrigin_ARTWORK_ORIGIN_SOURCES}, 7},
		{"origin TV only", &corev1.ListArtworksRequest{Origin: corev1.ArtworkOrigin_ARTWORK_ORIGIN_TV_ONLY}, 10},
		{"origin unspecified is both", &corev1.ListArtworksRequest{}, 17},
		{"origin TV only with a tag", &corev1.ListArtworksRequest{Origin: corev1.ArtworkOrigin_ARTWORK_ORIGIN_TV_ONLY, Tags: []string{"source/art-store"}}, 8},
		{"origin sources with a TV-only tag", &corev1.ListArtworksRequest{Origin: corev1.ArtworkOrigin_ARTWORK_ORIGIN_SOURCES, Tags: []string{"source/art-store"}}, 0},
		{"origin sources with a query", &corev1.ListArtworksRequest{Origin: corev1.ArtworkOrigin_ARTWORK_ORIGIN_SOURCES, Query: "snow"}, 1},
		{"origin TV only with a query", &corev1.ListArtworksRequest{Origin: corev1.ArtworkOrigin_ARTWORK_ORIGIN_TV_ONLY, Query: "sam-s270"}, 7},
		{"origin TV only excludes source files", &corev1.ListArtworksRequest{Origin: corev1.ArtworkOrigin_ARTWORK_ORIGIN_TV_ONLY, Query: "snow"}, 0},
	}
	for _, tt := range tests {
		if got := count(tt.req); got != tt.want {
			t.Errorf("%s: %d, want %d", tt.name, got, tt.want)
		}
	}

	var seen []string
	token := ""
	for page := 0; ; page++ {
		resp, err := e.lib.List(e.ctx, &corev1.ListArtworksRequest{Tags: []string{"source/art"}, PageSize: 3, PageToken: token})
		if err != nil {
			t.Fatal(err)
		}
		if resp.Total != 7 || len(resp.Artworks) > 3 {
			t.Fatalf("page %d: total %d, %d items", page, resp.Total, len(resp.Artworks))
		}
		for _, a := range resp.Artworks {
			seen = append(seen, a.Id)
		}
		if token = resp.NextPageToken; token == "" {
			break
		}
	}
	slices.Sort(seen)
	if len(slices.Compact(seen)) != 7 {
		t.Errorf("paging returned %d distinct artworks, want 7", len(seen))
	}
	if _, err := e.lib.List(e.ctx, &corev1.ListArtworksRequest{PageToken: "garbage!"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad token: %v", err)
	}

	tags, err := e.lib.ListTags(e.ctx, "folder/")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int32{}
	for _, tc := range tags {
		got[tc.Tag.Name] = tc.Count
	}
	want := map[string]int32{"folder/2023": 1, "folder/2024": 2, "folder/2024/dec": 1, "folder/misc": 2}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("tags = %v, want %v", got, want)
	}
	all, _ := e.lib.ListTags(e.ctx, "")
	if !slices.ContainsFunc(all, func(tc *corev1.TagCount) bool {
		return tc.Tag.Name == "favourite" && tc.Tag.Origin == corev1.TagOrigin_TAG_ORIGIN_USER
	}) {
		t.Error("user tag not listed as user")
	}
}

func TestCropPNGKeepsItsFormat(t *testing.T) {
	e := seeded(t)
	m := image.NewRGBA(image.Rect(0, 0, 400, 300))
	for i := range m.Pix {
		m.Pix[i] = byte(i)
	}
	var buf bytes.Buffer
	png.Encode(&buf, m)
	a, err := e.lib.AddFile(e.ctx, "art", "probe", "t43.png", buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	cropped, err := e.lib.Crop(e.ctx, a.Id, &corev1.Rect{X: 0, Y: 37, Width: 400, Height: 225})
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(e.root, "probe/t43.png"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(b, []byte("\x89PNG")) {
		t.Errorf("probe/t43.png holds %x after the crop", b[:4])
	}
	if len(cropped.Sources) != 1 || cropped.Sources[0].Path != "probe/t43.png" || cropped.Height != 225 {
		t.Errorf("cropped = %v", cropped)
	}
}

func TestDeletePrunesEmptyFolders(t *testing.T) {
	e := seeded(t)
	e.writeImage("trip/day1/only.jpg", 64, 36, 11)
	e.pass()
	if _, err := e.lib.Delete(e.ctx, e.byPath("trip/day1/only.jpg").Id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.root, "trip")); err == nil {
		t.Error("empty folder left behind")
	}
}

// TestFakeTVRestoredAcrossRestart covers --fake-tv development: a new fake
// TV restored from the database must not look like mass deletion.
func TestFakeTVRestoredAcrossRestart(t *testing.T) {
	e := seeded(t)
	items, err := LoadTVContent(e.ctx, e.lib.pool)
	if err != nil {
		t.Fatal(err)
	}
	restored := fake.New()
	restored.Restore(items)
	for _, a := range e.artworks() {
		if a.TvContentId != "" && !restored.Has(a.TvContentId) {
			t.Errorf("%s missing from the restored TV", a.TvContentId)
		}
	}

	// Point the library at a service over the restored TV and list twice.
	st := tv.NewMemoryStore()
	st.SetToken(e.ctx, fake.Token, "vigo")
	svc, err := tv.New(tv.Options{Defaults: tv.Config{Host: "tv.test"}, Store: st, NewClient: restored.Client, PollInterval: time.Hour, ReconnectMin: 5 * time.Millisecond, Prepare: smallPrepare})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(e.ctx)
	done := make(chan struct{})
	go func() { svc.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	for svc.Status().Connection != corev1.ConnectionState_CONNECTION_STATE_CONNECTED {
		time.Sleep(5 * time.Millisecond)
	}
	e.lib.o.TV = svc
	for range 3 {
		if err := e.lib.refreshMirror(e.ctx); err != nil {
			t.Fatal(err)
		}
	}
	e.lib.pass(e.ctx, e.src)
	for _, n := range []string{"a", "b", "c", "d", "e", "f"} {
		if a := e.byPath(n + ".jpg"); a == nil || a.SyncState != corev1.SyncState_SYNC_STATE_IN_SYNC {
			t.Errorf("%s.jpg after restart: %v", n, a)
		}
	}
	art, err := svc.Upload(e.ctx, bytes.NewReader(mustJPEG(t)), imageprep.FitPad, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range e.artworks() {
		if a.TvContentId == art.ContentId && a.Id != "tv:"+art.ContentId {
			t.Errorf("restored TV reused content id %s", art.ContentId)
		}
	}
}

func mustJPEG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 32, 18)), nil)
	return buf.Bytes()
}

// Files mid-write elsewhere can be empty or truncated: they are skipped,
// never errors, and never read as removals.
func TestEmptyAndUndecodableFilesAreSkipped(t *testing.T) {
	e := seeded(t)
	a := e.byPath("a.jpg")
	os.WriteFile(filepath.Join(e.root, "reserved.jpg"), nil, 0o644)
	os.WriteFile(filepath.Join(e.root, "partial.jpg"), []byte{0xFF, 0xD8, 0xFF}, 0o644)
	os.WriteFile(filepath.Join(e.root, "a.jpg"), nil, 0o644)
	e.pass()
	var lastErr string
	e.lib.pool.QueryRow(e.ctx, `SELECT last_error FROM sources WHERE id = 'art'`).Scan(&lastErr)
	if lastErr != "" {
		t.Errorf("last_error = %q", lastErr)
	}
	if e.byPath("reserved.jpg") != nil || e.byPath("partial.jpg") != nil {
		t.Error("an incomplete file was indexed")
	}
	if got := e.byPath("a.jpg"); got == nil || got.Id != a.Id || !e.tv.Has(a.TvContentId) {
		t.Errorf("a.jpg emptied mid-write was treated as changed or removed: %v", got)
	}
}

// The library mirrors the TV before it reports itself running, so a fresh
// database does not briefly show no TV artworks.
func TestTVArtworksIndexedBeforeServing(t *testing.T) {
	e := newEnv(t, "")
	if _, err := e.lib.Get(e.ctx, "tv:SAM-S2701"); err != nil {
		t.Fatalf("TV-only artwork missing right after start: %v", err)
	}
}

// Readers never see an existing artwork disappear while mirror passes,
// uploads and reconciles run concurrently.
func TestIndexStaysConsistentDuringSync(t *testing.T) {
	e := seeded(t)
	before := e.artworks()
	ctx, cancel := context.WithCancel(e.ctx)
	var wg sync.WaitGroup
	var failures []string
	var mu sync.Mutex
	wg.Go(func() {
		for ctx.Err() == nil {
			resp, err := e.lib.List(context.Background(), &corev1.ListArtworksRequest{IncludeDeleting: true, PageSize: 500})
			if err != nil {
				continue
			}
			seen := map[string]bool{}
			for _, a := range resp.Artworks {
				seen[a.Id] = true
			}
			for id := range before {
				if !seen[id] {
					mu.Lock()
					failures = append(failures, id)
					mu.Unlock()
				}
			}
		}
	})
	for i := range 4 {
		e.writeImage(fmt.Sprintf("new%d.jpg", i), 64, 36, 20+i)
		e.pass()
		e.lib.refreshMirror(e.ctx)
	}
	cancel()
	wg.Wait()
	if len(failures) > 0 {
		t.Errorf("artworks vanished from the index during sync, e.g. %v (%d sightings)", failures[0], len(failures))
	}
}
