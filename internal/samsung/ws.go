package samsung

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	channelRemote = "samsung.remote.control"
	channelArt    = "com.samsung.art-app"

	maxFrameSize = 16 << 20
)

// envelope is the outer shape of every message the TV sends on a channel.
type envelope struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

type connectData struct {
	ID      string `json:"id"`
	Token   string `json:"token"`
	Clients []struct {
		ID         string `json:"id"`
		DeviceName string `json:"deviceName"`
		IsHost     bool   `json:"isHost"`
	} `json:"clients"`
}

func (d *connectData) hosts() int {
	n := 0
	for _, c := range d.Clients {
		if c.IsHost {
			n++
		}
	}
	return n
}

// redactor scrubs the token from anything that may leave the package.
type redactor struct {
	token string
}

var (
	tokenQueryRE = regexp.MustCompile(`token=[^&\s"']*`)
	// Matches "token":"..." at any level of JSON-in-JSON escaping, since the
	// connect event lists every client with its token in attributes.token.
	tokenFieldRE = regexp.MustCompile(`(\\*"token\\*"\s*:\s*\\*")[^"\\]*`)
)

func (r redactor) string(s string) string {
	if r.token != "" {
		s = strings.ReplaceAll(s, r.token, "REDACTED")
		if q := url.QueryEscape(r.token); q != r.token {
			s = strings.ReplaceAll(s, q, "REDACTED")
		}
	}
	s = tokenQueryRE.ReplaceAllString(s, "token=REDACTED")
	return tokenFieldRE.ReplaceAllString(s, "${1}REDACTED")
}

// error returns err unchanged when its message is clean. Otherwise it returns
// an error with a scrubbed message that still matches errors.Is targets of the
// original but never exposes it through Unwrap.
func (r redactor) error(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	clean := r.string(msg)
	if clean == msg {
		return err
	}
	return &redactedError{msg: clean, orig: err}
}

type redactedError struct {
	msg  string
	orig error
}

func (e *redactedError) Error() string        { return e.msg }
func (e *redactedError) Is(target error) bool { return errors.Is(e.orig, target) }

// wsConn is a channel connection with a read pump. Every text frame is
// delivered on in, which is closed when reading stops; readErr is valid after
// that.
type wsConn struct {
	ws      *websocket.Conn
	log     *slog.Logger
	red     redactor
	in      chan []byte
	readErr error

	writeMu      sync.Mutex
	writeTimeout time.Duration
}

func newWSConn(ws *websocket.Conn, log *slog.Logger, red redactor, writeTimeout time.Duration) *wsConn {
	w := &wsConn{
		ws:           ws,
		log:          log,
		red:          red,
		in:           make(chan []byte, 64),
		writeTimeout: writeTimeout,
	}
	go w.readPump()
	return w
}

func (w *wsConn) readPump() {
	defer close(w.in)
	for {
		typ, data, err := w.ws.ReadMessage()
		if err != nil {
			w.readErr = err
			return
		}
		if typ != websocket.TextMessage {
			continue
		}
		w.log.Debug("samsung: recv", "frame", w.red.string(string(data)))
		w.in <- data
	}
}

func (w *wsConn) writeJSON(ctx context.Context, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(w.writeTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	w.writeMu.Lock()
	defer w.writeMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	w.log.Debug("samsung: send", "frame", w.red.string(string(b)))
	_ = w.ws.SetWriteDeadline(deadline)
	if err := w.ws.WriteMessage(websocket.TextMessage, b); err != nil {
		return w.red.error(fmt.Errorf("samsung: write: %w", err))
	}
	return nil
}

func (w *wsConn) close() {
	_ = w.ws.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		time.Now().Add(time.Second))
	_ = w.ws.Close()
}

// closedError describes why the read pump stopped. Call only after in is closed.
func (w *wsConn) closedError(channel string) error {
	err := w.readErr
	if err == nil {
		err = errors.New("closed")
	}
	return w.red.error(fmt.Errorf("samsung: %s connection closed: %w", channel, err))
}

// connState tracks closure for ArtConn and RemoteConn.
type connState struct {
	done chan struct{}
	mu   sync.Mutex
	err  error
}

func newConnState() connState { return connState{done: make(chan struct{})} }

func (s *connState) setErr(err error) {
	s.mu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.mu.Unlock()
}

// finish must be called exactly once, by the goroutine draining the socket.
func (s *connState) finish(err error) {
	s.setErr(err)
	close(s.done)
}

func (s *connState) Done() <-chan struct{} { return s.done }

func (s *connState) Err() error {
	select {
	case <-s.done:
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.err
	default:
		return nil
	}
}

var errClientClosed = fmt.Errorf("samsung: connection closed by client: %w", net.ErrClosed)

func (c *client) secure() bool { return c.cfg.Port != 8001 }

func (c *client) hostPort(port int) string {
	return net.JoinHostPort(c.cfg.Host, strconv.Itoa(port))
}

func (c *client) encodedName() string {
	return base64.StdEncoding.EncodeToString([]byte(c.cfg.ClientName))
}

func (c *client) channelURL(channel string, withToken bool) string {
	scheme := "wss"
	if !c.secure() {
		scheme = "ws"
	}
	// The name goes in unescaped base64 as every working client sends it; the
	// TV echoes it back verbatim as the client's deviceName.
	q := "name=" + c.encodedName()
	if withToken {
		q += "&token=" + url.QueryEscape(c.cfg.Token)
	}
	return scheme + "://" + c.hostPort(c.cfg.Port) + "/api/v2/channels/" + channel + "?" + q
}

func (c *client) dial(ctx context.Context, channel string, withToken bool) (*wsConn, error) {
	u := c.channelURL(channel, withToken)
	c.log.Debug("samsung: dial", "url", c.red.string(u))
	d := websocket.Dialer{
		Proxy:            http.ProxyFromEnvironment,
		TLSClientConfig:  insecureTLS(),
		HandshakeTimeout: c.timeouts.handshake,
	}
	ws, resp, err := d.DialContext(ctx, u, nil)
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	if err != nil {
		if resp != nil && (resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden) {
			return nil, fmt.Errorf("%w: HTTP %d on %s", ErrUnauthorized, resp.StatusCode, channel)
		}
		return nil, c.red.error(fmt.Errorf("samsung: dial %s: %w", channel, err))
	}
	ws.SetReadLimit(maxFrameSize)
	return newWSConn(ws, c.log, c.red, c.timeouts.write), nil
}

func (c *client) registerFrame(token string) map[string]any {
	return map[string]any{
		"method": "ms.channel.connect",
		"params": map[string]any{
			"deviceName": c.encodedName(),
			"id":         c.cfg.ClientName,
			"type":       "native",
			"token":      token,
		},
	}
}

// handshake waits for ms.channel.connect. With a valid token the TV sends it
// on its own; Tizen 9 instead stays silent until the client sends the register
// frame. pairing sends that frame immediately with an empty token and waits for
// the user without a deadline; otherwise it is sent with the token only after
// the TV has been silent for timeouts.register.
func (c *client) handshake(ctx context.Context, w *wsConn, channel string, pairing bool) (*connectData, error) {
	var register <-chan time.Time
	var deadline <-chan time.Time
	if pairing {
		if err := w.writeJSON(ctx, c.registerFrame("")); err != nil {
			return nil, err
		}
	} else {
		rt := time.NewTimer(c.timeouts.register)
		defer rt.Stop()
		register = rt.C
		dt := time.NewTimer(c.timeouts.handshake)
		defer dt.Stop()
		deadline = dt.C
	}
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline:
			return nil, fmt.Errorf("%w: no ms.channel.connect on %s within %s", ErrNoReply, channel, c.timeouts.handshake)
		case <-register:
			register = nil
			c.log.Debug("samsung: TV silent, sending register frame", "channel", channel)
			if err := w.writeJSON(ctx, c.registerFrame(c.cfg.Token)); err != nil {
				return nil, err
			}
		case b, ok := <-w.in:
			if !ok {
				return nil, w.closedError(channel)
			}
			var env envelope
			if err := json.Unmarshal(b, &env); err != nil {
				return nil, &ProtocolError{Kind: "handshake frame", Err: err}
			}
			switch env.Event {
			case "ms.channel.connect":
				var cd connectData
				if err := json.Unmarshal(env.Data, &cd); err != nil {
					return nil, &ProtocolError{Kind: "ms.channel.connect", Err: err}
				}
				return &cd, nil
			case "ms.channel.unauthorized", "ms.channel.timeOut":
				return nil, fmt.Errorf("%w: %s on %s", ErrUnauthorized, env.Event, channel)
			case "ms.error":
				var e struct {
					Message string `json:"message"`
				}
				_ = json.Unmarshal(env.Data, &e)
				unauthorized := strings.Contains(strings.ToLower(e.Message), "authorized")
				if pairing && unauthorized {
					// Tizen 9 answers the register frame with this and keeps the
					// socket open until the user responds to the prompt.
					continue
				}
				if unauthorized {
					return nil, fmt.Errorf("%w: ms.error %q on %s", ErrUnauthorized, e.Message, channel)
				}
				return nil, fmt.Errorf("samsung: ms.error %q on %s", e.Message, channel)
			}
		}
	}
}
