package tv

import (
	"context"
	"errors"
	"time"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/yuha0/vigo/gen/go/vigo/core/v1"
	"github.com/yuha0/vigo/internal/samsung"
)

// Pairing returns the pairing state.
func (s *Service) Pairing() *corev1.Pairing {
	s.pairMu.Lock()
	defer s.pairMu.Unlock()
	return s.pairingLocked()
}

// pairingLocked reports the active or failed attempt if there is one, and
// otherwise derives the state from the stored token.
func (s *Service) pairingLocked() *corev1.Pairing {
	if s.pairing != nil {
		return proto.CloneOf(s.pairing)
	}
	s.mu.Lock()
	token, rejected := s.token, s.tokenRejected
	s.mu.Unlock()
	switch {
	case token == "":
		return &corev1.Pairing{State: corev1.PairingState_PAIRING_STATE_UNPAIRED, Message: "No token is stored."}
	case rejected:
		return &corev1.Pairing{State: corev1.PairingState_PAIRING_STATE_UNPAIRED, Message: "The TV rejected the stored token."}
	}
	return &corev1.Pairing{State: corev1.PairingState_PAIRING_STATE_PAIRED}
}

func (s *Service) emitPairing(p *corev1.Pairing) {
	s.emit(&corev1.WatchEventsResponse{Event: &corev1.WatchEventsResponse_Pairing{Pairing: proto.CloneOf(p)}})
}

// StartPairing begins pairing in the background and returns at once in
// WAITING_FOR_ALLOW. Someone must press Allow on the TV before the deadline.
// The stored token is replaced only on success. Calling it while an attempt
// is running returns that attempt.
func (s *Service) StartPairing(ctx context.Context) (*corev1.Pairing, error) {
	if err := s.requireLeader(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	runCtx, client, host := s.runCtx, s.client, s.cfg.Host
	s.mu.Unlock()
	if host == "" {
		return nil, ErrNoHost
	}

	s.pairMu.Lock()
	defer s.pairMu.Unlock()
	if s.pairing != nil && s.pairing.State == corev1.PairingState_PAIRING_STATE_WAITING_FOR_ALLOW {
		return proto.CloneOf(s.pairing), nil
	}
	deadline := time.Now().Add(s.opts.PairTimeout)
	pctx, cancel := context.WithDeadline(runCtx, deadline)
	p := &corev1.Pairing{
		State:    corev1.PairingState_PAIRING_STATE_WAITING_FOR_ALLOW,
		Message:  "Press Allow on the TV.",
		Deadline: timestamppb.New(deadline),
	}
	s.pairing, s.pairCancel = p, cancel
	s.log.Info("pairing started", "deadline", deadline)
	s.emitPairing(p)
	go s.runPairing(pctx, cancel, client, p)
	return proto.CloneOf(p), nil
}

func (s *Service) runPairing(ctx context.Context, cancel context.CancelFunc, client samsung.Client, attempt *corev1.Pairing) {
	defer cancel()
	token, err := call(s, ctx, "pair", 0, client.Pair)

	s.pairMu.Lock()
	defer s.pairMu.Unlock()
	if s.pairing != attempt {
		return
	}
	s.pairCancel = nil
	failed := func(msg string) {
		s.log.Warn("pairing failed", "reason", msg, "err", err)
		s.pairing = &corev1.Pairing{State: corev1.PairingState_PAIRING_STATE_FAILED, Message: msg}
		s.emitPairing(s.pairing)
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		failed("Timed out waiting for Allow on the TV.")
		return
	case errors.Is(err, samsung.ErrUnauthorized):
		failed("The TV refused pairing.")
		return
	case err != nil:
		failed("Pairing failed: " + err.Error())
		return
	case token == "":
		failed("The TV returned no token.")
		return
	}

	sctx, scancel := context.WithTimeout(context.WithoutCancel(ctx), s.opts.RequestTimeout)
	defer scancel()
	if err = s.store.SetToken(sctx, token, s.Config().ClientName); err != nil {
		failed("Paired, but the token could not be saved: " + err.Error())
		return
	}
	s.mu.Lock()
	s.token = token
	s.tokenRejected = false
	s.tokenVerified = false
	s.rebuildClientLocked()
	s.mu.Unlock()
	s.pairing = nil
	s.log.Info("paired with the TV")
	s.clearWarning("token_rejected")
	s.closeRemote()
	s.signal(s.reconnect)
	s.emitPairing(s.pairingLocked())
}

// CancelPairing abandons a running attempt or clears a failed one.
func (s *Service) CancelPairing() *corev1.Pairing {
	s.pairMu.Lock()
	defer s.pairMu.Unlock()
	if s.pairCancel != nil {
		s.pairCancel()
		s.pairCancel = nil
	}
	changed := s.pairing != nil
	s.pairing = nil
	p := s.pairingLocked()
	if changed {
		s.log.Info("pairing cancelled")
		s.emitPairing(p)
	}
	return p
}
