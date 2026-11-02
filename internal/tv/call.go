package tv

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"time"

	"github.com/yuha0/vigo/internal/metrics"
	"github.com/yuha0/vigo/internal/samsung"
)

// Requests issued on every poll; logged at debug level when they succeed.
var quietRequests = map[string]bool{
	"awake":         true,
	"device_info":   true,
	"get_thumbnail": true,
}

// call runs one TV request with a timeout, recording metrics and a log line.
// name is the TV's request name, or a descriptive name for non-art calls.
func call[T any](s *Service, ctx context.Context, name string, timeout time.Duration, fn func(context.Context) (T, error)) (T, error) {
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	if !IsBackground(ctx) {
		defer s.busy.begin()()
	}
	start := time.Now()
	v, err := fn(ctx)
	d := time.Since(start)

	outcome := outcomeOf(err)
	s.m.Requests.WithLabelValues(name, outcome).Inc()
	s.m.RequestDuration.WithLabelValues(name).Observe(d.Seconds())

	var pe *samsung.ProtocolError
	if errors.As(err, &pe) {
		s.m.ProtocolErrors.WithLabelValues(pe.Kind).Inc()
		s.setWarning("protocol_error", "The TV sent an unexpected "+pe.Kind+" message; its firmware may have changed the API.")
		s.log.Error("tv protocol error", "request", name, "kind", pe.Kind, "err", err)
	}

	attrs := []any{"request", name, "duration_seconds", d.Seconds(), "outcome", outcome}
	switch {
	case err != nil:
		s.log.Warn("tv request", append(attrs, "err", err)...)
	case quietRequests[name]:
		s.log.Debug("tv request", attrs...)
	default:
		s.log.Info("tv request", attrs...)
	}
	return v, err
}

func do(s *Service, ctx context.Context, name string, timeout time.Duration, fn func(context.Context) error) error {
	_, err := call(s, ctx, name, timeout, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, fn(ctx)
	})
	return err
}

func outcomeOf(err error) string {
	if err == nil {
		return metrics.OutcomeOK
	}
	if isTimeout(err) {
		return metrics.OutcomeTimeout
	}
	return metrics.OutcomeError
}

func isTimeout(err error) bool {
	if errors.Is(err, samsung.ErrNoReply) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }
