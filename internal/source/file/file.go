// Package file is a source backed by a directory tree:
//
//	file:///srv/art?id=frame             read, write, edit
//	file:///mnt/nas/art?readonly=true    read only
//	file:.dev/art                        relative to the working directory
//
// Every access goes through os.Root, so no path, symlink included, can
// reach outside the root.
package file

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/yuha0/vigo/internal/errs"
	"github.com/yuha0/vigo/internal/source"
)

func init() { source.Register("file", Open) }

// Source is a directory tree. Read-only sources implement only Scanner and
// Reader.
type Source struct {
	id   string
	root string
}

// Writable adds Writer and Editor.
type Writable struct{ *Source }

// Open creates a source from a file URL. The root is not checked here:
// a missing root is reported by each scan, so a share mounted late still
// works.
func Open(u *url.URL, id string) (source.Source, error) {
	p := u.Path
	if u.Opaque != "" {
		p = u.Opaque
	}
	if u.Host != "" && u.Host != "localhost" {
		return nil, fmt.Errorf("file source: host %q is not supported; use file:///path", u.Host)
	}
	if p == "" {
		return nil, errors.New("file source: empty path")
	}
	s := &Source{id: id, root: filepath.Clean(p)}
	ro, _ := strconv.ParseBool(u.Query().Get("readonly"))
	if ro {
		return s, nil
	}
	return Writable{s}, nil
}

func (s *Source) ID() string      { return s.id }
func (s *Source) Kind() string    { return "file" }
func (s *Source) Display() string { return s.root }

func (s *Source) openRoot() (*os.Root, error) {
	fi, err := os.Stat(s.root)
	if err != nil {
		return nil, fmt.Errorf("file source %s: %w", s.id, err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("file source %s: %s is not a directory", s.id, s.root)
	}
	r, err := os.OpenRoot(s.root)
	if err != nil {
		return nil, fmt.Errorf("file source %s: %w", s.id, err)
	}
	return r, nil
}

func isHidden(name string) bool { return strings.HasPrefix(name, ".") }

// Scan walks the tree. Hidden entries are skipped, symlinks are not
// followed, and any error fails the whole scan.
func (s *Source) Scan(ctx context.Context) ([]source.Entry, error) {
	r, err := s.openRoot()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	var out []source.Entry
	err = fs.WalkDir(r.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if p == "." {
			return nil
		}
		if isHidden(d.Name()) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !source.IsImage(d.Name()) {
			return nil
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		out = append(out, source.Entry{Path: p, Size: fi.Size(), ModTime: fi.ModTime()})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("file source %s: scan: %w", s.id, err)
	}
	return out, nil
}

func clean(p string) (string, error) {
	c, err := source.CleanPath(p)
	if err != nil {
		return "", err
	}
	if c == "" {
		return "", errs.New(source.ErrInvalidPath, "the file path is empty")
	}
	return c, nil
}

func (s *Source) Open(ctx context.Context, p string) (io.ReadCloser, error) {
	c, err := clean(p)
	if err != nil {
		return nil, err
	}
	r, err := s.openRoot()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	f, err := r.Open(c)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errs.New(source.ErrNotFound, "no file %s", c)
	}
	if err != nil {
		return nil, fmt.Errorf("file source %s: %w", s.id, err)
	}
	return f, nil
}

// writeTemp writes r to a hidden temporary file in dir, which scans skip.
func writeTemp(r *os.Root, dir string, src io.Reader) (string, error) {
	var b [8]byte
	rand.Read(b[:])
	tmp := path.Join(dir, ".vigo-tmp-"+hex.EncodeToString(b[:]))
	f, err := r.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", err
	}
	_, err = io.Copy(f, src)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		r.Remove(tmp)
		return "", err
	}
	return tmp, nil
}

func entry(r *os.Root, p string) (source.Entry, error) {
	fi, err := r.Stat(p)
	if err != nil {
		return source.Entry{}, err
	}
	return source.Entry{Path: p, Size: fi.Size(), ModTime: fi.ModTime()}, nil
}

// Create writes the file through a hidden temporary file in the same
// directory, then moves it into place. Without overwrite it first reserves
// the name with an exclusive create, which fails atomically if the name is
// taken, and renames over the empty reservation. Hard links are avoided:
// SMB shares report success for them but the new name is unreadable.
func (w Writable) Create(ctx context.Context, p string, src io.Reader, overwrite bool) (source.Entry, error) {
	c, err := clean(p)
	if err != nil {
		return source.Entry{}, err
	}
	if !source.IsImage(c) {
		return source.Entry{}, errs.New(source.ErrInvalidPath, "%s is not a .jpg, .jpeg or .png name", c)
	}
	r, err := w.openRoot()
	if err != nil {
		return source.Entry{}, err
	}
	defer r.Close()
	dir := path.Dir(c)
	if dir != "." {
		if err := r.MkdirAll(dir, 0o755); err != nil {
			return source.Entry{}, fmt.Errorf("file source %s: %w", w.id, err)
		}
	}
	tmp, err := writeTemp(r, dir, src)
	if err != nil {
		return source.Entry{}, fmt.Errorf("file source %s: %w", w.id, err)
	}
	defer r.Remove(tmp)
	if !overwrite {
		f, err := r.OpenFile(c, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, fs.ErrExist) {
			return source.Entry{}, errs.New(source.ErrExists, "a file named %s already exists", c)
		}
		if err != nil {
			return source.Entry{}, fmt.Errorf("file source %s: %w", w.id, err)
		}
		f.Close()
	}
	if err := r.Rename(tmp, c); err != nil {
		if !overwrite {
			removeIfEmpty(r, c)
		}
		return source.Entry{}, fmt.Errorf("file source %s: %w", w.id, err)
	}
	return entry(r, c)
}

// removeIfEmpty removes a reservation that never received its content.
func removeIfEmpty(r *os.Root, p string) {
	if fi, err := r.Lstat(p); err == nil && fi.Mode().IsRegular() && fi.Size() == 0 {
		r.Remove(p)
	}
}

// Replace overwrites an existing file atomically: readers see the old or
// the new content, never a mix.
func (w Writable) Replace(ctx context.Context, p string, src io.Reader) (source.Entry, error) {
	c, err := clean(p)
	if err != nil {
		return source.Entry{}, err
	}
	r, err := w.openRoot()
	if err != nil {
		return source.Entry{}, err
	}
	defer r.Close()
	if _, err := r.Stat(c); errors.Is(err, fs.ErrNotExist) {
		return source.Entry{}, errs.New(source.ErrNotFound, "no file %s", c)
	}
	tmp, err := writeTemp(r, path.Dir(c), src)
	if err != nil {
		return source.Entry{}, fmt.Errorf("file source %s: %w", w.id, err)
	}
	if err := r.Rename(tmp, c); err != nil {
		r.Remove(tmp)
		return source.Entry{}, fmt.Errorf("file source %s: %w", w.id, err)
	}
	return entry(r, c)
}

func (w Writable) Remove(ctx context.Context, p string) error {
	c, err := clean(p)
	if err != nil {
		return err
	}
	r, err := w.openRoot()
	if err != nil {
		return err
	}
	defer r.Close()
	err = r.Remove(c)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	pruneEmptyDirs(r, path.Dir(c))
	return nil
}

// pruneEmptyDirs removes dir and its ancestors while they are empty, up to
// but excluding the root. It is best effort: it stops at the first
// directory that is not empty, and does nothing if any component is a
// symlink, so it never acts through one.
func pruneEmptyDirs(r *os.Root, dir string) {
	if dir == "." {
		return
	}
	parts := strings.Split(dir, "/")
	for i := range parts {
		fi, err := r.Lstat(strings.Join(parts[:i+1], "/"))
		if err != nil || !fi.IsDir() {
			return
		}
	}
	for ; dir != "."; dir = path.Dir(dir) {
		// Removing a directory fails unless it is empty.
		if r.Remove(dir) != nil {
			return
		}
	}
}
