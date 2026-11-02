package coreapi

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"image"
	"image/jpeg"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"

	corev1 "github.com/yuha0/vigo/gen/go/vigo/core/v1"
	"github.com/yuha0/vigo/gen/go/vigo/core/v1/corev1connect"
	homev1 "github.com/yuha0/vigo/gen/go/vigo/home/v1"
	"github.com/yuha0/vigo/gen/go/vigo/home/v1/homev1connect"
	"github.com/yuha0/vigo/internal/db/dbtest"
	"github.com/yuha0/vigo/internal/imageprep"
	"github.com/yuha0/vigo/internal/library"
	"github.com/yuha0/vigo/internal/metrics"
	"github.com/yuha0/vigo/internal/pki"
	"github.com/yuha0/vigo/internal/pki/pkitest"
	"github.com/yuha0/vigo/internal/samsung/fake"
	"github.com/yuha0/vigo/internal/source"
	_ "github.com/yuha0/vigo/internal/source/file"
	"github.com/yuha0/vigo/internal/tv"
)

type env struct {
	url    string
	opsURL string
	tv     *fake.TV
	svc    *tv.Service
	server *Server
}

type envOpts struct {
	standby bool
	web     fstest.MapFS
	tweak   func(*Options)
}

func newService(t *testing.T, replica string, run bool) (*tv.Service, *fake.TV, *metrics.Metrics) {
	t.Helper()
	ctx := context.Background()
	st := tv.NewMemoryStore()
	if err := st.SetToken(ctx, fake.Token, "vigo"); err != nil {
		t.Fatal(err)
	}
	ftv := fake.New()
	m := metrics.New()
	svc, err := tv.New(tv.Options{
		Defaults:         tv.Config{Host: "tv.test", Port: 8002, ClientName: "vigo"},
		Store:            st,
		NewClient:        ftv.Client,
		Metrics:          m,
		Replica:          replica,
		PollInterval:     20 * time.Millisecond,
		WakeTimeout:      500 * time.Millisecond,
		WakePollInterval: 5 * time.Millisecond,
		ReconnectMin:     5 * time.Millisecond,
		ReconnectMax:     20 * time.Millisecond,
		// The upload pipeline at a small size: these tests are about the API.
		Prepare: func(r io.Reader, fit imageprep.Fit) ([]byte, error) { return imageprep.PrepareTo(r, fit, 64, 36) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if run {
		runCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		go func() { svc.Run(runCtx); close(done) }()
		t.Cleanup(func() { cancel(); <-done })
	}
	t.Cleanup(svc.Close)
	return svc, ftv, m
}

func serveH2C(t *testing.T, h http.Handler) string {
	t.Helper()
	srv := httptest.NewUnstartedServer(h)
	srv.Config = NewServer("", h)
	srv.Start()
	t.Cleanup(srv.Close)
	return srv.URL
}

func newEnv(t *testing.T, o envOpts) *env {
	t.Helper()
	svc, ftv, m := newService(t, "core-0", !o.standby)
	opts := Options{TV: svc, Metrics: m}
	if o.web != nil {
		opts.Web = o.web
	}
	if o.tweak != nil {
		o.tweak(&opts)
	}
	server, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{url: serveH2C(t, server.Public()), opsURL: serveH2C(t, server.Ops()), tv: ftv, svc: svc, server: server}
	if !o.standby {
		e.waitConnected(t)
	}
	return e
}

func (e *env) waitConnected(t *testing.T) {
	t.Helper()
	waitConnected(t, e.svc)
}

func waitConnected(t *testing.T, svc *tv.Service) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		st := svc.Status()
		if st.Connection == corev1.ConnectionState_CONNECTION_STATE_CONNECTED && st.Power == corev1.PowerState_POWER_STATE_ON {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("never connected")
}

func h2cClient() *http.Client {
	var p http.Protocols
	p.SetUnencryptedHTTP2(true)
	return &http.Client{Transport: &http.Transport{Protocols: &p}}
}

func connectCode(err error) connect.Code {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Code()
	}
	return connect.CodeUnknown
}

func TestUnaryRPCs(t *testing.T) {
	e := newEnv(t, envOpts{})
	ctx := context.Background()
	tvc := corev1connect.NewTVServiceClient(http.DefaultClient, e.url)
	art := corev1connect.NewArtServiceClient(http.DefaultClient, e.url)
	pairing := corev1connect.NewPairingServiceClient(http.DefaultClient, e.url)
	config := corev1connect.NewConfigServiceClient(http.DefaultClient, e.url)

	st, err := tvc.GetStatus(ctx, connect.NewRequest(&corev1.GetStatusRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if s := st.Msg.Status; !s.ShowingArt || !s.Leader || s.Replica != "core-0" || s.Device.GetModel() != "24_PTM_FTV_T09" {
		t.Errorf("status = %v", s)
	}

	tests := []struct {
		name string
		call func() error
		want connect.Code
	}{
		{"send KEY_POWER", func() error {
			_, err := tvc.SendKey(ctx, connect.NewRequest(&corev1.SendKeyRequest{Key: "KEY_POWER"}))
			return err
		}, connect.CodeInvalidArgument},
		{"send KEY_UP", func() error {
			_, err := tvc.SendKey(ctx, connect.NewRequest(&corev1.SendKeyRequest{Key: "KEY_UP", Action: corev1.KeyAction_KEY_ACTION_CLICK}))
			return err
		}, 0},
		{"delete samsung art", func() error {
			_, err := art.DeleteArt(ctx, connect.NewRequest(&corev1.DeleteArtRequest{ContentIds: []string{"SAM-S2701"}}))
			return err
		}, connect.CodeInvalidArgument},
		{"delete unknown", func() error {
			_, err := art.DeleteArt(ctx, connect.NewRequest(&corev1.DeleteArtRequest{ContentIds: []string{"MY_F0999"}}))
			return err
		}, connect.CodeNotFound},
		{"unknown setting", func() error {
			_, err := tvc.UpdateSetting(ctx, connect.NewRequest(&corev1.UpdateSettingRequest{Key: "volume", Value: &corev1.UpdateSettingRequest_IntValue{IntValue: 3}}))
			return err
		}, connect.CodeNotFound},
		{"select empty", func() error {
			_, err := art.SelectArt(ctx, connect.NewRequest(&corev1.SelectArtRequest{}))
			return err
		}, connect.CodeInvalidArgument},
		{"update config without config", func() error {
			_, err := config.UpdateConfig(ctx, connect.NewRequest(&corev1.UpdateConfigRequest{}))
			return err
		}, connect.CodeInvalidArgument},
	}
	for _, tt := range tests {
		err := tt.call()
		if tt.want == 0 {
			if err != nil {
				t.Errorf("%s: %v", tt.name, err)
			}
			continue
		}
		if got := connectCode(err); got != tt.want {
			t.Errorf("%s: code = %v (%v), want %v", tt.name, got, err, tt.want)
		}
	}

	upd, err := tvc.UpdateSetting(ctx, connect.NewRequest(&corev1.UpdateSettingRequest{Key: "motion_timer", Value: &corev1.UpdateSettingRequest_EnumValue{EnumValue: "off"}}))
	if err != nil {
		t.Fatal(err)
	}
	if !upd.Msg.Confirmed || upd.Msg.Setting.GetEnum().GetValue() != "off" {
		t.Errorf("update = %v", upd.Msg)
	}

	list, err := art.ListArt(ctx, connect.NewRequest(&corev1.ListArtRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(list.Msg.Artworks) != 10 || len(list.Msg.CategoryIds) != 3 {
		t.Errorf("list = %d artworks, categories %v", len(list.Msg.Artworks), list.Msg.CategoryIds)
	}

	p, err := pairing.GetPairing(ctx, connect.NewRequest(&corev1.GetPairingRequest{}))
	if err != nil || p.Msg.Pairing.State != corev1.PairingState_PAIRING_STATE_PAIRED {
		t.Errorf("pairing = %v, %v", p, err)
	}

	cfg, err := config.GetConfig(ctx, connect.NewRequest(&corev1.GetConfigRequest{}))
	if err != nil || cfg.Msg.Config.TvHost != "tv.test" || cfg.Msg.Config.TvPort != 8002 {
		t.Errorf("config = %v, %v", cfg, err)
	}

	e.tv.SetReachable(false)
	_, err = tvc.ShowArt(ctx, connect.NewRequest(&corev1.ShowArtRequest{}))
	if got := connectCode(err); got != connect.CodeFailedPrecondition {
		t.Errorf("ShowArt unreachable: code = %v (%v)", got, err)
	}
}

func TestStandbyReplicaIsUnavailable(t *testing.T) {
	e := newEnv(t, envOpts{standby: true})
	tvc := corev1connect.NewTVServiceClient(http.DefaultClient, e.url)
	_, err := tvc.ShowArt(context.Background(), connect.NewRequest(&corev1.ShowArtRequest{}))
	if got := connectCode(err); got != connect.CodeUnavailable {
		t.Errorf("code = %v", got)
	}
	st, err := tvc.GetStatus(context.Background(), connect.NewRequest(&corev1.GetStatusRequest{}))
	if err != nil || st.Msg.Status.Connection != corev1.ConnectionState_CONNECTION_STATE_STANDBY_REPLICA {
		t.Errorf("status = %v, %v", st, err)
	}
}

func TestWatchEvents(t *testing.T) {
	e := newEnv(t, envOpts{})
	clients := map[string]corev1connect.TVServiceClient{
		"connect over HTTP/1.1": corev1connect.NewTVServiceClient(http.DefaultClient, e.url),
		"gRPC over h2c":         corev1connect.NewTVServiceClient(h2cClient(), e.url, connect.WithGRPC()),
	}
	for name, client := range clients {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			stream, err := client.WatchEvents(ctx, connect.NewRequest(&corev1.WatchEventsRequest{}))
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			if !stream.Receive() {
				t.Fatalf("no first message: %v", stream.Err())
			}
			first := stream.Msg().GetStatus()
			if first == nil || first.Connection != corev1.ConnectionState_CONNECTION_STATE_CONNECTED {
				t.Fatalf("first message = %v", stream.Msg())
			}

			want := first.ArtMode != corev1.ArtModeState_ART_MODE_STATE_ON
			go client.SetArtMode(ctx, connect.NewRequest(&corev1.SetArtModeRequest{On: want}))
			wantState := corev1.ArtModeState_ART_MODE_STATE_OFF
			if want {
				wantState = corev1.ArtModeState_ART_MODE_STATE_ON
			}
			for stream.Receive() {
				if s := stream.Msg().GetStatus(); s != nil && s.ArtMode == wantState {
					return
				}
			}
			t.Fatalf("stream ended before the art mode change: %v", stream.Err())
		})
	}
}

func TestUnaryOverH2C(t *testing.T) {
	e := newEnv(t, envOpts{})
	resp, err := h2cClient().Get(e.url + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.ProtoMajor != 2 {
		t.Errorf("protocol = %s, want HTTP/2", resp.Proto)
	}
	client := corev1connect.NewArtServiceClient(h2cClient(), e.url, connect.WithGRPC())
	if _, err := client.ListMattes(context.Background(), connect.NewRequest(&corev1.ListMattesRequest{})); err != nil {
		t.Fatal(err)
	}
}

func TestThumbnail(t *testing.T) {
	e := newEnv(t, envOpts{})
	resp, err := http.Get(e.url + "/art/SAM-S2702/thumbnail")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("status %d, type %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if _, err := jpeg.Decode(bytes.NewReader(body)); err != nil {
		t.Errorf("not a JPEG: %v", err)
	}
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}

	req, _ := http.NewRequest("GET", e.url+"/art/SAM-S2702/thumbnail", nil)
	req.Header.Set("If-None-Match", etag)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotModified {
		t.Errorf("conditional status = %d", resp.StatusCode)
	}

	resp, err = http.Get(e.url + "/art/NOPE/thumbnail")
	if err != nil {
		t.Fatal(err)
	}
	var errBody struct{ Code, Message string }
	json.NewDecoder(resp.Body).Decode(&errBody)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || errBody.Code != "failed_precondition" {
		t.Errorf("unknown thumbnail: %d %+v", resp.StatusCode, errBody)
	}
}

func multipartBody(t *testing.T, fields map[string]string, file []byte) (string, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if file != nil {
		fw, _ := mw.CreateFormFile("file", "photo.jpg")
		fw.Write(file)
	}
	for k, v := range fields {
		mw.WriteField(k, v)
	}
	mw.Close()
	return mw.FormDataContentType(), &buf
}

func TestLibraryWithoutDatabase(t *testing.T) {
	e := newEnv(t, envOpts{})
	_, err := corev1connect.NewLibraryServiceClient(http.DefaultClient, e.url).ListArtworks(context.Background(), connect.NewRequest(&corev1.ListArtworksRequest{}))
	if connectCode(err) != connect.CodeUnavailable {
		t.Errorf("ListArtworks: %v", err)
	}
	ctype, body := multipartBody(t, nil, []byte("x"))
	resp, err := http.Post(e.url+"/sources/art/files", ctype, body)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("upload without a library: %d", resp.StatusCode)
	}
	resp, err = http.Post(e.url+"/art", ctype, body)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST /art still served: %d", resp.StatusCode)
	}
}

func multipartFile(t *testing.T, name string, data []byte, fields map[string]string) (string, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if data != nil {
		fw, _ := mw.CreateFormFile("file", name)
		fw.Write(data)
	}
	for k, v := range fields {
		mw.WriteField(k, v)
	}
	mw.Close()
	return mw.FormDataContentType(), &buf
}

func TestLibraryHTTP(t *testing.T) {
	dbe := dbtest.Open(t)
	svc, _, m := newService(t, "core-0", true)
	waitConnected(t, svc)
	root := t.TempDir()
	reg, err := source.NewRegistry([]string{"file://" + root + "?id=art"})
	if err != nil {
		t.Fatal(err)
	}
	lib, err := library.New(library.Options{DB: dbe.DB, TV: svc, Sources: reg, CacheDir: t.TempDir(), Metrics: m, SyncInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	svc.SetLibraryHook(lib)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { lib.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	for lib.Sync(ctx, "art") != nil {
		time.Sleep(10 * time.Millisecond)
	}
	e := newEnv(t, envOpts{standby: true, tweak: func(o *Options) {
		o.Library = lib
		o.MaxUploadBytes = 1 << 20
	}})

	var img bytes.Buffer
	jpeg.Encode(&img, image.NewRGBA(image.Rect(0, 0, 400, 300)), nil)
	var other bytes.Buffer
	jpeg.Encode(&other, image.NewGray(image.Rect(0, 0, 300, 400)), nil)

	tests := []struct {
		name     string
		url      string
		file     string
		data     []byte
		fields   map[string]string
		wantCode int
		wantErr  string
	}{
		{name: "into a folder", url: "/sources/art/files", file: "IMG_0001.jpg", data: img.Bytes(), fields: map[string]string{"path": "phone/2026"}, wantCode: 201},
		{name: "at the root", url: "/sources/art/files", file: "portrait.jpg", data: other.Bytes(), wantCode: 201},
		{name: "taken", url: "/sources/art/files", file: "IMG_0001.jpg", data: img.Bytes(), fields: map[string]string{"path": "phone/2026"}, wantCode: 409, wantErr: "already_exists"},
		{name: "escaping path", url: "/sources/art/files", file: "x.jpg", data: img.Bytes(), fields: map[string]string{"path": "../.."}, wantCode: 400, wantErr: "invalid_argument"},
		{name: "not an image", url: "/sources/art/files", file: "x.heic", data: []byte("ftypheic"), wantCode: 400, wantErr: "invalid_argument"},
		{name: "no file", url: "/sources/art/files", wantCode: 400, wantErr: "invalid_argument"},
		{name: "unknown source", url: "/sources/nas/files", file: "x.jpg", data: img.Bytes(), wantCode: 404, wantErr: "not_found"},
		{name: "too large", url: "/sources/art/files", file: "big.jpg", data: bytes.Repeat([]byte{1}, 2<<20), wantCode: 429, wantErr: "resource_exhausted"},
	}
	var uploaded *corev1.LibraryArtwork
	for _, tt := range tests {
		ctype, body := multipartFile(t, tt.file, tt.data, tt.fields)
		resp, err := http.Post(e.url+tt.url, ctype, body)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != tt.wantCode {
			t.Errorf("%s: status %d: %s", tt.name, resp.StatusCode, b)
			continue
		}
		if tt.wantErr != "" {
			var eb struct{ Code, Message string }
			if json.Unmarshal(b, &eb); eb.Code != tt.wantErr || eb.Message == "" {
				t.Errorf("%s: body %s", tt.name, b)
			}
			for _, prefix := range []string{"invalid argument", "not found:", "source:", "already exists:"} {
				if strings.Contains(strings.ToLower(eb.Message), prefix) && !strings.Contains(eb.Message, "already exists.") {
					t.Errorf("%s: message has a Go prefix: %q", tt.name, eb.Message)
				}
			}
			continue
		}
		var a corev1.LibraryArtwork
		if err := protojson.Unmarshal(b, &a); err != nil {
			t.Fatalf("%s: %s: %v", tt.name, b, err)
		}
		if uploaded == nil {
			uploaded = &a
			if len(a.Sources) != 1 || a.Sources[0].Path != "phone/2026/IMG_0001.jpg" || a.Width != 400 {
				t.Errorf("uploaded = %v", &a)
			}
		}
	}
	if _, err := os.Stat(filepath.Join(root, "phone/2026/IMG_0001.jpg")); err != nil {
		t.Error("upload not written into the source")
	}

	get := func(path string, header ...string) *http.Response {
		req, _ := http.NewRequest("GET", e.url+path, nil)
		if len(header) == 2 {
			req.Header.Set(header[0], header[1])
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return resp
	}
	thumb := get("/library/" + uploaded.Id + "/thumbnail")
	if thumb.StatusCode != 200 || thumb.Header.Get("Content-Type") != "image/jpeg" || !strings.Contains(thumb.Header.Get("Cache-Control"), "immutable") {
		t.Errorf("thumbnail: %d %v", thumb.StatusCode, thumb.Header)
	}
	if r := get("/library/"+uploaded.Id+"/thumbnail", "If-None-Match", thumb.Header.Get("ETag")); r.StatusCode != 304 {
		t.Errorf("conditional thumbnail: %d", r.StatusCode)
	}
	for path, want := range map[string]int{
		"/library/" + uploaded.Id + "/image": 200,
		"/library/tv:SAM-S2701/thumbnail":    200,
		"/library/tv:SAM-S2701/image":        404,
		"/library/sha256:nope/thumbnail":     404,
	} {
		if r := get(path); r.StatusCode != want {
			t.Errorf("GET %s: %d, want %d", path, r.StatusCode, want)
		}
	}

	c := corev1connect.NewLibraryServiceClient(http.DefaultClient, e.url)
	list, err := c.ListArtworks(ctx, connect.NewRequest(&corev1.ListArtworksRequest{Tags: []string{"source/art"}}))
	if err != nil || list.Msg.Total != 2 {
		t.Errorf("list = %v, %v", list, err)
	}
	srcs, err := c.ListSources(ctx, connect.NewRequest(&corev1.ListSourcesRequest{}))
	if err != nil || len(srcs.Msg.Sources) != 1 || !srcs.Msg.Sources[0].Writable || srcs.Msg.Sources[0].Display != root {
		t.Errorf("sources = %v, %v", srcs, err)
	}
	batch, err := c.DeleteArtworks(ctx, connect.NewRequest(&corev1.DeleteArtworksRequest{Ids: []string{"tv:SAM-S2701", "sha256:nope"}}))
	if err != nil {
		t.Fatal(err)
	}
	if r := batch.Msg.Results; len(r) != 2 || r[0].ErrorCode != "invalid_argument" || r[1].ErrorCode != "not_found" || r[1].ErrorMessage == "" {
		t.Errorf("DeleteArtworks results = %v", r)
	}
	refresh, err := c.RefreshThumbnails(ctx, connect.NewRequest(&corev1.RefreshThumbnailsRequest{Ids: []string{uploaded.Id, "tv:SAM-S2702"}}))
	if err != nil {
		t.Fatal(err)
	}
	if r := refresh.Msg.Results; len(r) != 2 || r[0].ErrorCode != "invalid_argument" || r[1].ErrorCode != "" {
		t.Errorf("RefreshThumbnails results = %v", r)
	}
	if _, err := c.DeleteArtworks(ctx, connect.NewRequest(&corev1.DeleteArtworksRequest{})); connectCode(err) != connect.CodeInvalidArgument {
		t.Errorf("empty batch: %v", err)
	}

	// A standby with no reachable leader still serves stored thumbnails
	// from the database, and forwards the rest.
	standbySvc, _, _ := newService(t, "core-standby", false)
	standby, err := New(Options{
		TV:      standbySvc,
		Library: lib,
		Leader:  func(context.Context) (string, error) { return "", errors.New("no leader") },
		PeerTLS: &tls.Config{},
	})
	if err != nil {
		t.Fatal(err)
	}
	standbyURL := serveH2C(t, standby.Public())
	for path, want := range map[string]int{
		"/library/tv:SAM-S2701/thumbnail":  200,
		"/art/SAM-S2701/thumbnail":         200,
		"/library/tv:NOT-STORED/thumbnail": 503,
	} {
		resp, err := http.Get(standbyURL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("standby GET %s: %d, want %d", path, resp.StatusCode, want)
		}
	}

	type codeCase struct {
		name string
		err  error
		want connect.Code
	}
	var codes []codeCase
	_, err = c.UpdateTags(ctx, connect.NewRequest(&corev1.UpdateTagsRequest{ArtworkId: uploaded.Id, Remove: []string{"status/on-tv"}}))
	codes = append(codes, codeCase{"remove a system tag", err, connect.CodeInvalidArgument})
	_, err = c.DeleteArtwork(ctx, connect.NewRequest(&corev1.DeleteArtworkRequest{Id: "tv:SAM-S2701"}))
	codes = append(codes, codeCase{"delete Art Store content", err, connect.CodeInvalidArgument})
	_, err = c.CropArtwork(ctx, connect.NewRequest(&corev1.CropArtworkRequest{Id: "tv:SAM-S2701", Rect: &corev1.Rect{Width: 1, Height: 1}}))
	codes = append(codes, codeCase{"crop TV-only", err, connect.CodeFailedPrecondition})
	_, err = c.GetArtwork(ctx, connect.NewRequest(&corev1.GetArtworkRequest{Id: "sha256:nope"}))
	codes = append(codes, codeCase{"unknown artwork", err, connect.CodeNotFound})
	_, err = c.SyncSource(ctx, connect.NewRequest(&corev1.SyncSourceRequest{SourceId: "nas"}))
	codes = append(codes, codeCase{"unknown source", err, connect.CodeNotFound})
	for _, cc := range codes {
		if got := connectCode(cc.err); got != cc.want {
			t.Errorf("%s: %v (%v), want %v", cc.name, got, cc.err, cc.want)
		}
	}
}

type homeStub struct {
	homev1connect.UnimplementedHomeServiceHandler
}

func (homeStub) GetProvider(context.Context, *connect.Request[homev1.GetProviderRequest]) (*connect.Response[homev1.GetProviderResponse], error) {
	return connect.NewResponse(&homev1.GetProviderResponse{Provider: &homev1.Provider{Protocol: homev1.Protocol_PROTOCOL_HOMEKIT, Implemented: true}}), nil
}

// serveHome serves the HomeService over mTLS with HTTP/1.1 only, as
// vigo-home does, and returns its URL.
func serveHome(t *testing.T, src *pki.Source) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(homev1connect.NewHomeServiceHandler(homeStub{}))
	srv := httptest.NewUnstartedServer(pki.Authenticate(mux))
	cfg := src.ServerTLS(func(id string) bool { return id == pki.Core })
	cfg.NextProtos = []string{"http/1.1"}
	srv.Listener = tls.NewListener(srv.Listener, cfg)
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.Start()
	t.Cleanup(srv.Close)
	return "https://" + srv.Listener.Addr().String()
}

func TestHomeProxy(t *testing.T) {
	homekit := pki.IntegrationIdentity("homekit")
	src := pkitest.Sources(t, pki.Core, homekit, pki.IntegrationIdentity("matter"))
	home := serveHome(t, src[homekit])
	impostor := serveHome(t, src[pki.IntegrationIdentity("matter")])

	down := httptest.NewServer(http.NotFoundHandler())
	downURL := "https://" + down.Listener.Addr().String()
	down.Close()

	endpoint := func(ep string, err error) func(context.Context) (string, error) {
		return func(context.Context) (string, error) { return ep, err }
	}
	tests := []struct {
		name     string
		endpoint func(context.Context) (string, error)
		want     connect.Code
	}{
		{name: "proxied", endpoint: endpoint(home, nil)},
		{name: "not configured", want: connect.CodeUnavailable},
		{name: "nothing registered", endpoint: endpoint("", errors.New("none")), want: connect.CodeUnavailable},
		{name: "down", endpoint: endpoint(downURL, nil), want: connect.CodeUnavailable},
		{name: "endpoint with another identity", endpoint: endpoint(impostor, nil), want: connect.CodeUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t, envOpts{standby: true, tweak: func(o *Options) {
				if tt.endpoint != nil {
					o.HomeEndpoint = tt.endpoint
					o.HomeTLS = src[pki.Core].ClientTLS(homekit)
				}
			}})
			client := homev1connect.NewHomeServiceClient(http.DefaultClient, e.url)
			resp, err := client.GetProvider(context.Background(), connect.NewRequest(&homev1.GetProviderRequest{}))
			if tt.want != 0 {
				if got := connectCode(err); got != tt.want {
					t.Errorf("code = %v (%v), want %v", got, err, tt.want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !resp.Msg.Provider.Implemented {
				t.Errorf("provider = %v", resp.Msg.Provider)
			}
		})
	}
}

// TestForwardToLeader runs a leader and a standby. The standby serves every
// API request by forwarding it to the leader's internal listener over mTLS.
func TestForwardToLeader(t *testing.T) {
	homekit := pki.IntegrationIdentity("homekit")
	src := pkitest.Sources(t, pki.Core, homekit, "spiffe://vigo/admin")

	leaderSvc, _, _ := newService(t, "core-leader", true)
	leader, err := New(Options{TV: leaderSvc})
	if err != nil {
		t.Fatal(err)
	}
	leaderURL := pkitest.ServeTLS(t, src[pki.Core], AllowInternal, leader.Internal())
	waitConnected(t, leaderSvc)

	standbySvc, _, _ := newService(t, "core-standby", false)
	var lookups atomic.Int32
	standby, err := New(Options{
		TV: standbySvc,
		Leader: func(context.Context) (string, error) {
			lookups.Add(1)
			return strings.TrimPrefix(leaderURL, "https://"), nil
		},
		PeerTLS: src[pki.Core].ClientTLS(pki.Core),
	})
	if err != nil {
		t.Fatal(err)
	}
	standbyPublic := serveH2C(t, standby.Public())
	standbyInternal := pkitest.ServeTLS(t, src[pki.Core], AllowInternal, standby.Internal())
	ctx := context.Background()

	clients := map[string]corev1connect.TVServiceClient{
		"browser via the standby":     corev1connect.NewTVServiceClient(http.DefaultClient, standbyPublic),
		"integration via the standby": corev1connect.NewTVServiceClient(pkitest.Client(src[homekit], pki.Core), standbyInternal, connect.WithGRPC()),
	}
	for name, c := range clients {
		st, err := c.GetStatus(ctx, connect.NewRequest(&corev1.GetStatusRequest{}))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if st.Msg.Status.Replica != "core-leader" || !st.Msg.Status.Leader {
			t.Errorf("%s: answered by %v", name, st.Msg.Status.Replica)
		}

		sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		stream, err := c.WatchEvents(sctx, connect.NewRequest(&corev1.WatchEventsRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		if !stream.Receive() || stream.Msg().GetStatus().GetReplica() != "core-leader" {
			t.Fatalf("%s: first event = %v, %v", name, stream.Msg(), stream.Err())
		}
		go c.SetArtMode(sctx, connect.NewRequest(&corev1.SetArtModeRequest{On: false}))
		got := false
		for stream.Receive() {
			if s := stream.Msg().GetStatus(); s != nil && s.ArtMode == corev1.ArtModeState_ART_MODE_STATE_OFF {
				got = true
				break
			}
		}
		if !got {
			t.Errorf("%s: no streamed update through the standby: %v", name, stream.Err())
		}
		c.SetArtMode(sctx, connect.NewRequest(&corev1.SetArtModeRequest{On: true}))
		stream.Close()
		cancel()
	}
	if lookups.Load() == 0 {
		t.Error("leader address never looked up")
	}

	resp, err := http.Get(standbyPublic + "/art/SAM-S2702/thumbnail")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/jpeg" {
		t.Errorf("thumbnail via the standby: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}

	// A request already forwarded once is not forwarded again, even if the
	// browser forges the header on the public listener it is ignored.
	looped := corev1connect.NewTVServiceClient(pkitest.Client(src[pki.Core], pki.Core), standbyInternal, connect.WithGRPC(),
		connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
			return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
				req.Header().Set(forwardedHeader, "core-x")
				return next(ctx, req)
			}
		})))
	if _, err := looped.GetStatus(ctx, connect.NewRequest(&corev1.GetStatusRequest{})); connectCode(err) != connect.CodeUnavailable {
		t.Errorf("re-forwarded request: %v", err)
	}
	req, _ := http.NewRequest("POST", standbyPublic+"/vigo.core.v1.TVService/GetStatus", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(forwardedHeader, "forged")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 200 {
		t.Errorf("forged header on the public listener: %v %v", resp, err)
	}

	// Other identities are refused at the handshake, and the internal
	// listener serves nothing else.
	if _, err := pkitest.Client(src["spiffe://vigo/admin"], pki.Core).Get(standbyInternal + "/"); err == nil {
		t.Error("unknown identity accepted")
	}
	if resp, err := pkitest.Client(src[homekit], pki.Core).Get(standbyInternal + "/index.html"); err != nil || resp.StatusCode != 404 {
		t.Errorf("web UI on the internal listener: %v %v", resp, err)
	}
}

func TestNoLeaderElected(t *testing.T) {
	src := pkitest.Sources(t, pki.Core)
	e := newEnv(t, envOpts{standby: true, tweak: func(o *Options) {
		o.Leader = func(context.Context) (string, error) { return "", errors.New("none") }
		o.PeerTLS = src[pki.Core].ClientTLS(pki.Core)
	}})
	_, err := corev1connect.NewTVServiceClient(http.DefaultClient, e.url).GetStatus(context.Background(), connect.NewRequest(&corev1.GetStatusRequest{}))
	if connectCode(err) != connect.CodeUnavailable {
		t.Errorf("err = %v", err)
	}
}

func TestWebUI(t *testing.T) {
	web := fstest.MapFS{
		"index.html":           {Data: []byte("<html>vigo</html>")},
		"assets/index-abc1.js": {Data: []byte("console.log(1)")},
		"manifest.webmanifest": {Data: []byte("{}")},
	}
	e := newEnv(t, envOpts{web: web})
	tests := []struct {
		path      string
		wantCode  int
		wantBody  string
		wantCache string
	}{
		{"/", 200, "<html>vigo</html>", "no-cache"},
		{"/gallery", 200, "<html>vigo</html>", "no-cache"},
		{"/tv/settings", 200, "<html>vigo</html>", "no-cache"},
		{"/assets/index-abc1.js", 200, "console.log(1)", "public, max-age=31536000, immutable"},
		{"/manifest.webmanifest", 200, "{}", "no-cache"},
		{"/assets/missing.js", 404, "", ""},
	}
	for _, tt := range tests {
		resp, err := http.Get(e.url + tt.path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != tt.wantCode {
			t.Errorf("%s: status %d", tt.path, resp.StatusCode)
			continue
		}
		if tt.wantBody != "" && string(b) != tt.wantBody {
			t.Errorf("%s: body %q", tt.path, b)
		}
		if tt.wantCache != "" && resp.Header.Get("Cache-Control") != tt.wantCache {
			t.Errorf("%s: Cache-Control %q", tt.path, resp.Header.Get("Cache-Control"))
		}
	}

	resp, err := http.Post(e.url+"/gallery", "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST to SPA route: %d", resp.StatusCode)
	}
}

func TestEmbeddedWebWithoutBuild(t *testing.T) {
	s, err := New(Options{})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.Public().ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusOK && rec.Code != http.StatusNotFound {
		t.Errorf("status = %d", rec.Code)
	}
}

func TestMetricsEndpoint(t *testing.T) {
	e := newEnv(t, envOpts{})
	for _, path := range []string{"/healthz", "/readyz"} {
		resp, err := http.Get(e.opsURL + path)
		if err != nil || resp.StatusCode != 200 {
			t.Errorf("%s: %v %v", path, resp, err)
		}
		if resp, _ := http.Get(e.url + path); resp != nil && resp.StatusCode == 200 && resp.Header.Get("Content-Type") != "text/html; charset=utf-8" {
			t.Errorf("%s served on the public listener", path)
		}
	}
	resp, err := http.Get(e.opsURL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	for _, want := range []string{
		`vigo_tv_requests_total{outcome="ok",request="dial_art"}`,
		`vigo_tv_connection_state{state="connected"} 1`,
		`vigo_tv_power_state{state="on"} 1`,
		"vigo_tv_request_duration_seconds_bucket",
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("metrics missing %s", want)
		}
	}
}
