package library

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "github.com/yuha0/vigo/gen/go/vigo/core/v1"
	"github.com/yuha0/vigo/internal/samsung"
	"github.com/yuha0/vigo/internal/tv"
)

// recordingTV notes whether each thumbnail request was background work.
type recordingTV struct {
	TV
	mu    sync.Mutex
	calls []string
}

func (r *recordingTV) Thumbnail(ctx context.Context, id string) ([]byte, string, error) {
	kind := "user"
	if tv.IsBackground(ctx) {
		kind = "background"
	}
	r.mu.Lock()
	r.calls = append(r.calls, kind+":"+id)
	r.mu.Unlock()
	return r.TV.Thumbnail(ctx, id)
}

func (r *recordingTV) kinds() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

func (e *env) thumbRow(id string) (status string, attempts int, next time.Time, etag string) {
	var n *time.Time
	e.lib.pool.QueryRow(e.ctx, `SELECT status, attempts, next_attempt_at, etag FROM thumbnails WHERE artwork_id = $1`, id).Scan(&status, &attempts, &n, &etag)
	if n != nil {
		next = *n
	}
	return
}

func (e *env) tvThumbnailCalls() int {
	n := 0
	for _, c := range e.tv.Calls() {
		if strings.HasPrefix(c, "thumbnail:") {
			n++
		}
	}
	return n
}

func TestBackoff(t *testing.T) {
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute}
	for i, w := range want {
		if got := backoff(i + 1); got != w {
			t.Errorf("backoff(%d) = %v, want %v", i+1, got, w)
		}
	}
	if got := backoff(50); got != 6*time.Hour {
		t.Errorf("backoff caps at %v", got)
	}
}

func TestSourceThumbnailsStoredWhenIndexed(t *testing.T) {
	e := seeded(t)
	for _, n := range []string{"a", "b", "c"} {
		a := e.byPath(n + ".jpg")
		data, etag, status, err := e.lib.StoredThumbnail(e.ctx, a.Id)
		if err != nil || status != thumbOK || len(data) == 0 || etag == "" {
			t.Errorf("%s: status %q, %d bytes, %v", n, status, len(data), err)
		}
	}
	if n := e.tvThumbnailCalls(); n != 0 {
		t.Errorf("source thumbnails asked the TV %d times", n)
	}
}

func TestPrefetchFillsTVThumbnails(t *testing.T) {
	e := newEnv(t, "")
	rec := &recordingTV{TV: e.lib.o.TV}
	e.lib.o.TV = rec
	e.tv.FailNext("thumbnail", samsung.ErrNoData)
	e.lib.runThumbJobs(e.ctx)

	counts := map[string]int{}
	rows, _ := e.lib.pool.Query(e.ctx, `SELECT status FROM thumbnails WHERE artwork_id LIKE 'tv:%'`)
	for rows.Next() {
		var st string
		rows.Scan(&st)
		counts[st]++
	}
	rows.Close()
	if counts[thumbOK] != 9 || counts[thumbMissing] != 1 {
		t.Errorf("statuses = %v, want 9 ok and 1 missing", counts)
	}
	for _, k := range rec.kinds() {
		if !strings.HasPrefix(k, "background:") {
			t.Errorf("prefetch made an interactive TV call: %s", k)
		}
	}

	// Stored thumbnails are served from the database, and never fetched
	// again by later runs.
	e.tv.ResetCalls()
	data, _, err := e.lib.Thumbnail(e.ctx, "tv:SAM-S2702")
	if err != nil || len(data) == 0 {
		t.Fatal(err)
	}
	e.lib.runThumbJobs(e.ctx)
	if n := e.tvThumbnailCalls(); n != 0 {
		t.Errorf("served or re-ran with %d TV calls", n)
	}
	if got := testutilGauge(e, "tv", thumbOK); got != 9 {
		t.Errorf("vigo_thumbnails{tv,ok} = %v", got)
	}
}

func TestMissingThumbnailIsRecheckedWeekly(t *testing.T) {
	e := newEnv(t, "")
	e.tv.FailNext("thumbnail", samsung.ErrNoData)
	e.lib.runThumbJobs(e.ctx)
	var missing string
	e.lib.pool.QueryRow(e.ctx, `SELECT artwork_id FROM thumbnails WHERE status = 'missing'`).Scan(&missing)
	if missing == "" {
		t.Fatal("nothing recorded as missing")
	}
	_, _, next, _ := e.thumbRow(missing)
	if d := time.Until(next); d < 6*24*time.Hour || d > 8*24*time.Hour {
		t.Errorf("recheck in %v, want a week", d)
	}
	if _, _, err := e.lib.Thumbnail(e.ctx, missing); !errors.Is(err, ErrNotFound) {
		t.Errorf("serving a missing thumbnail: %v", err)
	}
	e.tv.ResetCalls()
	e.lib.runThumbJobs(e.ctx)
	if n := e.tvThumbnailCalls(); n != 0 {
		t.Errorf("rechecked early: %d calls", n)
	}
	e.lib.pool.Exec(e.ctx, `UPDATE thumbnails SET next_attempt_at = now() - interval '1 second' WHERE artwork_id = $1`, missing)
	e.lib.runThumbJobs(e.ctx)
	if st, _, _, _ := e.thumbRow(missing); st != thumbOK {
		t.Errorf("after the weekly recheck: %s", st)
	}
}

func TestThumbnailErrorsBackOff(t *testing.T) {
	e := newEnv(t, "")
	e.lib.runThumbJobs(e.ctx)
	e.lib.pool.Exec(e.ctx, `DELETE FROM thumbnails WHERE artwork_id = 'tv:SAM-S2705'`)
	for attempt, wantGap := range []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute} {
		e.tv.FailNext("thumbnail", errors.New("tls: handshake failure"))
		e.lib.pool.Exec(e.ctx, `UPDATE thumbnails SET next_attempt_at = now() - interval '1 second' WHERE artwork_id = 'tv:SAM-S2705'`)
		e.lib.runThumbJobs(e.ctx)
		st, attempts, next, _ := e.thumbRow("tv:SAM-S2705")
		if st != thumbError || attempts != attempt+1 {
			t.Fatalf("attempt %d: status %s, attempts %d", attempt+1, st, attempts)
		}
		if d := time.Until(next); d < wantGap-10*time.Second || d > wantGap+10*time.Second {
			t.Errorf("attempt %d: next try in %v, want %v", attempt+1, d, wantGap)
		}
	}
	e.tv.ResetCalls()
	e.lib.runThumbJobs(e.ctx)
	if n := e.tvThumbnailCalls(); n != 0 {
		t.Errorf("retried before the backoff: %d calls", n)
	}
	e.lib.pool.Exec(e.ctx, `UPDATE thumbnails SET next_attempt_at = now() - interval '1 second' WHERE artwork_id = 'tv:SAM-S2705'`)
	e.lib.runThumbJobs(e.ctx)
	if st, attempts, _, _ := e.thumbRow("tv:SAM-S2705"); st != thumbOK || attempts != 0 {
		t.Errorf("after recovering: %s, %d attempts", st, attempts)
	}
}

func TestUserThumbnailIsNotBackground(t *testing.T) {
	e := newEnv(t, "")
	rec := &recordingTV{TV: e.lib.o.TV}
	e.lib.o.TV = rec
	if _, _, err := e.lib.Thumbnail(e.ctx, "tv:SAM-S2703"); err != nil {
		t.Fatal(err)
	}
	if got := rec.kinds(); !slices.Equal(got, []string{"user:SAM-S2703"}) {
		t.Errorf("calls = %v", got)
	}
	if st, _, _, _ := e.thumbRow("tv:SAM-S2703"); st != thumbOK {
		t.Errorf("live fetch not stored: %s", st)
	}
}

func TestThumbnailRowsGoWithTheirArtwork(t *testing.T) {
	e := seeded(t)
	e.lib.runThumbJobs(e.ctx)
	a := e.byPath("a.jpg")
	if _, err := e.lib.Delete(e.ctx, a.Id); err != nil {
		t.Fatal(err)
	}
	e.pass()
	e.tv.RemoveSilently("SAM-S2706")
	e.lib.refreshMirror(e.ctx)
	e.lib.refreshMirror(e.ctx)
	for _, id := range []string{a.Id, "tv:SAM-S2706"} {
		if st, _, _, _ := e.thumbRow(id); st != "" {
			t.Errorf("%s: thumbnail row left behind (%s)", id, st)
		}
	}
}

func TestUploadMapsToSourceThumbnail(t *testing.T) {
	e := seeded(t)
	a := e.byPath("b.jpg")
	id, err := e.lib.ArtworkForContent(e.ctx, a.TvContentId)
	if err != nil || id != a.Id {
		t.Errorf("content %s maps to %q, %v; want %s", a.TvContentId, id, err, a.Id)
	}
}

func distinctJPEG(t *testing.T, shade uint8) []byte {
	t.Helper()
	m := image.NewRGBA(image.Rect(0, 0, 32, 18))
	for i := range m.Pix {
		m.Pix[i] = shade
	}
	m.Set(0, 0, color.RGBA{shade, 0, 0, 255})
	var buf bytes.Buffer
	jpeg.Encode(&buf, m, nil)
	return buf.Bytes()
}

func TestRefreshThumbnails(t *testing.T) {
	e := newEnv(t, "")
	e.lib.runThumbJobs(e.ctx)
	_, _, _, before := e.thumbRow("tv:SAM-S2702")
	_, events, cancel := e.svc.Subscribe()
	defer cancel()

	e.tv.SetThumbnail("SAM-S2702", distinctJPEG(t, 200))
	results, err := e.lib.RefreshThumbnails(e.ctx, []string{"tv:SAM-S2702", "sha256:abc", "tv:NOPE"})
	if err != nil {
		t.Fatal(err)
	}
	if results[0] != nil || !errors.Is(results[1], ErrInvalid) || !errors.Is(results[2], ErrNotFound) {
		t.Fatalf("results = %v", results)
	}
	e.lib.runThumbJobs(e.ctx)
	_, _, _, after := e.thumbRow("tv:SAM-S2702")
	if after == before || after == "" {
		t.Errorf("etag %q → %q: not replaced", before, after)
	}
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-events:
			if slices.Contains(ev.GetLibraryChanged().GetArtworkIds(), "tv:SAM-S2702") {
				return
			}
		case <-deadline:
			t.Fatal("no LibraryChanged for the refreshed thumbnail")
		}
	}
}

func TestDeleteMany(t *testing.T) {
	e := seeded(t)
	src := e.byPath("c.jpg")
	_, events, cancel := e.svc.Subscribe()
	defer cancel()

	ids := []string{src.Id, "tv:MY_F0001", "tv:MY_F0002", "tv:SAM-S2701", "sha256:nope"}
	results, err := e.lib.DeleteMany(e.ctx, ids)
	if err != nil {
		t.Fatal(err)
	}
	if results[0] != nil || results[1] != nil || results[2] != nil {
		t.Errorf("successes failed: %v", results)
	}
	if !errors.Is(results[3], ErrInvalid) || !errors.Is(results[4], ErrNotFound) {
		t.Errorf("failures = %v, %v", results[3], results[4])
	}
	// One LibraryChanged names every affected artwork.
	select {
	case ev := <-events:
		for ev.GetLibraryChanged() == nil {
			ev = <-events
		}
		got := ev.GetLibraryChanged().GetArtworkIds()
		if !slices.Equal(got, []string{src.Id, "tv:MY_F0001", "tv:MY_F0002"}) {
			t.Errorf("LibraryChanged ids = %v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no LibraryChanged")
	}
	for _, id := range []string{"tv:MY_F0001", "tv:MY_F0002"} {
		if a := e.artworks()[id]; a == nil || a.SyncState != corev1.SyncState_SYNC_STATE_PENDING_DELETE {
			t.Errorf("%s before the reconciler: %v", id, a)
		}
	}

	e.tv.ResetCalls()
	e.lib.tvMu.Lock()
	e.lib.deletions(e.ctx)
	e.lib.tvMu.Unlock()
	e.drain()
	var batches []string
	for _, c := range e.tv.Calls() {
		if strings.HasPrefix(c, "delete_image_list") {
			batches = append(batches, c)
		}
	}
	if len(batches) != 1 || !strings.Contains(batches[0], "MY_F0001") || !strings.Contains(batches[0], "MY_F0002") || !strings.Contains(batches[0], src.TvContentId) {
		t.Errorf("TV deletions = %q, want one batch with all three", batches)
	}
	for _, cid := range []string{"MY_F0001", "MY_F0002", src.TvContentId} {
		if e.tv.Has(cid) {
			t.Errorf("%s still on the TV", cid)
		}
	}
	if e.tv.Has("SAM-S2701") == false {
		t.Error("Art Store content deleted")
	}

	for _, bad := range [][]string{nil, make([]string, maxBatch+1)} {
		if _, err := e.lib.DeleteMany(e.ctx, bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%d ids: %v", len(bad), err)
		}
	}
}

func testutilGauge(e *env, origin, status string) float64 {
	mfs, _ := e.m.Registry.Gather()
	for _, mf := range mfs {
		if mf.GetName() != "vigo_thumbnails" {
			continue
		}
		for _, m := range mf.GetMetric() {
			labels := map[string]string{}
			for _, lp := range m.GetLabel() {
				labels[lp.GetName()] = lp.GetValue()
			}
			if labels["origin"] == origin && labels["status"] == status {
				return m.GetGauge().GetValue()
			}
		}
	}
	return -1
}
