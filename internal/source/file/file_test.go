package file

import (
	"context"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/yuha0/vigo/internal/source"
)

func open(t *testing.T, raw string) source.Source {
	t.Helper()
	s, err := source.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func write(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func paths(entries []source.Entry) []string {
	var out []string
	for _, e := range entries {
		out = append(out, e.Path)
	}
	slices.Sort(out)
	return out
}

func TestScan(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{
		"a.jpg", "b.JPEG", "c.png", "notes.txt", "photo.heic",
		".hidden.jpg", ".thumbs/x.jpg", "2024/d.jpg", "2024/.e.jpg", "2024/deep/f.png",
	} {
		write(t, root, p, "x")
	}
	os.Symlink(filepath.Join(root, "a.jpg"), filepath.Join(root, "link.jpg"))
	outside := t.TempDir()
	write(t, outside, "secret.jpg", "x")
	os.Symlink(outside, filepath.Join(root, "escape"))

	s := open(t, "file://"+root+"?id=art")
	entries, err := s.(source.Scanner).Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"2024/d.jpg", "2024/deep/f.png", "a.jpg", "b.JPEG", "c.png"}
	if got := paths(entries); !slices.Equal(got, want) {
		t.Errorf("scan = %v, want %v", got, want)
	}
	for _, e := range entries {
		if e.Size != 1 || e.ModTime.IsZero() {
			t.Errorf("%s: size %d mtime %v", e.Path, e.Size, e.ModTime)
		}
	}
}

func TestMissingOrUnreadableRootIsAnError(t *testing.T) {
	ctx := context.Background()
	missing := filepath.Join(t.TempDir(), "unmounted")
	if _, err := open(t, "file://"+missing).(source.Scanner).Scan(ctx); err == nil {
		t.Error("missing root scanned as empty")
	}
	f := filepath.Join(t.TempDir(), "file.jpg")
	os.WriteFile(f, nil, 0o644)
	if _, err := open(t, "file://"+f).(source.Scanner).Scan(ctx); err == nil {
		t.Error("a file as root scanned as empty")
	}
	if os.Geteuid() != 0 {
		root := t.TempDir()
		write(t, root, "ok.jpg", "x")
		write(t, root, "locked/a.jpg", "x")
		os.Chmod(filepath.Join(root, "locked"), 0)
		defer os.Chmod(filepath.Join(root, "locked"), 0o755)
		if _, err := open(t, "file://"+root).(source.Scanner).Scan(ctx); err == nil {
			t.Error("unreadable subdirectory gave a partial listing")
		}
	}
}

func TestPathTraversal(t *testing.T) {
	ctx := context.Background()
	parent := t.TempDir()
	root := filepath.Join(parent, "art")
	write(t, root, "in.jpg", "inside")
	write(t, parent, "outside.jpg", "outside")
	os.Symlink(filepath.Join(parent, "outside.jpg"), filepath.Join(root, "link.jpg"))
	os.Symlink(parent, filepath.Join(root, "up"))
	s := open(t, "file://"+root).(Writable)

	bad := []string{"../outside.jpg", "/etc/passwd", "a/../../outside.jpg", "..", ".hidden.jpg", "x/.git/y.jpg", "a\\b.jpg", "", "nul\x00.jpg"}
	for _, p := range bad {
		if _, err := s.Open(ctx, p); err == nil {
			t.Errorf("Open(%q) succeeded", p)
		}
		if _, err := s.Create(ctx, p, strings.NewReader("x"), true); err == nil {
			t.Errorf("Create(%q) succeeded", p)
		}
		if err := s.Remove(ctx, p); err == nil && p != "" {
			t.Errorf("Remove(%q) succeeded", p)
		}
	}
	for _, p := range []string{"link.jpg", "up/outside.jpg"} {
		if rc, err := s.Open(ctx, p); err == nil {
			b, _ := io.ReadAll(rc)
			rc.Close()
			t.Errorf("Open(%q) escaped the root and read %q", p, b)
		}
	}
	if _, err := s.Create(ctx, "up/outside.jpg", strings.NewReader("pwned"), true); err == nil {
		t.Error("Create wrote through a directory symlink")
	}
	// Overwriting a file symlink replaces the link itself, never its target.
	s.Create(ctx, "link.jpg", strings.NewReader("pwned"), true)
	if b, _ := os.ReadFile(filepath.Join(parent, "outside.jpg")); string(b) != "outside" {
		t.Errorf("file outside the root changed: %q", b)
	}
}

func TestCreate(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s := open(t, "file://"+root).(Writable)

	e, err := s.Create(ctx, "uploads/2026/new.jpg", strings.NewReader("one"), false)
	if err != nil {
		t.Fatal(err)
	}
	if e.Path != "uploads/2026/new.jpg" || e.Size != 3 {
		t.Errorf("entry = %+v", e)
	}
	if _, err := s.Create(ctx, "uploads/2026/new.jpg", strings.NewReader("two"), false); !errors.Is(err, source.ErrExists) {
		t.Errorf("second create: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "uploads/2026/new.jpg")); string(b) != "one" {
		t.Errorf("content = %q", b)
	}
	if _, err := s.Create(ctx, "uploads/2026/new.jpg", strings.NewReader("two"), true); err != nil {
		t.Errorf("overwrite: %v", err)
	}
	if _, err := s.Create(ctx, "notes.txt", strings.NewReader("x"), false); !errors.Is(err, source.ErrInvalidPath) {
		t.Errorf("non-image name: %v", err)
	}
	assertNoTemp(t, root)

	if err := s.Remove(ctx, "uploads/2026/new.jpg"); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(ctx, "uploads/2026/new.jpg"); err != nil {
		t.Errorf("removing a missing file: %v", err)
	}
}

func assertNoTemp(t *testing.T, root string) {
	t.Helper()
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if strings.Contains(p, ".vigo-tmp-") {
			t.Errorf("temporary file left behind: %s", p)
		}
		return nil
	})
}

// slowReader lets a test observe the file while a write is in progress.
type slowReader struct {
	data    []byte
	started chan struct{}
	release chan struct{}
	sent    bool
}

func (r *slowReader) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		n := copy(p, r.data[:len(r.data)/2])
		close(r.started)
		<-r.release
		return n, nil
	}
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.data[len(r.data)/2:])
	r.data = nil
	return n, nil
}

func TestReplaceIsAtomic(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	write(t, root, "art/crop-me.jpg", "original")
	s := open(t, "file://"+root).(Writable)

	r := &slowReader{data: []byte("cropped-new-content"), started: make(chan struct{}), release: make(chan struct{})}
	var wg sync.WaitGroup
	var replaceErr error
	wg.Go(func() { _, replaceErr = s.Replace(ctx, "art/crop-me.jpg", r) })
	<-r.started
	if b, _ := os.ReadFile(filepath.Join(root, "art/crop-me.jpg")); string(b) != "original" {
		t.Errorf("mid-write content = %q", b)
	}
	entries, err := s.Scan(ctx)
	if err != nil || len(entries) != 1 {
		t.Errorf("mid-write scan = %v, %v (the temporary file must be hidden)", entries, err)
	}
	close(r.release)
	wg.Wait()
	if replaceErr != nil {
		t.Fatal(replaceErr)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "art/crop-me.jpg")); string(b) != "cropped-new-content" {
		t.Errorf("content = %q", b)
	}
	assertNoTemp(t, root)
	if _, err := s.Replace(ctx, "missing.jpg", strings.NewReader("x")); !errors.Is(err, source.ErrNotFound) {
		t.Errorf("replace missing: %v", err)
	}
}

func TestReadOnly(t *testing.T) {
	s := open(t, "file://"+t.TempDir()+"?readonly=true")
	if source.CanWrite(s) || source.CanEdit(s) {
		t.Error("read-only source is writable")
	}
	if _, ok := s.(source.Scanner); !ok {
		t.Error("read-only source cannot scan")
	}
	w := open(t, "file://"+t.TempDir())
	if !source.CanWrite(w) || !source.CanEdit(w) {
		t.Error("writable source lacks capabilities")
	}
}

func TestIDs(t *testing.T) {
	a, _ := url.Parse("file:///srv/art")
	b, _ := url.Parse("file:///srv/art/")
	c, _ := url.Parse("file:///srv/other")
	ida, _ := source.ID(a)
	idb, _ := source.ID(b)
	idc, _ := source.ID(c)
	if ida != idb || ida == idc || !strings.HasPrefix(ida, "h") || len(ida) != 11 {
		t.Errorf("ids %q %q %q", ida, idb, idc)
	}
	named, _ := url.Parse("file:///srv/art?id=frame")
	if id, _ := source.ID(named); id != "frame" {
		t.Errorf("explicit id = %q", id)
	}
	for _, bad := range []string{"file:///x?id=Art", "file:///x?id=art-store", "file:///x?id=upload", "file:///x?id=a/b"} {
		u, _ := url.Parse(bad)
		if _, err := source.ID(u); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
	if _, err := source.NewRegistry([]string{"file:///a?id=x", "file:///b?id=x"}); err == nil {
		t.Error("duplicate ids accepted")
	}
	if _, err := source.NewRegistry([]string{"s3://bucket"}); err == nil {
		t.Error("unknown scheme accepted")
	}
	rel := open(t, "file:.dev/art")
	if rel.Display() != ".dev/art" {
		t.Errorf("relative display = %q", rel.Display())
	}
}

func TestRemovePrunesEmptyDirectories(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	write(t, root, "a/b/c/last.jpg", "x")
	write(t, root, "keep/sub/one.jpg", "x")
	write(t, root, "keep/other.jpg", "x")
	write(t, root, "hidden/sub/only.jpg", "x")
	write(t, root, "hidden/.DS_Store", "x")
	write(t, root, "real/dir/f.jpg", "x")
	os.Symlink("real", filepath.Join(root, "link"))
	s := open(t, "file://"+root).(Writable)

	steps := []struct {
		remove string
		gone   []string
		kept   []string
	}{
		{"a/b/c/last.jpg", []string{"a"}, nil},
		{"keep/sub/one.jpg", []string{"keep/sub"}, []string{"keep", "keep/other.jpg"}},
		{"hidden/sub/only.jpg", []string{"hidden/sub"}, []string{"hidden/.DS_Store"}},
		{"link/dir/f.jpg", nil, []string{"real/dir", "link"}},
	}
	for _, st := range steps {
		if err := s.Remove(ctx, st.remove); err != nil {
			t.Fatalf("Remove(%s): %v", st.remove, err)
		}
		for _, p := range st.gone {
			if _, err := os.Lstat(filepath.Join(root, p)); err == nil {
				t.Errorf("after removing %s: %s still exists", st.remove, p)
			}
		}
		for _, p := range st.kept {
			if _, err := os.Lstat(filepath.Join(root, p)); err != nil {
				t.Errorf("after removing %s: %s is gone", st.remove, p)
			}
		}
	}
	if _, err := os.Stat(root); err != nil {
		t.Error("the root was removed")
	}
}

func TestCreateUsesNoHardLinks(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s := open(t, "file://"+root).(Writable)
	for _, overwrite := range []bool{false, true} {
		if _, err := s.Create(ctx, "x/new.jpg", strings.NewReader("data"), overwrite); err != nil {
			t.Fatal(err)
		}
		fi, err := os.Stat(filepath.Join(root, "x/new.jpg"))
		if err != nil {
			t.Fatal(err)
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Nlink != 1 {
			t.Errorf("overwrite=%v: %d links to the new file", overwrite, st.Nlink)
		}
		assertNoTemp(t, root)
	}
}

func TestCreateRefusesTakenNameWithoutDamage(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	write(t, root, "taken.jpg", "original")
	write(t, root, "empty.jpg", "")
	s := open(t, "file://"+root).(Writable)
	for _, name := range []string{"taken.jpg", "empty.jpg"} {
		if _, err := s.Create(ctx, name, strings.NewReader("new"), false); !errors.Is(err, source.ErrExists) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(root, "taken.jpg")); string(b) != "original" {
		t.Errorf("taken.jpg = %q", b)
	}
	if _, err := os.Stat(filepath.Join(root, "empty.jpg")); err != nil {
		t.Error("an existing empty file was removed")
	}
	assertNoTemp(t, root)
}
