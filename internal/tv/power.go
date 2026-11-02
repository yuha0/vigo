package tv

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	corev1 "github.com/yuha0/vigo/gen/go/vigo/core/v1"
	"github.com/yuha0/vigo/internal/errs"
	"github.com/yuha0/vigo/internal/samsung"
)

// ShowArt puts art on the panel with the wake algorithm of docs/tv-api.md §5
// "Waking the panel". contentID may be empty to keep the current selection.
//
// KEY_POWER is a toggle: from motion-sleep it powers the TV fully off, after
// which only the physical remote may recover it. It is therefore sent only
// when a fresh read in this call shows PowerState standby and art mode off.
func (s *Service) ShowArt(ctx context.Context, contentID string) (*corev1.Status, error) {
	if err := s.requireLeader(); err != nil {
		return nil, err
	}
	s.powerMu.Lock()
	defer s.powerMu.Unlock()

	client := s.currentClient()
	if s.Config().Host == "" {
		return nil, ErrNoHost
	}
	conn, err := s.artConn()
	if err != nil {
		if s.readPower(ctx, client) == corev1.PowerState_POWER_STATE_UNREACHABLE {
			return nil, ErrUnreachable
		}
		return nil, err
	}
	power, err := s.showArt(ctx, client, conn, contentID)
	// For a few seconds after the TV is powered off, REST and the 9197 probe
	// still report it awake while it already ignores select_image. A fresh
	// read then takes the power-off path.
	if errors.Is(err, samsung.ErrNoReply) && power == corev1.PowerState_POWER_STATE_ON {
		_, err = s.showArt(ctx, client, conn, contentID)
	}
	if err != nil {
		return nil, err
	}
	s.readPower(ctx, client)
	s.refreshArt(ctx, conn, false)
	return s.Status(), nil
}

// showArt runs one pass of the wake algorithm and returns the power state it
// acted on.
func (s *Service) showArt(ctx context.Context, client samsung.Client, conn samsung.ArtConn, contentID string) (corev1.PowerState, error) {
	power := s.readPower(ctx, client)
	if power == corev1.PowerState_POWER_STATE_UNREACHABLE {
		return power, ErrUnreachable
	}
	artOn, err := call(s, ctx, "get_artmode_status", s.opts.RequestTimeout, conn.ArtModeStatus)
	if err != nil {
		return power, fmt.Errorf("read art mode: %w", err)
	}
	s.setArtMode(artOn)
	target := contentID
	if target == "" {
		if target, err = s.selectedContentID(ctx, conn); err != nil {
			return power, err
		}
	}

	switch power {
	case corev1.PowerState_POWER_STATE_ON:
		if !artOn {
			if err := s.setArtModeStatus(ctx, conn, true); err != nil {
				return power, err
			}
		}
		// select_image also lights a panel that is dark in Art Mode, which
		// nothing the TV reports can detect.
		return power, s.selectArt(ctx, conn, target)

	case corev1.PowerState_POWER_STATE_STANDBY:
		if artOn {
			// Motion-sleep: select_image wakes it; set_artmode_status would
			// be a no-op because art mode is already on.
			if err := s.selectArt(ctx, conn, target); err != nil {
				return power, err
			}
			if err := s.waitAwake(ctx, client); err != nil {
				return power, err
			}
		} else {
			// Power-off standby: select_image and set_artmode_status do
			// nothing here; KEY_POWER wakes it into Art Mode.
			if err := s.sendKey(ctx, "KEY_POWER", samsung.KeyClick); err != nil {
				return power, err
			}
			if err := s.waitAwake(ctx, client); err != nil {
				return power, err
			}
			// The KEY_POWER wake lands in Art Mode with the panel dark until
			// motion is detected; select_image lights it (verified 2026-11-01).
			if err := s.selectArt(ctx, conn, target); err != nil {
				return power, err
			}
		}
		// After waking the TV may be in normal TV mode (docs/tv-api.md §5).
		on, err := call(s, ctx, "get_artmode_status", s.opts.RequestTimeout, conn.ArtModeStatus)
		if err != nil {
			return power, fmt.Errorf("read art mode: %w", err)
		}
		s.setArtMode(on)
		if !on {
			return power, s.setArtModeStatus(ctx, conn, true)
		}
		return power, nil

	default:
		return power, errs.New(ErrUnreachable, "the TV's power state is unknown")
	}
}

// SetArtMode switches between Art Mode and normal TV mode. The TV ignores it
// in standby.
func (s *Service) SetArtMode(ctx context.Context, on bool) (*corev1.Status, error) {
	if err := s.requireLeader(); err != nil {
		return nil, err
	}
	s.powerMu.Lock()
	defer s.powerMu.Unlock()
	conn, err := s.artConn()
	if err != nil {
		return nil, err
	}
	if err := s.setArtModeStatus(ctx, conn, on); err != nil {
		return nil, err
	}
	s.refreshArt(ctx, conn, false)
	return s.Status(), nil
}

// SelectArt selects artwork, which also wakes the TV from motion-sleep. In
// power-off standby the TV ignores it, so that fails fast with ErrPoweredOff.
func (s *Service) SelectArt(ctx context.Context, contentID string) (*corev1.Status, error) {
	if contentID == "" {
		return nil, errs.New(ErrInvalidArgument, "content_id is required")
	}
	if err := s.requireLeader(); err != nil {
		return nil, err
	}
	s.powerMu.Lock()
	defer s.powerMu.Unlock()
	client := s.currentClient()
	conn, err := s.artConn()
	if err != nil {
		return nil, err
	}
	if err := s.requireNotPoweredOff(ctx, client, conn); err != nil {
		return nil, err
	}
	if err := s.selectArt(ctx, conn, contentID); err != nil {
		// See ShowArt: the TV may have been powered off moments ago.
		if errors.Is(err, samsung.ErrNoReply) {
			if perr := s.requireNotPoweredOff(ctx, client, conn); perr != nil {
				return nil, perr
			}
		}
		return nil, err
	}
	s.readPower(ctx, client)
	return s.Status(), nil
}

func (s *Service) requireNotPoweredOff(ctx context.Context, client samsung.Client, conn samsung.ArtConn) error {
	switch s.readPower(ctx, client) {
	case corev1.PowerState_POWER_STATE_UNREACHABLE:
		return ErrUnreachable
	case corev1.PowerState_POWER_STATE_STANDBY:
		artOn, err := call(s, ctx, "get_artmode_status", s.opts.RequestTimeout, conn.ArtModeStatus)
		if err != nil {
			return fmt.Errorf("read art mode: %w", err)
		}
		s.setArtMode(artOn)
		if !artOn {
			return ErrPoweredOff
		}
	}
	return nil
}

// selectArt sends select_image and records the selection once the TV has
// confirmed it.
func (s *Service) selectArt(ctx context.Context, conn samsung.ArtConn, contentID string) error {
	if err := s.selectImage(ctx, conn, contentID); err != nil {
		return err
	}
	s.selected(ctx, &corev1.SelectedArt{ContentId: contentID})
	return nil
}

var keyPattern = regexp.MustCompile(`^KEY_[A-Z0-9_]+$`)

// SendKey sends a remote key. KEY_POWER is refused: its effect depends on
// state and from motion-sleep it powers the TV off; use ShowArt.
func (s *Service) SendKey(ctx context.Context, key string, action samsung.KeyAction) error {
	key = strings.ToUpper(strings.TrimSpace(key))
	if !keyPattern.MatchString(key) {
		return errs.New(ErrInvalidArgument, "key must look like KEY_UP")
	}
	if key == "KEY_POWER" || key == "KEY_POWEROFF" || key == "KEY_POWERON" {
		return errs.New(ErrInvalidArgument, "%s is never sent directly; use ShowArt to wake the TV", key)
	}
	if action == "" {
		action = samsung.KeyClick
	}
	if err := s.requireLeader(); err != nil {
		return err
	}
	return s.sendKey(ctx, key, action)
}

func (s *Service) sendKey(ctx context.Context, key string, action samsung.KeyAction) error {
	rc, err := s.remoteConn(ctx)
	if err != nil {
		return err
	}
	err = do(s, ctx, "send_key", s.opts.RequestTimeout, func(ctx context.Context) error {
		return rc.SendKey(ctx, key, action)
	})
	if err != nil {
		s.closeRemote()
	}
	return err
}

func (s *Service) remoteConn(ctx context.Context) (samsung.RemoteConn, error) {
	s.remoteMu.Lock()
	defer s.remoteMu.Unlock()
	if s.remote != nil {
		select {
		case <-s.remote.Done():
			s.remote.Close()
			s.remote = nil
			s.m.Reconnects.WithLabelValues("remote").Inc()
		default:
			return s.remote, nil
		}
	}
	s.mu.Lock()
	client, host, token := s.client, s.cfg.Host, s.token
	s.mu.Unlock()
	switch {
	case host == "":
		return nil, ErrNoHost
	case token == "":
		return nil, ErrNotPaired
	}
	rc, err := call(s, ctx, "dial_remote", s.opts.RequestTimeout, client.DialRemote)
	if errors.Is(err, samsung.ErrUnauthorized) {
		return nil, errs.New(ErrNotPaired, "the TV rejected the stored token: %w", err)
	}
	if err != nil {
		return nil, err
	}
	s.remote = rc
	return rc, nil
}

func (s *Service) selectImage(ctx context.Context, conn samsung.ArtConn, contentID string) error {
	return do(s, ctx, "select_image", s.opts.RequestTimeout, func(ctx context.Context) error {
		return conn.SelectImage(ctx, contentID)
	})
}

func (s *Service) setArtModeStatus(ctx context.Context, conn samsung.ArtConn, on bool) error {
	return do(s, ctx, "set_artmode_status", s.opts.RequestTimeout, func(ctx context.Context) error {
		return conn.SetArtModeStatus(ctx, on)
	})
}

func (s *Service) currentContentID(ctx context.Context, conn samsung.ArtConn) (string, error) {
	cur, err := call(s, ctx, "get_current_artwork", s.opts.RequestTimeout, conn.CurrentArtwork)
	if err != nil {
		return "", fmt.Errorf("read selected artwork: %w", err)
	}
	if cur.ContentID == "" {
		return "", errs.New(ErrInvalidArgument, "no artwork is selected; pass content_id")
	}
	return cur.ContentID, nil
}

// waitAwake polls the 9197 probe until the TV is awake.
func (s *Service) waitAwake(ctx context.Context, client samsung.Client) error {
	deadline := time.Now().Add(s.opts.WakeTimeout)
	for {
		awake, err := call(s, ctx, "awake", s.opts.RequestTimeout, client.Awake)
		if err == nil && awake {
			return nil
		}
		if time.Now().After(deadline) {
			return ErrWakeTimeout
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(s.opts.WakePollInterval):
		}
	}
}
