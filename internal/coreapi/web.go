package coreapi

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// webdist holds the built web UI. During development it may contain only
// .keep; `make build-web` or the image build fills it.
//
//go:embed all:webdist
var webdist embed.FS

func embeddedWeb() fs.FS {
	sub, err := fs.Sub(webdist, "webdist")
	if err != nil {
		panic(err)
	}
	return sub
}

// webHandler serves the SPA. Existing files are served as is; paths without
// a file extension fall back to index.html so client-side routes work.
// Vite puts content-hashed files under assets/, which are cached for a year;
// everything else, index.html in particular, is revalidated.
func webHandler(web fs.FS) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name == "" {
			name = "index.html"
		}
		if serveFile(w, r, web, name) {
			return
		}
		if name != "index.html" && path.Ext(name) != "" {
			http.NotFound(w, r)
			return
		}
		if !serveFile(w, r, web, "index.html") {
			http.Error(w, "the web UI is not built into this binary", http.StatusNotFound)
		}
	})
}

func serveFile(w http.ResponseWriter, r *http.Request, web fs.FS, name string) bool {
	b, err := fs.ReadFile(web, name)
	if err != nil {
		return false
	}
	if strings.HasPrefix(name, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		sum := sha256.Sum256(b)
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("ETag", `"`+hex.EncodeToString(sum[:12])+`"`)
	}
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(b))
	return true
}
