package middleware

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/mohabnazmy/API-Gateway/internal/proxy"
)

// Metrics records Prometheus request counters and latency histograms, labelled
// by route, method and status. Register it on a dedicated registry so each
// instance is self-contained and testable.
type Metrics struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
	inflight *prometheus.GaugeVec
}

// NewMetrics registers the gateway's metrics on reg.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	factory := promauto.With(reg)
	return &Metrics{
		requests: factory.NewCounterVec(prometheus.CounterOpts{
			Name: "gateway_requests_total",
			Help: "Total number of requests handled by the gateway.",
		}, []string{"route", "method", "status"}),
		duration: factory.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "gateway_request_duration_seconds",
			Help:    "Request latency in seconds.",
			Buckets: prometheus.DefBuckets,
		}, []string{"route", "method", "status"}),
		inflight: factory.NewGaugeVec(prometheus.GaugeOpts{
			Name: "gateway_requests_in_flight",
			Help: "Number of requests currently being served.",
		}, []string{"route"}),
	}
}

// Middleware instruments each request.
func (m *Metrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := newRecorder(w)

		routeName := "unmatched"
		if entry, ok := proxy.EntryFromContext(r.Context()); ok {
			routeName = entry.Route().Name
		}
		m.inflight.WithLabelValues(routeName).Inc()
		defer m.inflight.WithLabelValues(routeName).Dec()

		next.ServeHTTP(rec, r)

		if rec.status == 0 {
			rec.status = http.StatusOK
		}
		status := strconv.Itoa(rec.status)
		m.requests.WithLabelValues(routeName, r.Method, status).Inc()
		m.duration.WithLabelValues(routeName, r.Method, status).Observe(time.Since(start).Seconds())
	})
}
