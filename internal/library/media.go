package library

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/yuha0/vigo/internal/errs"
	"github.com/yuha0/vigo/internal/imageprep"
)

const (
	thumbSize      = 480
	thumbQuality   = 80
	previewSize    = 2048
	previewQuality = 85
)

// Preview returns an upright JPEG of a source artwork with its long side at
// most 2048 pixels, for the crop editor.
func (l *Library) Preview(ctx context.Context, id string) ([]byte, string, error) {
	if err := l.requireRunning(); err != nil {
		return nil, "", err
	}
	hash, ok := hashOf(id)
	if !ok {
		return nil, "", errs.New(ErrNotFound, "TV-only artworks have no preview")
	}
	a, err := l.Get(ctx, id)
	if err != nil {
		return nil, "", err
	}
	if len(a.Sources) == 0 {
		return nil, "", errs.New(ErrNotFound, "%s is in no source", id)
	}
	data, err := l.rendition(ctx, hash, a.Sources[0].SourceId, a.Sources[0].Path, "preview", previewSize, previewQuality)
	if err != nil {
		return nil, "", err
	}
	return data, `"` + hash[:16] + `-p"`, nil
}

// rendition returns a cached scaled copy, generating it once even under
// concurrent requests. Content addressing makes the cache never stale.
func (l *Library) rendition(ctx context.Context, hash, sid, p, kind string, size, quality int) ([]byte, error) {
	file := filepath.Join(l.o.CacheDir, sid, kind, hash+".jpg")
	if b, err := os.ReadFile(file); err == nil {
		return b, nil
	}
	v, err, _ := l.media.Do(file, func() (any, error) {
		src, ok := l.o.Sources.Get(sid)
		if !ok {
			return nil, errs.New(ErrNotFound, "source %s is not configured", sid)
		}
		data, got, err := readFile(context.WithoutCancel(ctx), src, p)
		if err != nil {
			return nil, err
		}
		if got != hash {
			return nil, fmt.Errorf("%s changed since it was indexed", p)
		}
		out, err := imageprep.Scaled(data, size, size, quality)
		if err != nil {
			return nil, err
		}
		if err := writeAtomic(file, out); err != nil {
			l.log.Warn("cache rendition", "file", file, "err", err)
		}
		return out, nil
	})
	if err != nil {
		return nil, err
	}
	return v.([]byte), nil
}

func writeAtomic(file string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), file)
}
