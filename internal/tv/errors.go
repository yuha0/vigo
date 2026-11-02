package tv

import "errors"

// Sentinel errors returned by Service. Callers map them to API error codes;
// errors from package samsung pass through wrapped and keep their identity.
var (
	// ErrStandbyReplica: this replica is not the leader and holds no TV
	// connection.
	ErrStandbyReplica = errors.New("this replica is a standby and holds no TV connection")
	// ErrNotConnected: the art channel is down, typically while reconnecting.
	ErrNotConnected = errors.New("not connected to the TV")
	ErrNotPaired    = errors.New("not paired with the TV")
	ErrNoArtHost    = errors.New("the TV has no art app registered as host; enter Art Mode once on the TV")
	ErrUnreachable  = errors.New("the TV is unreachable")
	// ErrPoweredOff: power-off standby, where select_image and
	// set_artmode_status are ignored; only ShowArt can wake it.
	ErrPoweredOff = errors.New("the TV is powered off; use Show art to turn it on")
	ErrNoHost     = errors.New("no TV host configured")
	// ErrWakeTimeout: the wake command was acknowledged but the TV did not
	// report itself awake in time.
	ErrWakeTimeout     = errors.New("the TV did not wake in time")
	ErrInvalidArgument = errors.New("invalid argument")
	ErrNotFound        = errors.New("not found")
)
