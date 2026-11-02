package tv

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

// thumbFetcher runs D2D thumbnail transfers one at a time, sharing a fetch
// among concurrent callers for the same id. Results are stored by the
// library, not here.
type thumbFetcher struct {
	group singleflight.Group
	slot  chan struct{}
}

func newThumbFetcher() *thumbFetcher {
	return &thumbFetcher{slot: make(chan struct{}, 1)}
}

func etagOf(data []byte) string {
	sum := sha256.Sum256(data)
	return `"` + hex.EncodeToString(sum[:12]) + `"`
}

type backgroundKey struct{}

// Background marks ctx as low-priority work: TV calls made with it wait
// until no interactive call is in flight, so prefetching never delays a
// user's request by more than the one transfer already running.
func Background(ctx context.Context) context.Context {
	return context.WithValue(ctx, backgroundKey{}, true)
}

// IsBackground reports whether ctx was marked by Background.
func IsBackground(ctx context.Context) bool {
	b, _ := ctx.Value(backgroundKey{}).(bool)
	return b
}

// interactive counts TV calls made for users that are in flight.
type interactive struct{ n atomic.Int64 }

func (i *interactive) begin() func() {
	i.n.Add(1)
	return func() { i.n.Add(-1) }
}

// waitIdle returns once no interactive call is in flight.
func (i *interactive) waitIdle(ctx context.Context) error {
	for i.n.Load() > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	return nil
}
