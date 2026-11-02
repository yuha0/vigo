package coreapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	corev1 "github.com/yuha0/vigo/gen/go/vigo/core/v1"
	"github.com/yuha0/vigo/gen/go/vigo/core/v1/corev1connect"
	"github.com/yuha0/vigo/internal/library"
)

var errNoLibrary = connect.NewError(connect.CodeUnavailable, errors.New("the art library is not available"))

type libraryHandlers struct {
	lib *library.Library
}

var _ corev1connect.LibraryServiceHandler = (*libraryHandlers)(nil)

func (h *libraryHandlers) get() (*library.Library, error) {
	if h.lib == nil {
		return nil, errNoLibrary
	}
	return h.lib, nil
}

func (h *libraryHandlers) ListArtworks(ctx context.Context, req *connect.Request[corev1.ListArtworksRequest]) (*connect.Response[corev1.ListArtworksResponse], error) {
	lib, err := h.get()
	if err != nil {
		return nil, err
	}
	resp, err := lib.List(ctx, req.Msg)
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(resp), nil
}

func (h *libraryHandlers) GetArtwork(ctx context.Context, req *connect.Request[corev1.GetArtworkRequest]) (*connect.Response[corev1.GetArtworkResponse], error) {
	lib, err := h.get()
	if err != nil {
		return nil, err
	}
	a, err := lib.Get(ctx, req.Msg.Id)
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&corev1.GetArtworkResponse{Artwork: a}), nil
}

func (h *libraryHandlers) ListTags(ctx context.Context, req *connect.Request[corev1.ListTagsRequest]) (*connect.Response[corev1.ListTagsResponse], error) {
	lib, err := h.get()
	if err != nil {
		return nil, err
	}
	tags, err := lib.ListTags(ctx, req.Msg.Prefix)
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&corev1.ListTagsResponse{Tags: tags}), nil
}

func (h *libraryHandlers) UpdateTags(ctx context.Context, req *connect.Request[corev1.UpdateTagsRequest]) (*connect.Response[corev1.UpdateTagsResponse], error) {
	lib, err := h.get()
	if err != nil {
		return nil, err
	}
	a, err := lib.UpdateTags(ctx, req.Msg.ArtworkId, req.Msg.Add, req.Msg.Remove)
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&corev1.UpdateTagsResponse{Artwork: a}), nil
}

func (h *libraryHandlers) DeleteArtwork(ctx context.Context, req *connect.Request[corev1.DeleteArtworkRequest]) (*connect.Response[corev1.DeleteArtworkResponse], error) {
	lib, err := h.get()
	if err != nil {
		return nil, err
	}
	a, err := lib.Delete(ctx, req.Msg.Id)
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&corev1.DeleteArtworkResponse{Artwork: a}), nil
}

func (h *libraryHandlers) CropArtwork(ctx context.Context, req *connect.Request[corev1.CropArtworkRequest]) (*connect.Response[corev1.CropArtworkResponse], error) {
	lib, err := h.get()
	if err != nil {
		return nil, err
	}
	a, err := lib.Crop(ctx, req.Msg.Id, req.Msg.Rect)
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&corev1.CropArtworkResponse{Artwork: a}), nil
}

func (h *libraryHandlers) ListSources(ctx context.Context, _ *connect.Request[corev1.ListSourcesRequest]) (*connect.Response[corev1.ListSourcesResponse], error) {
	lib, err := h.get()
	if err != nil {
		return nil, err
	}
	s, err := lib.ListSources(ctx)
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&corev1.ListSourcesResponse{Sources: s}), nil
}

func (h *libraryHandlers) SyncSource(ctx context.Context, req *connect.Request[corev1.SyncSourceRequest]) (*connect.Response[corev1.SyncSourceResponse], error) {
	lib, err := h.get()
	if err != nil {
		return nil, err
	}
	if err := lib.Sync(ctx, req.Msg.SourceId); err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&corev1.SyncSourceResponse{}), nil
}

// imageHandler serves a library rendition. Source renditions are
// content-addressed, so they never change under the same URL.
func imageHandler(lib *library.Library, render func(*library.Library, context.Context, string) ([]byte, string, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if lib == nil {
			writeErr(w, errNoLibrary)
			return
		}
		id := r.PathValue("artwork_id")
		data, etag, err := render(lib, r.Context(), id)
		if err != nil {
			writeErr(w, err)
			return
		}
		if strings.HasPrefix(id, "sha256:") {
			w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "private, no-cache")
		}
		serveJPEG(w, r, data, etag)
	}
}

// sourceFileHandler stores a browser upload: multipart "file" and an
// optional "path", a directory relative to the source root. The file is
// buffered in memory, bounded by maxBytes, because fields may arrive after
// it and the runtime filesystem is read-only.
func sourceFileHandler(lib *library.Library, maxBytes int64, log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if lib == nil {
			writeErr(w, errNoLibrary)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
		mr, err := r.MultipartReader()
		if err != nil {
			writeError(w, connect.CodeInvalidArgument, "the request must be multipart/form-data")
			return
		}
		var (
			file     []byte
			name     string
			haveFile bool
			dir      string
		)
		for {
			part, err := mr.NextPart()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				writeUploadReadError(w, err)
				return
			}
			switch part.FormName() {
			case "file":
				name = part.FileName()
				file, err = io.ReadAll(part)
				haveFile = true
			case "path":
				var b []byte
				b, err = io.ReadAll(io.LimitReader(part, 1024))
				dir = strings.TrimSpace(string(b))
			default:
				_, err = io.Copy(io.Discard, part)
			}
			part.Close()
			if err != nil {
				writeUploadReadError(w, err)
				return
			}
		}
		if !haveFile || len(file) == 0 {
			writeError(w, connect.CodeInvalidArgument, "the file field is required")
			return
		}
		art, err := lib.AddFile(r.Context(), r.PathValue("source_id"), dir, name, file)
		if err != nil {
			log.Warn("store upload", "source", r.PathValue("source_id"), "err", err)
			writeErr(w, err)
			return
		}
		b, err := protojson.Marshal(art)
		if err != nil {
			writeErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write(b)
	}
}

func writeUploadReadError(w http.ResponseWriter, err error) {
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		writeError(w, connect.CodeResourceExhausted, fmt.Sprintf("upload is larger than %d bytes", tooBig.Limit))
		return
	}
	writeError(w, connect.CodeInvalidArgument, "the upload could not be read; it may have been interrupted")
}
