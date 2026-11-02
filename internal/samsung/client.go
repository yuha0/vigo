package samsung

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"
)

// awakePort is open exactly when the TV reports PowerState "on".
const awakePort = 9197

type timeouts struct {
	// handshake bounds the WebSocket upgrade and the wait for
	// ms.channel.connect. Pairing is bounded only by its context.
	handshake time.Duration
	// register is how long the TV may stay silent after the upgrade before the
	// register frame is sent.
	register time.Duration
	write    time.Duration
	// request is the per-request reply deadline on the art channel.
	request time.Duration
	// keepalive is the idle interval after which a get_artmode_status is sent;
	// the TV closes art connections idle for about 18s.
	keepalive time.Duration
	// d2d bounds one D2D socket transfer.
	d2d time.Duration
	// d2dConnectDelay is waited before dialing a D2D socket. The TV announces
	// the port before it is ready to serve it: connecting immediately makes
	// it close the TLS handshake with EOF and then report error_code -1.
	// Measured sequential thumbnail fetches: 4/10 succeeded with no delay,
	// 16/16 each with 250ms, 500ms and 1s.
	d2dConnectDelay time.Duration
	// imageAdded bounds the wait for image_added after an upload's bytes are
	// sent.
	imageAdded time.Duration
	probe      time.Duration
	http       time.Duration
}

var defaultTimeouts = timeouts{
	handshake:       15 * time.Second,
	register:        5 * time.Second,
	write:           10 * time.Second,
	request:         10 * time.Second,
	keepalive:       8 * time.Second,
	d2d:             60 * time.Second,
	d2dConnectDelay: 500 * time.Millisecond,
	imageAdded:      60 * time.Second,
	probe:           5 * time.Second,
	http:            10 * time.Second,
}

// New returns a Client for the TV described by cfg.
func New(cfg Config, log *slog.Logger) Client {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &client{
		cfg:       cfg,
		log:       log,
		red:       redactor{token: cfg.Token},
		timeouts:  defaultTimeouts,
		awakePort: awakePort,
		http: &http.Client{
			Transport: &http.Transport{
				Proxy:           http.ProxyFromEnvironment,
				TLSClientConfig: insecureTLS(),
			},
		},
	}
}

type client struct {
	cfg       Config
	log       *slog.Logger
	red       redactor
	timeouts  timeouts
	awakePort int
	http      *http.Client
}

// insecureTLS is used for every TV connection: the TV presents a self-signed
// certificate on 8002 and on D2D sockets.
func insecureTLS() *tls.Config {
	return &tls.Config{InsecureSkipVerify: true}
}

func (c *client) DeviceInfo(ctx context.Context) (DeviceInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeouts.http)
	defer cancel()
	scheme := "https"
	if !c.secure() {
		scheme = "http"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, scheme+"://"+c.hostPort(c.cfg.Port)+"/api/v2/", nil)
	if err != nil {
		return DeviceInfo{}, fmt.Errorf("samsung: device info: %w", err)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return DeviceInfo{}, fmt.Errorf("samsung: device info: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return DeviceInfo{}, &ProtocolError{Kind: "device info response", Err: fmt.Errorf("HTTP %d", resp.StatusCode)}
	}
	var body struct {
		Device *struct {
			PowerState       string   `json:"PowerState"`
			Model            string   `json:"model"`
			ModelName        string   `json:"modelName"`
			Name             string   `json:"name"`
			NetworkType      string   `json:"networkType"`
			FrameTVSupport   flexBool `json:"FrameTVSupport"`
			TokenAuthSupport flexBool `json:"TokenAuthSupport"`
		} `json:"device"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return DeviceInfo{}, &ProtocolError{Kind: "device info", Err: err}
	}
	d := body.Device
	if d == nil {
		return DeviceInfo{}, &ProtocolError{Kind: "device info", Err: errors.New(`no "device" object`)}
	}
	return DeviceInfo{
		PowerState: d.PowerState,
		Model:      d.Model,
		ModelName:  d.ModelName,
		// The TV HTML-escapes the name, e.g. `75&quot; The Frame`.
		Name:             html.UnescapeString(d.Name),
		NetworkType:      d.NetworkType,
		FrameTVSupport:   bool(d.FrameTVSupport),
		TokenAuthSupport: bool(d.TokenAuthSupport),
	}, nil
}

func (c *client) Awake(ctx context.Context) (bool, error) {
	d := net.Dialer{Timeout: c.timeouts.probe}
	conn, err := d.DialContext(ctx, "tcp", c.hostPort(c.awakePort))
	if err == nil {
		conn.Close()
		return true, nil
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return false, nil
	}
	return false, fmt.Errorf("samsung: awake probe: %w", err)
}

func (c *client) Pair(ctx context.Context) (string, error) {
	w, err := c.dial(ctx, channelRemote, false)
	if err != nil {
		return "", err
	}
	defer w.close()
	c.log.Debug("samsung: pairing, waiting for the TV prompt to be answered")
	cd, err := c.handshake(ctx, w, channelRemote, true)
	if err != nil {
		return "", err
	}
	if cd.Token == "" {
		return "", &ProtocolError{Kind: "pairing reply", Err: errors.New("ms.channel.connect carried no token")}
	}
	return cd.Token, nil
}

func (c *client) dialChannel(ctx context.Context, channel string) (*wsConn, *connectData, error) {
	if c.cfg.Token == "" {
		// Connecting without a token would put a pairing prompt on the TV.
		return nil, nil, fmt.Errorf("%w: no token, pair first", ErrUnauthorized)
	}
	w, err := c.dial(ctx, channel, true)
	if err != nil {
		return nil, nil, err
	}
	cd, err := c.handshake(ctx, w, channel, false)
	if err != nil {
		w.close()
		return nil, nil, err
	}
	return w, cd, nil
}

func (c *client) DialArt(ctx context.Context) (ArtConn, error) {
	w, cd, err := c.dialChannel(ctx, channelArt)
	if err != nil {
		return nil, err
	}
	if cd.hosts() == 0 {
		w.close()
		return nil, ErrNoArtHost
	}
	return newArtConn(c, w), nil
}

func (c *client) DialRemote(ctx context.Context) (RemoteConn, error) {
	w, _, err := c.dialChannel(ctx, channelRemote)
	if err != nil {
		return nil, err
	}
	return newRemoteConn(w), nil
}

// flexBool accepts true, "true" and "TRUE".
type flexBool bool

func (b *flexBool) UnmarshalJSON(data []byte) error {
	s := strings.Trim(string(data), `"`)
	*b = flexBool(strings.EqualFold(s, "true"))
	return nil
}

// flexString accepts a JSON string, number or bool and keeps its text.
type flexString string

func (s *flexString) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		return nil
	}
	if len(data) > 0 && data[0] == '"' {
		var v string
		if err := json.Unmarshal(data, &v); err != nil {
			return err
		}
		*s = flexString(v)
		return nil
	}
	*s = flexString(data)
	return nil
}

// flexInt accepts a JSON number or a numeric string.
type flexInt int

func (n *flexInt) UnmarshalJSON(data []byte) error {
	s := strings.Trim(string(data), `"`)
	if s == "" || s == "null" {
		return nil
	}
	var v json.Number = json.Number(s)
	i, err := v.Int64()
	if err != nil {
		return fmt.Errorf("not an integer: %s", data)
	}
	*n = flexInt(i)
	return nil
}

// nested decodes a value the TV may send either directly or as a JSON string
// holding the JSON, as it does for content_list, data, conn_info and others.
type nested[T any] struct{ V T }

func (n *nested[T]) UnmarshalJSON(data []byte) error {
	if len(data) > 0 && data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		data = []byte(s)
	}
	return json.Unmarshal(data, &n.V)
}
