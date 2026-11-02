package integration_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"

	integrationv1 "github.com/yuha0/vigo/gen/go/vigo/integration/v1"
	"github.com/yuha0/vigo/gen/go/vigo/integration/v1/integrationv1connect"
	"github.com/yuha0/vigo/internal/db/dbtest"
	"github.com/yuha0/vigo/internal/integration"
	"github.com/yuha0/vigo/internal/metrics"
	"github.com/yuha0/vigo/internal/pki"
	"github.com/yuha0/vigo/internal/pki/pkitest"
)

var (
	homekit = pki.IntegrationIdentity("homekit")
	matter  = pki.IntegrationIdentity("matter")
)

type env struct {
	e       *dbtest.Env
	url     string
	sources map[string]*pki.Source
	m       *metrics.Metrics
}

func newEnv(t *testing.T) *env {
	t.Helper()
	e := dbtest.Open(t)
	m := metrics.New()
	svc := integration.New(integration.Options{DB: e.DB, Metrics: m, RetryPeriod: 20 * time.Millisecond, CheckInterval: 20 * time.Millisecond})
	mux := http.NewServeMux()
	mux.Handle(integrationv1connect.NewIntegrationServiceHandler(svc))
	sources := pkitest.Sources(t, pki.Core, homekit, matter)
	url := pkitest.ServeTLS(t, sources[pki.Core], func(string) bool { return true }, mux)
	return &env{e: e, url: url, sources: sources, m: m}
}

func (e *env) client(id string) integrationv1connect.IntegrationServiceClient {
	return integrationv1connect.NewIntegrationServiceClient(pkitest.Client(e.sources[id], pki.Core), e.url, connect.WithGRPC())
}

type leadership struct {
	stream *connect.ServerStreamForClient[integrationv1.AcquireLeadershipResponse]
	cancel context.CancelFunc
}

func acquire(t *testing.T, c integrationv1connect.IntegrationServiceClient, instance string) *leadership {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s, err := c.AcquireLeadership(ctx, connect.NewRequest(&integrationv1.AcquireLeadershipRequest{Instance: instance}))
	if err != nil {
		t.Fatal(err)
	}
	if !s.Receive() || s.Msg().Role != integrationv1.Role_ROLE_STANDBY {
		t.Fatalf("first message = %v, %v", s.Msg(), s.Err())
	}
	return &leadership{stream: s, cancel: cancel}
}

// lease waits for LEADER.
func (l *leadership) lease(t *testing.T) string {
	t.Helper()
	got := make(chan *integrationv1.AcquireLeadershipResponse, 1)
	go func() {
		if l.stream.Receive() {
			got <- l.stream.Msg()
		}
		close(got)
	}()
	select {
	case msg, ok := <-got:
		if !ok || msg.Role != integrationv1.Role_ROLE_LEADER || msg.Lease == "" {
			t.Fatalf("second message = %v, %v", msg, l.stream.Err())
		}
		return msg.Lease
	case <-time.After(5 * time.Second):
		t.Fatal("never became leader")
		return ""
	}
}

func code(err error) connect.Code {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Code()
	}
	return 0
}

func TestLeadershipAndState(t *testing.T) {
	env := newEnv(t)
	ctx := context.Background()
	c := env.client(homekit)

	a := acquire(t, c, "home-a")
	leaseA := a.lease(t)
	b := acquire(t, c, "home-b")
	if v := testutilGauge(env, "homekit"); v != 1 {
		t.Errorf("vigo_integration_leader = %v", v)
	}

	put := func(lease, key, value string, expected int64) (int64, error) {
		r, err := c.PutState(ctx, connect.NewRequest(&integrationv1.PutStateRequest{Lease: lease, Key: key, Value: []byte(value), ExpectedVersion: expected}))
		if err != nil {
			return 0, err
		}
		return r.Msg.Version, nil
	}
	if v, err := put(leaseA, "hap.AccessoryInfo", "one", 0); err != nil || v != 1 {
		t.Fatalf("put = %d, %v", v, err)
	}
	if _, err := put(leaseA, "hap.AccessoryInfo", "two", 7); code(err) != connect.CodeAborted {
		t.Errorf("stale expected_version: %v", err)
	}
	if _, err := put("forged", "x", "y", 0); code(err) != connect.CodeFailedPrecondition {
		t.Errorf("wrong lease: %v", err)
	}
	if _, err := put(leaseA, "bad/key", "y", 0); code(err) != connect.CodeInvalidArgument {
		t.Errorf("bad key: %v", err)
	}
	if _, err := c.Register(ctx, connect.NewRequest(&integrationv1.RegisterRequest{Lease: leaseA, Endpoint: "http://10.0.0.9:8081"})); code(err) != connect.CodeInvalidArgument {
		t.Errorf("non-https endpoint: %v", err)
	}
	if _, err := c.Register(ctx, connect.NewRequest(&integrationv1.RegisterRequest{Lease: leaseA, Endpoint: "https://10.0.0.9:8081"})); err != nil {
		t.Fatal(err)
	}
	if ep, err := env.e.DB.IntegrationEndpoint(ctx, "homekit"); err != nil || ep != "https://10.0.0.9:8081" {
		t.Errorf("endpoint = %q, %v", ep, err)
	}

	// The kind comes from the certificate: matter sees none of homekit's
	// state and cannot use its lease.
	m := env.client(matter)
	if r, err := m.ListState(ctx, connect.NewRequest(&integrationv1.ListStateRequest{})); err != nil || len(r.Msg.Items) != 0 {
		t.Errorf("matter list = %v, %v", r, err)
	}
	if _, err := m.PutState(ctx, connect.NewRequest(&integrationv1.PutStateRequest{Lease: leaseA, Key: "k", Value: []byte("v")})); code(err) != connect.CodeFailedPrecondition {
		t.Errorf("matter with homekit's lease: %v", err)
	}
	if _, err := env.client(pki.Core).ListState(ctx, connect.NewRequest(&integrationv1.ListStateRequest{})); code(err) != connect.CodePermissionDenied {
		t.Errorf("core identity: %v", err)
	}

	// The leader goes away; the standby takes over and the old lease stops
	// working.
	a.cancel()
	leaseB := b.lease(t)
	if _, err := put(leaseA, "hap.AccessoryInfo", "late", 0); code(err) != connect.CodeFailedPrecondition {
		t.Errorf("old lease after failover: %v", err)
	}
	if _, err := env.e.DB.IntegrationEndpoint(ctx, "homekit"); err == nil {
		t.Error("old endpoint survived the failover")
	}
	if v, err := put(leaseB, "hap.AccessoryInfo", "two", 1); err != nil || v != 2 {
		t.Errorf("new leader put = %d, %v", v, err)
	}
	r, err := c.ListState(ctx, connect.NewRequest(&integrationv1.ListStateRequest{}))
	if err != nil || len(r.Msg.Items) != 1 || string(r.Msg.Items[0].Value) != "two" || r.Msg.Items[0].Version != 2 {
		t.Errorf("list = %v, %v", r, err)
	}
	if _, err := c.DeleteState(ctx, connect.NewRequest(&integrationv1.DeleteStateRequest{Lease: leaseB, Key: "hap.AccessoryInfo"})); err != nil {
		t.Error(err)
	}
	if _, err := c.DeleteState(ctx, connect.NewRequest(&integrationv1.DeleteStateRequest{Lease: leaseA, Key: "hap.AccessoryInfo"})); code(err) != connect.CodeFailedPrecondition {
		t.Errorf("delete with the old lease: %v", err)
	}
}

func TestStreamEndsWhenSessionDies(t *testing.T) {
	env := newEnv(t)
	ctx := context.Background()
	l := acquire(t, env.client(homekit), "home-a")
	lease := l.lease(t)

	var pid int32
	env.e.DB.Pool().QueryRow(ctx, `SELECT backend_pid FROM integrations WHERE kind = 'homekit'`).Scan(&pid)
	if _, err := env.e.DB.Pool().Exec(ctx, `SELECT pg_terminate_backend($1)`, pid); err != nil {
		t.Fatal(err)
	}
	done := make(chan bool, 1)
	go func() { done <- l.stream.Receive() }()
	select {
	case more := <-done:
		if more {
			t.Fatalf("got another message: %v", l.stream.Msg())
		}
		if code(l.stream.Err()) != connect.CodeUnavailable {
			t.Errorf("stream error = %v", l.stream.Err())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not end")
	}
	_, err := env.client(homekit).PutState(ctx, connect.NewRequest(&integrationv1.PutStateRequest{Lease: lease, Key: "k"}))
	if code(err) != connect.CodeFailedPrecondition {
		t.Errorf("lease after the session died: %v", err)
	}
}

func testutilGauge(env *env, kind string) float64 {
	mfs, _ := env.m.Registry.Gather()
	for _, mf := range mfs {
		if mf.GetName() != "vigo_integration_leader" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, lp := range m.GetLabel() {
				if lp.GetName() == "kind" && lp.GetValue() == kind {
					return m.GetGauge().GetValue()
				}
			}
		}
	}
	return -1
}
