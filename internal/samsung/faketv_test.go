package samsung

import (
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeTV is an in-process TV speaking the channel protocol as observed on a
// Tizen 9 The Frame: double-encoded d2d_service_message replies echoing
// request_id, error events, silently ignored unknown requests, D2D sockets.
type fakeTV struct {
	t   *testing.T
	srv *httptest.Server

	token string
	// silent makes the TV say nothing until the client sends the register
	// frame, as Tizen 9 does.
	silent bool
	noHost bool
	// reject is sent instead of ms.channel.connect.
	reject string
	// idleClose closes art connections that send nothing for this long.
	idleClose time.Duration
	// pairToken is issued after a tokenless register frame; empty means the
	// user denies.
	pairToken string
	// unsolicitedImageAdded sends image_added without request_id.
	unsolicitedImageAdded bool

	mu         sync.Mutex
	handlers   map[string]func(req map[string]any) []map[string]any
	requests   []map[string]any
	registers  []map[string]any
	remoteKeys []map[string]any
	artConns   []*fakeConn
	uploaded   []byte
	uploadHdr  map[string]any
	nextImage  int
}

type fakeConn struct {
	ws *websocket.Conn
	mu sync.Mutex
}

func (c *fakeConn) send(v any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ws.WriteJSON(v)
}

const fakeDeviceJSON = `{"device":{"FrameTVSupport":"true","TokenAuthSupport":"true","PowerState":"on",
"model":"24_PTM_FTV_T09","modelName":"QN75LS03DAFXZA","name":"75&quot; The Frame","networkType":"wired"},
"id":"uuid:x","type":"Samsung SmartTV"}`

func newFakeTV(t *testing.T) *fakeTV {
	f := &fakeTV{t: t, token: "11223344", pairToken: "55667788", nextImage: 1}
	f.handlers = defaultHandlers(f)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/" {
			http.NotFound(w, r)
			return
		}
		io.WriteString(w, fakeDeviceJSON)
	})
	mux.HandleFunc("/api/v2/channels/", f.serveChannel)
	f.srv = httptest.NewTLSServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeTV) config() Config {
	u, _ := url.Parse(f.srv.URL)
	port, _ := strconv.Atoi(u.Port())
	return Config{Host: u.Hostname(), Port: port, ClientName: "vigo", Token: f.token}
}

func (f *fakeTV) handle(name string, h func(req map[string]any) []map[string]any) {
	f.mu.Lock()
	f.handlers[name] = h
	f.mu.Unlock()
}

type fakeSnapshot struct {
	registers []map[string]any
	uploaded  []byte
	uploadHdr map[string]any
}

func (f *fakeTV) snapshot() fakeSnapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return fakeSnapshot{registers: f.registers, uploaded: f.uploaded, uploadHdr: f.uploadHdr}
}

func (f *fakeTV) requestsNamed(name string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, r := range f.requests {
		if r["request"] == name {
			out = append(out, r)
		}
	}
	return out
}

func d2dMessage(inner map[string]any) map[string]any {
	b, _ := json.MarshalIndent(inner, "", "  ")
	return map[string]any{"event": "d2d_service_message", "from": "host-2", "data": string(b)}
}

// emit sends an unsolicited art event to every open art connection.
func (f *fakeTV) emit(inner map[string]any) {
	f.mu.Lock()
	conns := append([]*fakeConn(nil), f.artConns...)
	f.mu.Unlock()
	for _, c := range conns {
		c.send(d2dMessage(inner))
	}
}

var upgrader = websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}

func (f *fakeTV) serveChannel(w http.ResponseWriter, r *http.Request) {
	channel := strings.TrimPrefix(r.URL.Path, "/api/v2/channels/")
	// Parse the query by hand: the name is unescaped base64, as the TV sees it.
	q := map[string]string{}
	for _, kv := range strings.Split(r.URL.RawQuery, "&") {
		k, v, _ := strings.Cut(kv, "=")
		q[k] = v
	}
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close()
	c := &fakeConn{ws: ws}
	token, hasToken := q["token"]

	if !hasToken {
		f.pairing(c)
		return
	}
	if f.reject != "" {
		c.send(map[string]any{"event": f.reject})
		return
	}
	if token != f.token {
		c.send(map[string]any{"event": "ms.channel.unauthorized"})
		return
	}
	if f.silent {
		if f.readRegister(c) == nil {
			return
		}
	}
	clients := []map[string]any{}
	if !f.noHost {
		clients = append(clients,
			map[string]any{"id": "host-1", "deviceName": "Smart Device", "isHost": true},
			map[string]any{"id": "host-2", "deviceName": "Smart Device", "isHost": true})
	}
	clients = append(clients, map[string]any{
		"id": "me", "deviceName": q["name"], "isHost": false,
		"attributes": map[string]any{"name": q["name"], "token": token},
	})
	if channel == channelArt {
		f.mu.Lock()
		f.artConns = append(f.artConns, c)
		f.mu.Unlock()
	}
	c.send(map[string]any{"event": "ms.channel.connect", "data": map[string]any{"id": "me", "clients": clients}})
	c.send(map[string]any{"event": "ms.channel.ready", "data": map[string]any{}})

	for {
		if f.idleClose > 0 {
			ws.SetReadDeadline(time.Now().Add(f.idleClose))
		}
		var msg struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := ws.ReadJSON(&msg); err != nil {
			return
		}
		switch msg.Method {
		case "ms.remote.control":
			f.mu.Lock()
			f.remoteKeys = append(f.remoteKeys, msg.Params)
			f.mu.Unlock()
		case "ms.channel.emit":
			data, _ := msg.Params["data"].(string)
			var req map[string]any
			if err := json.Unmarshal([]byte(data), &req); err != nil {
				f.t.Errorf("fake TV: bad art request data %q", data)
				return
			}
			go f.answer(c, data, req)
		}
	}
}

func (f *fakeTV) readRegister(c *fakeConn) map[string]any {
	var msg struct {
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}
	if err := c.ws.ReadJSON(&msg); err != nil {
		return nil
	}
	if msg.Method != "ms.channel.connect" {
		f.t.Errorf("fake TV: expected register frame, got %q", msg.Method)
		return nil
	}
	f.mu.Lock()
	f.registers = append(f.registers, msg.Params)
	f.mu.Unlock()
	return msg.Params
}

func (f *fakeTV) pairing(c *fakeConn) {
	p := f.readRegister(c)
	if p == nil {
		return
	}
	c.send(map[string]any{"event": "ms.error", "data": map[string]any{"message": "No Authorized"}})
	time.Sleep(50 * time.Millisecond)
	if f.pairToken == "" {
		c.send(map[string]any{"event": "ms.channel.unauthorized"})
		return
	}
	c.send(map[string]any{"event": "ms.channel.connect", "data": map[string]any{
		"id": "me", "token": f.pairToken, "clients": []any{},
	}})
	for {
		if _, _, err := c.ws.ReadMessage(); err != nil {
			return
		}
	}
}

func (f *fakeTV) answer(c *fakeConn, data string, req map[string]any) {
	name, _ := req["request"].(string)
	f.mu.Lock()
	f.requests = append(f.requests, req)
	h := f.handlers[name]
	f.mu.Unlock()
	if h == nil {
		return
	}
	rid, ok := req["request_id"].(string)
	if !ok {
		rid = data
	}
	for _, reply := range h(req) {
		if _, ok := reply["request_id"]; !ok {
			reply["request_id"] = rid
		}
		if reply["request_id"] == nil {
			delete(reply, "request_id")
		}
		if _, ok := reply["event"]; !ok {
			reply["event"] = name
		}
		c.send(d2dMessage(reply))
	}
}

func jsonString(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

func defaultHandlers(f *fakeTV) map[string]func(map[string]any) []map[string]any {
	one := func(m map[string]any) []map[string]any { return []map[string]any{m} }
	return map[string]func(map[string]any) []map[string]any{
		"get_artmode_status": func(map[string]any) []map[string]any {
			return one(map[string]any{"value": "on"})
		},
		"get_artmode_settings": func(map[string]any) []map[string]any {
			return one(map[string]any{"data": jsonString([]map[string]any{
				{"item": "brightness", "value": "4", "min": "0", "max": "10"},
				{"item": "motion_timer", "value": "off", "valid_values": jsonString([]string{"off", "5", "15"})},
				{"item": "brightness_sensor_setting", "value": "on"},
			})})
		},
		"get_content_list": func(map[string]any) []map[string]any {
			return one(map[string]any{"content_list": jsonString([]map[string]any{
				{"content_id": "SAM-F0222", "category_id": "MY-C0008", "slideshow": "false", "matte_id": "none",
					"portrait_matte_id": "flexible_polar", "width": 3840, "height": 2160, "image_date": "", "content_type": "preinstall"},
				{"content_id": "MY_F0001", "category_id": "MY-C0002", "slideshow": "true", "matte_id": "none",
					"portrait_matte_id": "shadowbox_polar", "width": "2160", "height": "3840", "image_date": "2026:11:01 06:39:13", "content_type": "mobile"},
			})})
		},
		"get_current_artwork": func(map[string]any) []map[string]any {
			return one(map[string]any{"content_id": "SAM-F0222", "matte_id": "none", "portrait_matte_id": "flexible_polar",
				"category_id": "DEFAULT_CATEGORY", "content_type": "artstore"})
		},
		"get_matte_list": func(map[string]any) []map[string]any {
			return one(map[string]any{
				"matte_type_list":  jsonString([]map[string]any{{"matte_type": "none"}, {"matte_type": "modern"}}),
				"matte_color_list": jsonString([]map[string]any{{"color": "black", "R": 34, "G": 34, "B": 33}}),
			})
		},
		"get_device_info": func(map[string]any) []map[string]any {
			return one(map[string]any{"tv_flash_size": 16, "support_motion_sensor": "TRUE", "support_brightness_sensor": "FALSE"})
		},
		"set_brightness": func(req map[string]any) []map[string]any {
			return one(map[string]any{"value": req["value"]})
		},
		"set_artmode_status": func(req map[string]any) []map[string]any {
			return one(map[string]any{"event": "set_artmode_status", "value": req["value"]})
		},
		"select_image": func(req map[string]any) []map[string]any {
			if _, ok := req["category_id"]; ok {
				return one(map[string]any{"event": "error", "error_code": "-7"})
			}
			return one(map[string]any{"event": "select_image", "is_shown": "Yes"})
		},
		"change_matte": func(map[string]any) []map[string]any {
			return one(map[string]any{"event": "error", "error_code": "-7"})
		},
		"delete_image_list": func(req map[string]any) []map[string]any {
			list := req["content_id_list"].([]any)
			var out []map[string]any
			for _, c := range list {
				out = append(out, map[string]any{"event": "image_deleted", "request_id": nil, "content_id": c.(map[string]any)["content_id"]})
			}
			return append(out, map[string]any{"content_id_list": jsonString(list)})
		},
		"get_thumbnail_list": func(req map[string]any) []map[string]any {
			return one(map[string]any{"conn_info": f.serveThumbnail()})
		},
		"send_image": f.receiveUpload,
	}
}

func (f *fakeTV) listen(secured bool) net.Listener {
	var ln net.Listener
	var err error
	if secured {
		ln, err = tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: f.srv.TLS.Certificates})
	} else {
		ln, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		f.t.Fatal(err)
	}
	return ln
}

func connInfoJSON(ln net.Listener, secured bool) string {
	return jsonString(map[string]any{
		"ip": "127.0.0.1", "port": ln.Addr().(*net.TCPAddr).Port, "key": "sec-key", "secured": secured,
	})
}

var thumbnailJPEG = []byte("\xff\xd8\xff\xe0fake-jpeg-payload\xff\xd9")

// serveThumbnail serves one file on a plaintext D2D socket.
func (f *fakeTV) serveThumbnail() string {
	ln := f.listen(false)
	go func() {
		defer ln.Close()
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		hdr, _ := json.Marshal(map[string]any{"fileID": "x", "fileLength": len(thumbnailJPEG), "fileType": "jpg", "num": "0", "total": "1"})
		binary.Write(conn, binary.BigEndian, uint32(len(hdr)))
		conn.Write(hdr)
		conn.Write(thumbnailJPEG)
	}()
	return connInfoJSON(ln, false)
}

// receiveUpload accepts a send_image on a TLS D2D socket. Like the TV it
// silently drops the image unless id, request_id and conn_info.id agree.
func (f *fakeTV) receiveUpload(req map[string]any) []map[string]any {
	ln := f.listen(true)
	ci, _ := req["conn_info"].(map[string]any)
	valid := req["id"] == req["request_id"] && ci["id"] == req["id"]
	rid := req["request_id"]
	go func() {
		defer ln.Close()
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var n uint32
		if binary.Read(conn, binary.BigEndian, &n) != nil {
			return
		}
		hb := make([]byte, n)
		io.ReadFull(conn, hb)
		var hdr map[string]any
		json.Unmarshal(hb, &hdr)
		data, _ := io.ReadAll(conn)
		f.mu.Lock()
		f.uploaded, f.uploadHdr = data, hdr
		id := fmt.Sprintf("MY_F%04d", f.nextImage)
		f.nextImage++
		f.mu.Unlock()
		if !valid || hdr["secKey"] != "sec-key" || int(hdr["fileLength"].(float64)) != len(data) {
			return
		}
		added := map[string]any{"event": "image_added", "content_id": id}
		if !f.unsolicitedImageAdded {
			added["request_id"] = rid
		}
		f.emit(map[string]any{"event": "send_image", "content_id": id, "request_id": rid})
		f.emit(added)
	}()
	return []map[string]any{{"event": "ready_to_use", "conn_info": connInfoJSON(ln, true)}}
}
