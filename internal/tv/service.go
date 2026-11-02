// Package tv is vigo-core's domain service for one Samsung The Frame. It owns
// the connection lifecycle, the power and art-mode state model, the wake
// algorithm, setter verification and the event stream. Wire details live in
// package samsung; this package decides what to send in which state.
package tv

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/yuha0/vigo/gen/go/vigo/core/v1"
	"github.com/yuha0/vigo/internal/errs"
	"github.com/yuha0/vigo/internal/metrics"
	"github.com/yuha0/vigo/internal/samsung"
)

// Config identifies the TV and how vigo presents itself to it.
type Config struct {
	Host       string
	Port       int
	ClientName string
}

// overlay returns c with every non-zero field of o applied.
func (c Config) overlay(o Config) Config {
	if o.Host != "" {
		c.Host = o.Host
	}
	if o.Port != 0 {
		c.Port = o.Port
	}
	if o.ClientName != "" {
		c.ClientName = o.ClientName
	}
	return c
}

type Options struct {
	// Defaults from flags and environment; the stored config overrides them
	// at runtime.
	Defaults  Config
	Store     Store
	NewClient func(samsung.Config) samsung.Client
	Metrics   *metrics.Metrics
	Log       *slog.Logger
	// Replica names this process in Status, usually the pod name.
	Replica string

	PollInterval         time.Duration
	RequestTimeout       time.Duration
	UploadTimeout        time.Duration
	PairTimeout          time.Duration
	WakeTimeout          time.Duration
	WakePollInterval     time.Duration
	ReconnectMin         time.Duration
	ReconnectMax         time.Duration
	ThumbnailCacheSize   int
	ThumbnailConcurrency int
	// ThumbnailRetryInterval spaces retries of a fresh upload's thumbnail.
	ThumbnailRetryInterval time.Duration
}

func (o *Options) setDefaults() {
	def := func(d *time.Duration, v time.Duration) {
		if *d <= 0 {
			*d = v
		}
	}
	def(&o.PollInterval, 10*time.Second)
	def(&o.RequestTimeout, 10*time.Second)
	def(&o.UploadTimeout, 2*time.Minute)
	def(&o.PairTimeout, 90*time.Second)
	def(&o.WakeTimeout, 20*time.Second)
	def(&o.WakePollInterval, 500*time.Millisecond)
	def(&o.ReconnectMin, time.Second)
	def(&o.ReconnectMax, time.Minute)
	def(&o.ThumbnailRetryInterval, 3*time.Second)
	if o.ThumbnailCacheSize <= 0 {
		o.ThumbnailCacheSize = 512
	}
	if o.ThumbnailConcurrency <= 0 {
		o.ThumbnailConcurrency = 2
	}
	if o.Log == nil {
		o.Log = discardLogger()
	}
	if o.Metrics == nil {
		o.Metrics = metrics.New()
	}
}

// Service is safe for concurrent use. A new Service is a standby replica
// until Run is called.
type Service struct {
	opts  Options
	m     *metrics.Metrics
	log   *slog.Logger
	store Store

	mu        sync.Mutex
	overrides Config
	cfg       Config
	token     string
	// tokenRejected is set when the TV refused the stored token;
	// tokenVerified once it accepted it.
	tokenRejected bool
	tokenVerified bool
	client        samsung.Client
	art           samsung.ArtConn
	leader        bool
	runCtx        context.Context
	st            state
	published     *corev1.Status
	subs          map[*subscriber]struct{}
	hook          LibraryHook
	announced     map[string]time.Time

	pairMu     sync.Mutex
	pairCancel context.CancelFunc
	pairing    *corev1.Pairing

	remoteMu sync.Mutex
	remote   samsung.RemoteConn

	// powerMu serialises the state-dependent power and art-mode commands so
	// two concurrent ShowArt calls cannot both decide to send KEY_POWER.
	powerMu sync.Mutex

	refreshGroup singleflight.Group
	refreshMu    sync.Mutex
	lastRefresh  time.Time

	selMu     sync.Mutex
	selection *selectionRecord
	// reported is the latest get_current_artwork value.
	reported string

	modelMu       sync.Mutex
	modelObserved string
	modelAt       time.Time

	thumbs *thumbCache

	reconnect chan struct{}
	pollNow   chan struct{}
}

type state struct {
	power    corev1.PowerState
	artMode  corev1.ArtModeState
	conn     corev1.ConnectionState
	selected *corev1.SelectedArt
	device   *corev1.Device
	warnings map[string]*corev1.Warning
}

// New returns a standby service. Persisted state is loaded by Run, so a
// replica that becomes leader starts from current state.
func New(opts Options) (*Service, error) {
	opts.setDefaults()
	if opts.Store == nil || opts.NewClient == nil {
		return nil, errors.New("tv: Store and NewClient are required")
	}
	s := &Service{
		opts:      opts,
		m:         opts.Metrics,
		log:       opts.Log,
		store:     opts.Store,
		subs:      map[*subscriber]struct{}{},
		announced: map[string]time.Time{},
		thumbs:    newThumbCache(opts.ThumbnailCacheSize, opts.ThumbnailConcurrency),
		reconnect: make(chan struct{}, 1),
		pollNow:   make(chan struct{}, 1),
		st: state{
			conn:     corev1.ConnectionState_CONNECTION_STATE_STANDBY_REPLICA,
			device:   &corev1.Device{},
			warnings: map[string]*corev1.Warning{},
		},
	}
	s.cfg = opts.Defaults
	s.rebuildClientLocked()
	s.mu.Lock()
	s.publishLocked()
	s.mu.Unlock()
	return s, nil
}

// load reads the persisted configuration, token and selection.
func (s *Service) load(ctx context.Context) error {
	overrides, err := s.store.Config(ctx)
	if err != nil {
		return fmt.Errorf("tv: load config: %w", err)
	}
	token, err := s.store.Token(ctx)
	if err != nil {
		return fmt.Errorf("tv: load token: %w", err)
	}
	if err := s.loadSelection(ctx); err != nil {
		return fmt.Errorf("tv: load selection: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.overrides = overrides
	s.token = strings.TrimSpace(token)
	s.cfg = s.opts.Defaults.overlay(overrides)
	s.rebuildClientLocked()
	s.publishLocked()
	return nil
}

func (s *Service) rebuildClientLocked() {
	s.client = s.opts.NewClient(samsung.Config{
		Host:       s.cfg.Host,
		Port:       s.cfg.Port,
		ClientName: s.cfg.ClientName,
		Token:      s.token,
	})
}

// Run makes this replica active: it loads persisted state, connects to the
// TV, polls its state and serves commands until ctx ends. Call it once,
// after winning leader election or immediately when election is disabled.
// It returns an error only if the persisted state cannot be loaded.
func (s *Service) Run(ctx context.Context) error {
	if err := s.load(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	s.leader = true
	s.runCtx = ctx
	s.st.conn = corev1.ConnectionState_CONNECTION_STATE_DISCONNECTED
	s.publishLocked()
	s.mu.Unlock()

	var wg sync.WaitGroup
	wg.Go(func() { s.pollLoop(ctx) })
	wg.Go(func() { s.connLoop(ctx) })
	wg.Wait()

	s.remoteMu.Lock()
	if s.remote != nil {
		s.remote.Close()
		s.remote = nil
	}
	s.remoteMu.Unlock()
	s.pairMu.Lock()
	if s.pairCancel != nil {
		s.pairCancel()
	}
	s.pairMu.Unlock()
	return nil
}

// IsLeader reports whether Run has made this replica active.
func (s *Service) IsLeader() bool {
	return s.requireLeader() == nil
}

func (s *Service) currentClient() samsung.Client {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.client
}

func (s *Service) requireLeader() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.leader {
		return ErrStandbyReplica
	}
	return nil
}

// artConn returns the open art channel, or an error explaining why there is
// none.
func (s *Service) artConn() (samsung.ArtConn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.leader {
		return nil, ErrStandbyReplica
	}
	if s.art != nil {
		return s.art, nil
	}
	switch {
	case s.cfg.Host == "":
		return nil, ErrNoHost
	case s.st.conn == corev1.ConnectionState_CONNECTION_STATE_NOT_PAIRED:
		return nil, ErrNotPaired
	case s.st.conn == corev1.ConnectionState_CONNECTION_STATE_NO_ART_HOST:
		return nil, ErrNoArtHost
	case s.st.power == corev1.PowerState_POWER_STATE_UNREACHABLE:
		return nil, ErrUnreachable
	}
	return nil, ErrNotConnected
}

func (s *Service) signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// Status returns the current status snapshot.
func (s *Service) Status() *corev1.Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return proto.CloneOf(s.published)
}

// update mutates the state and publishes a new Status if anything changed.
func (s *Service) update(fn func(*state)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.st)
	s.publishLocked()
}

func (s *Service) setWarning(code, msg string) {
	s.update(func(st *state) {
		if w, ok := st.warnings[code]; ok && w.Message == msg {
			return
		}
		st.warnings[code] = &corev1.Warning{Code: code, Message: msg, Since: timestamppb.Now()}
	})
}

func (s *Service) clearWarning(code string) {
	s.update(func(st *state) { delete(st.warnings, code) })
}

func (s *Service) buildStatusLocked() *corev1.Status {
	st := &s.st
	out := &corev1.Status{
		Power:      st.power,
		ArtMode:    st.artMode,
		Connection: st.conn,
		ShowingArt: st.power == corev1.PowerState_POWER_STATE_ON && st.artMode == corev1.ArtModeState_ART_MODE_STATE_ON,
		Device:     proto.CloneOf(st.device),
		Leader:     s.leader,
		Replica:    s.opts.Replica,
	}
	if st.selected != nil {
		out.SelectedArt = proto.CloneOf(st.selected)
	}
	for _, code := range sortedKeys(st.warnings) {
		out.Warnings = append(out.Warnings, proto.CloneOf(st.warnings[code]))
	}
	return out
}

func (s *Service) publishLocked() {
	next := s.buildStatusLocked()
	if s.published != nil {
		prev := proto.CloneOf(s.published)
		prev.UpdatedAt = nil
		if proto.Equal(prev, next) {
			return
		}
	}
	next.UpdatedAt = timestamppb.Now()
	s.published = next
	s.recordStateMetrics(next)
	s.broadcastLocked(&corev1.WatchEventsResponse{
		Time:  next.UpdatedAt,
		Event: &corev1.WatchEventsResponse_Status{Status: proto.CloneOf(next)},
	})
}

func (s *Service) recordStateMetrics(st *corev1.Status) {
	metrics.SetState(s.m.PowerState, enumLabel(st.Power.String(), "POWER_STATE_"), enumLabels(corev1.PowerState_name, "POWER_STATE_"))
	metrics.SetState(s.m.ArtMode, enumLabel(st.ArtMode.String(), "ART_MODE_STATE_"), enumLabels(corev1.ArtModeState_name, "ART_MODE_STATE_"))
	metrics.SetState(s.m.ConnectionState, enumLabel(st.Connection.String(), "CONNECTION_STATE_"), enumLabels(corev1.ConnectionState_name, "CONNECTION_STATE_"))
}

func enumLabel(name, prefix string) string {
	return strings.ToLower(strings.TrimPrefix(name, prefix))
}

func enumLabels(names map[int32]string, prefix string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, enumLabel(n, prefix))
	}
	return out
}

// emit sends a non-status event to all subscribers.
func (s *Service) emit(ev *corev1.WatchEventsResponse) {
	ev.Time = timestamppb.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.broadcastLocked(ev)
}

// announce reports whether key has not been announced in the last two
// minutes, and marks it. It suppresses the duplicate event the TV sends for
// a change vigo made and already announced.
func (s *Service) announce(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for k, t := range s.announced {
		if now.Sub(t) > 2*time.Minute {
			delete(s.announced, k)
		}
	}
	if _, ok := s.announced[key]; ok {
		return false
	}
	s.announced[key] = now
	return true
}

// recentlyAnnounced reports whether key was announced within d.
func (s *Service) recentlyAnnounced(key string, d time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.announced[key]
	return ok && time.Since(t) < d
}

func (s *Service) Config() Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg
}

// UpdateConfig replaces the runtime overrides. A zero field falls back to
// the flag default. Changing anything reconnects.
func (s *Service) UpdateConfig(ctx context.Context, o Config) (Config, error) {
	if err := s.requireLeader(); err != nil {
		return Config{}, err
	}
	o.Host = strings.TrimSpace(o.Host)
	o.ClientName = strings.TrimSpace(o.ClientName)
	if strings.ContainsAny(o.Host, "/: \t") && !isIPv6(o.Host) {
		return Config{}, errs.New(ErrInvalidArgument, "tv_host must be a host name or IP address without scheme or port")
	}
	if o.Port < 0 || o.Port > 65535 {
		return Config{}, errs.New(ErrInvalidArgument, "tv_port must be between 1 and 65535")
	}
	if len(o.ClientName) > 64 {
		return Config{}, errs.New(ErrInvalidArgument, "client_name is longer than 64 characters")
	}
	if err := s.store.SetConfig(ctx, o); err != nil {
		return Config{}, err
	}

	s.mu.Lock()
	prev := s.cfg
	s.overrides = o
	s.cfg = s.opts.Defaults.overlay(o)
	cfg := s.cfg
	changed := cfg != prev
	if changed {
		s.rebuildClientLocked()
	}
	s.mu.Unlock()

	if changed {
		s.log.Info("tv config changed; reconnecting", "host", cfg.Host, "port", cfg.Port, "client_name", cfg.ClientName)
		s.closeRemote()
		s.signal(s.reconnect)
		s.signal(s.pollNow)
	}
	return cfg, nil
}

func isIPv6(h string) bool {
	return strings.Count(h, ":") >= 2 && !strings.ContainsAny(h, "/ \t[]")
}

func (s *Service) closeRemote() {
	s.remoteMu.Lock()
	defer s.remoteMu.Unlock()
	if s.remote != nil {
		s.remote.Close()
		s.remote = nil
	}
}
