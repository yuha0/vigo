package leader

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/yuha0/vigo/internal/db"
	"github.com/yuha0/vigo/internal/db/dbtest"
)

func gauge() prometheus.Gauge {
	return prometheus.NewGauge(prometheus.GaugeOpts{Name: "vigo_leader"})
}

func TestDisabledIsAlwaysLeader(t *testing.T) {
	g := gauge()
	ctx, cancel := context.WithCancel(context.Background())
	var elected, led atomic.Bool
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()
	err := Run(ctx, Config{
		Gauge:   g,
		Elected: func(_ context.Context, conn *pgx.Conn) error { elected.Store(conn == nil); return nil },
	}, func(ctx context.Context) error {
		led.Store(testutil.ToFloat64(g) == 1)
		<-ctx.Done()
		return nil
	})
	if err != nil || !elected.Load() || !led.Load() {
		t.Fatalf("err = %v, elected %v, led with gauge 1 %v", err, elected.Load(), led.Load())
	}
	if testutil.ToFloat64(g) != 0 {
		t.Error("gauge not reset")
	}
}

type candidate struct {
	name   string
	g      prometheus.Gauge
	led    chan context.Context
	result chan error
	cancel context.CancelFunc
}

func start(t *testing.T, d *db.DB, name string) *candidate {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	c := &candidate{name: name, g: gauge(), led: make(chan context.Context, 1), result: make(chan error, 1), cancel: cancel}
	t.Cleanup(cancel)
	go func() {
		c.result <- Run(ctx, Config{
			Enabled:       true,
			DB:            d,
			RetryPeriod:   20 * time.Millisecond,
			CheckInterval: 20 * time.Millisecond,
			Gauge:         c.g,
			Elected: func(ctx context.Context, conn *pgx.Conn) error {
				return d.SetLeader(ctx, conn, name, name+":8443")
			},
		}, func(ctx context.Context) error {
			c.led <- ctx
			<-ctx.Done()
			return nil
		})
	}()
	return c
}

func (c *candidate) waitLead(t *testing.T) context.Context {
	t.Helper()
	select {
	case ctx := <-c.led:
		return ctx
	case <-time.After(5 * time.Second):
		t.Fatalf("%s never became leader", c.name)
		return nil
	}
}

func TestFailover(t *testing.T) {
	e := dbtest.Open(t)
	ctx := context.Background()
	a := start(t, e.DB, "core-a")
	aCtx := a.waitLead(t)
	b := start(t, e.DB, "core-b")

	select {
	case <-b.led:
		t.Fatal("two leaders")
	case <-time.After(200 * time.Millisecond):
	}
	if l, err := e.DB.CurrentLeader(ctx); err != nil || l.Identity != "core-a" || l.Address != "core-a:8443" {
		t.Fatalf("leader = %+v, %v", l, err)
	}
	if testutil.ToFloat64(a.g) != 1 || testutil.ToFloat64(b.g) != 0 {
		t.Error("gauges wrong")
	}

	// Kill the leader's session from outside, as a network partition or a
	// database failover would.
	var pid int32
	e.DB.Pool().QueryRow(ctx, `SELECT backend_pid FROM leader`).Scan(&pid)
	if _, err := e.DB.Pool().Exec(ctx, `SELECT pg_terminate_backend($1)`, pid); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-a.result:
		if !errors.Is(err, ErrLostLeadership) {
			t.Errorf("a: err = %v, want ErrLostLeadership", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a did not notice losing its session")
	}
	if aCtx.Err() == nil {
		t.Error("a's lead context not cancelled")
	}
	b.waitLead(t)
	if l, _ := e.DB.CurrentLeader(ctx); l.Identity != "core-b" {
		t.Errorf("leader after failover = %+v", l)
	}

	b.cancel()
	if err := <-b.result; err != nil {
		t.Errorf("b on shutdown: %v", err)
	}
	if _, err := e.DB.CurrentLeader(ctx); !errors.Is(err, db.ErrNotFound) {
		t.Errorf("leader after shutdown: %v", err)
	}
}

func TestElectedErrorAborts(t *testing.T) {
	e := dbtest.Open(t)
	boom := errors.New("migration failed")
	err := Run(context.Background(), Config{
		Enabled: true,
		DB:      e.DB,
		Elected: func(context.Context, *pgx.Conn) error { return boom },
	}, func(context.Context) error { t.Error("lead ran"); return nil })
	if !errors.Is(err, boom) {
		t.Errorf("err = %v", err)
	}
	conn, _ := e.DB.Dedicated(context.Background())
	defer conn.Close(context.Background())
	if ok, _ := db.TryLock(context.Background(), conn, e.DB.CoreLeaderLock()); !ok {
		t.Error("lock not released after the abort")
	}
}
