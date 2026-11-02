// Package source defines stores of image files outside the TV. A source has
// an identity and optional capabilities, each its own interface: Scanner,
// Reader, Writer and Editor.
package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/yuha0/vigo/internal/errs"
	"io"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"
)

var (
	ErrExists      = errors.New("source: file already exists")
	ErrNotFound    = errors.New("source: file not found")
	ErrInvalidPath = errors.New("source: invalid path")
)

// Entry is one image file. Path is relative to the source root and
// "/"-separated.
type Entry struct {
	Path    string
	Size    int64
	ModTime time.Time
}

type Source interface {
	ID() string
	// Kind is the URL scheme, e.g. "file".
	Kind() string
	// Display is shown to people and never contains secrets.
	Display() string
}

// Scanner lists every image file. An unreadable or missing root, or any
// unreadable directory, is an error: a partial listing would look like
// deletions.
type Scanner interface {
	Scan(ctx context.Context) ([]Entry, error)
}

type Reader interface {
	Open(ctx context.Context, path string) (io.ReadCloser, error)
}

// Writer accepts new files and removes files.
type Writer interface {
	// Create writes a file, creating directories as needed. Without
	// overwrite it fails with ErrExists when the path is taken.
	Create(ctx context.Context, path string, r io.Reader, overwrite bool) (Entry, error)
	Remove(ctx context.Context, path string) error
}

// Editor replaces an existing file atomically.
type Editor interface {
	Replace(ctx context.Context, path string, r io.Reader) (Entry, error)
}

func CanWrite(s Source) bool { _, ok := s.(Writer); return ok }
func CanEdit(s Source) bool  { _, ok := s.(Editor); return ok }

// IsImage reports whether a file name has a JPEG or PNG extension.
func IsImage(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".jpg", ".jpeg", ".png":
		return true
	}
	return false
}

// CleanPath validates a relative, "/"-separated path: no absolute paths, no
// "..", no hidden components, no backslashes or control characters. An
// empty result means the root.
func CleanPath(p string) (string, error) {
	if strings.ContainsAny(p, "\\\x00") || strings.HasPrefix(p, "/") {
		return "", errs.New(ErrInvalidPath, "path %q is not allowed", p)
	}
	var parts []string
	for _, c := range strings.Split(p, "/") {
		switch {
		case c == "" || c == ".":
			continue
		case c == ".." || strings.HasPrefix(c, "."):
			return "", errs.New(ErrInvalidPath, "path %q is not allowed", p)
		}
		for _, r := range c {
			if r < 0x20 || r == 0x7f {
				return "", errs.New(ErrInvalidPath, "path %q is not allowed", p)
			}
		}
		parts = append(parts, c)
	}
	return strings.Join(parts, "/"), nil
}

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// reservedIDs would collide with system tags source/art-store and
// source/upload.
var reservedIDs = []string{"art-store", "upload"}

// ID returns the id query parameter of u, else "h" and a short hash of the
// URL with that parameter removed. The id appears in tags and the cache
// layout, so it must stay stable.
func ID(u *url.URL) (string, error) {
	if id := u.Query().Get("id"); id != "" {
		if !idPattern.MatchString(id) || slices.Contains(reservedIDs, id) {
			return "", fmt.Errorf("source: invalid id %q: lowercase letters, digits and dashes; not %v", id, reservedIDs)
		}
		return id, nil
	}
	n := *u
	q := n.Query()
	q.Del("id")
	n.RawQuery = q.Encode()
	n.Scheme = strings.ToLower(n.Scheme)
	if n.Path != "" {
		n.Path = path.Clean(n.Path)
	}
	if n.Opaque != "" {
		n.Opaque = path.Clean(n.Opaque)
	}
	sum := sha256.Sum256([]byte(n.String()))
	return "h" + hex.EncodeToString(sum[:])[:10], nil
}

// Opener creates a source of one URL scheme.
type Opener func(u *url.URL, id string) (Source, error)

var openers = map[string]Opener{}

// Register makes a scheme available to Parse. Implementations call it from
// init.
func Register(scheme string, o Opener) { openers[scheme] = o }

func Parse(raw string) (Source, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("source: %w", err)
	}
	o, ok := openers[strings.ToLower(u.Scheme)]
	if !ok {
		return nil, fmt.Errorf("source: unsupported scheme %q in %q", u.Scheme, raw)
	}
	id, err := ID(u)
	if err != nil {
		return nil, err
	}
	return o(u, id)
}

// Registry holds the configured sources in flag order.
type Registry struct {
	list []Source
	byID map[string]Source
}

func NewRegistry(urls []string) (*Registry, error) {
	r := &Registry{byID: map[string]Source{}}
	for _, raw := range urls {
		s, err := Parse(raw)
		if err != nil {
			return nil, err
		}
		if _, dup := r.byID[s.ID()]; dup {
			return nil, fmt.Errorf("source: duplicate id %q; set ?id= to tell them apart", s.ID())
		}
		r.list = append(r.list, s)
		r.byID[s.ID()] = s
	}
	return r, nil
}

func (r *Registry) All() []Source { return slices.Clone(r.list) }

func (r *Registry) Get(id string) (Source, bool) {
	s, ok := r.byID[id]
	return s, ok
}
