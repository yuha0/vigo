package samsung

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func testClient(t *testing.T, cfg Config) (*client, *syncBuffer) {
	t.Helper()
	logs := &syncBuffer{}
	c := New(cfg, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))).(*client)
	c.timeouts.register = 200 * time.Millisecond
	c.timeouts.handshake = 2 * time.Second
	c.timeouts.request = time.Second
	c.timeouts.d2dConnectDelay = 0
	c.timeouts.imageAdded = 2 * time.Second
	t.Cleanup(func() {
		if cfg.Token != "" && strings.Contains(logs.String(), cfg.Token) {
			t.Errorf("token leaked into logs:\n%s", logs.String())
		}
	})
	return c, logs
}

func dialArt(t *testing.T, f *fakeTV) (*client, *artConn) {
	t.Helper()
	c, _ := testClient(t, f.config())
	conn, err := c.DialArt(context.Background())
	if err != nil {
		t.Fatalf("DialArt: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return c, conn.(*artConn)
}

func TestDeviceInfo(t *testing.T) {
	f := newFakeTV(t)
	c, _ := testClient(t, f.config())
	got, err := c.DeviceInfo(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := DeviceInfo{PowerState: "on", Model: "24_PTM_FTV_T09", ModelName: "QN75LS03DAFXZA",
		Name: `75" The Frame`, NetworkType: "wired", FrameTVSupport: true, TokenAuthSupport: true}
	if got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestAwake(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	c, _ := testClient(t, Config{Host: "127.0.0.1", Port: 8002})
	c.awakePort = ln.Addr().(*net.TCPAddr).Port
	if ok, err := c.Awake(context.Background()); !ok || err != nil {
		t.Errorf("listening: got %v, %v; want true", ok, err)
	}
	ln.Close()
	if ok, err := c.Awake(context.Background()); ok || err != nil {
		t.Errorf("refused: got %v, %v; want false, nil", ok, err)
	}
}

func TestDialArtPlainHandshake(t *testing.T) {
	f := newFakeTV(t)
	dialArt(t, f)
	if len(f.snapshot().registers) != 0 {
		t.Errorf("sent %d register frames to a TV that volunteered ms.channel.connect", len(f.snapshot().registers))
	}
}

func TestDialArtSilentHandshake(t *testing.T) {
	f := newFakeTV(t)
	f.silent = true
	dialArt(t, f)
	if len(f.snapshot().registers) != 1 {
		t.Fatalf("got %d register frames, want 1", len(f.snapshot().registers))
	}
	r := f.snapshot().registers[0]
	if r["token"] != f.token || r["deviceName"] != "dmlnbw==" || r["id"] != "vigo" || r["type"] != "native" {
		t.Errorf("register frame %v", r)
	}
}

func TestDialArtNoHost(t *testing.T) {
	f := newFakeTV(t)
	f.noHost = true
	c, _ := testClient(t, f.config())
	if _, err := c.DialArt(context.Background()); !errors.Is(err, ErrNoArtHost) {
		t.Errorf("got %v, want ErrNoArtHost", err)
	}
	if _, err := c.DialRemote(context.Background()); err != nil {
		t.Errorf("remote channel needs no art host: %v", err)
	}
}

func TestDialUnauthorized(t *testing.T) {
	for _, tc := range []struct {
		name   string
		token  string
		reject string
	}{
		{name: "wrong token", token: "99999999"},
		{name: "timeOut", reject: "ms.channel.timeOut"},
		{name: "unauthorized", reject: "ms.channel.unauthorized"},
		{name: "no token"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeTV(t)
			f.reject = tc.reject
			cfg := f.config()
			if tc.name != "timeOut" && tc.name != "unauthorized" {
				cfg.Token = tc.token
			}
			c, _ := testClient(t, cfg)
			if _, err := c.DialArt(context.Background()); !errors.Is(err, ErrUnauthorized) {
				t.Errorf("art: got %v, want ErrUnauthorized", err)
			}
			if _, err := c.DialRemote(context.Background()); !errors.Is(err, ErrUnauthorized) {
				t.Errorf("remote: got %v, want ErrUnauthorized", err)
			}
		})
	}
}

func TestTokenNeverInErrors(t *testing.T) {
	const token = "31415926"
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	for _, cfg := range []Config{
		{Host: "127.0.0.1", Port: port, ClientName: "vigo", Token: token},
		{Host: "bad host%zz", Port: 8002, ClientName: "vigo", Token: token},
		{Host: "127.0.0.1", Port: port, ClientName: "vigo", Token: token + "&x=" + token},
	} {
		c, _ := testClient(t, cfg)
		_, err := c.DialArt(context.Background())
		if err == nil {
			t.Fatalf("%q: dial succeeded", cfg.Host)
		}
		if strings.Contains(err.Error(), token) {
			t.Errorf("token in error: %v", err)
		}
		for e := err; e != nil; e = errors.Unwrap(e) {
			if strings.Contains(e.Error(), token) {
				t.Errorf("token reachable by Unwrap: %v", e)
			}
		}
	}
}

func TestDialContextCanceled(t *testing.T) {
	f := newFakeTV(t)
	f.silent = true
	c, _ := testClient(t, f.config())
	c.timeouts.register = time.Hour
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := c.DialArt(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("got %v, want DeadlineExceeded", err)
	}
}

func TestPair(t *testing.T) {
	f := newFakeTV(t)
	cfg := f.config()
	cfg.Token = ""
	c, logs := testClient(t, cfg)
	tok, err := c.Pair(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok != f.pairToken {
		t.Errorf("got token %q, want %q", tok, f.pairToken)
	}
	if r := f.snapshot().registers[0]; r["token"] != "" || r["deviceName"] != "dmlnbw==" {
		t.Errorf("register frame %v", r)
	}
	if strings.Contains(logs.String(), f.pairToken) {
		t.Errorf("issued token leaked into logs:\n%s", logs.String())
	}
}

func TestPairDenied(t *testing.T) {
	f := newFakeTV(t)
	f.pairToken = ""
	cfg := f.config()
	cfg.Token = ""
	c, _ := testClient(t, cfg)
	if _, err := c.Pair(context.Background()); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("got %v, want ErrUnauthorized", err)
	}
}

func TestArtReads(t *testing.T) {
	f := newFakeTV(t)
	_, a := dialArt(t, f)
	ctx := context.Background()

	if on, err := a.ArtModeStatus(ctx); err != nil || !on {
		t.Errorf("ArtModeStatus: %v, %v", on, err)
	}
	settings, err := a.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantSettings := []ArtSetting{
		{Item: "brightness", Value: "4", Min: "0", Max: "10"},
		{Item: "motion_timer", Value: "off", ValidValues: []string{"off", "5", "15"}},
		{Item: "brightness_sensor_setting", Value: "on"},
	}
	if !reflect.DeepEqual(settings, wantSettings) {
		t.Errorf("Settings: got %+v", settings)
	}

	content, err := a.ContentList(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	wantContent := []Content{
		{ContentID: "SAM-F0222", CategoryID: "MY-C0008", ContentType: "preinstall", MatteID: "none",
			PortraitMatteID: "flexible_polar", Width: 3840, Height: 2160},
		{ContentID: "MY_F0001", CategoryID: "MY-C0002", ContentType: "mobile", MatteID: "none",
			PortraitMatteID: "shadowbox_polar", Width: 2160, Height: 3840, ImageDate: "2026:11:01 06:39:13", Slideshow: true},
	}
	if !reflect.DeepEqual(content, wantContent) {
		t.Errorf("ContentList: got %+v", content)
	}
	if _, ok := f.requestsNamed("get_content_list")[0]["category"]; ok {
		t.Error("empty category was sent")
	}
	mine, err := a.ContentList(ctx, "MY-C0002")
	if err != nil {
		t.Fatal(err)
	}
	if len(mine) != 1 || mine[0].ContentID != "MY_F0001" {
		t.Errorf("ContentList(MY-C0002): got %+v", mine)
	}
	if got := f.requestsNamed("get_content_list")[1]["category"]; got != "MY-C0002" {
		t.Errorf("category %v", got)
	}

	cur, err := a.CurrentArtwork(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cur != (CurrentArtwork{ContentID: "SAM-F0222", MatteID: "none", PortraitMatteID: "flexible_polar",
		CategoryID: "DEFAULT_CATEGORY", ContentType: "artstore"}) {
		t.Errorf("CurrentArtwork: %+v", cur)
	}

	ml, err := a.MatteList(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(ml, MatteList{Types: []string{"none", "modern"}, Colors: []MatteColor{{Name: "black", R: 34, G: 34, B: 33}}}) {
		t.Errorf("MatteList: %+v", ml)
	}

	di, err := a.ArtDeviceInfo(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if di != (ArtDeviceInfo{FlashSizeGB: 16, SupportsMotionSensor: true}) {
		t.Errorf("ArtDeviceInfo: %+v", di)
	}
}

func TestRequestIDCorrelation(t *testing.T) {
	f := newFakeTV(t)
	_, a := dialArt(t, f)
	if _, err := a.ArtModeStatus(context.Background()); err != nil {
		t.Fatal(err)
	}
	req := f.requestsNamed("get_artmode_status")[0]
	if req["id"] == "" || req["id"] != req["request_id"] {
		t.Errorf("id %v and request_id %v must be one uuid", req["id"], req["request_id"])
	}

	// Without request_id the TV echoes the entire request JSON instead.
	f.handle("get_current_artwork", func(req map[string]any) []map[string]any {
		data := `{"request": "get_current_artwork", "id": "` + req["id"].(string) + `"}`
		return []map[string]any{{"request_id": data, "content_id": "SAM-1"}}
	})
	cur, err := a.CurrentArtwork(context.Background())
	if err != nil || cur.ContentID != "SAM-1" {
		t.Errorf("got %+v, %v", cur, err)
	}
}

func TestSetters(t *testing.T) {
	f := newFakeTV(t)
	_, a := dialArt(t, f)
	ctx := context.Background()
	if err := a.SetSetting(ctx, "brightness", "7"); err != nil {
		t.Fatal(err)
	}
	if got := f.requestsNamed("set_brightness")[0]["value"]; got != "7" {
		t.Errorf("value %v", got)
	}
	if err := a.SetArtModeStatus(ctx, true); err != nil {
		t.Fatal(err)
	}
	if got := f.requestsNamed("set_artmode_status")[0]["value"]; got != "on" {
		t.Errorf("value %v", got)
	}
}

func TestSelectImageOmitsCategory(t *testing.T) {
	f := newFakeTV(t)
	_, a := dialArt(t, f)
	if err := a.SelectImage(context.Background(), "SAM-F0222"); err != nil {
		t.Fatal(err)
	}
	req := f.requestsNamed("select_image")[0]
	if _, ok := req["category_id"]; ok {
		t.Errorf("category_id sent: %v", req)
	}
	if req["content_id"] != "SAM-F0222" || req["show"] != true {
		t.Errorf("request %v", req)
	}
}

func TestTVError(t *testing.T) {
	f := newFakeTV(t)
	_, a := dialArt(t, f)
	err := a.ChangeMatte(context.Background(), "MY_F0001", "modern_polar", "")
	var tvErr *TVError
	if !errors.As(err, &tvErr) || tvErr.Code != "-7" || tvErr.Request != "change_matte" {
		t.Fatalf("got %v, want TVError -7", err)
	}
	if _, ok := f.requestsNamed("change_matte")[0]["portrait_matte_id"]; ok {
		t.Error("empty portrait_matte_id sent")
	}
}

func TestNoReply(t *testing.T) {
	f := newFakeTV(t)
	c, a := dialArt(t, f)
	c.timeouts.request = 100 * time.Millisecond
	f.handle("get_artmode_status", nil)
	start := time.Now()
	if _, err := a.ArtModeStatus(context.Background()); !errors.Is(err, ErrNoReply) {
		t.Fatalf("got %v, want ErrNoReply", err)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("took %s", d)
	}

	c.timeouts.request = time.Hour
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := a.ArtModeStatus(ctx)
	if !errors.Is(err, ErrNoReply) || !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("context deadline: got %v", err)
	}
}

func TestProtocolError(t *testing.T) {
	f := newFakeTV(t)
	_, a := dialArt(t, f)
	ctx := context.Background()
	var pe *ProtocolError

	f.handle("get_artmode_status", func(map[string]any) []map[string]any {
		return []map[string]any{{"status": "on"}}
	})
	if _, err := a.ArtModeStatus(ctx); !errors.As(err, &pe) {
		t.Errorf("missing value: got %v", err)
	}
	f.handle("get_content_list", func(map[string]any) []map[string]any {
		return []map[string]any{{"content_list": "not json"}}
	})
	if _, err := a.ContentList(ctx, ""); !errors.As(err, &pe) {
		t.Errorf("bad content_list: got %v", err)
	}
	f.handle("get_thumbnail_list", func(map[string]any) []map[string]any {
		return []map[string]any{{"content_id": "x"}}
	})
	if _, err := a.Thumbnail(ctx, "x"); !errors.As(err, &pe) {
		t.Errorf("no conn_info: got %v", err)
	}
}

func TestEvents(t *testing.T) {
	f := newFakeTV(t)
	_, a := dialArt(t, f)
	f.emit(map[string]any{"event": "art_mode_changed", "status": "on"})
	select {
	case ev := <-a.Events():
		if ev.Name != "art_mode_changed" || ev.Data["status"] != "on" {
			t.Errorf("event %+v", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no event")
	}

	// A consumer that never reads must not stall replies.
	for range eventBuffer + 10 {
		f.emit(map[string]any{"event": "image_selected"})
	}
	if _, err := a.ArtModeStatus(context.Background()); err != nil {
		t.Fatalf("request behind a full event buffer: %v", err)
	}
}

func TestDeleteImages(t *testing.T) {
	f := newFakeTV(t)
	_, a := dialArt(t, f)
	got, err := a.DeleteImages(context.Background(), []string{"MY_F0001", "MY_F0002"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"MY_F0001", "MY_F0002"}) {
		t.Errorf("deleted %v", got)
	}
}

func TestThumbnail(t *testing.T) {
	f := newFakeTV(t)
	_, a := dialArt(t, f)
	got, err := a.Thumbnail(context.Background(), "SAM-F0222")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, thumbnailJPEG) {
		t.Errorf("got %q", got)
	}
	req := f.requestsNamed("get_thumbnail_list")[0]
	if ci := req["conn_info"].(map[string]any); ci["d2d_mode"] != "socket" || ci["id"] != req["id"] {
		t.Errorf("conn_info %v", ci)
	}
}

func TestThumbnailTVError(t *testing.T) {
	f := newFakeTV(t)
	_, a := dialArt(t, f)
	f.handle("get_thumbnail_list", func(req map[string]any) []map[string]any {
		ln := f.listen(true)
		go func() {
			conn, err := ln.Accept()
			if err == nil {
				conn.(*tls.Conn).NetConn().Close()
			}
			ln.Close()
		}()
		return []map[string]any{
			{"conn_info": connInfoJSON(ln, true)},
			{"event": "error", "error_code": "-1"},
		}
	})
	_, err := a.Thumbnail(context.Background(), "MY_F0004")
	var tvErr *TVError
	if !errors.As(err, &tvErr) || tvErr.Code != "-1" || tvErr.Request != "get_thumbnail_list" {
		t.Errorf("got %v, want TVError -1", err)
	}
}

func TestThumbnailNoData(t *testing.T) {
	f := newFakeTV(t)
	_, a := dialArt(t, f)
	f.handle("get_thumbnail_list", func(req map[string]any) []map[string]any {
		ln := f.listen(true)
		go func() {
			conn, err := ln.Accept()
			if err == nil {
				conn.(*tls.Conn).Handshake()
				conn.Close()
			}
			ln.Close()
		}()
		return []map[string]any{{"conn_info": connInfoJSON(ln, true)}}
	})
	if _, err := a.Thumbnail(context.Background(), "SAM-S10003825"); !errors.Is(err, ErrNoData) {
		t.Errorf("got %v, want ErrNoData", err)
	}
}

func TestUpload(t *testing.T) {
	for _, unsolicited := range []bool{false, true} {
		f := newFakeTV(t)
		f.unsolicitedImageAdded = unsolicited
		_, a := dialArt(t, f)
		img := []byte("\xff\xd8 not really a jpeg \xff\xd9")
		date := time.Date(2026, 11, 1, 6, 39, 13, 0, time.UTC)
		id, err := a.Upload(context.Background(), Upload{Data: img, Date: date})
		if err != nil {
			t.Fatalf("unsolicited=%v: %v", unsolicited, err)
		}
		if id != "MY_F0001" {
			t.Errorf("content id %q", id)
		}
		if !bytes.Equal(f.snapshot().uploaded, img) {
			t.Errorf("uploaded %q", f.snapshot().uploaded)
		}
		req := f.requestsNamed("send_image")[0]
		if req["file_type"] != "jpg" || req["image_date"] != "2026:11:01 06:39:13" || req["matte_id"] != "none" ||
			req["file_size"] != float64(len(img)) {
			t.Errorf("request %v", req)
		}
		if f.snapshot().uploadHdr["fileType"] != "jpg" || f.snapshot().uploadHdr["fileName"] != "image" || f.snapshot().uploadHdr["version"] != "0.0.1" {
			t.Errorf("d2d header %v", f.snapshot().uploadHdr)
		}
	}
}

func TestUploadDiscardedTimesOut(t *testing.T) {
	f := newFakeTV(t)
	_, a := dialArt(t, f)
	f.handle("send_image", func(req map[string]any) []map[string]any {
		req["request_id"] = "something-else"
		return f.receiveUpload(req)
	})
	a.c.timeouts.imageAdded = 200 * time.Millisecond
	if _, err := a.Upload(context.Background(), Upload{Data: []byte("x")}); !errors.Is(err, ErrNoReply) {
		t.Errorf("got %v, want ErrNoReply", err)
	}
}

func TestKeepalive(t *testing.T) {
	f := newFakeTV(t)
	f.idleClose = 300 * time.Millisecond
	c, _ := testClient(t, f.config())
	c.timeouts.keepalive = 100 * time.Millisecond
	conn, err := c.DialArt(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	select {
	case <-conn.Done():
		t.Fatalf("closed despite keepalive: %v", conn.Err())
	case <-time.After(time.Second):
	}
	if n := len(f.requestsNamed("get_artmode_status")); n < 3 {
		t.Errorf("%d keepalives", n)
	}
}

func TestIdleCloseReported(t *testing.T) {
	f := newFakeTV(t)
	f.idleClose = 100 * time.Millisecond
	c, _ := testClient(t, f.config())
	c.timeouts.keepalive = time.Hour
	conn, err := c.DialArt(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if conn.Err() != nil {
		t.Error("Err non-nil while open")
	}
	select {
	case <-conn.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("TV close not noticed")
	}
	if conn.Err() == nil {
		t.Error("Err nil after close")
	}
	if _, ok := <-conn.Events(); ok {
		t.Error("Events not closed")
	}
	if _, err := conn.ArtModeStatus(context.Background()); err == nil {
		t.Error("request on closed connection succeeded")
	}
}

func TestClose(t *testing.T) {
	f := newFakeTV(t)
	_, a := dialArt(t, f)
	f.handle("get_artmode_status", nil)
	errc := make(chan error, 1)
	go func() {
		_, err := a.ArtModeStatus(context.Background())
		errc <- err
	}()
	time.Sleep(50 * time.Millisecond)
	a.Close()
	if err := <-errc; !errors.Is(err, net.ErrClosed) {
		t.Errorf("pending request: got %v", err)
	}
	if !errors.Is(a.Err(), net.ErrClosed) {
		t.Errorf("Err: %v", a.Err())
	}
	a.Close()
}

func TestRemote(t *testing.T) {
	f := newFakeTV(t)
	c, _ := testClient(t, f.config())
	r, err := c.DialRemote(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SendKey(context.Background(), "KEY_UP", KeyClick); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; {
		f.mu.Lock()
		n := len(f.remoteKeys)
		f.mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	r.Close()
	if !errors.Is(r.Err(), net.ErrClosed) {
		t.Errorf("Err: %v", r.Err())
	}
	if err := r.SendKey(context.Background(), "KEY_UP", KeyClick); err == nil {
		t.Error("SendKey on closed connection succeeded")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	want := map[string]any{"Cmd": "Click", "DataOfCmd": "KEY_UP", "Option": "false", "TypeOfRemote": "SendRemoteKey"}
	if len(f.remoteKeys) != 1 || !reflect.DeepEqual(f.remoteKeys[0], want) {
		t.Errorf("keys %v", f.remoteKeys)
	}
}

func TestRedactor(t *testing.T) {
	r := redactor{token: "12345678"}
	for in, want := range map[string]string{
		"wss://h:8002/x?name=dmlnbw==&token=12345678":                 "wss://h:8002/x?name=dmlnbw==&token=REDACTED",
		`{"attributes":{"token":"87654321"}}`:                         `{"attributes":{"token":"REDACTED"}}`,
		`{"data":"{\"token\": \"87654321\", \"x\": 1}"}`:              `{"data":"{\"token\": \"REDACTED\", \"x\": 1}"}`,
		"plain 12345678 text":                                         "plain REDACTED text",
		`{"event":"ms.channel.connect","data":{"token":"","id":"x"}}`: `{"event":"ms.channel.connect","data":{"token":"REDACTED","id":"x"}}`,
	} {
		if got := r.string(in); got != want {
			t.Errorf("%s\n got %s\nwant %s", in, got, want)
		}
	}
}
