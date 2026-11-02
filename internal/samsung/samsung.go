// Package samsung speaks the Samsung TV local API: REST device info, the
// WebSocket remote and art channels, and D2D socket transfers. It carries no
// policy; deciding what to send in which TV state belongs to package tv.
package samsung

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Config identifies the TV and this client.
type Config struct {
	Host string
	// 8002 is TLS with a self-signed certificate; 8001 is plaintext.
	Port int
	// Shown on the TV's pairing prompt and in its Device Connection Manager.
	ClientName string
	// Empty when not yet paired.
	Token string
}

// Client opens connections to one TV. Implementations are safe for
// concurrent use.
type Client interface {
	// DeviceInfo reads GET /api/v2/. Needs no pairing and works from any subnet.
	DeviceInfo(ctx context.Context) (DeviceInfo, error)

	// Awake probes TCP 9197, which is open exactly when PowerState is "on".
	Awake(ctx context.Context) (bool, error)

	// Pair runs the Tizen 9 register-frame handshake on the remote channel and
	// blocks until the user presses Allow, the TV refuses, or ctx ends.
	Pair(ctx context.Context) (token string, err error)

	// DialArt opens the art channel. It fails with ErrUnauthorized when the
	// token is missing or rejected and with ErrNoArtHost when no art app has
	// registered as host since boot.
	DialArt(ctx context.Context) (ArtConn, error)

	// DialRemote opens the remote-control channel.
	DialRemote(ctx context.Context) (RemoteConn, error)
}

// ArtConn is an open art channel. It keeps itself alive against the TV's
// idle close and reports closure through Done; it never reconnects itself.
type ArtConn interface {
	ArtModeStatus(ctx context.Context) (on bool, err error)
	SetArtModeStatus(ctx context.Context, on bool) error

	Settings(ctx context.Context) ([]ArtSetting, error)
	// SetSetting sends set_<item>. An acknowledgement does not mean the value
	// changed; re-read Settings to confirm.
	SetSetting(ctx context.Context, item, value string) error

	// ContentList returns get_content_list as reported, duplicates included.
	// An empty category lists all categories.
	ContentList(ctx context.Context, category string) ([]Content, error)
	CurrentArtwork(ctx context.Context) (CurrentArtwork, error)
	// SelectImage sends select_image without category_id.
	SelectImage(ctx context.Context, contentID string) error
	DeleteImages(ctx context.Context, contentIDs []string) (deleted []string, err error)
	MatteList(ctx context.Context) (MatteList, error)
	ChangeMatte(ctx context.Context, contentID, matteID, portraitMatteID string) error
	ArtDeviceInfo(ctx context.Context) (ArtDeviceInfo, error)

	// Thumbnail fetches a JPEG thumbnail over a D2D socket.
	Thumbnail(ctx context.Context, contentID string) ([]byte, error)
	// Upload sends an image over a D2D socket and waits for image_added.
	Upload(ctx context.Context, img Upload) (contentID string, err error)

	// Events delivers unsolicited TV events. It is closed when the connection
	// closes. Slow consumers lose events rather than block the connection.
	Events() <-chan ArtEvent

	Done() <-chan struct{}
	// Err reports why the connection closed; nil while open.
	Err() error
	Close() error
}

// RemoteConn is an open remote-control channel.
type RemoteConn interface {
	SendKey(ctx context.Context, key string, action KeyAction) error
	Done() <-chan struct{}
	Err() error
	Close() error
}

type KeyAction string

const (
	KeyClick   KeyAction = "Click"
	KeyPress   KeyAction = "Press"
	KeyRelease KeyAction = "Release"
)

// DeviceInfo is the "device" object of GET /api/v2/.
type DeviceInfo struct {
	// "on" or "standby".
	PowerState string
	// Encodes the Tizen generation, e.g. 24_PTM_FTV_T09.
	Model            string
	ModelName        string
	Name             string
	NetworkType      string
	FrameTVSupport   bool
	TokenAuthSupport bool
}

// ArtSetting is one item of get_artmode_settings. Values are strings as the
// TV sends them. Min and Max are set for ranges; ValidValues for enums;
// neither for on/off toggles.
type ArtSetting struct {
	Item        string
	Value       string
	Min         string
	Max         string
	ValidValues []string
}

type Content struct {
	ContentID  string
	CategoryID string
	// "mobile", "preinstall" or "server".
	ContentType     string
	MatteID         string
	PortraitMatteID string
	Width           int
	Height          int
	// Format "2006:01:02 15:04:05"; empty for Samsung content.
	ImageDate string
	Slideshow bool
}

type CurrentArtwork struct {
	ContentID       string
	MatteID         string
	PortraitMatteID string
	CategoryID      string
	ContentType     string
}

type MatteList struct {
	Types  []string
	Colors []MatteColor
}

type MatteColor struct {
	Name    string
	R, G, B uint8
}

// ArtDeviceInfo is the art channel's get_device_info.
type ArtDeviceInfo struct {
	FlashSizeGB              int
	SupportsMotionSensor     bool
	SupportsBrightnessSensor bool
}

type Upload struct {
	Data []byte
	// "jpg" or "png".
	FileType        string
	MatteID         string
	PortraitMatteID string
	Date            time.Time
}

// ArtEvent is an unsolicited message on the art channel, e.g.
// art_mode_changed, image_added, image_deleted, image_selected.
type ArtEvent struct {
	Name string
	// The parsed inner payload of the d2d_service_message.
	Data map[string]any
}

var (
	// ErrUnauthorized means the TV refused the token or the client is
	// off-subnet. Both look the same from here.
	ErrUnauthorized = errors.New("samsung: unauthorized")
	ErrNoArtHost    = errors.New("samsung: no art app registered as host")
	// ErrNoReply means the TV sent nothing before the deadline. Unknown
	// request names also produce this, since the TV ignores them silently.
	ErrNoReply = errors.New("samsung: no reply")
	// ErrNoData means the TV accepted a D2D socket and closed it without
	// sending anything. Some Art Store items consistently get this for
	// thumbnails, so it describes the item rather than the connection.
	ErrNoData = errors.New("samsung: the TV closed the d2d socket without sending data")
)

// TVError is an {"event":"error"} reply on the art channel.
type TVError struct {
	Request string
	Code    string
}

func (e *TVError) Error() string {
	return fmt.Sprintf("samsung: %s refused with error_code %s", e.Request, e.Code)
}

// ProtocolError means a message did not have the expected shape. It is the
// main signal that a firmware update changed the API.
type ProtocolError struct {
	Kind string
	Err  error
}

func (e *ProtocolError) Error() string {
	return fmt.Sprintf("samsung: unexpected %s: %v", e.Kind, e.Err)
}

func (e *ProtocolError) Unwrap() error { return e.Err }
