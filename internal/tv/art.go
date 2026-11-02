package tv

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/yuha0/vigo/gen/go/vigo/core/v1"
	"github.com/yuha0/vigo/internal/errs"
	"github.com/yuha0/vigo/internal/imageprep"
	"github.com/yuha0/vigo/internal/metrics"
	"github.com/yuha0/vigo/internal/samsung"
)

const (
	thumbnailTimeout  = 30 * time.Second
	thumbnailAttempts = 5
	// freshUpload is how long after an upload a thumbnail refusal is
	// treated as the TV still processing the image.
	freshUpload = 30 * time.Second
)

// defaultPortraitMatte is the portrait matte of the verified upload request
// (docs/tv-api.md §5 "Upload"). Prepared images are always landscape, so it only
// matters if the TV decides to show one in portrait.
const defaultPortraitMatte = "shadowbox_polar"

var matteID = regexp.MustCompile(`^[a-z0-9]+(_[a-z0-9]+)*$`)

func contentType(t string) corev1.ContentType {
	switch t {
	case "mobile":
		return corev1.ContentType_CONTENT_TYPE_MOBILE
	case "preinstall":
		return corev1.ContentType_CONTENT_TYPE_PREINSTALL
	case "server":
		return corev1.ContentType_CONTENT_TYPE_SERVER
	}
	return corev1.ContentType_CONTENT_TYPE_UNSPECIFIED
}

// dedupe merges get_content_list entries by content_id, which repeats items
// once per category (docs/tv-api.md §5 "How content is stored"). Order is first
// appearance; categories are listed in order of first appearance too.
func dedupe(items []samsung.Content, tally map[string]int64) (arts []*corev1.Artwork, categories []string) {
	byID := map[string]*corev1.Artwork{}
	for _, c := range items {
		if c.CategoryID != "" && !slices.Contains(categories, c.CategoryID) {
			categories = append(categories, c.CategoryID)
		}
		if a, ok := byID[c.ContentID]; ok {
			if c.CategoryID != "" && !slices.Contains(a.CategoryIds, c.CategoryID) {
				a.CategoryIds = append(a.CategoryIds, c.CategoryID)
			}
			a.Slideshow = a.Slideshow || c.Slideshow
			continue
		}
		a := &corev1.Artwork{
			ContentId:       c.ContentID,
			ContentType:     contentType(c.ContentType),
			MatteId:         c.MatteID,
			PortraitMatteId: c.PortraitMatteID,
			Width:           int32(c.Width),
			Height:          int32(c.Height),
			Slideshow:       c.Slideshow,
			UploadedBytes:   tally[c.ContentID],
		}
		if c.CategoryID != "" {
			a.CategoryIds = []string{c.CategoryID}
		}
		if c.ImageDate != "" {
			if t, err := time.ParseInLocation("2006:01:02 15:04:05", c.ImageDate, time.Local); err == nil {
				a.ImageDate = timestamppb.New(t)
			}
		}
		byID[c.ContentID] = a
		arts = append(arts, a)
	}
	return arts, categories
}

func (s *Service) contentList(ctx context.Context, conn samsung.ArtConn) ([]*corev1.Artwork, []string, error) {
	items, err := call(s, ctx, "get_content_list", s.opts.RequestTimeout, func(ctx context.Context) ([]samsung.Content, error) {
		return conn.ContentList(ctx, "")
	})
	if err != nil {
		return nil, nil, err
	}
	tally, err := s.uploadTally(ctx)
	if err != nil {
		s.log.Error("read upload tally", "err", err)
	}
	arts, cats := dedupe(items, tally)
	return arts, cats, nil
}

// ListArt lists all artwork once each. With category set, only artwork in
// that category is returned; category_ids always lists every category.
func (s *Service) ListArt(ctx context.Context, category string) ([]*corev1.Artwork, []string, error) {
	conn, err := s.artConn()
	if err != nil {
		return nil, nil, err
	}
	arts, cats, err := s.contentList(ctx, conn)
	if err != nil {
		return nil, nil, err
	}
	if category != "" {
		arts = slices.DeleteFunc(arts, func(a *corev1.Artwork) bool { return !slices.Contains(a.CategoryIds, category) })
	}
	return arts, cats, nil
}

func (s *Service) findArtwork(ctx context.Context, conn samsung.ArtConn, id string) (*corev1.Artwork, error) {
	arts, _, err := s.contentList(ctx, conn)
	if err != nil {
		return nil, err
	}
	for _, a := range arts {
		if a.ContentId == id {
			return a, nil
		}
	}
	return nil, errs.New(ErrNotFound, "no artwork %s", id)
}

// Thumbnail fetches a JPEG thumbnail from the TV and returns it with its
// ETag. A ctx marked Background waits for interactive calls to finish
// first. The TV has no thumbnail for some Art Store items (docs/tv-api.md
// §5 "D2D socket transfers"); that is ErrNotFound.
func (s *Service) Thumbnail(ctx context.Context, contentID string) (data []byte, etag string, err error) {
	if contentID == "" {
		return nil, "", errs.New(ErrInvalidArgument, "content_id is required")
	}
	if IsBackground(ctx) {
		if err := s.busy.waitIdle(ctx); err != nil {
			return nil, "", err
		}
	} else {
		defer s.busy.begin()()
	}
	conn, err := s.artConn()
	if err != nil {
		return nil, "", err
	}
	ch := s.thumbs.group.DoChan(contentID, func() (any, error) {
		// The fetch is shared, so one caller going away must not cancel it.
		fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), thumbnailTimeout)
		defer cancel()
		select {
		case s.thumbs.slot <- struct{}{}:
		case <-fctx.Done():
			return nil, fctx.Err()
		}
		defer func() { <-s.thumbs.slot }()
		start := time.Now()
		data, err := s.fetchThumbnail(fctx, conn, contentID)
		if errors.Is(err, samsung.ErrNoData) {
			return nil, noThumbnail(contentID)
		}
		if err != nil {
			return nil, err
		}
		s.m.D2DBytes.WithLabelValues(metrics.DirectionDownload).Add(float64(len(data)))
		s.m.D2DDuration.WithLabelValues(metrics.DirectionDownload).Observe(time.Since(start).Seconds())
		return data, nil
	})
	select {
	case <-ctx.Done():
		return nil, "", ctx.Err()
	case r := <-ch:
		if r.Err != nil {
			return nil, "", r.Err
		}
		data := r.Val.([]byte)
		return data, etagOf(data), nil
	}
}

func noThumbnail(contentID string) error {
	return errs.New(ErrNotFound, "the TV has no thumbnail for %s", contentID)
}

// fetchThumbnail fetches over D2D. The TV refuses thumbnails of uploads for
// roughly ten seconds after image_added with error_code -1 (verified live),
// so for artwork vigo uploaded recently that refusal is retried.
func (s *Service) fetchThumbnail(ctx context.Context, conn samsung.ArtConn, contentID string) ([]byte, error) {
	for attempt := 1; ; attempt++ {
		data, err := call(s, ctx, "get_thumbnail", 0, func(ctx context.Context) ([]byte, error) {
			return conn.Thumbnail(ctx, contentID)
		})
		var tvErr *samsung.TVError
		if err == nil || !errors.As(err, &tvErr) || tvErr.Code != "-1" ||
			attempt >= thumbnailAttempts || !s.recentlyAnnounced("added:"+contentID, freshUpload) {
			return data, err
		}
		select {
		case <-ctx.Done():
			return nil, err
		case <-time.After(s.opts.ThumbnailRetryInterval):
		}
	}
}

// Upload prepares an image with imageprep, sends it to the TV and records
// its size in the upload tally, the only storage figure vigo has (docs/tv-api.md §5
// "Storage limits").
func (s *Service) Upload(ctx context.Context, r io.Reader, fit imageprep.Fit, matte string) (*corev1.Artwork, error) {
	if matte == "" {
		matte = "none"
	}
	if !matteID.MatchString(matte) {
		return nil, errs.New(ErrInvalidArgument, "matte_id %q is invalid", matte)
	}
	conn, err := s.artConn()
	if err != nil {
		return nil, err
	}
	data, err := s.opts.Prepare(r, fit)
	if err != nil {
		return nil, errs.Wrap(ErrInvalidArgument, err)
	}

	start := time.Now()
	id, err := call(s, ctx, "send_image", s.opts.UploadTimeout, func(ctx context.Context) (string, error) {
		return conn.Upload(ctx, samsung.Upload{
			Data:            data,
			FileType:        "jpg",
			MatteID:         matte,
			PortraitMatteID: defaultPortraitMatte,
			Date:            time.Now(),
		})
	})
	if err != nil {
		return nil, err
	}
	s.m.D2DBytes.WithLabelValues(metrics.DirectionUpload).Add(float64(len(data)))
	s.m.D2DDuration.WithLabelValues(metrics.DirectionUpload).Observe(time.Since(start).Seconds())
	s.log.Info("uploaded artwork", "content_id", id, "bytes", len(data))

	if err := s.store.SetUpload(ctx, id, int64(len(data))); err != nil {
		s.log.Error("record upload tally", "content_id", id, "err", err)
	}
	art, err := s.findArtwork(ctx, conn, id)
	if err != nil {
		art = &corev1.Artwork{
			ContentId:       id,
			ContentType:     corev1.ContentType_CONTENT_TYPE_MOBILE,
			MatteId:         matte,
			PortraitMatteId: defaultPortraitMatte,
			UploadedBytes:   int64(len(data)),
		}
	}
	if h := s.libraryHook(); h != nil {
		h.TVContentAdded(id)
	}
	if s.announce("added:" + id) {
		s.emit(&corev1.WatchEventsResponse{Event: &corev1.WatchEventsResponse_ArtAdded{ArtAdded: art}})
	}
	return art, nil
}

// DeleteArt deletes uploads (content type mobile). Any other id in the
// request fails the whole request before anything is deleted.
func (s *Service) DeleteArt(ctx context.Context, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return nil, errs.New(ErrInvalidArgument, "content_ids is required")
	}
	conn, err := s.artConn()
	if err != nil {
		return nil, err
	}
	arts, _, err := s.contentList(ctx, conn)
	if err != nil {
		return nil, err
	}
	byID := map[string]*corev1.Artwork{}
	for _, a := range arts {
		byID[a.ContentId] = a
	}
	ids = slices.Compact(slices.Sorted(slices.Values(ids)))
	for _, id := range ids {
		a, ok := byID[id]
		if !ok {
			return nil, errs.New(ErrNotFound, "no artwork %s", id)
		}
		if a.ContentType != corev1.ContentType_CONTENT_TYPE_MOBILE {
			return nil, errs.New(ErrInvalidArgument, "%s is not an upload; only uploads can be deleted", id)
		}
	}
	deleted, err := call(s, ctx, "delete_image_list", s.opts.RequestTimeout, func(ctx context.Context) ([]string, error) {
		return conn.DeleteImages(ctx, ids)
	})
	if err != nil {
		return nil, err
	}
	var fresh []string
	for _, id := range deleted {
		s.forgetArtwork(ctx, id)
		if s.announce("deleted:" + id) {
			fresh = append(fresh, id)
		}
	}
	if len(fresh) > 0 {
		s.emit(&corev1.WatchEventsResponse{Event: &corev1.WatchEventsResponse_ArtDeleted{ArtDeleted: &corev1.ArtDeleted{ContentIds: fresh}}})
	}
	return deleted, nil
}

func (s *Service) forgetArtwork(ctx context.Context, id string) {
	s.selectionDeleted(ctx, id)
	if h := s.libraryHook(); h != nil {
		h.TVContentDeleted(id)
	}
	err := s.store.DeleteUpload(ctx, id)
	if err != nil {
		s.log.Error("update upload tally", "content_id", id, "err", err)
	}
}

func (s *Service) ListMattes(ctx context.Context) (types []string, colors []*corev1.MatteColor, err error) {
	conn, err := s.artConn()
	if err != nil {
		return nil, nil, err
	}
	ml, err := call(s, ctx, "get_matte_list", s.opts.RequestTimeout, conn.MatteList)
	if err != nil {
		return nil, nil, err
	}
	for _, c := range ml.Colors {
		colors = append(colors, &corev1.MatteColor{Name: c.Name, Hex: fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)})
	}
	return ml.Types, colors, nil
}

// ChangeMatte changes an artwork's matte. The LS03D may refuse with -7.
func (s *Service) ChangeMatte(ctx context.Context, contentID, matte, portraitMatte string) (*corev1.Artwork, error) {
	if contentID == "" {
		return nil, errs.New(ErrInvalidArgument, "content_id is required")
	}
	if !matteID.MatchString(matte) {
		return nil, errs.New(ErrInvalidArgument, "matte_id %q is invalid", matte)
	}
	if portraitMatte != "" && !matteID.MatchString(portraitMatte) {
		return nil, errs.New(ErrInvalidArgument, "portrait_matte_id %q is invalid", portraitMatte)
	}
	conn, err := s.artConn()
	if err != nil {
		return nil, err
	}
	if _, err := s.findArtwork(ctx, conn, contentID); err != nil {
		return nil, err
	}
	if err := do(s, ctx, "change_matte", s.opts.RequestTimeout, func(ctx context.Context) error {
		return conn.ChangeMatte(ctx, contentID, matte, portraitMatte)
	}); err != nil {
		return nil, err
	}
	return s.findArtwork(ctx, conn, contentID)
}

func (s *Service) uploadTally(ctx context.Context) (map[string]int64, error) {
	return s.store.Uploads(ctx)
}
