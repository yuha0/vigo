// Package fake is an in-memory samsung.Client that models The Frame's
// power, art-mode and content behaviour as recorded in docs/tv-api.md §4 and §5. It
// backs tests and the --fake-tv development mode.
package fake

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"image"
	"image/color"
	"image/jpeg"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/disintegration/imaging"

	"github.com/yuha0/vigo/internal/samsung"
)

// Token is the token the fake accepts by default and hands out on pairing.
const Token = "fake-token"

const (
	PowerOn      = "on"
	PowerStandby = "standby"
)

// errTimeout stands in for a TV that has left the network.
var errTimeout = errors.New("fake: i/o timeout")

// TV is the simulated television. All methods are safe for concurrent use.
type TV struct {
	mu        sync.Mutex
	power     string
	artMode   bool
	reachable bool
	artHost   bool
	token     string
	model     string
	pairDelay time.Duration
	pairErr   error

	settings  []samsung.ArtSetting
	ignoreSet map[string]bool
	content   []samsung.Content
	current   samsung.CurrentArtwork
	// reported, when set, is what get_current_artwork answers instead of
	// current, like the real TV's minutes-long lag.
	reported    *samsung.CurrentArtwork
	mattes      samsung.MatteList
	refuseMatte bool
	nextUpload  int
	thumbs      map[string][]byte
	calls       []string
	injected    map[string]error
	conns       map[*artConn]struct{}
}

// New returns a TV awake in Art Mode, holding the fixture library.
func New() *TV {
	tv := &TV{
		power:      PowerOn,
		artMode:    true,
		reachable:  true,
		artHost:    true,
		token:      Token,
		model:      "24_PTM_FTV_T09",
		pairDelay:  2 * time.Second,
		ignoreSet:  map[string]bool{},
		thumbs:     map[string][]byte{},
		injected:   map[string]error{},
		conns:      map[*artConn]struct{}{},
		nextUpload: 3,
		settings: []samsung.ArtSetting{
			{Item: "brightness", Value: "4", Min: "0", Max: "10"},
			{Item: "color_temperature", Value: "0", Min: "-5", Max: "5"},
			{Item: "motion_sensitivity", Value: "2", Min: "1", Max: "3"},
			{Item: "motion_timer", Value: "5", ValidValues: []string{"off", "5", "15", "30", "60", "120", "240"}},
			{Item: "brightness_sensor_setting", Value: "on"},
		},
		mattes: samsung.MatteList{
			Types: []string{"none", "modernthin", "modern", "modernwide", "flexible", "shadowbox", "panoramic", "triptych", "mix", "squares"},
			Colors: []samsung.MatteColor{
				{Name: "black", R: 0x22, G: 0x22, B: 0x22},
				{Name: "neutral", R: 0xd9, G: 0xd4, B: 0xc8},
				{Name: "antique", R: 0xe0, G: 0xd2, B: 0xb4},
				{Name: "warm", R: 0xe8, G: 0xdc, B: 0xc8},
				{Name: "polar", R: 0xf4, G: 0xf4, B: 0xf2},
				{Name: "sand", R: 0xc9, G: 0xb8, B: 0x9a},
				{Name: "seafoam", R: 0xa8, G: 0xc8, B: 0xb8},
				{Name: "sage", R: 0x9c, G: 0xa8, B: 0x8c},
				{Name: "burgandy", R: 0x6e, G: 0x22, B: 0x2c},
				{Name: "navy", R: 0x22, G: 0x2c, B: 0x48},
			},
		},
	}
	server := func(id, cat string, slideshow bool) samsung.Content {
		return samsung.Content{ContentID: id, CategoryID: cat, ContentType: "server", MatteID: "none", PortraitMatteID: "shadowbox_polar", Width: 3840, Height: 2160, Slideshow: slideshow}
	}
	mobile := func(id, cat, date string) samsung.Content {
		// Uploads report width and height swapped (docs/tv-api.md §5 "How content is stored").
		return samsung.Content{ContentID: id, CategoryID: cat, ContentType: "mobile", MatteID: "none", PortraitMatteID: "shadowbox_polar", Width: 2160, Height: 3840, ImageDate: date}
	}
	tv.content = []samsung.Content{
		{ContentID: "SAM-F0222", CategoryID: "MY-C0004", ContentType: "preinstall", MatteID: "none", PortraitMatteID: "none", Width: 3840, Height: 2160},
		server("SAM-S2701", "MY-C0008", true),
		server("SAM-S2702", "MY-C0008", false),
		server("SAM-S2703", "MY-C0008", false),
		server("SAM-S2704", "MY-C0008", true),
		server("SAM-S2705", "MY-C0004", false),
		server("SAM-S2706", "MY-C0004", false),
		server("SAM-S2707", "MY-C0008", false),
		server("SAM-S2701", "MY-C0004", true),
		server("SAM-S2703", "MY-C0004", false),
		mobile("MY_F0001", "MY-C0002", "2026:10:31 18:02:11"),
		mobile("MY_F0002", "MY-C0002", "2026:11:01 06:39:13"),
		mobile("MY_F0001", "MY-C0008", "2026:10:31 18:02:11"),
		mobile("MY_F0002", "MY-C0008", "2026:11:01 06:39:13"),
	}
	tv.current = samsung.CurrentArtwork{ContentID: "SAM-S2701", MatteID: "none", PortraitMatteID: "shadowbox_polar", CategoryID: "MY-C0008", ContentType: "server"}
	return tv
}

// Client returns a samsung.Client for this TV. Connections fail with
// samsung.ErrUnauthorized unless cfg.Token matches the TV's token.
func (tv *TV) Client(cfg samsung.Config) samsung.Client {
	return &client{tv: tv, cfg: cfg}
}

// SetPower sets PowerState and artmode_status directly.
func (tv *TV) SetPower(power string, artMode bool) {
	tv.mu.Lock()
	defer tv.mu.Unlock()
	tv.power, tv.artMode = power, artMode
}

// State returns PowerState and artmode_status.
func (tv *TV) State() (power string, artMode bool) {
	tv.mu.Lock()
	defer tv.mu.Unlock()
	return tv.power, tv.artMode
}

// SetReachable simulates the TV leaving or rejoining the network. Leaving
// closes open connections.
func (tv *TV) SetReachable(ok bool) {
	tv.mu.Lock()
	tv.reachable = ok
	tv.mu.Unlock()
	if !ok {
		tv.DropConnections()
	}
}

// SetArtHost controls whether an art app is registered as host.
func (tv *TV) SetArtHost(ok bool) {
	tv.mu.Lock()
	defer tv.mu.Unlock()
	tv.artHost = ok
}

// SetToken changes the token the TV accepts and hands out on pairing.
func (tv *TV) SetToken(token string) {
	tv.mu.Lock()
	defer tv.mu.Unlock()
	tv.token = token
}

func (tv *TV) SetModel(model string) {
	tv.mu.Lock()
	defer tv.mu.Unlock()
	tv.model = model
}

// SetPairing configures how long Pair waits for "Allow" and how it ends.
func (tv *TV) SetPairing(delay time.Duration, err error) {
	tv.mu.Lock()
	defer tv.mu.Unlock()
	tv.pairDelay, tv.pairErr = delay, err
}

// IgnoreSetting makes set_<item> acknowledge without changing the value.
func (tv *TV) IgnoreSetting(item string) {
	tv.mu.Lock()
	defer tv.mu.Unlock()
	tv.ignoreSet[item] = true
}

// RefuseMatte makes change_matte fail with error_code -7, as reported for
// LS03D.
func (tv *TV) RefuseMatte(refuse bool) {
	tv.mu.Lock()
	defer tv.mu.Unlock()
	tv.refuseMatte = refuse
}

// ReportCurrent pins what get_current_artwork answers to contentID,
// regardless of the real selection. An empty id reports the real selection.
func (tv *TV) ReportCurrent(contentID string) {
	tv.mu.Lock()
	defer tv.mu.Unlock()
	if contentID == "" {
		tv.reported = nil
		return
	}
	tv.reported = &samsung.CurrentArtwork{ContentID: contentID, MatteID: "none"}
}

// FailNext makes the next art request named request (e.g. "get_content_list")
// fail with err.
func (tv *TV) FailNext(request string, err error) {
	tv.mu.Lock()
	defer tv.mu.Unlock()
	tv.injected[request] = err
}

// Calls returns the requests received so far, e.g. "select_image:SAM-S2701"
// or "key:KEY_POWER".
func (tv *TV) Calls() []string {
	tv.mu.Lock()
	defer tv.mu.Unlock()
	return slices.Clone(tv.calls)
}

func (tv *TV) ResetCalls() {
	tv.mu.Lock()
	defer tv.mu.Unlock()
	tv.calls = nil
}

// DropConnections closes every open art connection, as the TV does to idle
// clients.
func (tv *TV) DropConnections() {
	tv.mu.Lock()
	conns := make([]*artConn, 0, len(tv.conns))
	for c := range tv.conns {
		conns = append(conns, c)
	}
	tv.mu.Unlock()
	for _, c := range conns {
		c.closeWith(errors.New("fake: connection closed by TV"))
	}
}

// Restore replaces the library with items, one per content id, listed
// under each of its categories. Development mode uses it to bring the fake
// TV back with the content it had before a restart; an empty TV would look
// as if everything had been deleted on it.
func (tv *TV) Restore(items []samsung.Content) {
	tv.mu.Lock()
	defer tv.mu.Unlock()
	tv.content = tv.content[:0]
	tv.thumbs = map[string][]byte{}
	for _, it := range items {
		if it.MatteID == "" {
			it.MatteID = "none"
		}
		if n, err := strconv.Atoi(strings.TrimPrefix(it.ContentID, "MY_F")); err == nil && strings.HasPrefix(it.ContentID, "MY_F") && n >= tv.nextUpload {
			tv.nextUpload = n + 1
		}
		tv.content = append(tv.content, it)
	}
	if _, ok := tv.find(tv.current.ContentID); !ok && len(tv.content) > 0 {
		c := tv.content[0]
		tv.current = samsung.CurrentArtwork{ContentID: c.ContentID, MatteID: c.MatteID, PortraitMatteID: c.PortraitMatteID, CategoryID: c.CategoryID, ContentType: c.ContentType}
	}
}

// SetThumbnail replaces the thumbnail the TV serves for contentID.
func (tv *TV) SetThumbnail(contentID string, data []byte) {
	tv.mu.Lock()
	defer tv.mu.Unlock()
	tv.thumbs[contentID] = data
}

// RemoveSilently deletes content without emitting image_deleted, as a
// deletion the art channel never reported would look.
func (tv *TV) RemoveSilently(contentID string) {
	tv.mu.Lock()
	defer tv.mu.Unlock()
	tv.content = slices.DeleteFunc(tv.content, func(c samsung.Content) bool { return c.ContentID == contentID })
}

// Has reports whether the TV holds contentID.
func (tv *TV) Has(contentID string) bool {
	tv.mu.Lock()
	defer tv.mu.Unlock()
	_, ok := tv.find(contentID)
	return ok
}

// Setting returns the current value of an art-mode setting.
func (tv *TV) Setting(item string) string {
	tv.mu.Lock()
	defer tv.mu.Unlock()
	for _, s := range tv.settings {
		if s.Item == item {
			return s.Value
		}
	}
	return ""
}

func (tv *TV) record(call string) {
	tv.calls = append(tv.calls, call)
}

// emit must be called without tv.mu held.
func (tv *TV) emit(name string, data map[string]any) {
	tv.mu.Lock()
	conns := make([]*artConn, 0, len(tv.conns))
	for c := range tv.conns {
		conns = append(conns, c)
	}
	tv.mu.Unlock()
	data["event"] = name
	for _, c := range conns {
		c.send(samsung.ArtEvent{Name: name, Data: data})
	}
}

type client struct {
	tv  *TV
	cfg samsung.Config
}

func (c *client) DeviceInfo(ctx context.Context) (samsung.DeviceInfo, error) {
	tv := c.tv
	tv.mu.Lock()
	defer tv.mu.Unlock()
	tv.record("device_info")
	if !tv.reachable {
		return samsung.DeviceInfo{}, errTimeout
	}
	return samsung.DeviceInfo{
		PowerState:       tv.power,
		Model:            tv.model,
		ModelName:        "QN75LS03DAFXZA",
		Name:             "Samsung The Frame (75)",
		NetworkType:      "wired",
		FrameTVSupport:   true,
		TokenAuthSupport: true,
	}, nil
}

// Awake mirrors port 9197, open exactly when PowerState is "on".
func (c *client) Awake(ctx context.Context) (bool, error) {
	tv := c.tv
	tv.mu.Lock()
	defer tv.mu.Unlock()
	tv.record("awake")
	if !tv.reachable {
		return false, errTimeout
	}
	return tv.power == PowerOn, nil
}

func (c *client) Pair(ctx context.Context) (string, error) {
	tv := c.tv
	tv.mu.Lock()
	tv.record("pair")
	reachable, delay, err, token := tv.reachable, tv.pairDelay, tv.pairErr, tv.token
	tv.mu.Unlock()
	if !reachable {
		return "", errTimeout
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-time.After(delay):
	}
	if err != nil {
		return "", err
	}
	return token, nil
}

func (c *client) checkDial(name string) error {
	tv := c.tv
	tv.record(name)
	switch {
	case !tv.reachable:
		return errTimeout
	case c.cfg.Token == "" || c.cfg.Token != tv.token:
		return samsung.ErrUnauthorized
	}
	return nil
}

func (c *client) DialArt(ctx context.Context) (samsung.ArtConn, error) {
	tv := c.tv
	tv.mu.Lock()
	defer tv.mu.Unlock()
	if err := c.checkDial("dial_art"); err != nil {
		return nil, err
	}
	if !tv.artHost {
		return nil, samsung.ErrNoArtHost
	}
	conn := &artConn{tv: tv, events: make(chan samsung.ArtEvent, 32), done: make(chan struct{})}
	tv.conns[conn] = struct{}{}
	return conn, nil
}

func (c *client) DialRemote(ctx context.Context) (samsung.RemoteConn, error) {
	tv := c.tv
	tv.mu.Lock()
	defer tv.mu.Unlock()
	if err := c.checkDial("dial_remote"); err != nil {
		return nil, err
	}
	return &remoteConn{tv: tv, done: make(chan struct{})}, nil
}

type artConn struct {
	tv     *TV
	events chan samsung.ArtEvent

	mu     sync.Mutex
	done   chan struct{}
	err    error
	closed bool
}

func (a *artConn) send(ev samsung.ArtEvent) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return
	}
	select {
	case a.events <- ev:
	default:
	}
}

func (a *artConn) closeWith(err error) {
	a.tv.mu.Lock()
	delete(a.tv.conns, a)
	a.tv.mu.Unlock()
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return
	}
	a.closed = true
	a.err = err
	close(a.events)
	close(a.done)
}

func (a *artConn) Events() <-chan samsung.ArtEvent { return a.events }
func (a *artConn) Done() <-chan struct{}           { return a.done }

func (a *artConn) Err() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.err
}

func (a *artConn) Close() error {
	a.closeWith(errors.New("fake: closed by client"))
	return nil
}

// begin records a request and checks the TV can answer it. Callers hold
// a.tv.mu.
func (a *artConn) begin(call string) error {
	a.tv.record(call)
	a.mu.Lock()
	closed := a.closed
	a.mu.Unlock()
	if closed {
		return errors.New("fake: connection closed")
	}
	if !a.tv.reachable {
		return samsung.ErrNoReply
	}
	name, _, _ := strings.Cut(call, ":")
	if err, ok := a.tv.injected[name]; ok {
		delete(a.tv.injected, name)
		return err
	}
	return nil
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func (a *artConn) ArtModeStatus(ctx context.Context) (bool, error) {
	tv := a.tv
	tv.mu.Lock()
	defer tv.mu.Unlock()
	if err := a.begin("get_artmode_status"); err != nil {
		return false, err
	}
	return tv.artMode, nil
}

// SetArtModeStatus is a no-op in either standby (docs/tv-api.md §4 "Transitions").
func (a *artConn) SetArtModeStatus(ctx context.Context, on bool) error {
	tv := a.tv
	tv.mu.Lock()
	if err := a.begin("set_artmode_status:" + onOff(on)); err != nil {
		tv.mu.Unlock()
		return err
	}
	changed := tv.power == PowerOn && tv.artMode != on
	if changed {
		tv.artMode = on
	}
	tv.mu.Unlock()
	if changed {
		tv.emit("art_mode_changed", map[string]any{"status": onOff(on)})
	}
	return nil
}

func (a *artConn) Settings(ctx context.Context) ([]samsung.ArtSetting, error) {
	tv := a.tv
	tv.mu.Lock()
	defer tv.mu.Unlock()
	if err := a.begin("get_artmode_settings"); err != nil {
		return nil, err
	}
	out := make([]samsung.ArtSetting, len(tv.settings))
	for i, s := range tv.settings {
		s.ValidValues = slices.Clone(s.ValidValues)
		out[i] = s
	}
	return out, nil
}

func (a *artConn) SetSetting(ctx context.Context, item, value string) error {
	tv := a.tv
	tv.mu.Lock()
	defer tv.mu.Unlock()
	if err := a.begin("set_" + item + ":" + value); err != nil {
		return err
	}
	i := slices.IndexFunc(tv.settings, func(s samsung.ArtSetting) bool { return s.Item == item })
	if i < 0 {
		// The TV ignores unknown requests silently (docs/tv-api.md §5 "Storage limits").
		return samsung.ErrNoReply
	}
	s := &tv.settings[i]
	switch {
	case s.Min != "":
		v, err := strconv.Atoi(value)
		lo, _ := strconv.Atoi(s.Min)
		hi, _ := strconv.Atoi(s.Max)
		if err != nil || v < lo || v > hi {
			return &samsung.TVError{Request: "set_" + item, Code: "-7"}
		}
	case len(s.ValidValues) > 0:
		if !slices.Contains(s.ValidValues, value) {
			return &samsung.TVError{Request: "set_" + item, Code: "-7"}
		}
	default:
		if value != "on" && value != "off" {
			return &samsung.TVError{Request: "set_" + item, Code: "-7"}
		}
	}
	if !tv.ignoreSet[item] {
		s.Value = value
	}
	return nil
}

func (a *artConn) ContentList(ctx context.Context, category string) ([]samsung.Content, error) {
	tv := a.tv
	tv.mu.Lock()
	defer tv.mu.Unlock()
	if err := a.begin("get_content_list"); err != nil {
		return nil, err
	}
	var out []samsung.Content
	for _, c := range tv.content {
		if category == "" || c.CategoryID == category {
			out = append(out, c)
		}
	}
	return out, nil
}

func (a *artConn) CurrentArtwork(ctx context.Context) (samsung.CurrentArtwork, error) {
	tv := a.tv
	tv.mu.Lock()
	defer tv.mu.Unlock()
	if err := a.begin("get_current_artwork"); err != nil {
		return samsung.CurrentArtwork{}, err
	}
	if tv.reported != nil {
		return *tv.reported, nil
	}
	return tv.current, nil
}

func (tv *TV) find(id string) (samsung.Content, bool) {
	i := slices.IndexFunc(tv.content, func(c samsung.Content) bool { return c.ContentID == id })
	if i < 0 {
		return samsung.Content{}, false
	}
	return tv.content[i], true
}

// SelectImage wakes the TV from motion-sleep but does nothing from
// power-off standby (docs/tv-api.md §5 "Waking the panel").
func (a *artConn) SelectImage(ctx context.Context, contentID string) error {
	tv := a.tv
	tv.mu.Lock()
	if err := a.begin("select_image:" + contentID); err != nil {
		tv.mu.Unlock()
		return err
	}
	c, ok := tv.find(contentID)
	if !ok {
		tv.mu.Unlock()
		return &samsung.TVError{Request: "select_image", Code: "-1"}
	}
	if tv.power == PowerStandby && !tv.artMode {
		tv.mu.Unlock()
		return nil
	}
	if tv.power == PowerStandby {
		tv.power = PowerOn
	}
	tv.current = samsung.CurrentArtwork{ContentID: c.ContentID, MatteID: c.MatteID, PortraitMatteID: c.PortraitMatteID, CategoryID: c.CategoryID, ContentType: c.ContentType}
	tv.mu.Unlock()
	tv.emit("image_selected", map[string]any{"content_id": contentID, "is_shown": "Yes"})
	return nil
}

func (a *artConn) DeleteImages(ctx context.Context, contentIDs []string) ([]string, error) {
	tv := a.tv
	tv.mu.Lock()
	if err := a.begin("delete_image_list:" + fmt.Sprint(contentIDs)); err != nil {
		tv.mu.Unlock()
		return nil, err
	}
	var deleted []string
	for _, id := range contentIDs {
		before := len(tv.content)
		tv.content = slices.DeleteFunc(tv.content, func(c samsung.Content) bool { return c.ContentID == id })
		if len(tv.content) != before {
			deleted = append(deleted, id)
			delete(tv.thumbs, id)
		}
	}
	// Deleting the selected artwork moves the selection on (docs/tv-api.md §5
	// "Deleting the displayed artwork").
	if slices.Contains(deleted, tv.current.ContentID) && len(tv.content) > 0 {
		c := tv.content[0]
		tv.current = samsung.CurrentArtwork{ContentID: c.ContentID, MatteID: c.MatteID, PortraitMatteID: c.PortraitMatteID, CategoryID: c.CategoryID, ContentType: c.ContentType}
	}
	tv.mu.Unlock()
	for _, id := range deleted {
		tv.emit("image_deleted", map[string]any{"content_id": id})
	}
	return deleted, nil
}

func (a *artConn) MatteList(ctx context.Context) (samsung.MatteList, error) {
	tv := a.tv
	tv.mu.Lock()
	defer tv.mu.Unlock()
	if err := a.begin("get_matte_list"); err != nil {
		return samsung.MatteList{}, err
	}
	return samsung.MatteList{Types: slices.Clone(tv.mattes.Types), Colors: slices.Clone(tv.mattes.Colors)}, nil
}

func (a *artConn) ChangeMatte(ctx context.Context, contentID, matteID, portraitMatteID string) error {
	tv := a.tv
	tv.mu.Lock()
	defer tv.mu.Unlock()
	if err := a.begin("change_matte:" + contentID + ":" + matteID); err != nil {
		return err
	}
	if tv.refuseMatte {
		return &samsung.TVError{Request: "change_matte", Code: "-7"}
	}
	if _, ok := tv.find(contentID); !ok {
		return &samsung.TVError{Request: "change_matte", Code: "-1"}
	}
	for i := range tv.content {
		if tv.content[i].ContentID == contentID {
			tv.content[i].MatteID = matteID
			if portraitMatteID != "" {
				tv.content[i].PortraitMatteID = portraitMatteID
			}
		}
	}
	return nil
}

func (a *artConn) ArtDeviceInfo(ctx context.Context) (samsung.ArtDeviceInfo, error) {
	tv := a.tv
	tv.mu.Lock()
	defer tv.mu.Unlock()
	if err := a.begin("get_device_info"); err != nil {
		return samsung.ArtDeviceInfo{}, err
	}
	return samsung.ArtDeviceInfo{FlashSizeGB: 16, SupportsMotionSensor: true, SupportsBrightnessSensor: true}, nil
}

func (a *artConn) Thumbnail(ctx context.Context, contentID string) ([]byte, error) {
	tv := a.tv
	tv.mu.Lock()
	defer tv.mu.Unlock()
	if err := a.begin("thumbnail:" + contentID); err != nil {
		return nil, err
	}
	if _, ok := tv.find(contentID); !ok {
		return nil, &samsung.TVError{Request: "get_thumbnail_list", Code: "-1"}
	}
	if b, ok := tv.thumbs[contentID]; ok {
		return b, nil
	}
	b, err := generatedThumbnail(contentID)
	if err != nil {
		return nil, err
	}
	tv.thumbs[contentID] = b
	return b, nil
}

func (a *artConn) Upload(ctx context.Context, img samsung.Upload) (string, error) {
	tv := a.tv
	tv.mu.Lock()
	if err := a.begin("send_image"); err != nil {
		tv.mu.Unlock()
		return "", err
	}
	if img.FileType != "jpg" && img.FileType != "png" {
		tv.mu.Unlock()
		return "", &samsung.TVError{Request: "send_image", Code: "-1"}
	}
	id := fmt.Sprintf("MY_F%04d", tv.nextUpload)
	tv.nextUpload++
	date := img.Date.Format("2006:01:02 15:04:05")
	matte := img.MatteID
	if matte == "" {
		matte = "none"
	}
	for _, cat := range []string{"MY-C0002", "MY-C0008"} {
		tv.content = append(tv.content, samsung.Content{
			ContentID: id, CategoryID: cat, ContentType: "mobile",
			MatteID: matte, PortraitMatteID: img.PortraitMatteID,
			Width: 2160, Height: 3840, ImageDate: date,
		})
	}
	if thumb, err := thumbnailOf(img.Data); err == nil {
		tv.thumbs[id] = thumb
	}
	tv.mu.Unlock()
	tv.emit("image_added", map[string]any{"content_id": id})
	return id, nil
}

type remoteConn struct {
	tv   *TV
	once sync.Once
	done chan struct{}
}

// SendKey models KEY_POWER as the state-dependent toggle of docs/tv-api.md §4.
func (r *remoteConn) SendKey(ctx context.Context, key string, action samsung.KeyAction) error {
	tv := r.tv
	tv.mu.Lock()
	tv.record("key:" + key)
	if !tv.reachable {
		tv.mu.Unlock()
		return errTimeout
	}
	var artEvent string
	if key == "KEY_POWER" && (action == samsung.KeyClick || action == samsung.KeyRelease) {
		switch {
		case tv.power == PowerStandby && !tv.artMode:
			tv.power, tv.artMode = PowerOn, true
			artEvent = "on"
		case tv.power == PowerStandby && tv.artMode:
			tv.artMode = false
			artEvent = "off"
		case tv.artMode:
			tv.artMode = false
			artEvent = "off"
		default:
			tv.artMode = true
			artEvent = "on"
		}
	}
	tv.mu.Unlock()
	if artEvent != "" {
		tv.emit("art_mode_changed", map[string]any{"status": artEvent})
	}
	return nil
}

func (r *remoteConn) Done() <-chan struct{} { return r.done }
func (r *remoteConn) Err() error            { return nil }
func (r *remoteConn) Close() error {
	r.once.Do(func() { close(r.done) })
	return nil
}

// generatedThumbnail draws a 320x180 gradient whose hue is derived from the
// content id, so each artwork looks distinct.
func generatedThumbnail(contentID string) ([]byte, error) {
	h := fnv.New32a()
	h.Write([]byte(contentID))
	sum := h.Sum32()
	base := color.RGBA{uint8(sum), uint8(sum >> 8), uint8(sum >> 16), 255}
	img := image.NewRGBA(image.Rect(0, 0, 320, 180))
	for y := range 180 {
		for x := range 320 {
			f := float64(x+y) / 830
			img.Set(x, y, color.RGBA{
				R: uint8(float64(base.R) * (0.4 + f)),
				G: uint8(float64(base.G) * (0.4 + f)),
				B: uint8(float64(base.B) * (0.4 + f)),
				A: 255,
			})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func thumbnailOf(data []byte) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, imaging.Fit(img, 320, 180, imaging.Box), &jpeg.Options{Quality: 80}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
