package tv

import (
	"context"
	"encoding/json"
	"slices"

	corev1 "github.com/yuha0/vigo/gen/go/vigo/core/v1"
	"github.com/yuha0/vigo/internal/samsung"
)

// Selection tracking.
//
// get_current_artwork lags the panel by minutes and reports the artworks it
// passes through on the way (docs/tv-api.md §5); it was observed still reporting the
// previous artwork more than five minutes after a select_image the TV had
// confirmed with is_shown "Yes". The select_image reply and the
// image_selected event are immediate, so they define the selection.
// get_current_artwork only reveals a change made elsewhere: a value that is
// neither the one it reported when vigo last selected, nor one of vigo's own
// earlier selections it is catching up through.
//
// The record is persisted so a restart or a new leader does not fall back to
// the stale report.

const maxPendingSelections = 20

type selectionRecord struct {
	ContentID       string `json:"content_id"`
	MatteID         string `json:"matte_id,omitempty"`
	PortraitMatteID string `json:"portrait_matte_id,omitempty"`
	// Reported is the latest get_current_artwork value seen since this
	// selection was made.
	Reported string `json:"reported"`
	// Pending are selections made before this one that get_current_artwork
	// has not caught up with.
	Pending []string `json:"pending,omitempty"`
}

func (r *selectionRecord) selectedArt() *corev1.SelectedArt {
	return &corev1.SelectedArt{ContentId: r.ContentID, MatteId: r.MatteID, PortraitMatteId: r.PortraitMatteID}
}

// loadSelection runs from Run, before the TV connection starts.
func (s *Service) loadSelection(ctx context.Context) error {
	b, err := s.store.Selection(ctx)
	if err != nil {
		return err
	}
	if b == nil {
		s.selMu.Lock()
		s.selection = nil
		s.selMu.Unlock()
		return nil
	}
	var rec selectionRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		s.log.Warn("discarding unreadable selection record", "err", err)
		return nil
	}
	if rec.ContentID != "" {
		s.selMu.Lock()
		s.selection = &rec
		s.selMu.Unlock()
		s.mu.Lock()
		s.st.selected = rec.selectedArt()
		s.mu.Unlock()
	}
	return nil
}

func (s *Service) saveSelection(ctx context.Context, rec *selectionRecord) {
	var err error
	if rec == nil {
		err = s.store.SetSelection(ctx, nil)
	} else {
		var b []byte
		if b, err = json.Marshal(rec); err == nil {
			err = s.store.SetSelection(ctx, b)
		}
	}
	if err != nil {
		s.log.Error("persist selection", "err", err)
	}
}

// selected records a selection the TV confirmed, from a select_image reply or
// an image_selected event. Empty mattes keep the known ones for the same id.
func (s *Service) selected(ctx context.Context, sel *corev1.SelectedArt) {
	if sel.ContentId == "" {
		return
	}
	s.selMu.Lock()
	rec := &selectionRecord{
		ContentID:       sel.ContentId,
		MatteID:         sel.MatteId,
		PortraitMatteID: sel.PortraitMatteId,
		Reported:        s.reported,
	}
	if old := s.selection; old != nil {
		if old.ContentID == rec.ContentID {
			rec.Pending = old.Pending
			if rec.MatteID == "" && rec.PortraitMatteID == "" {
				rec.MatteID, rec.PortraitMatteID = old.MatteID, old.PortraitMatteID
			}
		} else if old.ContentID != s.reported {
			rec.Pending = append(slices.Clone(old.Pending), old.ContentID)
			if n := len(rec.Pending); n > maxPendingSelections {
				rec.Pending = rec.Pending[n-maxPendingSelections:]
			}
		}
	}
	s.selection = rec
	s.selMu.Unlock()
	s.saveSelection(ctx, rec)
	s.setSelected(rec.selectedArt())
}

// refreshSelected reconciles with get_current_artwork.
func (s *Service) refreshSelected(ctx context.Context, conn samsung.ArtConn) {
	cur, err := call(s, ctx, "get_current_artwork", s.opts.RequestTimeout, conn.CurrentArtwork)
	if err != nil || cur.ContentID == "" {
		return
	}
	reported := &corev1.SelectedArt{ContentId: cur.ContentID, MatteId: cur.MatteID, PortraitMatteId: cur.PortraitMatteID}

	// Records are never modified once published: saveSelection may be
	// marshalling one concurrently.
	s.selMu.Lock()
	s.reported = cur.ContentID
	rec := s.selection
	var save bool
	switch {
	case rec == nil:
	case cur.ContentID == rec.ContentID:
		if rec.Reported != cur.ContentID || len(rec.Pending) > 0 {
			next := *rec
			next.Reported, next.Pending = cur.ContentID, nil
			s.selection, rec, save = &next, &next, true
		}
	case cur.ContentID == rec.Reported || slices.Contains(rec.Pending, cur.ContentID):
		if rec.Reported != cur.ContentID {
			next := *rec
			next.Reported = cur.ContentID
			s.selection, rec, save = &next, &next, true
		}
	default:
		s.selection, rec, save = nil, nil, true
	}
	var keep *corev1.SelectedArt
	if rec != nil {
		keep = rec.selectedArt()
	}
	s.selMu.Unlock()

	if save {
		s.saveSelection(ctx, rec)
	}
	if keep != nil {
		s.setSelected(keep)
		return
	}
	s.setSelected(reported)
}

// selectedContentID is the tracked selection, falling back to the TV's report.
func (s *Service) selectedContentID(ctx context.Context, conn samsung.ArtConn) (string, error) {
	if st := s.Status(); st.GetSelectedArt().GetContentId() != "" {
		return st.SelectedArt.ContentId, nil
	}
	return s.currentContentID(ctx, conn)
}
