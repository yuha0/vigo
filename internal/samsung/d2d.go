package samsung

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

const (
	maxD2DHeader = 1 << 20
	maxD2DFile   = 256 << 20
)

// connInfo is the TV's half of a D2D transfer: where to connect and the key
// to present.
type connInfo struct {
	IP      string          `json:"ip"`
	Port    flexInt         `json:"port"`
	Key     json.RawMessage `json:"key"`
	Secured flexBool        `json:"secured"`
}

func parseConnInfo(request string, m *message) (connInfo, error) {
	var r struct {
		ConnInfo *nested[connInfo] `json:"conn_info"`
	}
	if err := json.Unmarshal(m.raw, &r); err != nil {
		return connInfo{}, &ProtocolError{Kind: request + " reply", Err: err}
	}
	if r.ConnInfo == nil {
		return connInfo{}, &ProtocolError{Kind: request + " reply", Err: fmt.Errorf("no conn_info in %q", m.name)}
	}
	ci := r.ConnInfo.V
	if ci.Port <= 0 {
		return connInfo{}, missing(request, "conn_info.port")
	}
	return ci, nil
}

func (c *client) dialD2D(ctx context.Context, ci connInfo) (net.Conn, error) {
	ip := ci.IP
	if ip == "" {
		ip = c.cfg.Host
	}
	addr := net.JoinHostPort(ip, strconv.Itoa(int(ci.Port)))
	select {
	case <-time.After(c.timeouts.d2dConnectDelay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("samsung: d2d dial: %w", err)
	}
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	if !ci.Secured {
		return conn, nil
	}
	tc := tls.Client(conn, insecureTLS())
	if err := tc.HandshakeContext(ctx); err != nil {
		conn.Close()
		return nil, fmt.Errorf("samsung: d2d tls: %w", err)
	}
	return tc, nil
}

// d2dRead reads one file: a 4-byte big-endian header length, a JSON header
// with fileLength, then the payload.
func (c *client) d2dRead(ctx context.Context, ci connInfo) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeouts.d2d)
	defer cancel()
	start := time.Now()
	conn, err := c.dialD2D(ctx, ci)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	var n uint32
	if err := binary.Read(conn, binary.BigEndian, &n); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, ErrNoData
		}
		return nil, fmt.Errorf("samsung: d2d read header length: %w", err)
	}
	if n == 0 || n > maxD2DHeader {
		return nil, &ProtocolError{Kind: "d2d header", Err: fmt.Errorf("length %d", n)}
	}
	hb := make([]byte, n)
	if _, err := io.ReadFull(conn, hb); err != nil {
		return nil, fmt.Errorf("samsung: d2d read header: %w", err)
	}
	var hdr struct {
		FileLength *flexInt `json:"fileLength"`
	}
	if err := json.Unmarshal(hb, &hdr); err != nil {
		return nil, &ProtocolError{Kind: "d2d header", Err: err}
	}
	if hdr.FileLength == nil || *hdr.FileLength < 0 || *hdr.FileLength > maxD2DFile {
		return nil, &ProtocolError{Kind: "d2d header", Err: errors.New("missing or invalid fileLength")}
	}
	data := make([]byte, int(*hdr.FileLength))
	if _, err := io.ReadFull(conn, data); err != nil {
		return nil, fmt.Errorf("samsung: d2d read payload: %w", err)
	}
	c.log.Debug("samsung: d2d read", "bytes", len(data), "duration", time.Since(start))
	return data, nil
}

// d2dWrite sends one file in the same framing and closes the socket, which is
// what tells the TV the transfer is complete.
func (c *client) d2dWrite(ctx context.Context, ci connInfo, fileType string, data []byte) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeouts.d2d)
	defer cancel()
	start := time.Now()
	conn, err := c.dialD2D(ctx, ci)
	if err != nil {
		return err
	}
	defer conn.Close()

	key := ci.Key
	if len(key) == 0 {
		key = json.RawMessage(`""`)
	}
	hdr, err := json.Marshal(map[string]any{
		"num":        0,
		"total":      1,
		"fileLength": len(data),
		"fileName":   "image",
		"fileType":   fileType,
		"secKey":     key,
		"version":    "0.0.1",
	})
	if err != nil {
		return err
	}
	buf := make([]byte, 4, 4+len(hdr))
	binary.BigEndian.PutUint32(buf, uint32(len(hdr)))
	buf = append(buf, hdr...)
	if _, err := conn.Write(buf); err != nil {
		return fmt.Errorf("samsung: d2d write header: %w", err)
	}
	if _, err := conn.Write(data); err != nil {
		return fmt.Errorf("samsung: d2d write payload: %w", err)
	}
	if err := conn.Close(); err != nil {
		return fmt.Errorf("samsung: d2d close: %w", err)
	}
	c.log.Debug("samsung: d2d write", "bytes", len(data), "duration", time.Since(start))
	return nil
}
