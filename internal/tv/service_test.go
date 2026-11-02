package tv

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/jpeg"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	corev1 "github.com/yuha0/vigo/gen/go/vigo/core/v1"
	"github.com/yuha0/vigo/internal/imageprep"
	"github.com/yuha0/vigo/internal/metrics"
	"github.com/yuha0/vigo/internal/samsung"
	"github.com/yuha0/vigo/internal/samsung/fake"
)

type harness struct {
	svc   *Service
	tv    *fake.TV
	store *MemoryStore
	m     *metrics.Metrics
}

type setup struct {
	token  string
	run    bool
	tweak  func(*Options)
	before func(context.Context, *MemoryStore)
}

func newHarness(t *testing.T, su setup) *harness {
	t.Helper()
	ctx := context.Background()
	st := NewMemoryStore()
	if su.token != "" {
		if err := st.SetToken(ctx, su.token, "vigo"); err != nil {
			t.Fatal(err)
		}
	}
	if su.before != nil {
		su.before(ctx, st)
	}
	tv := fake.New()
	tv.SetPairing(20*time.Millisecond, nil)
	m := metrics.New()
	opts := Options{
		Defaults:         Config{Host: "tv.test", Port: 8002, ClientName: "vigo"},
		Store:            st,
		NewClient:        tv.Client,
		Metrics:          m,
		Replica:          "test-0",
		PollInterval:     20 * time.Millisecond,
		RequestTimeout:   time.Second,
		WakeTimeout:      500 * time.Millisecond,
		WakePollInterval: 5 * time.Millisecond,
		ReconnectMin:     5 * time.Millisecond,
		ReconnectMax:     20 * time.Millisecond,
		PairTimeout:      time.Second,

		ThumbnailRetryInterval: 5 * time.Millisecond,
	}
	if su.tweak != nil {
		su.tweak(&opts)
	}
	svc, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	if su.run {
		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { svc.Run(runCtx); close(done) }()
		t.Cleanup(func() { cancel(); <-done })
	}
	return &harness{svc: svc, tv: tv, store: st, m: m}
}

func connected(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, setup{token: fake.Token, run: true})
	h.waitConn(t, corev1.ConnectionState_CONNECTION_STATE_CONNECTED)
	h.waitFor(t, "power on", func(s *corev1.Status) bool { return s.Power == corev1.PowerState_POWER_STATE_ON })
	return h
}

func (h *harness) waitFor(t *testing.T, what string, cond func(*corev1.Status) bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond(h.svc.Status()) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; status: %v", what, h.svc.Status())
}

func (h *harness) waitConn(t *testing.T, want corev1.ConnectionState) {
	t.Helper()
	h.waitFor(t, want.String(), func(s *corev1.Status) bool { return s.Connection == want })
}

// commands filters the fake's call log to state-changing requests.
func commands(calls []string) []string {
	var out []string
	for _, c := range calls {
		if strings.HasPrefix(c, "select_image:") || strings.HasPrefix(c, "key:") ||
			strings.HasPrefix(c, "set_artmode_status:") || c == "dial_remote" {
			out = append(out, c)
		}
	}
	return out
}

func TestShowArtStateMatrix(t *testing.T) {
	tests := []struct {
		name        string
		power       string
		artMode     bool
		unreachable bool
		contentID   string
		want        []string
		wantErr     error
		wantPower   string
		wantArtMode bool
	}{
		{
			name: "awake in art mode re-selects to light the panel", power: fake.PowerOn, artMode: true,
			want: []string{"select_image:SAM-S2701"}, wantPower: fake.PowerOn, wantArtMode: true,
		},
		{
			name: "awake in art mode with content", power: fake.PowerOn, artMode: true, contentID: "SAM-S2702",
			want: []string{"select_image:SAM-S2702"}, wantPower: fake.PowerOn, wantArtMode: true,
		},
		{
			name: "awake in normal TV mode", power: fake.PowerOn, artMode: false,
			want: []string{"set_artmode_status:on", "select_image:SAM-S2701"}, wantPower: fake.PowerOn, wantArtMode: true,
		},
		{
			name: "motion-sleep selects the current art", power: fake.PowerStandby, artMode: true,
			want: []string{"select_image:SAM-S2701"}, wantPower: fake.PowerOn, wantArtMode: true,
		},
		{
			name: "motion-sleep selects the requested art", power: fake.PowerStandby, artMode: true, contentID: "SAM-S2705",
			want: []string{"select_image:SAM-S2705"}, wantPower: fake.PowerOn, wantArtMode: true,
		},
		{
			name: "power-off standby sends KEY_POWER then lights the panel", power: fake.PowerStandby, artMode: false,
			want: []string{"dial_remote", "key:KEY_POWER", "select_image:SAM-S2701"}, wantPower: fake.PowerOn, wantArtMode: true,
		},
		{
			name: "power-off standby with content", power: fake.PowerStandby, artMode: false, contentID: "SAM-S2702",
			want: []string{"dial_remote", "key:KEY_POWER", "select_image:SAM-S2702"}, wantPower: fake.PowerOn, wantArtMode: true,
		},
		{
			name: "unreachable does nothing", power: fake.PowerStandby, artMode: false, unreachable: true,
			want: nil, wantErr: ErrUnreachable, wantPower: fake.PowerStandby, wantArtMode: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := connected(t)
			h.tv.SetPower(tt.power, tt.artMode)
			if tt.unreachable {
				h.tv.SetReachable(false)
			}
			h.tv.ResetCalls()

			_, err := h.svc.ShowArt(context.Background(), tt.contentID)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if got := commands(h.tv.Calls()); !slices.Equal(got, tt.want) {
				t.Errorf("commands = %q, want %q", got, tt.want)
			}
			power, art := h.tv.State()
			if power != tt.wantPower || art != tt.wantArtMode {
				t.Errorf("TV state = %s/%v, want %s/%v", power, art, tt.wantPower, tt.wantArtMode)
			}
		})
	}
}

func TestSelectionSurvivesStaleReport(t *testing.T) {
	ctx := context.Background()
	h := connected(t)
	h.tv.ReportCurrent("SAM-S2701")
	selected := func(id string) func(*corev1.Status) bool {
		return func(s *corev1.Status) bool { return s.GetSelectedArt().GetContentId() == id }
	}

	if _, err := h.svc.SelectArt(ctx, "SAM-S2702"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.svc.SelectArt(ctx, "SAM-S2703"); err != nil {
		t.Fatal(err)
	}
	h.tv.DropConnections()
	h.waitConn(t, corev1.ConnectionState_CONNECTION_STATE_CONNECTED)
	h.waitFor(t, "SAM-S2703 while the TV reports SAM-S2701", selected("SAM-S2703"))

	h.tv.ReportCurrent("SAM-S2702")
	h.tv.DropConnections()
	h.waitConn(t, corev1.ConnectionState_CONNECTION_STATE_CONNECTED)
	h.waitFor(t, "SAM-S2703 while the report catches up through SAM-S2702", selected("SAM-S2703"))

	restarted, err := New(h.svc.opts)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.load(ctx); err != nil {
		t.Fatal(err)
	}
	if got := restarted.Status().GetSelectedArt().GetContentId(); got != "SAM-S2703" {
		t.Errorf("after restart selected %q, want SAM-S2703", got)
	}

	h.tv.ReportCurrent("SAM-S2705")
	h.tv.DropConnections()
	h.waitFor(t, "a change made elsewhere, seen on reconnect", selected("SAM-S2705"))
}

func TestRefreshReadsTheTV(t *testing.T) {
	h := connected(t)
	h.tv.ReportCurrent("SAM-S2705")
	h.tv.ResetCalls()
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if _, err := h.svc.Refresh(context.Background()); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	st, _ := h.svc.Refresh(context.Background())
	if got := st.GetSelectedArt().GetContentId(); got != "SAM-S2705" {
		t.Errorf("selected %q, want SAM-S2705", got)
	}
	n := 0
	for _, c := range h.tv.Calls() {
		if c == "get_current_artwork" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("read get_current_artwork %d times, want 1", n)
	}
}

func TestSelectArtWhilePoweredOff(t *testing.T) {
	h := connected(t)
	h.tv.SetPower(fake.PowerStandby, false)
	h.tv.ResetCalls()
	if _, err := h.svc.SelectArt(context.Background(), "SAM-S2702"); !errors.Is(err, ErrPoweredOff) {
		t.Fatalf("err = %v, want ErrPoweredOff", err)
	}
	if got := commands(h.tv.Calls()); len(got) != 0 {
		t.Errorf("commands = %q, want none", got)
	}
}

func TestShowArtNeverSendsKeyPowerWithoutArtState(t *testing.T) {
	h := connected(t)
	h.tv.SetToken("rotated")
	h.tv.DropConnections()
	h.waitConn(t, corev1.ConnectionState_CONNECTION_STATE_NOT_PAIRED)
	h.tv.SetPower(fake.PowerStandby, false)
	h.tv.ResetCalls()

	if _, err := h.svc.ShowArt(context.Background(), ""); !errors.Is(err, ErrNotPaired) {
		t.Fatalf("err = %v, want ErrNotPaired", err)
	}
	if got := commands(h.tv.Calls()); len(got) != 0 {
		t.Errorf("commands = %q, want none", got)
	}
}

func TestShowArtConcurrentSendsKeyPowerOnce(t *testing.T) {
	h := connected(t)
	h.tv.SetPower(fake.PowerStandby, false)
	h.tv.ResetCalls()
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() { h.svc.ShowArt(context.Background(), "") })
	}
	wg.Wait()
	n := 0
	for _, c := range h.tv.Calls() {
		if c == "key:KEY_POWER" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("KEY_POWER sent %d times, want 1", n)
	}
}

func TestSendKey(t *testing.T) {
	h := connected(t)
	tests := []struct {
		key     string
		wantErr error
		want    []string
	}{
		{key: "KEY_POWER", wantErr: ErrInvalidArgument},
		{key: " key_power ", wantErr: ErrInvalidArgument},
		{key: "KEY_POWEROFF", wantErr: ErrInvalidArgument},
		{key: "UP", wantErr: ErrInvalidArgument},
		{key: "", wantErr: ErrInvalidArgument},
		{key: "KEY_UP", want: []string{"dial_remote", "key:KEY_UP"}},
		{key: "key_enter", want: []string{"key:KEY_ENTER"}},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			h.tv.ResetCalls()
			err := h.svc.SendKey(context.Background(), tt.key, samsung.KeyClick)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if got := commands(h.tv.Calls()); !slices.Equal(got, tt.want) {
				t.Errorf("commands = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSettingFromTV(t *testing.T) {
	tests := []struct {
		in      samsung.ArtSetting
		want    string
		wantErr bool
	}{
		{in: samsung.ArtSetting{Item: "brightness", Value: "4", Min: "0", Max: "10"}, want: `key:"brightness" range:{value:4 max:10}`},
		{in: samsung.ArtSetting{Item: "color_temperature", Value: "0", Min: "-5", Max: "5"}, want: `key:"color_temperature" range:{min:-5 max:5}`},
		{in: samsung.ArtSetting{Item: "motion_timer", Value: "5", ValidValues: []string{"off", "5", "15"}}, want: `key:"motion_timer" enum:{value:"5" options:"off" options:"5" options:"15"}`},
		{in: samsung.ArtSetting{Item: "brightness_sensor_setting", Value: "on"}, want: `key:"brightness_sensor_setting" toggle:{value:true}`},
		{in: samsung.ArtSetting{Item: "brightness_sensor_setting", Value: "off"}, want: `key:"brightness_sensor_setting" toggle:{}`},
		{in: samsung.ArtSetting{Item: "brightness", Value: "high", Min: "0", Max: "10"}, wantErr: true},
		{in: samsung.ArtSetting{Item: "mystery", Value: "42"}, wantErr: true},
	}
	for _, tt := range tests {
		got, err := settingFromTV(tt.in)
		if (err != nil) != tt.wantErr {
			t.Errorf("%v: err = %v", tt.in, err)
			continue
		}
		if err == nil && strings.Join(strings.Fields(got.String()), " ") != tt.want {
			t.Errorf("%v: got %s, want %s", tt.in, got, tt.want)
		}
	}
}

func TestUpdateSetting(t *testing.T) {
	intV := func(v int32) *corev1.UpdateSettingRequest_IntValue {
		return &corev1.UpdateSettingRequest_IntValue{IntValue: v}
	}
	enumV := func(v string) *corev1.UpdateSettingRequest_EnumValue {
		return &corev1.UpdateSettingRequest_EnumValue{EnumValue: v}
	}
	boolV := func(v bool) *corev1.UpdateSettingRequest_BoolValue {
		return &corev1.UpdateSettingRequest_BoolValue{BoolValue: v}
	}
	tests := []struct {
		name          string
		req           *corev1.UpdateSettingRequest
		ignore        bool
		wantErr       error
		wantConfirmed bool
		wantTV        string
		wantCall      string
	}{
		{name: "range", req: &corev1.UpdateSettingRequest{Key: "brightness", Value: intV(9)}, wantConfirmed: true, wantTV: "9", wantCall: "set_brightness:9"},
		{name: "negative range", req: &corev1.UpdateSettingRequest{Key: "color_temperature", Value: intV(-3)}, wantConfirmed: true, wantTV: "-3", wantCall: "set_color_temperature:-3"},
		{name: "enum", req: &corev1.UpdateSettingRequest{Key: "motion_timer", Value: enumV("off")}, wantConfirmed: true, wantTV: "off", wantCall: "set_motion_timer:off"},
		{name: "toggle sent as off", req: &corev1.UpdateSettingRequest{Key: "brightness_sensor_setting", Value: boolV(false)}, wantConfirmed: true, wantTV: "off", wantCall: "set_brightness_sensor_setting:off"},
		{name: "acknowledged but unchanged", req: &corev1.UpdateSettingRequest{Key: "brightness", Value: intV(7)}, ignore: true, wantConfirmed: false, wantTV: "4", wantCall: "set_brightness:7"},
		{name: "out of range", req: &corev1.UpdateSettingRequest{Key: "brightness", Value: intV(11)}, wantErr: ErrInvalidArgument, wantTV: "4"},
		{name: "wrong type", req: &corev1.UpdateSettingRequest{Key: "brightness", Value: boolV(true)}, wantErr: ErrInvalidArgument, wantTV: "4"},
		{name: "invalid enum", req: &corev1.UpdateSettingRequest{Key: "motion_timer", Value: enumV("7")}, wantErr: ErrInvalidArgument, wantTV: "5"},
		{name: "unknown key", req: &corev1.UpdateSettingRequest{Key: "volume", Value: intV(1)}, wantErr: ErrNotFound},
		{name: "no value", req: &corev1.UpdateSettingRequest{Key: "brightness"}, wantErr: ErrInvalidArgument, wantTV: "4"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := connected(t)
			if tt.ignore {
				h.tv.IgnoreSetting(tt.req.Key)
			}
			h.tv.ResetCalls()
			got, confirmed, err := h.svc.UpdateSetting(context.Background(), tt.req)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantTV != "" {
				if v := h.tv.Setting(tt.req.Key); v != tt.wantTV {
					t.Errorf("TV value = %q, want %q", v, tt.wantTV)
				}
			}
			var sets []string
			for _, c := range h.tv.Calls() {
				if strings.HasPrefix(c, "set_") {
					sets = append(sets, c)
				}
			}
			if tt.wantCall == "" && len(sets) > 0 || tt.wantCall != "" && !slices.Equal(sets, []string{tt.wantCall}) {
				t.Errorf("setter calls = %q, want %q", sets, tt.wantCall)
			}
			if err != nil {
				return
			}
			if confirmed != tt.wantConfirmed {
				t.Errorf("confirmed = %v, want %v", confirmed, tt.wantConfirmed)
			}
			if got.Key != tt.req.Key {
				t.Errorf("setting key = %s", got.Key)
			}
			unconfirmed := testutil.ToFloat64(h.m.SettingUnconfirmed.WithLabelValues(tt.req.Key))
			if (unconfirmed == 1) == tt.wantConfirmed {
				t.Errorf("vigo_tv_setting_unconfirmed_total = %v", unconfirmed)
			}
		})
	}
}

func TestDedupe(t *testing.T) {
	items := []samsung.Content{
		{ContentID: "A", CategoryID: "MY-C0008", ContentType: "server"},
		{ContentID: "B", CategoryID: "MY-C0004", ContentType: "preinstall"},
		{ContentID: "A", CategoryID: "MY-C0004", ContentType: "server", Slideshow: true},
		{ContentID: "M", CategoryID: "MY-C0002", ContentType: "mobile", ImageDate: "2026:11:01 06:39:13"},
		{ContentID: "M", CategoryID: "MY-C0008", ContentType: "mobile", ImageDate: "2026:11:01 06:39:13"},
		{ContentID: "A", CategoryID: "MY-C0008", ContentType: "server"},
	}
	arts, cats := dedupe(items, map[string]int64{"M": 1234})
	if want := []string{"MY-C0008", "MY-C0004", "MY-C0002"}; !slices.Equal(cats, want) {
		t.Errorf("categories = %v, want %v", cats, want)
	}
	want := map[string][]string{"A": {"MY-C0008", "MY-C0004"}, "B": {"MY-C0004"}, "M": {"MY-C0002", "MY-C0008"}}
	if len(arts) != len(want) {
		t.Fatalf("got %d artworks, want %d", len(arts), len(want))
	}
	for _, a := range arts {
		if !slices.Equal(a.CategoryIds, want[a.ContentId]) {
			t.Errorf("%s categories = %v, want %v", a.ContentId, a.CategoryIds, want[a.ContentId])
		}
	}
	if !arts[0].Slideshow {
		t.Error("A should be flagged slideshow from any listing")
	}
	m := arts[2]
	if m.ContentType != corev1.ContentType_CONTENT_TYPE_MOBILE || m.UploadedBytes != 1234 || m.ImageDate == nil {
		t.Errorf("upload = %v", m)
	}
}

func TestListArt(t *testing.T) {
	h := connected(t)
	tests := []struct {
		category string
		wantN    int
	}{
		{"", 10},
		{"MY-C0002", 2},
		{"MY-C0004", 5},
		{"NOPE", 0},
	}
	for _, tt := range tests {
		arts, cats, err := h.svc.ListArt(context.Background(), tt.category)
		if err != nil {
			t.Fatal(err)
		}
		if len(arts) != tt.wantN {
			t.Errorf("category %q: %d artworks, want %d", tt.category, len(arts), tt.wantN)
		}
		if len(cats) != 3 {
			t.Errorf("categories = %v", cats)
		}
		seen := map[string]bool{}
		for _, a := range arts {
			if seen[a.ContentId] {
				t.Errorf("duplicate %s", a.ContentId)
			}
			seen[a.ContentId] = true
		}
	}
}

func TestDeleteArt(t *testing.T) {
	tests := []struct {
		name    string
		ids     []string
		wantErr error
		want    []string
	}{
		{name: "upload", ids: []string{"MY_F0001"}, want: []string{"MY_F0001"}},
		{name: "samsung content refused", ids: []string{"SAM-S2701"}, wantErr: ErrInvalidArgument},
		{name: "mixed refused entirely", ids: []string{"MY_F0001", "SAM-F0222"}, wantErr: ErrInvalidArgument},
		{name: "unknown", ids: []string{"MY_F9999"}, wantErr: ErrNotFound},
		{name: "empty", wantErr: ErrInvalidArgument},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := connected(t)
			_, events, cancel := h.svc.Subscribe()
			defer cancel()
			h.tv.ResetCalls()
			got, err := h.svc.DeleteArt(context.Background(), tt.ids)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			deleteCalls := slices.ContainsFunc(h.tv.Calls(), func(c string) bool { return strings.HasPrefix(c, "delete_image_list") })
			if deleteCalls != (tt.wantErr == nil) {
				t.Errorf("delete_image_list sent = %v", deleteCalls)
			}
			if err != nil {
				return
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("deleted = %v, want %v", got, tt.want)
			}
			ev := waitEvent(t, events, func(e *corev1.WatchEventsResponse) bool { return e.GetArtDeleted() != nil })
			if !slices.Equal(ev.GetArtDeleted().ContentIds, tt.want) {
				t.Errorf("art_deleted = %v", ev.GetArtDeleted().ContentIds)
			}
			// The TV's own image_deleted event must not produce a duplicate.
			time.Sleep(50 * time.Millisecond)
			for {
				select {
				case e := <-events:
					if e.GetArtDeleted() != nil {
						t.Errorf("duplicate art_deleted: %v", e)
					}
					continue
				default:
				}
				break
			}
		})
	}
}

func waitEvent(t *testing.T, ch <-chan *corev1.WatchEventsResponse, match func(*corev1.WatchEventsResponse) bool) *corev1.WatchEventsResponse {
	t.Helper()
	timeout := time.After(3 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("event channel closed")
			}
			if match(ev) {
				return ev
			}
		case <-timeout:
			t.Fatal("timed out waiting for event")
		}
	}
}

func testJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestUpload(t *testing.T) {
	h := connected(t)
	ctx := context.Background()
	_, events, cancel := h.svc.Subscribe()
	defer cancel()

	art, err := h.svc.Upload(ctx, bytes.NewReader(testJPEG(t, 640, 480)), imageprep.FitPad, "")
	if err != nil {
		t.Fatal(err)
	}
	if art.ContentId != "MY_F0003" || art.ContentType != corev1.ContentType_CONTENT_TYPE_MOBILE || art.UploadedBytes == 0 {
		t.Errorf("artwork = %v", art)
	}
	if !slices.Equal(art.CategoryIds, []string{"MY-C0002", "MY-C0008"}) {
		t.Errorf("categories = %v", art.CategoryIds)
	}
	tally, _ := h.store.Uploads(ctx)
	if tally["MY_F0003"] != art.UploadedBytes {
		t.Errorf("tally = %v", tally)
	}
	waitEvent(t, events, func(e *corev1.WatchEventsResponse) bool { return e.GetArtAdded().GetContentId() == "MY_F0003" })
	if got := testutil.ToFloat64(h.m.D2DBytes.WithLabelValues(metrics.DirectionUpload)); got != float64(art.UploadedBytes) {
		t.Errorf("d2d upload bytes = %v", got)
	}

	if _, err := h.svc.Upload(ctx, strings.NewReader("not an image"), imageprep.FitPad, ""); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("bad image: err = %v", err)
	}
	if _, err := h.svc.Upload(ctx, bytes.NewReader(testJPEG(t, 8, 8)), imageprep.FitPad, "../x"); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("bad matte: err = %v", err)
	}

	if _, err := h.svc.DeleteArt(ctx, []string{"MY_F0003"}); err != nil {
		t.Fatal(err)
	}
	if tally, _ := h.store.Uploads(ctx); len(tally) != 0 {
		t.Errorf("tally after delete = %v", tally)
	}
}

func TestThumbnailCache(t *testing.T) {
	h := connected(t)
	h.tv.ResetCalls()
	var wg sync.WaitGroup
	etags := make([]string, 8)
	for i := range etags {
		wg.Go(func() {
			data, etag, err := h.svc.Thumbnail(context.Background(), "SAM-S2702")
			if err != nil || len(data) == 0 {
				t.Errorf("thumbnail: %v", err)
			}
			etags[i] = etag
		})
	}
	wg.Wait()
	if _, _, err := h.svc.Thumbnail(context.Background(), "SAM-S2702"); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, c := range h.tv.Calls() {
		if c == "thumbnail:SAM-S2702" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("fetched %d times, want 1", n)
	}
	for _, e := range etags {
		if e == "" || e != etags[0] {
			t.Errorf("etags differ: %v", etags)
			break
		}
	}
}

func TestThumbnailMissIsRemembered(t *testing.T) {
	h := connected(t)
	h.tv.FailNext("thumbnail", samsung.ErrNoData)
	for range 2 {
		if _, _, err := h.svc.Thumbnail(context.Background(), "SAM-S2702"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("got %v, want ErrNotFound", err)
		}
	}
	n := 0
	for _, c := range h.tv.Calls() {
		if c == "thumbnail:SAM-S2702" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("asked the TV %d times, want 1", n)
	}
}

func TestThumbnailOfFreshUpload(t *testing.T) {
	h := connected(t)
	ctx := context.Background()
	refused := &samsung.TVError{Request: "get_thumbnail_list", Code: "-1"}

	h.tv.FailNext("thumbnail", refused)
	if _, _, err := h.svc.Thumbnail(ctx, "SAM-S2703"); !errors.As(err, new(*samsung.TVError)) {
		t.Fatalf("old artwork: err = %v, want the refusal without retry", err)
	}
	if _, _, err := h.svc.Thumbnail(ctx, "SAM-S2703"); err != nil {
		t.Fatalf("failure was cached: %v", err)
	}

	art, err := h.svc.Upload(ctx, bytes.NewReader(testJPEG(t, 64, 64)), imageprep.FitPad, "")
	if err != nil {
		t.Fatal(err)
	}
	h.tv.FailNext("thumbnail", refused)
	h.tv.ResetCalls()
	if _, _, err := h.svc.Thumbnail(ctx, art.ContentId); err != nil {
		t.Fatalf("fresh upload: %v", err)
	}
	n := 0
	for _, c := range h.tv.Calls() {
		if c == "thumbnail:"+art.ContentId {
			n++
		}
	}
	if n != 2 {
		t.Errorf("fetched %d times, want 2", n)
	}
}

func TestThumbnailLRU(t *testing.T) {
	c := newThumbCache(2, 1)
	c.put("a", []byte("a"))
	c.put("b", []byte("b"))
	c.get("a")
	c.put("c", []byte("c"))
	if _, ok := c.get("b"); ok {
		t.Error("b should have been evicted")
	}
	for _, k := range []string{"a", "c"} {
		if _, ok := c.get(k); !ok {
			t.Errorf("%s missing", k)
		}
	}
}

func TestProtocolErrorMetric(t *testing.T) {
	h := connected(t)
	h.tv.FailNext("get_content_list", &samsung.ProtocolError{Kind: "content_list", Err: errors.New("missing content_list")})
	if _, _, err := h.svc.ListArt(context.Background(), ""); err == nil {
		t.Fatal("ListArt succeeded")
	}
	if got := testutil.ToFloat64(h.m.ProtocolErrors.WithLabelValues("content_list")); got != 1 {
		t.Errorf("vigo_tv_protocol_errors_total = %v", got)
	}
	if got := testutil.ToFloat64(h.m.Requests.WithLabelValues("get_content_list", metrics.OutcomeError)); got != 1 {
		t.Errorf("error requests = %v", got)
	}
	h.tv.FailNext("get_content_list", samsung.ErrNoReply)
	h.svc.ListArt(context.Background(), "")
	if got := testutil.ToFloat64(h.m.Requests.WithLabelValues("get_content_list", metrics.OutcomeTimeout)); got != 1 {
		t.Errorf("timeout requests = %v", got)
	}
	if !slices.ContainsFunc(h.svc.Status().Warnings, func(w *corev1.Warning) bool { return w.Code == "protocol_error" }) {
		t.Error("no protocol_error warning")
	}
}

func TestWatchEvents(t *testing.T) {
	h := connected(t)
	first, events, cancel := h.svc.Subscribe()
	defer cancel()
	if first.Connection != corev1.ConnectionState_CONNECTION_STATE_CONNECTED || !first.ShowingArt || !first.Leader || first.Replica != "test-0" {
		t.Fatalf("first = %v", first)
	}
	if _, err := h.svc.SetArtMode(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	ev := waitEvent(t, events, func(e *corev1.WatchEventsResponse) bool {
		return e.GetStatus() != nil && e.GetStatus().ArtMode == corev1.ArtModeState_ART_MODE_STATE_OFF
	})
	if ev.GetStatus().ShowingArt {
		t.Error("showing_art should be false in normal TV mode")
	}
	if got := testutil.ToFloat64(h.m.ArtMode.WithLabelValues("off")); got != 1 {
		t.Errorf("vigo_tv_art_mode{state=off} = %v", got)
	}

	if _, err := h.svc.SelectArt(context.Background(), "SAM-S2706"); err != nil {
		t.Fatal(err)
	}
	waitEvent(t, events, func(e *corev1.WatchEventsResponse) bool { return e.GetArtSelected().GetContentId() == "SAM-S2706" })
	if got := h.svc.Status().SelectedArt.GetContentId(); got != "SAM-S2706" {
		t.Errorf("selected = %s", got)
	}
}

func TestSlowSubscriberIsDropped(t *testing.T) {
	h := newHarness(t, setup{token: fake.Token})
	_, events, cancel := h.svc.Subscribe()
	defer cancel()
	for i := range subscriberBuffer + 1 {
		h.svc.setWarning("w", strings.Repeat("x", i+1))
	}
	n := 0
	for range events {
		n++
	}
	if n != subscriberBuffer {
		t.Errorf("received %d events before close, want %d", n, subscriberBuffer)
	}
}

func TestConnectionStates(t *testing.T) {
	t.Run("no token", func(t *testing.T) {
		h := newHarness(t, setup{run: true})
		h.waitConn(t, corev1.ConnectionState_CONNECTION_STATE_NOT_PAIRED)
		if p := h.svc.Pairing(); p.State != corev1.PairingState_PAIRING_STATE_UNPAIRED {
			t.Errorf("pairing = %v", p)
		}
	})
	t.Run("rejected token", func(t *testing.T) {
		h := newHarness(t, setup{token: "stale", run: true})
		h.waitConn(t, corev1.ConnectionState_CONNECTION_STATE_NOT_PAIRED)
		h.waitFor(t, "token_rejected warning", func(s *corev1.Status) bool {
			return slices.ContainsFunc(s.Warnings, func(w *corev1.Warning) bool { return w.Code == "token_rejected" })
		})
	})
	t.Run("no art host", func(t *testing.T) {
		h := newHarness(t, setup{token: fake.Token})
		h.tv.SetArtHost(false)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { h.svc.Run(ctx); close(done) }()
		defer func() { cancel(); <-done }()
		h.waitConn(t, corev1.ConnectionState_CONNECTION_STATE_NO_ART_HOST)
		if _, _, err := h.svc.ListArt(context.Background(), ""); !errors.Is(err, ErrNoArtHost) {
			t.Errorf("ListArt err = %v", err)
		}
		h.tv.SetArtHost(true)
		h.waitConn(t, corev1.ConnectionState_CONNECTION_STATE_CONNECTED)
	})
	t.Run("reconnects after the TV closes the channel", func(t *testing.T) {
		h := connected(t)
		h.tv.DropConnections()
		h.waitFor(t, "reconnect", func(*corev1.Status) bool {
			return testutil.ToFloat64(h.m.Reconnects.WithLabelValues("art")) >= 1
		})
		h.waitConn(t, corev1.ConnectionState_CONNECTION_STATE_CONNECTED)
	})
	t.Run("unreachable", func(t *testing.T) {
		h := connected(t)
		h.tv.SetReachable(false)
		h.waitFor(t, "unreachable", func(s *corev1.Status) bool { return s.Power == corev1.PowerState_POWER_STATE_UNREACHABLE })
		h.tv.SetReachable(true)
		h.waitConn(t, corev1.ConnectionState_CONNECTION_STATE_CONNECTED)
	})
}

func TestStandbyReplica(t *testing.T) {
	h := newHarness(t, setup{token: fake.Token})
	st := h.svc.Status()
	if st.Connection != corev1.ConnectionState_CONNECTION_STATE_STANDBY_REPLICA || st.Leader {
		t.Fatalf("status = %v", st)
	}
	ctx := context.Background()
	if _, err := h.svc.ShowArt(ctx, ""); !errors.Is(err, ErrStandbyReplica) {
		t.Errorf("ShowArt err = %v", err)
	}
	if err := h.svc.SendKey(ctx, "KEY_UP", samsung.KeyClick); !errors.Is(err, ErrStandbyReplica) {
		t.Errorf("SendKey err = %v", err)
	}
	if _, err := h.svc.StartPairing(ctx); !errors.Is(err, ErrStandbyReplica) {
		t.Errorf("StartPairing err = %v", err)
	}
	if calls := h.tv.Calls(); len(calls) != 0 {
		t.Errorf("standby replica talked to the TV: %v", calls)
	}
}

func TestPairing(t *testing.T) {
	tests := []struct {
		name      string
		delay     time.Duration
		pairErr   error
		wantState corev1.PairingState
		wantToken bool
	}{
		{name: "allowed", delay: 20 * time.Millisecond, wantState: corev1.PairingState_PAIRING_STATE_PAIRED, wantToken: true},
		{name: "refused", delay: 20 * time.Millisecond, pairErr: samsung.ErrUnauthorized, wantState: corev1.PairingState_PAIRING_STATE_FAILED},
		{name: "timed out", delay: 5 * time.Second, wantState: corev1.PairingState_PAIRING_STATE_FAILED},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newHarness(t, setup{run: true, tweak: func(o *Options) { o.PairTimeout = 200 * time.Millisecond }})
			h.tv.SetPairing(tt.delay, tt.pairErr)
			h.waitConn(t, corev1.ConnectionState_CONNECTION_STATE_NOT_PAIRED)
			_, events, cancel := h.svc.Subscribe()
			defer cancel()

			p, err := h.svc.StartPairing(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if p.State != corev1.PairingState_PAIRING_STATE_WAITING_FOR_ALLOW || p.Deadline == nil {
				t.Fatalf("start = %v", p)
			}
			again, _ := h.svc.StartPairing(context.Background())
			if !again.Deadline.AsTime().Equal(p.Deadline.AsTime()) {
				t.Error("second StartPairing started a new attempt")
			}
			ev := waitEvent(t, events, func(e *corev1.WatchEventsResponse) bool {
				return e.GetPairing() != nil && e.GetPairing().State != corev1.PairingState_PAIRING_STATE_WAITING_FOR_ALLOW
			})
			if ev.GetPairing().State != tt.wantState {
				t.Fatalf("pairing = %v, want %v", ev.GetPairing(), tt.wantState)
			}
			tok, _ := h.store.Token(context.Background())
			if tt.wantToken {
				if tok != fake.Token {
					t.Errorf("token = %q", tok)
				}
				h.waitConn(t, corev1.ConnectionState_CONNECTION_STATE_CONNECTED)
				if verified, _ := h.store.TokenState(); !verified {
					t.Error("token not marked verified after connecting")
				}
			} else if tok != "" {
				t.Errorf("token stored after failed pairing: %q", tok)
			}
		})
	}
}

func TestCancelPairing(t *testing.T) {
	h := newHarness(t, setup{run: true})
	h.tv.SetPairing(5*time.Second, nil)
	h.waitConn(t, corev1.ConnectionState_CONNECTION_STATE_NOT_PAIRED)
	if _, err := h.svc.StartPairing(context.Background()); err != nil {
		t.Fatal(err)
	}
	p := h.svc.CancelPairing()
	if p.State != corev1.PairingState_PAIRING_STATE_UNPAIRED {
		t.Errorf("after cancel = %v", p)
	}
	time.Sleep(50 * time.Millisecond)
	if tok, _ := h.store.Token(context.Background()); tok != "" {
		t.Error("token stored after cancel")
	}
}

func TestModelChangeWarning(t *testing.T) {
	h := newHarness(t, setup{token: fake.Token, run: true, before: func(ctx context.Context, st *MemoryStore) {
		st.ObserveModel(ctx, "24_PONTUSM_FTV")
	}})
	hasWarning := func(s *corev1.Status) bool {
		return slices.ContainsFunc(s.Warnings, func(w *corev1.Warning) bool {
			return w.Code == "model_changed" && strings.Contains(w.Message, "from 24_PONTUSM_FTV to 24_PTM_FTV_T09")
		})
	}
	h.waitFor(t, "model_changed warning", hasWarning)

	// Acknowledged models clear the warning on the next recheck.
	h.store.AcknowledgeModels()
	h.svc.modelMu.Lock()
	h.svc.modelAt = time.Time{}
	h.svc.modelMu.Unlock()
	h.svc.signal(h.svc.pollNow)
	h.waitFor(t, "warning cleared", func(s *corev1.Status) bool { return !hasWarning(s) })
}

func TestModelRecordedOnFirstRun(t *testing.T) {
	h := connected(t)
	if obs, _ := h.store.ObserveModel(context.Background(), "24_PTM_FTV_T09"); !obs.Acknowledged || obs.Previous != "" {
		t.Errorf("first model = %+v, want acknowledged baseline", obs)
	}
	if w := h.svc.Status().Warnings; len(w) != 0 {
		t.Errorf("warnings = %v", w)
	}
}

func TestUpdateConfig(t *testing.T) {
	h := connected(t)
	ctx := context.Background()
	cfg, err := h.svc.UpdateConfig(ctx, Config{Host: "192.0.2.10"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg != (Config{Host: "192.0.2.10", Port: 8002, ClientName: "vigo"}) {
		t.Errorf("config = %+v", cfg)
	}
	if stored, _ := h.store.Config(ctx); stored != (Config{Host: "192.0.2.10"}) {
		t.Errorf("stored = %+v", stored)
	}
	h.waitFor(t, "reconnect", func(*corev1.Status) bool {
		return testutil.ToFloat64(h.m.Reconnects.WithLabelValues("art")) >= 1
	})
	h.waitConn(t, corev1.ConnectionState_CONNECTION_STATE_CONNECTED)

	for _, bad := range []Config{{Host: "http://tv"}, {Host: "tv:8002"}, {Port: 70000}, {ClientName: strings.Repeat("x", 65)}} {
		if _, err := h.svc.UpdateConfig(ctx, bad); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("%+v: err = %v", bad, err)
		}
	}
	if cfg, _ := h.svc.UpdateConfig(ctx, Config{}); cfg.Host != "tv.test" {
		t.Errorf("clearing overrides: %+v", cfg)
	}
}

func TestConfigOverridesLoadedFromStore(t *testing.T) {
	h := newHarness(t, setup{before: func(ctx context.Context, st *MemoryStore) {
		st.SetConfig(ctx, Config{Port: 8001, ClientName: "frame"})
	}})
	if err := h.svc.load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := h.svc.Config(); got != (Config{Host: "tv.test", Port: 8001, ClientName: "frame"}) {
		t.Errorf("config = %+v", got)
	}
}

func TestStatusMetrics(t *testing.T) {
	h := connected(t)
	if got := testutil.ToFloat64(h.m.ConnectionState.WithLabelValues("connected")); got != 1 {
		t.Errorf("connected = %v", got)
	}
	if got := testutil.ToFloat64(h.m.ConnectionState.WithLabelValues("not_paired")); got != 0 {
		t.Errorf("not_paired = %v", got)
	}
	if got := testutil.ToFloat64(h.m.PowerState.WithLabelValues("on")); got != 1 {
		t.Errorf("power on = %v", got)
	}
}

func TestChangeMatte(t *testing.T) {
	h := connected(t)
	ctx := context.Background()
	art, err := h.svc.ChangeMatte(ctx, "MY_F0001", "shadowbox_polar", "")
	if err != nil {
		t.Fatal(err)
	}
	if art.MatteId != "shadowbox_polar" {
		t.Errorf("matte = %s", art.MatteId)
	}
	h.tv.RefuseMatte(true)
	_, err = h.svc.ChangeMatte(ctx, "MY_F0001", "none", "")
	var tvErr *samsung.TVError
	if !errors.As(err, &tvErr) || tvErr.Code != "-7" {
		t.Errorf("err = %v, want TVError -7", err)
	}
	if _, err := h.svc.ChangeMatte(ctx, "NOPE", "none", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown: err = %v", err)
	}
	types, colors, err := h.svc.ListMattes(ctx)
	if err != nil || len(types) == 0 || len(colors) == 0 || !strings.HasPrefix(colors[0].Hex, "#") || len(colors[0].Hex) != 7 {
		t.Errorf("mattes = %v %v %v", types, colors, err)
	}
}
