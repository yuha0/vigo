// Package leader elects the active vigo-core replica with a PostgreSQL
// advisory lock held on a dedicated session. Postgres releases the lock when
// that session ends, and a process that loses it exits rather than demoting
// itself.
package leader

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/yuha0/vigo/internal/db"
)

// ErrLostLeadership is returned by Run when the lock session died while
// this process was leader.
var ErrLostLeadership = errors.New("leader: lost the database session holding the leader lock")

type Config struct {
	// Enabled turns election on. When off, this process is always leader.
	Enabled bool
	DB      *db.DB

	// RetryPeriod spaces attempts to take the lock; CheckInterval spaces
	// liveness checks of the session once it is held.
	RetryPeriod   time.Duration
	CheckInterval time.Duration
	CheckTimeout  time.Duration

	// Elected runs after the lock is taken and before lead, e.g. to migrate
	// the schema and record the leader address. conn is the session holding
	// the lock, nil when election is disabled. An error ends Run.
	Elected func(ctx context.Context, conn *pgx.Conn) error

	Gauge prometheus.Gauge
	Log   *slog.Logger
}

func (c *Config) setDefaults() {
	if c.RetryPeriod <= 0 {
		c.RetryPeriod = 2 * time.Second
	}
	if c.CheckInterval <= 0 {
		c.CheckInterval = 5 * time.Second
	}
	if c.CheckTimeout <= 0 {
		c.CheckTimeout = 5 * time.Second
	}
	if c.Log == nil {
		c.Log = slog.New(slog.DiscardHandler)
	}
	if c.Elected == nil {
		c.Elected = func(context.Context, *pgx.Conn) error { return nil }
	}
}

func (c *Config) setGauge(v float64) {
	if c.Gauge != nil {
		c.Gauge.Set(v)
	}
}

// Run waits for leadership, then runs lead until ctx ends, lead returns or
// leadership is lost. It returns nil on ctx cancellation, lead's error if it
// returns one, and ErrLostLeadership on losing the lock.
func Run(ctx context.Context, cfg Config, lead func(context.Context) error) error {
	cfg.setDefaults()
	cfg.setGauge(0)
	if !cfg.Enabled {
		if err := cfg.Elected(ctx, nil); err != nil {
			return err
		}
		cfg.setGauge(1)
		defer cfg.setGauge(0)
		return lead(ctx)
	}
	if cfg.DB == nil {
		return errors.New("leader: election needs a database")
	}

	conn, err := acquire(ctx, cfg)
	if err != nil || conn == nil {
		return err
	}
	defer conn.Close(context.WithoutCancel(ctx))
	cfg.Log.Info("became leader", "backend_pid", conn.PgConn().PID())
	if err := cfg.Elected(ctx, conn); err != nil {
		return err
	}
	cfg.setGauge(1)
	defer cfg.setGauge(0)

	leadCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	leadErr := make(chan error, 1)
	go func() { leadErr <- lead(leadCtx) }()

	t := time.NewTicker(cfg.CheckInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			cancel()
			<-leadErr
			return nil
		case err := <-leadErr:
			return err
		case <-t.C:
			pctx, pcancel := context.WithTimeout(ctx, cfg.CheckTimeout)
			err := conn.Ping(pctx)
			pcancel()
			if err != nil && ctx.Err() == nil {
				cfg.Log.Error("leader session lost", "err", err)
				cancel()
				<-leadErr
				return ErrLostLeadership
			}
		}
	}
}

// acquire returns a session holding the leader lock, or nil when ctx ends
// first. Connection failures are retried.
func acquire(ctx context.Context, cfg Config) (*pgx.Conn, error) {
	var conn *pgx.Conn
	waiting := false
	for {
		if conn == nil {
			c, err := cfg.DB.Dedicated(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return nil, nil
				}
				cfg.Log.Warn("connect for leader election", "err", err)
			}
			conn = c
		}
		if conn != nil {
			ok, err := db.TryLock(ctx, conn, cfg.DB.CoreLeaderLock())
			switch {
			case ok:
				return conn, nil
			case err != nil && ctx.Err() == nil:
				cfg.Log.Warn("take leader lock", "err", err)
				conn.Close(context.WithoutCancel(ctx))
				conn = nil
			case !waiting:
				cfg.Log.Info("standing by; another replica is leader")
				waiting = true
			}
		}
		select {
		case <-ctx.Done():
			if conn != nil {
				conn.Close(context.WithoutCancel(ctx))
			}
			return nil, nil
		case <-time.After(cfg.RetryPeriod):
		}
	}
}
