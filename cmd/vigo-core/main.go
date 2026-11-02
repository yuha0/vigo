// Command vigo-core controls a Samsung The Frame TV and serves its API and
// web UI.
package main

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/sync/errgroup"

	"github.com/yuha0/vigo/internal/auth"
	"github.com/yuha0/vigo/internal/coreapi"
	"github.com/yuha0/vigo/internal/db"
	"github.com/yuha0/vigo/internal/integration"
	"github.com/yuha0/vigo/internal/leader"
	"github.com/yuha0/vigo/internal/library"
	"github.com/yuha0/vigo/internal/metrics"
	"github.com/yuha0/vigo/internal/pki"
	"github.com/yuha0/vigo/internal/samsung"
	"github.com/yuha0/vigo/internal/samsung/fake"
	"github.com/yuha0/vigo/internal/secretbox"
	"github.com/yuha0/vigo/internal/source"
	_ "github.com/yuha0/vigo/internal/source/file"
	"github.com/yuha0/vigo/internal/tv"
)

// version is set with -ldflags "-X main.version=...".
var version = "dev"

type dbOptions struct {
	urlFile     string
	caFile      string
	keyringFile string
}

type options struct {
	db               dbOptions
	listen           string
	listenInternal   string
	listenOps        string
	advertiseAddress string
	pkiDir           string
	tvHost           string
	tvPort           int
	clientName       string
	leaderElect      bool
	pollInterval     time.Duration
	sources          []string
	cacheDir         string
	sourceSync       time.Duration
	trustedProxies   []string
	publicOrigins    []string
	fakeTV           bool
	logLevel         string
}

func main() {
	if err := newCommand().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "vigo-core:", err)
		os.Exit(1)
	}
}

func newCommand() *cobra.Command {
	var o options
	cmd := &cobra.Command{
		Use:           "vigo-core",
		Short:         "Control a Samsung The Frame TV and serve its API and web UI",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return bindEnv(cmd.Flags())
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			log, err := newLogger(o.logLevel)
			if err != nil {
				return err
			}
			if err := serve(cmd.Context(), o, log); err != nil {
				log.Error("exiting", "err", err)
				return err
			}
			return nil
		},
	}
	pf := cmd.PersistentFlags()
	pf.StringVar(&o.db.urlFile, "database-url-file", "", "file holding the PostgreSQL connection URI")
	pf.StringVar(&o.db.caFile, "database-ca-file", "", "CA bundle for verify-full TLS to PostgreSQL")
	pf.StringVar(&o.db.keyringFile, "keyring-file", "", `encryption keyring: {"primary":"k1","keys":{"k1":"<base64 32 bytes>"}}`)
	pf.StringVar(&o.logLevel, "log-level", "info", "debug, info, warn or error")

	f := cmd.Flags()
	f.StringVar(&o.listen, "listen", ":8080", "plain HTTP listener for browsers")
	f.StringVar(&o.listenInternal, "listen-internal", ":8443", "mTLS listener for integrations and other replicas")
	f.StringVar(&o.listenOps, "listen-ops", ":9090", "plain HTTP listener for /healthz, /readyz and /metrics")
	f.StringVar(&o.advertiseAddress, "advertise-address", "", "host:port other replicas reach the mTLS listener at (default $POD_IP and the --listen-internal port)")
	f.StringVar(&o.pkiDir, "pki-dir", "", "directory with tls.crt, tls.key and ca.crt; empty disables the mTLS listener")
	f.StringVar(&o.tvHost, "tv-host", "", "TV host name or IP address")
	f.IntVar(&o.tvPort, "tv-port", 8002, "TV API port: 8002 (TLS) or 8001")
	f.StringVar(&o.clientName, "client-name", "vigo", "name shown on the TV's pairing prompt")
	f.BoolVar(&o.leaderElect, "leader-elect", false, "elect a leader through a PostgreSQL advisory lock")
	f.DurationVar(&o.pollInterval, "poll-interval", 10*time.Second, "TV power-state poll interval")
	f.StringArrayVar(&o.sources, "source", nil, "art source URL, repeatable, e.g. file:///srv/art?id=nas (add readonly=true to never write)")
	f.StringVar(&o.cacheDir, "cache-dir", filepath.Join(os.TempDir(), "vigo-core-cache"), "directory for generated thumbnails and previews; emptied at startup")
	f.DurationVar(&o.sourceSync, "source-sync-interval", 10*time.Minute, "interval between reconciliation passes of each source")
	f.StringArrayVar(&o.trustedProxies, "trusted-proxy", nil, "CIDR of a reverse proxy whose X-Forwarded-For is trusted for rate limiting, repeatable")
	f.StringArrayVar(&o.publicOrigins, "public-origin", nil, "origin browsers use, e.g. https://vigo.example.com, when a proxy rewrites Host; repeatable")
	f.BoolVar(&o.fakeTV, "fake-tv", false, "use an in-memory simulated TV instead of a real one")

	cmd.AddCommand(importTokenCommand(&o), ackModelCommand(&o), resetPasswordCommand(&o), keyringCommand())
	return cmd
}

// bindEnv sets each flag not given on the command line from VIGO_<FLAG>,
// e.g. --tv-host from VIGO_TV_HOST.
func bindEnv(fs *pflag.FlagSet) error {
	var errs []error
	fs.VisitAll(func(f *pflag.Flag) {
		if f.Changed {
			return
		}
		env := "VIGO_" + strings.ToUpper(strings.ReplaceAll(f.Name, "-", "_"))
		if v, ok := os.LookupEnv(env); ok {
			if err := fs.Set(f.Name, v); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", env, err))
			}
		}
	})
	return errors.Join(errs...)
}

func newLogger(level string) (*slog.Logger, error) {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("--log-level: %w", err)
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l})), nil
}

func openDB(ctx context.Context, o dbOptions) (*db.DB, error) {
	if o.urlFile == "" || o.keyringFile == "" {
		return nil, errors.New("--database-url-file and --keyring-file are required")
	}
	keys, err := secretbox.LoadKeyring(o.keyringFile)
	if err != nil {
		return nil, err
	}
	url, err := db.ReadURLFile(o.urlFile)
	if err != nil {
		return nil, err
	}
	return db.Open(ctx, db.Options{URL: url, CAFile: o.caFile, Keyring: keys})
}

// advertiseAddress defaults to the pod IP and the internal listener's port.
func advertiseAddress(o options) (string, error) {
	if o.advertiseAddress != "" {
		return o.advertiseAddress, nil
	}
	_, port, err := net.SplitHostPort(o.listenInternal)
	if err != nil {
		return "", fmt.Errorf("--listen-internal: %w", err)
	}
	host := os.Getenv("POD_IP")
	if host == "" {
		if host, err = os.Hostname(); err != nil {
			return "", err
		}
	}
	return net.JoinHostPort(host, port), nil
}

type listener struct {
	name string
	srv  *http.Server
	ln   net.Listener
	tls  bool
}

func serve(ctx context.Context, o options, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	replica := os.Getenv("POD_NAME")
	if replica == "" {
		replica, _ = os.Hostname()
	}
	log = log.With("replica", replica)
	slog.SetDefault(log)
	log.Info("starting vigo-core", "version", version, "leader_elect", o.leaderElect, "fake_tv", o.fakeTV, "schema_version", db.SchemaVersion())

	if o.leaderElect && o.pkiDir == "" {
		return errors.New("--leader-elect needs --pki-dir: standby replicas forward to the leader over mTLS")
	}
	advertise, err := advertiseAddress(o)
	if err != nil {
		return err
	}

	d, err := openDB(ctx, o.db)
	if err != nil {
		return err
	}
	defer d.Close()

	m := metrics.New()
	go pingDB(ctx, d, m, log)

	var src *pki.Source
	if o.pkiDir != "" {
		src, err = pki.Load(o.pkiDir, pki.WithLogger(log.With("component", "pki")))
		if err != nil {
			return err
		}
		go src.Watch(ctx, 10*time.Second)
		notAfter := func() time.Time { return src.Leaf().NotAfter }
		m.CertExpiry("internal", notAfter)
		m.CertExpiry("client", notAfter)
		logCert(log, src.Leaf())
	}

	newClient := func(cfg samsung.Config) samsung.Client {
		return samsung.New(cfg, log.With("component", "samsung"))
	}
	defaults := tv.Config{Host: o.tvHost, Port: o.tvPort, ClientName: o.clientName}
	if o.fakeTV {
		ftv := fake.New()
		// Bring the fake TV back as it was: starting empty against an
		// existing database would read as everything deleted on the TV.
		if items, err := library.LoadTVContent(ctx, d.Pool()); err != nil {
			log.Info("fake TV starts with its fixtures", "reason", err)
		} else if len(items) > 0 {
			ftv.Restore(items)
			log.Info("fake TV restored from the database", "items", len(items))
		}
		newClient = ftv.Client
		if defaults.Host == "" {
			defaults.Host = "fake-tv"
		}
	}
	svc, err := tv.New(tv.Options{
		Defaults:     defaults,
		Store:        d.TV(),
		NewClient:    newClient,
		Metrics:      m,
		Log:          log.With("component", "tv"),
		Replica:      replica,
		PollInterval: o.pollInterval,
	})
	if err != nil {
		return err
	}

	sources, err := source.NewRegistry(o.sources)
	if err != nil {
		return err
	}
	lib, err := library.New(library.Options{
		DB:           d,
		TV:           svc,
		Sources:      sources,
		CacheDir:     o.cacheDir,
		Metrics:      m,
		Log:          log.With("component", "library"),
		SyncInterval: o.sourceSync,
	})
	if err != nil {
		return err
	}
	svc.SetLibraryHook(lib)
	for _, s := range sources.All() {
		log.Info("art source", "source", s.ID(), "kind", s.Kind(), "display", s.Display(),
			"writable", source.CanWrite(s), "editable", source.CanEdit(s))
	}

	var trusted []netip.Prefix
	for _, c := range o.trustedProxies {
		p, err := netip.ParsePrefix(c)
		if err != nil {
			return fmt.Errorf("--trusted-proxy %q: %w", c, err)
		}
		trusted = append(trusted, p)
	}
	authn := auth.New(auth.Options{
		Pool:           d.Pool(),
		Metrics:        m,
		Log:            log.With("component", "auth"),
		TrustedProxies: trusted,
		PublicOrigins:  o.publicOrigins,
	})

	var ready atomic.Bool
	apiOpts := coreapi.Options{
		TV:      svc,
		Library: lib,
		Auth:    authn,
		Metrics: m,
		Log:     log.With("component", "api"),
		Ready:   ready.Load,
		Integration: integration.New(integration.Options{
			DB:      d,
			Metrics: m,
			Log:     log.With("component", "integration"),
		}),
	}
	if src != nil {
		apiOpts.HomeEndpoint = func(ctx context.Context) (string, error) { return d.IntegrationEndpoint(ctx, "homekit") }
		apiOpts.HomeTLS = src.ClientTLS(pki.IntegrationIdentity("homekit"))
		if o.leaderElect {
			apiOpts.Leader = func(ctx context.Context) (string, error) {
				l, err := d.CurrentLeader(ctx)
				return l.Address, err
			}
			apiOpts.PeerTLS = src.ClientTLS(pki.Core)
		}
	}
	api, err := coreapi.New(apiOpts)
	if err != nil {
		return err
	}

	listeners := []*listener{
		{name: "public", srv: coreapi.NewServer(o.listen, api.Public())},
		{name: "ops", srv: coreapi.NewServer(o.listenOps, api.Ops())},
	}
	if src != nil {
		listeners = append(listeners, &listener{
			name: "internal",
			srv:  coreapi.NewTLSServer(o.listenInternal, pki.Authenticate(api.Internal()), src.ServerTLS(coreapi.AllowInternal)),
			tls:  true,
		})
	} else {
		log.Warn("no --pki-dir: the mTLS listener, integrations and the HomeService proxy are disabled")
	}
	serveErr := make(chan error, len(listeners))
	for _, l := range listeners {
		l.srv.ErrorLog = slog.NewLogLogger(log.With("listener", l.name).Handler(), slog.LevelWarn)
		if l.ln, err = net.Listen("tcp", l.srv.Addr); err != nil {
			return err
		}
		go func() {
			var err error
			if l.tls {
				err = l.srv.ServeTLS(l.ln, "", "")
			} else {
				err = l.srv.Serve(l.ln)
			}
			if !errors.Is(err, http.ErrServerClosed) {
				serveErr <- fmt.Errorf("%s listener: %w", l.name, err)
			}
		}()
		log.Info("listening", "listener", l.name, "addr", l.ln.Addr().String())
	}
	ready.Store(true)

	electErr := make(chan error, 1)
	go func() {
		electErr <- leader.Run(ctx, leader.Config{
			Enabled: o.leaderElect,
			DB:      d,
			Gauge:   m.Leader,
			Log:     log.With("component", "leader"),
			Elected: func(ctx context.Context, conn *pgx.Conn) error {
				applied, err := d.Migrate(ctx)
				if err != nil {
					return err
				}
				if len(applied) > 0 {
					log.Info("applied database migrations", "migrations", applied)
				}
				if conn != nil {
					return d.SetLeader(ctx, conn, replica, advertise)
				}
				return nil
			},
		}, func(ctx context.Context) error {
			go reencrypt(ctx, d, log)
			g, ctx := errgroup.WithContext(ctx)
			g.Go(func() error { return svc.Run(ctx) })
			g.Go(func() error { return lib.Run(ctx) })
			return g.Wait()
		})
	}()

	var runErr error
	select {
	case <-ctx.Done():
		log.Info("shutting down")
		runErr = <-electErr
	case runErr = <-electErr:
		if runErr == nil && ctx.Err() == nil {
			runErr = errors.New("the TV service stopped unexpectedly")
		}
	case runErr = <-serveErr:
	}
	stop()

	ready.Store(false)
	svc.Close()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, l := range listeners {
		if err := l.srv.Shutdown(shutdownCtx); err != nil {
			log.Warn("http shutdown", "listener", l.name, "err", err)
			l.srv.Close()
		}
	}
	if runErr != nil {
		return runErr
	}
	log.Info("stopped")
	return nil
}

func logCert(log *slog.Logger, leaf *x509.Certificate) {
	id, _ := pki.Identity(leaf)
	log.Info("loaded TLS identity", "identity", id, "not_after", leaf.NotAfter)
}

func pingDB(ctx context.Context, d *db.DB, m *metrics.Metrics, log *slog.Logger) {
	up := true
	for {
		pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := d.Ping(pctx)
		cancel()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			m.DBUp.Set(0)
			if up {
				log.Error("database unreachable", "err", err)
			}
		} else {
			m.DBUp.Set(1)
			if !up {
				log.Info("database reachable again")
			}
		}
		up = err == nil
		select {
		case <-ctx.Done():
			return
		case <-time.After(15 * time.Second):
		}
	}
}

// reencrypt moves values to the primary key after a rotation, at startup
// and then hourly.
func reencrypt(ctx context.Context, d *db.DB, log *slog.Logger) {
	for {
		n, err := d.Reencrypt(ctx)
		if err != nil && ctx.Err() == nil {
			log.Error("re-encrypt to the primary key", "err", err)
		}
		if n > 0 {
			log.Info("re-encrypted values with the primary key", "values", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Hour):
		}
	}
}
