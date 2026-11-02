// Package metrics defines vigo-core's Prometheus collectors.
package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Outcomes of a TV request, as used in the outcome label.
const (
	OutcomeOK      = "ok"
	OutcomeError   = "error"
	OutcomeTimeout = "timeout"
)

// D2D transfer directions.
const (
	DirectionDownload = "download"
	DirectionUpload   = "upload"
)

type Metrics struct {
	Registry *prometheus.Registry

	PowerState         *prometheus.GaugeVec
	ArtMode            *prometheus.GaugeVec
	ConnectionState    *prometheus.GaugeVec
	Requests           *prometheus.CounterVec
	RequestDuration    *prometheus.HistogramVec
	SettingUnconfirmed *prometheus.CounterVec
	ProtocolErrors     *prometheus.CounterVec
	Reconnects         *prometheus.CounterVec
	D2DBytes           *prometheus.CounterVec
	D2DDuration        *prometheus.HistogramVec
	Leader             prometheus.Gauge
	DBUp               prometheus.Gauge
	IntegrationLeader  *prometheus.GaugeVec
	LibraryArtworks    *prometheus.GaugeVec
	SourceSyncDuration *prometheus.HistogramVec
	SourceSyncErrors   *prometheus.CounterVec
	AuthLogins         *prometheus.CounterVec
	Thumbnails         *prometheus.GaugeVec
	ThumbnailJobs      *prometheus.CounterVec
}

// New creates the collectors and registers them, plus the Go and process
// collectors, on a fresh registry.
func New() *Metrics {
	m := &Metrics{
		Registry: prometheus.NewRegistry(),
		PowerState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "vigo_tv_power_state",
			Help: "TV power state; 1 for the current state.",
		}, []string{"state"}),
		ArtMode: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "vigo_tv_art_mode",
			Help: "TV art mode; 1 for the current state.",
		}, []string{"state"}),
		ConnectionState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "vigo_tv_connection_state",
			Help: "Art channel connection state; 1 for the current state.",
		}, []string{"state"}),
		Requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "vigo_tv_requests_total",
			Help: "Requests sent to the TV.",
		}, []string{"request", "outcome"}),
		RequestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "vigo_tv_request_duration_seconds",
			Help:    "Duration of requests sent to the TV.",
			Buckets: []float64{.01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60},
		}, []string{"request"}),
		SettingUnconfirmed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "vigo_tv_setting_unconfirmed_total",
			Help: "Setting writes the TV acknowledged but whose re-read value differed.",
		}, []string{"key"}),
		ProtocolErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "vigo_tv_protocol_errors_total",
			Help: "Messages from the TV with an unexpected shape; a firmware-change signal.",
		}, []string{"kind"}),
		Reconnects: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "vigo_tv_reconnects_total",
			Help: "Reconnections to a TV channel.",
		}, []string{"channel"}),
		D2DBytes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "vigo_tv_d2d_bytes_total",
			Help: "Bytes moved over D2D sockets.",
		}, []string{"direction"}),
		D2DDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "vigo_tv_d2d_duration_seconds",
			Help:    "Duration of D2D transfers.",
			Buckets: []float64{.1, .25, .5, 1, 2.5, 5, 10, 30, 60, 120},
		}, []string{"direction"}),
		Leader: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_leader",
			Help: "1 when this replica is the leader.",
		}),
		DBUp: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "vigo_db_up",
			Help: "1 when the database answered the last ping.",
		}),
		IntegrationLeader: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "vigo_integration_leader",
			Help: "1 while this replica holds a leadership stream for the integration kind.",
		}, []string{"kind"}),
		LibraryArtworks: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "vigo_library_artworks",
			Help: "Artworks in the library by sync state.",
		}, []string{"sync_state"}),
		SourceSyncDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "vigo_source_sync_duration_seconds",
			Help:    "Duration of reconciliation passes per source.",
			Buckets: []float64{.1, .5, 1, 5, 15, 30, 60, 120, 300, 900},
		}, []string{"source"}),
		Thumbnails: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "vigo_thumbnails",
			Help: "Gallery thumbnails by origin (source, tv) and status (ok, missing, error, pending).",
		}, []string{"origin", "status"}),
		ThumbnailJobs: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "vigo_thumbnail_jobs_total",
			Help: "Thumbnail renders and downloads by origin and outcome (ok, missing, error).",
		}, []string{"origin", "outcome"}),
		AuthLogins: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "vigo_auth_logins_total",
			Help: "Browser sign-in attempts by outcome: ok, wrong_password or rate_limited.",
		}, []string{"outcome"}),
		SourceSyncErrors: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "vigo_source_sync_errors_total",
			Help: "Reconciliation passes that failed, e.g. an unreadable source root.",
		}, []string{"source"}),
	}
	m.Registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.PowerState, m.ArtMode, m.ConnectionState,
		m.Requests, m.RequestDuration, m.SettingUnconfirmed, m.ProtocolErrors,
		m.Reconnects, m.D2DBytes, m.D2DDuration, m.Leader, m.DBUp, m.IntegrationLeader,
		m.LibraryArtworks, m.SourceSyncDuration, m.SourceSyncErrors, m.AuthLogins, m.Thumbnails, m.ThumbnailJobs,
	)
	return m
}

// CertExpiry registers vigo_tls_cert_expiry_seconds for listener: the
// seconds remaining until notAfter() (negative once expired). It is
// evaluated at scrape time, so it follows certificate reloads.
func (m *Metrics) CertExpiry(listener string, notAfter func() time.Time) {
	m.Registry.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name:        "vigo_tls_cert_expiry_seconds",
		Help:        "Seconds until the TLS certificate in use expires.",
		ConstLabels: prometheus.Labels{"listener": listener},
	}, func() float64 { return time.Until(notAfter()).Seconds() }))
}

// SetState sets current to 1 and every other value in all to 0.
func SetState(g *prometheus.GaugeVec, current string, all []string) {
	for _, s := range all {
		v := 0.0
		if s == current {
			v = 1
		}
		g.WithLabelValues(s).Set(v)
	}
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{Registry: m.Registry})
}
