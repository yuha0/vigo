package tv

import (
	"context"
	"errors"
	"time"

	corev1 "github.com/yuha0/vigo/gen/go/vigo/core/v1"
	"github.com/yuha0/vigo/internal/samsung"
)

// Every restEvery-th poll reads REST even when the 9197 probe agrees with
// the last known power state, to refresh device info and the model check.
const restEvery = 6

// A connection that lived at least this long is considered healthy, so
// losing it reconnects without backoff.
const healthyConn = 10 * time.Second

func (s *Service) setConn(c corev1.ConnectionState) {
	s.update(func(st *state) { st.conn = c })
}

// wait blocks for d (forever when d is 0), a reconnect request or ctx.
func (s *Service) wait(ctx context.Context, d time.Duration) {
	var timer <-chan time.Time
	if d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		timer = t.C
	}
	select {
	case <-ctx.Done():
	case <-s.reconnect:
	case <-timer:
	}
}

// connLoop keeps the art channel open while this replica is active.
func (s *Service) connLoop(ctx context.Context) {
	backoff := s.opts.ReconnectMin
	nextBackoff := func() time.Duration {
		d := backoff
		backoff = min(backoff*2, s.opts.ReconnectMax)
		return d
	}
	first := true
	for ctx.Err() == nil {
		s.mu.Lock()
		client, host, token := s.client, s.cfg.Host, s.token
		s.mu.Unlock()

		if host == "" {
			s.setConn(corev1.ConnectionState_CONNECTION_STATE_DISCONNECTED)
			s.setWarning("no_tv_host", "No TV host is configured.")
			s.wait(ctx, 0)
			continue
		}
		s.clearWarning("no_tv_host")
		if token == "" {
			s.setConn(corev1.ConnectionState_CONNECTION_STATE_NOT_PAIRED)
			s.wait(ctx, 0)
			continue
		}

		if !first {
			s.m.Reconnects.WithLabelValues("art").Inc()
		}
		first = false
		s.setConn(corev1.ConnectionState_CONNECTION_STATE_CONNECTING)
		conn, err := call(s, ctx, "dial_art", s.opts.RequestTimeout, client.DialArt)
		if err != nil {
			switch {
			case errors.Is(err, samsung.ErrUnauthorized):
				// Also what an off-subnet client sees (docs/tv-api.md §1), so keep
				// retrying slowly rather than giving up.
				s.mu.Lock()
				first := !s.tokenRejected
				s.tokenRejected, s.tokenVerified = true, false
				s.mu.Unlock()
				if first {
					if err := s.store.TokenRejected(ctx); err != nil {
						s.log.Error("record token rejection", "err", err)
					}
				}
				s.setConn(corev1.ConnectionState_CONNECTION_STATE_NOT_PAIRED)
				s.setWarning("token_rejected", "The TV rejected the stored token. Pair again.")
			case errors.Is(err, samsung.ErrNoArtHost):
				s.setConn(corev1.ConnectionState_CONNECTION_STATE_NO_ART_HOST)
			default:
				s.setConn(corev1.ConnectionState_CONNECTION_STATE_DISCONNECTED)
			}
			s.wait(ctx, nextBackoff())
			continue
		}

		backoff = s.opts.ReconnectMin
		s.mu.Lock()
		s.art = conn
		s.tokenRejected = false
		verify := !s.tokenVerified
		s.tokenVerified = true
		s.mu.Unlock()
		if verify {
			if err := s.store.TokenVerified(ctx); err != nil {
				s.log.Error("record token verification", "err", err)
			}
		}
		s.clearWarning("token_rejected")
		s.setConn(corev1.ConnectionState_CONNECTION_STATE_CONNECTED)
		s.refreshArt(ctx, conn, true)

		start := time.Now()
		s.serve(ctx, conn)

		s.mu.Lock()
		if s.art == conn {
			s.art = nil
		}
		s.mu.Unlock()
		conn.Close()
		s.update(func(st *state) {
			st.conn = corev1.ConnectionState_CONNECTION_STATE_DISCONNECTED
			st.artMode = corev1.ArtModeState_ART_MODE_STATE_UNSPECIFIED
		})
		if err := conn.Err(); err != nil && ctx.Err() == nil {
			s.log.Info("art channel closed", "err", err)
		}
		if time.Since(start) < healthyConn {
			s.wait(ctx, nextBackoff())
		}
	}
}

// serve handles events until the connection closes, a reconnect is
// requested or ctx ends.
func (s *Service) serve(ctx context.Context, conn samsung.ArtConn) {
	events := conn.Events()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.reconnect:
			return
		case <-conn.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			s.handleEvent(ctx, conn, ev)
		}
	}
}

func (s *Service) handleEvent(ctx context.Context, conn samsung.ArtConn, ev samsung.ArtEvent) {
	str := func(k string) string {
		v, _ := ev.Data[k].(string)
		return v
	}
	s.log.Debug("tv event", "event", ev.Name)
	switch ev.Name {
	case "art_mode_changed":
		switch str("status") {
		case "on":
			s.setArtMode(true)
		case "off":
			s.setArtMode(false)
		default:
			s.refreshArt(ctx, conn, false)
		}
	case "image_selected":
		s.selected(ctx, &corev1.SelectedArt{ContentId: str("content_id"), MatteId: str("matte_id"), PortraitMatteId: str("portrait_matte_id")})
		// select_image wakes the TV from motion-sleep.
		s.signal(s.pollNow)
	case "image_added":
		id := str("content_id")
		if id == "" {
			return
		}
		if h := s.libraryHook(); h != nil {
			h.TVContentAdded(id)
		}
		if !s.announce("added:" + id) {
			return
		}
		art, err := s.findArtwork(ctx, conn, id)
		if err != nil {
			art = &corev1.Artwork{ContentId: id}
		}
		s.emit(&corev1.WatchEventsResponse{Event: &corev1.WatchEventsResponse_ArtAdded{ArtAdded: art}})
	case "image_deleted":
		id := str("content_id")
		if id == "" {
			return
		}
		s.forgetArtwork(ctx, id)
		if s.announce("deleted:" + id) {
			s.emit(&corev1.WatchEventsResponse{Event: &corev1.WatchEventsResponse_ArtDeleted{ArtDeleted: &corev1.ArtDeleted{ContentIds: []string{id}}}})
		}
	}
}

func artModeState(on bool) corev1.ArtModeState {
	if on {
		return corev1.ArtModeState_ART_MODE_STATE_ON
	}
	return corev1.ArtModeState_ART_MODE_STATE_OFF
}

func (s *Service) setArtMode(on bool) {
	s.update(func(st *state) { st.artMode = artModeState(on) })
}

// refreshArt re-reads art mode and the selected artwork; with device it
// also reads the art channel's device info.
func (s *Service) refreshArt(ctx context.Context, conn samsung.ArtConn, device bool) {
	if on, err := call(s, ctx, "get_artmode_status", s.opts.RequestTimeout, conn.ArtModeStatus); err == nil {
		s.setArtMode(on)
	}
	s.refreshSelected(ctx, conn)
	if !device {
		return
	}
	if info, err := call(s, ctx, "get_device_info", s.opts.RequestTimeout, conn.ArtDeviceInfo); err == nil {
		s.update(func(st *state) {
			st.device.FlashSizeGb = int32(info.FlashSizeGB)
			st.device.SupportsMotionSensor = info.SupportsMotionSensor
			st.device.SupportsBrightnessSensor = info.SupportsBrightnessSensor
		})
	}
}

func (s *Service) setSelected(sel *corev1.SelectedArt) {
	changed := false
	s.update(func(st *state) {
		if st.selected == nil || st.selected.ContentId != sel.ContentId ||
			st.selected.MatteId != sel.MatteId || st.selected.PortraitMatteId != sel.PortraitMatteId ||
			st.selected.Confirmed != sel.Confirmed {
			st.selected = sel
			changed = true
		}
	})
	if changed {
		s.emit(&corev1.WatchEventsResponse{Event: &corev1.WatchEventsResponse_ArtSelected{ArtSelected: sel}})
	}
}

func (s *Service) pollLoop(ctx context.Context) {
	t := time.NewTicker(s.opts.PollInterval)
	defer t.Stop()
	s.poll(ctx, true)
	for n := 1; ; n++ {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.poll(ctx, n%restEvery == 0)
		case <-s.pollNow:
			s.poll(ctx, true)
		}
	}
}

func (s *Service) powerState() corev1.PowerState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st.power
}

// poll reads the power state. The 9197 probe is cheap and flips exactly
// with PowerState (docs/tv-api.md §4 "Consequences for vigo"), so REST is read only
// when the probe disagrees with what is known, fails, or full is set.
func (s *Service) poll(ctx context.Context, full bool) {
	s.mu.Lock()
	client, host := s.client, s.cfg.Host
	s.mu.Unlock()
	if host == "" {
		s.update(func(st *state) { st.power = corev1.PowerState_POWER_STATE_UNSPECIFIED })
		return
	}
	if !full {
		awake, err := call(s, ctx, "awake", s.opts.RequestTimeout, client.Awake)
		if err == nil {
			p := s.powerState()
			if (awake && p == corev1.PowerState_POWER_STATE_ON) || (!awake && p == corev1.PowerState_POWER_STATE_STANDBY) {
				return
			}
		}
	}
	s.readPower(ctx, client)
}

// readPower reads REST device info and returns the power state it found.
func (s *Service) readPower(ctx context.Context, client samsung.Client) corev1.PowerState {
	info, err := call(s, ctx, "device_info", s.opts.RequestTimeout, client.DeviceInfo)
	if err != nil {
		s.update(func(st *state) { st.power = corev1.PowerState_POWER_STATE_UNREACHABLE })
		return corev1.PowerState_POWER_STATE_UNREACHABLE
	}
	power := corev1.PowerState_POWER_STATE_UNSPECIFIED
	switch info.PowerState {
	case "on":
		power = corev1.PowerState_POWER_STATE_ON
	case "standby":
		power = corev1.PowerState_POWER_STATE_STANDBY
	default:
		s.m.ProtocolErrors.WithLabelValues("power_state").Inc()
		s.log.Error("unexpected REST PowerState", "value", info.PowerState)
	}
	prev := s.powerState()
	s.update(func(st *state) {
		st.power = power
		st.device.Model = info.Model
		st.device.ModelName = info.ModelName
		st.device.Name = info.Name
		st.device.NetworkType = info.NetworkType
	})
	s.checkModel(ctx, info.Model)
	if prev != power {
		s.mu.Lock()
		conn := s.art
		s.mu.Unlock()
		if conn != nil {
			s.refreshArt(ctx, conn, false)
		} else if prev == corev1.PowerState_POWER_STATE_UNREACHABLE {
			s.signal(s.reconnect)
		}
	}
	return power
}

// modelRecheck is how often an unchanged model is re-recorded, which also
// picks up an operator's acknowledgement.
const modelRecheck = 5 * time.Minute

// checkModel records the REST model. The model string encodes the Tizen
// generation (docs/tv-api.md §4), so a new one usually means a firmware update, the
// main operational risk. The warning stays until an operator acknowledges
// the model (vigo-core ack-model).
func (s *Service) checkModel(ctx context.Context, model string) {
	if model == "" {
		return
	}
	s.modelMu.Lock()
	defer s.modelMu.Unlock()
	if model == s.modelObserved && time.Since(s.modelAt) < modelRecheck {
		return
	}
	obs, err := s.store.ObserveModel(ctx, model)
	if err != nil {
		s.log.Error("record TV model", "err", err)
		return
	}
	s.modelObserved, s.modelAt = model, time.Now()
	if obs.Acknowledged {
		s.clearWarning("model_changed")
		return
	}
	msg := "The TV model changed to " + model + ", which usually means a firmware update. Verify vigo against the TV, then acknowledge it with vigo-core ack-model."
	if obs.Previous != "" {
		msg = "The TV model changed from " + obs.Previous + " to " + model + ", which usually means a firmware update. Verify vigo against the TV, then acknowledge it with vigo-core ack-model."
	}
	s.mu.Lock()
	_, known := s.st.warnings["model_changed"]
	s.mu.Unlock()
	if !known {
		s.log.Error("TV model changed", "previous", obs.Previous, "current", model)
	}
	s.setWarning("model_changed", msg)
}
