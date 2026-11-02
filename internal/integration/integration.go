// Package integration serves vigo.integration.v1.IntegrationService:
// leadership for integration replicas, the endpoint registry and their
// persisted state. The caller's kind always comes from its certificate's URI
// SAN (spiffe://vigo/integration/<kind>), never from the request.
package integration

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"time"

	"connectrpc.com/connect"

	integrationv1 "github.com/yuha0/vigo/gen/go/vigo/integration/v1"
	"github.com/yuha0/vigo/gen/go/vigo/integration/v1/integrationv1connect"
	"github.com/yuha0/vigo/internal/db"
	"github.com/yuha0/vigo/internal/metrics"
	"github.com/yuha0/vigo/internal/pki"
)

const (
	maxValueBytes    = 1 << 20
	maxInstanceBytes = 200
)

var keyPattern = regexp.MustCompile(`^[-._a-zA-Z0-9]{1,200}$`)

type Options struct {
	DB      *db.DB
	Metrics *metrics.Metrics
	Log     *slog.Logger
	// RetryPeriod spaces a standby's attempts to take the lock;
	// CheckInterval spaces liveness checks of a leader's session.
	RetryPeriod   time.Duration
	CheckInterval time.Duration
}

type Service struct {
	opts Options
	log  *slog.Logger
}

var _ integrationv1connect.IntegrationServiceHandler = (*Service)(nil)

func New(o Options) *Service {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.RetryPeriod <= 0 {
		o.RetryPeriod = 2 * time.Second
	}
	if o.CheckInterval <= 0 {
		o.CheckInterval = 5 * time.Second
	}
	return &Service{opts: o, log: o.Log}
}

// caller returns the integration kind and identity of the authenticated
// peer.
func caller(ctx context.Context) (kind, identity string, err error) {
	id, ok := pki.Peer(ctx)
	if !ok {
		return "", "", connect.NewError(connect.CodeUnauthenticated, errors.New("no client certificate"))
	}
	kind, ok = pki.IntegrationKind(id)
	if !ok {
		return "", "", connect.NewError(connect.CodePermissionDenied, fmt.Errorf("%s is not an integration identity", id))
	}
	return kind, id, nil
}

func hashLease(lease string) []byte {
	sum := sha256.Sum256([]byte(lease))
	return sum[:]
}

func dbError(err error) error {
	switch {
	case errors.Is(err, db.ErrNotLeader):
		return connect.NewError(connect.CodeFailedPrecondition, errors.New("the lease is not the current leadership of this kind"))
	case errors.Is(err, db.ErrVersionMismatch):
		return connect.NewError(connect.CodeAborted, errors.New("the stored version differs from expected_version"))
	case errors.Is(err, context.Canceled):
		return connect.NewError(connect.CodeCanceled, err)
	case errors.Is(err, context.DeadlineExceeded):
		return connect.NewError(connect.CodeDeadlineExceeded, err)
	}
	return connect.NewError(connect.CodeUnavailable, err)
}

// AcquireLeadership backs the stream with an advisory lock for the kind on
// its own database session: STANDBY first, LEADER once the lock is taken,
// and the stream ends when the session dies or the client goes away.
func (s *Service) AcquireLeadership(ctx context.Context, req *connect.Request[integrationv1.AcquireLeadershipRequest], stream *connect.ServerStream[integrationv1.AcquireLeadershipResponse]) error {
	kind, identity, err := caller(ctx)
	if err != nil {
		return err
	}
	instance := req.Msg.Instance
	if len(instance) > maxInstanceBytes {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("instance is too long"))
	}
	log := s.log.With("kind", kind, "instance", instance)

	conn, err := s.opts.DB.Dedicated(ctx)
	if err != nil {
		return connect.NewError(connect.CodeUnavailable, err)
	}
	defer conn.Close(context.WithoutCancel(ctx))

	if err := stream.Send(&integrationv1.AcquireLeadershipResponse{Role: integrationv1.Role_ROLE_STANDBY}); err != nil {
		return err
	}
	lock := s.opts.DB.IntegrationLock(kind)
	for {
		ok, err := db.TryLock(ctx, conn, lock)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return connect.NewError(connect.CodeUnavailable, err)
		}
		if ok {
			break
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(s.opts.RetryPeriod):
		}
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	lease := base64.RawURLEncoding.EncodeToString(raw)
	leaseHash := hashLease(lease)
	if err := s.opts.DB.ClaimIntegration(ctx, conn, kind, instance, identity, leaseHash); err != nil {
		return connect.NewError(connect.CodeUnavailable, err)
	}
	if s.opts.Metrics != nil {
		g := s.opts.Metrics.IntegrationLeader.WithLabelValues(kind)
		g.Set(1)
		defer g.Set(0)
	}
	log.Info("integration leader elected")
	if err := stream.Send(&integrationv1.AcquireLeadershipResponse{Role: integrationv1.Role_ROLE_LEADER, Lease: lease}); err != nil {
		return err
	}

	t := time.NewTicker(s.opts.CheckInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Info("integration leader stream ended")
			return nil
		case <-t.C:
			pctx, cancel := context.WithTimeout(ctx, s.opts.CheckInterval)
			err := conn.Ping(pctx)
			cancel()
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				log.Error("integration leadership session lost", "err", err)
				return connect.NewError(connect.CodeUnavailable, errors.New("the database session backing this leadership was lost"))
			}
			if err := s.opts.DB.TouchIntegration(ctx, kind, leaseHash); err != nil && ctx.Err() == nil {
				log.Warn("update integration last_seen", "err", err)
			}
		}
	}
}

func (s *Service) Register(ctx context.Context, req *connect.Request[integrationv1.RegisterRequest]) (*connect.Response[integrationv1.RegisterResponse], error) {
	kind, _, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	u, err := url.Parse(req.Msg.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("endpoint must be an https:// base URL, e.g. https://198.51.100.21:8081"))
	}
	if err := s.opts.DB.RegisterEndpoint(ctx, kind, hashLease(req.Msg.Lease), u.Scheme+"://"+u.Host); err != nil {
		return nil, dbError(err)
	}
	s.log.Info("integration registered", "kind", kind, "endpoint", u.Host)
	return connect.NewResponse(&integrationv1.RegisterResponse{}), nil
}

func (s *Service) ListState(ctx context.Context, _ *connect.Request[integrationv1.ListStateRequest]) (*connect.Response[integrationv1.ListStateResponse], error) {
	kind, _, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	items, err := s.opts.DB.ListState(ctx, kind)
	if err != nil {
		return nil, dbError(err)
	}
	out := make([]*integrationv1.StateItem, len(items))
	for i, it := range items {
		out[i] = &integrationv1.StateItem{Key: it.Key, Value: it.Value, Version: it.Version}
	}
	return connect.NewResponse(&integrationv1.ListStateResponse{Items: out}), nil
}

func checkKey(key string) error {
	if !keyPattern.MatchString(key) {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("key must match [-._a-zA-Z0-9]{1,200}"))
	}
	return nil
}

func (s *Service) PutState(ctx context.Context, req *connect.Request[integrationv1.PutStateRequest]) (*connect.Response[integrationv1.PutStateResponse], error) {
	kind, _, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	m := req.Msg
	if err := checkKey(m.Key); err != nil {
		return nil, err
	}
	if len(m.Value) > maxValueBytes {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("value is larger than %d bytes", maxValueBytes))
	}
	if m.ExpectedVersion < 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("expected_version must not be negative"))
	}
	v, err := s.opts.DB.PutState(ctx, kind, hashLease(m.Lease), m.Key, m.Value, m.ExpectedVersion)
	if err != nil {
		return nil, dbError(err)
	}
	return connect.NewResponse(&integrationv1.PutStateResponse{Version: v}), nil
}

func (s *Service) DeleteState(ctx context.Context, req *connect.Request[integrationv1.DeleteStateRequest]) (*connect.Response[integrationv1.DeleteStateResponse], error) {
	kind, _, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	if err := checkKey(req.Msg.Key); err != nil {
		return nil, err
	}
	if err := s.opts.DB.DeleteState(ctx, kind, hashLease(req.Msg.Lease), req.Msg.Key); err != nil {
		return nil, dbError(err)
	}
	return connect.NewResponse(&integrationv1.DeleteStateResponse{}), nil
}
