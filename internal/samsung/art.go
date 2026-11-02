package samsung

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

const (
	eventBuffer   = 64
	d2dErrorGrace = 2 * time.Second
)

// message is the parsed inner payload of a d2d_service_message.
type message struct {
	name   string
	raw    json.RawMessage
	fields map[string]any
}

type pending struct {
	request string
	ch      chan *message
}

type artConn struct {
	connState
	c   *client
	w   *wsConn
	log *slog.Logger

	mu      sync.Mutex
	pending map[string]*pending
	// uploadWait receives image_added while an upload is in flight. The TV
	// sends image_added without request_id, so uploads are serialized by
	// uploadMu to attribute it.
	uploadWait *pending

	uploadMu  sync.Mutex
	events    chan ArtEvent
	lastSend  atomic.Int64
	closeOnce sync.Once
}

func newArtConn(c *client, w *wsConn) *artConn {
	a := &artConn{
		connState: newConnState(),
		c:         c,
		w:         w,
		log:       c.log,
		pending:   make(map[string]*pending),
		events:    make(chan ArtEvent, eventBuffer),
	}
	a.lastSend.Store(time.Now().UnixNano())
	go a.run()
	go a.keepalive()
	return a
}

func (a *artConn) run() {
	for b := range a.w.in {
		a.dispatch(b)
	}
	close(a.events)
	a.finish(a.w.closedError(channelArt))
}

func (a *artConn) dispatch(b []byte) {
	var env envelope
	if err := json.Unmarshal(b, &env); err != nil {
		a.log.Warn("samsung: unparseable art frame", "err", err)
		return
	}
	if env.Event != "d2d_service_message" {
		return
	}
	m, err := parseMessage(env.Data)
	if err != nil {
		a.log.Warn("samsung: unparseable d2d_service_message", "err", err)
		return
	}
	a.mu.Lock()
	p := a.pending[replyID(m.fields)]
	if p == nil && m.name == "image_added" && a.uploadWait != nil {
		p = a.uploadWait
	}
	a.mu.Unlock()
	if p != nil {
		select {
		case p.ch <- m:
		default:
			a.log.Warn("samsung: reply dropped, waiter backlog full", "request", p.request, "event", m.name)
		}
		return
	}
	select {
	case a.events <- ArtEvent{Name: m.name, Data: m.fields}:
	default:
		a.log.Debug("samsung: art event dropped, consumer slow", "event", m.name)
	}
}

// parseMessage decodes the data of a d2d_service_message, which is a JSON
// string holding a JSON object.
func parseMessage(data json.RawMessage) (*message, error) {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("data is not a JSON string: %w", err)
	}
	m := &message{raw: json.RawMessage(s)}
	if err := json.Unmarshal(m.raw, &m.fields); err != nil {
		return nil, err
	}
	m.name, _ = m.fields["event"].(string)
	return m, nil
}

// replyID returns the id a reply belongs to. The TV echoes request_id when
// the request carries one; otherwise it puts the whole request JSON there.
func replyID(f map[string]any) string {
	rid, _ := f["request_id"].(string)
	if strings.HasPrefix(rid, "{") {
		var req struct {
			ID string `json:"id"`
		}
		if json.Unmarshal([]byte(rid), &req) == nil {
			return req.ID
		}
	}
	if rid == "" {
		rid, _ = f["id"].(string)
	}
	return rid
}

func (a *artConn) keepalive() {
	tick := time.NewTicker(a.c.timeouts.keepalive / 8)
	defer tick.Stop()
	for {
		select {
		case <-a.done:
			return
		case <-tick.C:
			if time.Since(time.Unix(0, a.lastSend.Load())) < a.c.timeouts.keepalive {
				continue
			}
			if _, err := a.ArtModeStatus(context.Background()); err != nil {
				a.log.Debug("samsung: keepalive failed", "err", err)
			}
		}
	}
}

// send emits an art_app_request with id and request_id both set to id and
// registers a waiter for replies carrying it.
func (a *artConn) send(ctx context.Context, id string, req map[string]any) (*pending, error) {
	name, _ := req["request"].(string)
	req["id"] = id
	req["request_id"] = id
	data, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	p := &pending{request: name, ch: make(chan *message, 16)}
	a.mu.Lock()
	a.pending[id] = p
	a.mu.Unlock()
	select {
	case <-a.done:
		a.forget(id)
		return nil, a.Err()
	default:
	}
	err = a.w.writeJSON(ctx, map[string]any{
		"method": "ms.channel.emit",
		"params": map[string]any{
			"event": "art_app_request",
			"to":    "host",
			"data":  string(data),
		},
	})
	if err != nil {
		a.forget(id)
		return nil, err
	}
	a.lastSend.Store(time.Now().UnixNano())
	return p, nil
}

func (a *artConn) forget(id string) {
	a.mu.Lock()
	delete(a.pending, id)
	a.mu.Unlock()
}

// await returns the next reply for p. An error event becomes *TVError.
func (a *artConn) await(ctx context.Context, p *pending, timeout time.Duration) (*message, error) {
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case m := <-p.ch:
		if m.name == "error" {
			return nil, tvError(p.request, m)
		}
		return m, nil
	case <-t.C:
		return nil, fmt.Errorf("%w: %s within %s", ErrNoReply, p.request, timeout)
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("%w: %s: %w", ErrNoReply, p.request, ctx.Err())
		}
		return nil, ctx.Err()
	case <-a.done:
		return nil, a.Err()
	}
}

func tvError(request string, m *message) *TVError {
	var e struct {
		Code flexString `json:"error_code"`
	}
	_ = json.Unmarshal(m.raw, &e)
	return &TVError{Request: request, Code: string(e.Code)}
}

// request sends req and returns its first reply.
func (a *artConn) request(ctx context.Context, req map[string]any) (*message, error) {
	start := time.Now()
	id := uuid.NewString()
	p, err := a.send(ctx, id, req)
	if err != nil {
		return nil, err
	}
	defer a.forget(id)
	m, err := a.await(ctx, p, a.c.timeouts.request)
	a.log.Debug("samsung: art request", "request", p.request, "duration", time.Since(start), "err", err)
	return m, err
}

// requestInto sends req and decodes its reply into out.
func (a *artConn) requestInto(ctx context.Context, req map[string]any, out any) error {
	m, err := a.request(ctx, req)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(m.raw, out); err != nil {
		return &ProtocolError{Kind: req["request"].(string) + " reply", Err: err}
	}
	return nil
}

func missing(request, field string) error {
	return &ProtocolError{Kind: request + " reply", Err: fmt.Errorf("no %s", field)}
}

func (a *artConn) ArtModeStatus(ctx context.Context) (bool, error) {
	var r struct {
		Value *string `json:"value"`
	}
	if err := a.requestInto(ctx, map[string]any{"request": "get_artmode_status"}, &r); err != nil {
		return false, err
	}
	if r.Value == nil {
		return false, missing("get_artmode_status", "value")
	}
	switch *r.Value {
	case "on":
		return true, nil
	case "off":
		return false, nil
	}
	return false, &ProtocolError{Kind: "get_artmode_status reply", Err: fmt.Errorf("value %q", *r.Value)}
}

func (a *artConn) SetArtModeStatus(ctx context.Context, on bool) error {
	v := "off"
	if on {
		v = "on"
	}
	_, err := a.request(ctx, map[string]any{"request": "set_artmode_status", "value": v})
	return err
}

func (a *artConn) Settings(ctx context.Context) ([]ArtSetting, error) {
	var r struct {
		Data *nested[[]struct {
			Item        string                `json:"item"`
			Value       flexString            `json:"value"`
			Min         flexString            `json:"min"`
			Max         flexString            `json:"max"`
			ValidValues *nested[[]flexString] `json:"valid_values"`
		}] `json:"data"`
	}
	if err := a.requestInto(ctx, map[string]any{"request": "get_artmode_settings"}, &r); err != nil {
		return nil, err
	}
	if r.Data == nil {
		return nil, missing("get_artmode_settings", "data")
	}
	out := make([]ArtSetting, 0, len(r.Data.V))
	for _, s := range r.Data.V {
		if s.Item == "" {
			return nil, missing("get_artmode_settings", "item")
		}
		as := ArtSetting{Item: s.Item, Value: string(s.Value), Min: string(s.Min), Max: string(s.Max)}
		if s.ValidValues != nil {
			for _, v := range s.ValidValues.V {
				as.ValidValues = append(as.ValidValues, string(v))
			}
		}
		out = append(out, as)
	}
	return out, nil
}

func (a *artConn) SetSetting(ctx context.Context, item, value string) error {
	_, err := a.request(ctx, map[string]any{"request": "set_" + item, "value": value})
	return err
}

func (a *artConn) ContentList(ctx context.Context, category string) ([]Content, error) {
	req := map[string]any{"request": "get_content_list"}
	if category != "" {
		req["category"] = category
	}
	var r struct {
		List *nested[[]struct {
			ContentID       string   `json:"content_id"`
			CategoryID      string   `json:"category_id"`
			ContentType     string   `json:"content_type"`
			MatteID         string   `json:"matte_id"`
			PortraitMatteID string   `json:"portrait_matte_id"`
			Width           flexInt  `json:"width"`
			Height          flexInt  `json:"height"`
			ImageDate       string   `json:"image_date"`
			Slideshow       flexBool `json:"slideshow"`
		}] `json:"content_list"`
	}
	if err := a.requestInto(ctx, req, &r); err != nil {
		return nil, err
	}
	if r.List == nil {
		return nil, missing("get_content_list", "content_list")
	}
	out := make([]Content, 0, len(r.List.V))
	for _, c := range r.List.V {
		if c.ContentID == "" {
			return nil, missing("get_content_list", "content_id")
		}
		// Tizen 9 ignores the category parameter and lists everything.
		if category != "" && c.CategoryID != category {
			continue
		}
		out = append(out, Content{
			ContentID:       c.ContentID,
			CategoryID:      c.CategoryID,
			ContentType:     c.ContentType,
			MatteID:         c.MatteID,
			PortraitMatteID: c.PortraitMatteID,
			Width:           int(c.Width),
			Height:          int(c.Height),
			ImageDate:       c.ImageDate,
			Slideshow:       bool(c.Slideshow),
		})
	}
	return out, nil
}

func (a *artConn) CurrentArtwork(ctx context.Context) (CurrentArtwork, error) {
	var r struct {
		ContentID       *string `json:"content_id"`
		MatteID         string  `json:"matte_id"`
		PortraitMatteID string  `json:"portrait_matte_id"`
		CategoryID      string  `json:"category_id"`
		ContentType     string  `json:"content_type"`
	}
	if err := a.requestInto(ctx, map[string]any{"request": "get_current_artwork"}, &r); err != nil {
		return CurrentArtwork{}, err
	}
	if r.ContentID == nil {
		return CurrentArtwork{}, missing("get_current_artwork", "content_id")
	}
	return CurrentArtwork{
		ContentID:       *r.ContentID,
		MatteID:         r.MatteID,
		PortraitMatteID: r.PortraitMatteID,
		CategoryID:      r.CategoryID,
		ContentType:     r.ContentType,
	}, nil
}

func (a *artConn) SelectImage(ctx context.Context, contentID string) error {
	// Including category_id makes this TV refuse with error_code -7.
	_, err := a.request(ctx, map[string]any{"request": "select_image", "content_id": contentID, "show": true})
	return err
}

func (a *artConn) DeleteImages(ctx context.Context, contentIDs []string) ([]string, error) {
	list := make([]map[string]string, len(contentIDs))
	for i, id := range contentIDs {
		list[i] = map[string]string{"content_id": id}
	}
	var r struct {
		List *nested[[]struct {
			ContentID string `json:"content_id"`
		}] `json:"content_id_list"`
	}
	if err := a.requestInto(ctx, map[string]any{"request": "delete_image_list", "content_id_list": list}, &r); err != nil {
		return nil, err
	}
	if r.List == nil {
		return nil, missing("delete_image_list", "content_id_list")
	}
	deleted := make([]string, 0, len(r.List.V))
	for _, c := range r.List.V {
		deleted = append(deleted, c.ContentID)
	}
	return deleted, nil
}

func (a *artConn) MatteList(ctx context.Context) (MatteList, error) {
	var r struct {
		Types *nested[[]struct {
			Type string `json:"matte_type"`
		}] `json:"matte_type_list"`
		Colors *nested[[]struct {
			Color string  `json:"color"`
			R     flexInt `json:"R"`
			G     flexInt `json:"G"`
			B     flexInt `json:"B"`
		}] `json:"matte_color_list"`
	}
	if err := a.requestInto(ctx, map[string]any{"request": "get_matte_list"}, &r); err != nil {
		return MatteList{}, err
	}
	if r.Types == nil {
		return MatteList{}, missing("get_matte_list", "matte_type_list")
	}
	if r.Colors == nil {
		return MatteList{}, missing("get_matte_list", "matte_color_list")
	}
	var ml MatteList
	for _, t := range r.Types.V {
		ml.Types = append(ml.Types, t.Type)
	}
	for _, c := range r.Colors.V {
		ml.Colors = append(ml.Colors, MatteColor{Name: c.Color, R: uint8(c.R), G: uint8(c.G), B: uint8(c.B)})
	}
	return ml, nil
}

func (a *artConn) ChangeMatte(ctx context.Context, contentID, matteID, portraitMatteID string) error {
	req := map[string]any{"request": "change_matte", "content_id": contentID, "matte_id": matteID}
	if portraitMatteID != "" {
		req["portrait_matte_id"] = portraitMatteID
	}
	_, err := a.request(ctx, req)
	return err
}

func (a *artConn) ArtDeviceInfo(ctx context.Context) (ArtDeviceInfo, error) {
	var r struct {
		Flash      *flexInt `json:"tv_flash_size"`
		Motion     flexBool `json:"support_motion_sensor"`
		Brightness flexBool `json:"support_brightness_sensor"`
	}
	if err := a.requestInto(ctx, map[string]any{"request": "get_device_info"}, &r); err != nil {
		return ArtDeviceInfo{}, err
	}
	if r.Flash == nil {
		return ArtDeviceInfo{}, missing("get_device_info", "tv_flash_size")
	}
	return ArtDeviceInfo{
		FlashSizeGB:              int(*r.Flash),
		SupportsMotionSensor:     bool(r.Motion),
		SupportsBrightnessSensor: bool(r.Brightness),
	}, nil
}

// connInfoRequest is the conn_info block of a D2D request. Its id must equal
// the request's id and request_id: with any other value the TV accepts an
// upload's bytes and silently discards the image.
func connInfoRequest(id string) map[string]any {
	return map[string]any{
		"d2d_mode":      "socket",
		"connection_id": rand.Uint32(),
		"id":            id,
	}
}

func (a *artConn) Thumbnail(ctx context.Context, contentID string) ([]byte, error) {
	id := uuid.NewString()
	p, err := a.send(ctx, id, map[string]any{
		"request":         "get_thumbnail_list",
		"content_id_list": []map[string]string{{"content_id": contentID}},
		"conn_info":       connInfoRequest(id),
	})
	if err != nil {
		return nil, err
	}
	defer a.forget(id)
	m, err := a.await(ctx, p, a.c.timeouts.request)
	if err != nil {
		return nil, err
	}
	ci, err := parseConnInfo("get_thumbnail_list", m)
	if err != nil {
		return nil, err
	}
	data, err := a.c.d2dRead(ctx, ci)
	if err != nil {
		return nil, a.explainD2D(p, err)
	}
	return data, nil
}

// explainD2D attaches the TV's view to a failed D2D transfer. When the TV
// cannot serve a transfer it has already offered a socket for, it closes that
// socket and only then sends an error event for the request.
func (a *artConn) explainD2D(p *pending, d2dErr error) error {
	t := time.NewTimer(d2dErrorGrace)
	defer t.Stop()
	for {
		select {
		case m := <-p.ch:
			if m.name == "error" {
				return fmt.Errorf("%w (d2d transfer: %w)", tvError(p.request, m), d2dErr)
			}
		case <-t.C:
			return d2dErr
		case <-a.done:
			return d2dErr
		}
	}
}

func (a *artConn) Upload(ctx context.Context, img Upload) (string, error) {
	a.uploadMu.Lock()
	defer a.uploadMu.Unlock()

	fileType := img.FileType
	if fileType == "" {
		fileType = sniffFileType(img.Data)
	}
	matte := img.MatteID
	if matte == "" {
		matte = "none"
	}
	portrait := img.PortraitMatteID
	if portrait == "" {
		portrait = "shadowbox_polar"
	}
	date := img.Date
	if date.IsZero() {
		date = time.Now()
	}

	id := uuid.NewString()
	p, err := a.send(ctx, id, map[string]any{
		"request":           "send_image",
		"file_type":         fileType,
		"file_size":         len(img.Data),
		"image_date":        date.Format("2006:01:02 15:04:05"),
		"matte_id":          matte,
		"portrait_matte_id": portrait,
		"conn_info":         connInfoRequest(id),
	})
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	a.uploadWait = p
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.uploadWait = nil
		a.mu.Unlock()
		a.forget(id)
	}()

	m, err := a.await(ctx, p, a.c.timeouts.request)
	if err != nil {
		return "", err
	}
	ci, err := parseConnInfo("send_image", m)
	if err != nil {
		return "", err
	}
	if err := a.c.d2dWrite(ctx, ci, fileType, img.Data); err != nil {
		return "", a.explainD2D(p, err)
	}

	deadline := time.Now().Add(a.c.timeouts.imageAdded)
	for {
		m, err := a.await(ctx, p, time.Until(deadline))
		if err != nil {
			if errors.Is(err, ErrNoReply) {
				return "", fmt.Errorf("%w: image_added after upload", ErrNoReply)
			}
			return "", err
		}
		if m.name != "image_added" {
			continue
		}
		var r struct {
			ContentID string `json:"content_id"`
		}
		if err := json.Unmarshal(m.raw, &r); err != nil || r.ContentID == "" {
			return "", missing("image_added", "content_id")
		}
		return r.ContentID, nil
	}
}

func sniffFileType(b []byte) string {
	if len(b) >= 8 && string(b[:8]) == "\x89PNG\r\n\x1a\n" {
		return "png"
	}
	return "jpg"
}

func (a *artConn) Events() <-chan ArtEvent { return a.events }

func (a *artConn) Close() error {
	a.closeOnce.Do(func() {
		a.setErr(errClientClosed)
		a.w.close()
	})
	<-a.done
	return nil
}
