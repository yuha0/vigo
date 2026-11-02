package tv

import (
	"context"
	"time"

	corev1 "github.com/yuha0/vigo/gen/go/vigo/core/v1"
)

// refreshInterval is the minimum spacing of on-demand refreshes; calls
// within it of the last one return the current status.
const refreshInterval = 2 * time.Second

// Refresh re-reads power, art mode and the selected artwork from the TV.
// Failures are not errors: they show up in the returned status, e.g. as
// unreachable or not connected.
func (s *Service) Refresh(ctx context.Context) (*corev1.Status, error) {
	if err := s.requireLeader(); err != nil {
		return nil, err
	}
	s.refreshGroup.Do("refresh", func() (any, error) {
		s.refreshMu.Lock()
		recent := time.Since(s.lastRefresh) < refreshInterval
		s.refreshMu.Unlock()
		if recent {
			return nil, nil
		}
		// The shared call must finish even if the caller that started it goes away.
		ctx := context.WithoutCancel(ctx)
		if s.readPower(ctx, s.currentClient()) != corev1.PowerState_POWER_STATE_UNREACHABLE {
			if conn, err := s.artConn(); err == nil {
				s.refreshArt(ctx, conn, false)
			}
		}
		s.refreshMu.Lock()
		s.lastRefresh = time.Now()
		s.refreshMu.Unlock()
		if h := s.libraryHook(); h != nil {
			h.TVRefreshed()
		}
		return nil, nil
	})
	return s.Status(), nil
}
