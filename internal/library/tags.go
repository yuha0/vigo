package library

import (
	"github.com/yuha0/vigo/internal/errs"
	"math"
	"path"
	"slices"
	"strings"
	"unicode/utf8"
)

// ref is one place an artwork's content is stored.
type ref struct {
	sourceID string
	path     string
}

// facts are what an artwork's system tags derive from.
type facts struct {
	tvOnly bool
	// contentType is the TV's content_type of the TV copy, if any.
	contentType string
	categories  []string
	onTV        bool
	refs        []ref
	// Upright dimensions; unknown (0) for TV-only artworks, whose reported
	// dimensions are unreliable (swapped for uploads, docs/tv-api.md §5).
	width, height int
}

const (
	targetW = 3840
	targetH = 2160
)

func lowResolution(w, h int) bool { return w < targetW || h < targetH }

// offAspect reports more than 1% deviation from 16:9.
func offAspect(w, h int) bool {
	if w <= 0 || h <= 0 {
		return false
	}
	return math.Abs(float64(w)/float64(h)/(16.0/9.0)-1) > 0.01
}

// systemTags derives the system tags of docs/design.md "Tags".
func systemTags(f facts) []string {
	var tags []string
	add := func(t string) {
		if !slices.Contains(tags, t) {
			tags = append(tags, t)
		}
	}
	if f.tvOnly {
		switch f.contentType {
		case "server", "preinstall":
			add("source/art-store")
		case "mobile":
			add("source/upload")
		}
	}
	for _, r := range f.refs {
		add("source/" + r.sourceID)
		if dir := path.Dir(r.path); dir != "." {
			add("folder/" + dir)
		}
	}
	if f.onTV {
		add("status/on-tv")
		for _, c := range f.categories {
			if c != "" {
				add("tv-category/" + c)
			}
		}
	}
	if !f.tvOnly && f.width > 0 && f.height > 0 {
		if lowResolution(f.width, f.height) {
			add("issue/low-resolution")
		}
		if offAspect(f.width, f.height) {
			add("issue/aspect-ratio")
		}
	}
	slices.Sort(tags)
	return tags
}

// searchText is what the substring search runs over: file names, tags and
// the TV content id.
func searchText(f facts, tags []string, contentID string) string {
	var parts []string
	for _, r := range f.refs {
		parts = append(parts, path.Base(r.path))
	}
	parts = append(parts, tags...)
	if contentID != "" {
		parts = append(parts, contentID)
	}
	return strings.ToLower(strings.Join(parts, " "))
}

const maxTagLen = 64

// checkUserTag validates a user tag: no "/" (that is the system namespace),
// no control characters, at most 64 characters.
func checkUserTag(t string) (string, error) {
	t = strings.TrimSpace(t)
	switch {
	case t == "":
		return "", errs.New(ErrInvalid, "tags cannot be empty")
	case strings.Contains(t, "/"):
		return "", errs.New(ErrInvalid, "%q contains \"/\"; tags with a namespace are system tags", t)
	case utf8.RuneCountInString(t) > maxTagLen:
		return "", errs.New(ErrInvalid, "tag longer than %d characters", maxTagLen)
	}
	for _, r := range t {
		if r < 0x20 || r == 0x7f {
			return "", errs.New(ErrInvalid, "control character in tag")
		}
	}
	return t, nil
}

// fileName makes a safe file name for an upload: the base name only,
// without leading dots or control characters, with an extension matching
// the detected format.
func fileName(name, format string) string {
	name = path.Base(strings.ReplaceAll(name, "\\", "/"))
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '/' {
			return -1
		}
		return r
	}, name)
	name = strings.TrimLeft(strings.TrimSpace(name), ".")
	ext := strings.ToLower(path.Ext(name))
	stem := strings.TrimSuffix(name, path.Ext(name))
	want := ".jpg"
	if format == "png" {
		want = ".png"
	}
	switch {
	case format == "jpeg" && (ext == ".jpg" || ext == ".jpeg"):
	case format == "png" && ext == ".png":
	default:
		if stem == "" || ext == "" {
			stem = name
		}
		name = stem + want
	}
	if strings.TrimSuffix(name, path.Ext(name)) == "" {
		name = "upload" + want
	}
	if len(name) > 200 {
		e := path.Ext(name)
		name = name[:200-len(e)] + e
	}
	return name
}
