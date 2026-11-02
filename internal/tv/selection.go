package tv

import (
	"context"
	"encoding/json"

	corev1 "github.com/yuha0/vigo/gen/go/vigo/core/v1"
	"github.com/yuha0/vigo/internal/samsung"
)

// Selection tracking.
//
// get_current_artwork is unreliable (docs/tv-api.md §5 "Three gotchas"): it
// has reported a previous artwork for hours after a confirmed select_image
// and never converged. Only the select_image reply and the image_selected
// event reflect the selection, so a selection taken from them is confirmed,
// and a confirmed selection is replaced only by another one of those. It is
// dropped only when its artwork is deleted, since the TV then moves on by
// itself.
//
// get_current_artwork is used only while there is no confirmed selection,
// and its value is always unconfirmed.
//
// Changes made outside vigo, e.g. from a phone app, arrive as image_selected
// events while vigo is connected. A change made while vigo was disconnected
// is not seen until the next one; nothing the TV reports can reveal it.
//
// The confirmed selection is persisted so a restart or a new leader keeps
// it. Records written by earlier versions, which had no confirmed flag, read
// as unconfirmed.

type selectionRecord struct {
	ContentID       string `json:"content_id"`
	MatteID         string `json:"matte_id,omitempty"`
	PortraitMatteID string `json:"portrait_matte_id,omitempty"`
	Confirmed       bool   `json:"confirmed,omitempty"`
}

func (r *selectionRecord) selectedArt() *corev1.SelectedArt {
	return &corev1.SelectedArt{ContentId: r.ContentID, MatteId: r.MatteID, PortraitMatteId: r.PortraitMatteID, Confirmed: r.Confirmed}
}

// loadSelection runs from Run, before the TV connection starts. An
// unconfirmed record is shown until the TV is read, but not kept.
func (s *Service) loadSelection(ctx context.Context) error {
	s.selMu.Lock()
	s.selection = nil
	s.selMu.Unlock()
	b, err := s.store.Selection(ctx)
	if err != nil || b == nil {
		return err
	}
	var rec selectionRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		s.log.Warn("discarding unreadable selection record", "err", err)
		return nil
	}
	if rec.ContentID == "" {
		return nil
	}
	if rec.Confirmed {
		s.selMu.Lock()
		s.selection = &rec
		s.selMu.Unlock()
	}
	s.mu.Lock()
	s.st.selected = rec.selectedArt()
	s.mu.Unlock()
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
	rec := &selectionRecord{
		ContentID:       sel.ContentId,
		MatteID:         sel.MatteId,
		PortraitMatteID: sel.PortraitMatteId,
		Confirmed:       true,
	}
	s.selMu.Lock()
	if old := s.selection; old != nil && old.ContentID == rec.ContentID && rec.MatteID == "" && rec.PortraitMatteID == "" {
		rec.MatteID, rec.PortraitMatteID = old.MatteID, old.PortraitMatteID
	}
	s.selection = rec
	s.selMu.Unlock()
	s.saveSelection(ctx, rec)
	s.setSelected(rec.selectedArt())
}

// selectionDeleted drops a confirmed selection whose artwork was deleted.
func (s *Service) selectionDeleted(ctx context.Context, contentID string) {
	s.selMu.Lock()
	drop := s.selection != nil && s.selection.ContentID == contentID
	if drop {
		s.selection = nil
	}
	s.selMu.Unlock()
	if drop {
		s.saveSelection(ctx, nil)
	}
}

// refreshSelected adopts get_current_artwork, unconfirmed, while there is no
// confirmed selection.
func (s *Service) refreshSelected(ctx context.Context, conn samsung.ArtConn) {
	s.selMu.Lock()
	confirmed := s.selection != nil
	s.selMu.Unlock()
	if confirmed {
		return
	}
	cur, err := call(s, ctx, "get_current_artwork", s.opts.RequestTimeout, conn.CurrentArtwork)
	if err != nil || cur.ContentID == "" {
		return
	}
	s.selMu.Lock()
	confirmed = s.selection != nil
	s.selMu.Unlock()
	if !confirmed {
		s.setSelected(&corev1.SelectedArt{ContentId: cur.ContentID, MatteId: cur.MatteID, PortraitMatteId: cur.PortraitMatteID})
	}
}

// wakeTarget is the artwork to select when select_image is needed to wake
// or light the panel: the confirmed selection if there is one, otherwise the
// TV's report. The report may be stale, but it is the best available and
// selecting something is the only way to light the panel.
func (s *Service) wakeTarget(ctx context.Context, conn samsung.ArtConn) (string, error) {
	if sel := s.Status().GetSelectedArt(); sel.GetConfirmed() && sel.GetContentId() != "" {
		return sel.ContentId, nil
	}
	return s.currentContentID(ctx, conn)
}
