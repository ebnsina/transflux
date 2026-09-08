package obs

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics is a deliberately small set.
//
// Labels never carry a tenant, asset or job id: those are unbounded, and a
// metrics system that falls over under cardinality tells you nothing at the
// moment you most need it. Per-entity detail belongs in logs and in the
// database, both of which can answer questions about one job.
type Metrics struct {
	registry *prometheus.Registry

	HTTPRequests *prometheus.CounterVec
	HTTPDuration *prometheus.HistogramVec

	TasksByState  *prometheus.GaugeVec
	TaskOutcomes  *prometheus.CounterVec
	TaskDuration  *prometheus.HistogramVec
	LeaseRequests *prometheus.CounterVec
	// Unschedulable is the number of queued tasks no online worker can run.
	// A task nothing can take looks identical to a busy queue from the
	// outside, which is why it gets its own number.
	Unschedulable prometheus.Gauge

	Workers       *prometheus.GaugeVec
	LeaseExpiries prometheus.Counter

	BytesProcessed *prometheus.CounterVec
	CPUSeconds     *prometheus.CounterVec
}

func NewMetrics() *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		registry: reg,
		HTTPRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "transflux_http_requests_total",
			Help: "API requests by route and status.",
		}, []string{"method", "route", "status"}),
		HTTPDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "transflux_http_request_duration_seconds",
			Help:    "API request latency.",
			Buckets: prometheus.DefBuckets,
		}, []string{"route"}),

		TasksByState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "transflux_tasks",
			Help: "Tasks by operation and state.",
		}, []string{"operation", "state"}),
		TaskOutcomes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "transflux_task_outcomes_total",
			Help: "Finished task attempts by operation and outcome.",
		}, []string{"operation", "outcome"}),
		TaskDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "transflux_task_duration_seconds",
			Help: "How long tasks take, by operation.",
			// Media work spans seconds to hours, so the default buckets are
			// useless here.
			Buckets: []float64{1, 5, 15, 60, 300, 900, 1800, 3600, 7200},
		}, []string{"operation"}),
		LeaseRequests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "transflux_lease_requests_total",
			Help: "Worker polls, by whether they were given work.",
		}, []string{"result"}),
		Unschedulable: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "transflux_unschedulable_tasks",
			Help: "Queued tasks that no online worker is capable of running.",
		}),

		Workers: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "transflux_workers",
			Help: "Workers by state.",
		}, []string{"state"}),
		LeaseExpiries: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "transflux_lease_expiries_total",
			Help: "Leases reclaimed because a worker stopped reporting.",
		}),

		BytesProcessed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "transflux_bytes_total",
			Help: "Bytes read and written by media tasks.",
		}, []string{"direction"}),
		CPUSeconds: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "transflux_task_cpu_seconds_total",
			Help: "CPU time spent by media tasks, for cost analysis.",
		}, []string{"operation"}),
	}

	reg.MustRegister(m.HTTPRequests, m.HTTPDuration, m.TasksByState, m.TaskOutcomes,
		m.TaskDuration, m.LeaseRequests, m.Unschedulable, m.Workers,
		m.LeaseExpiries, m.BytesProcessed, m.CPUSeconds)
	return m
}

func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// Middleware records request counts and latency.
func (m *Metrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		route := Route(r.URL.Path)
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		m.HTTPRequests.WithLabelValues(r.Method, route, strconv.Itoa(rec.status)).Inc()
		m.HTTPDuration.WithLabelValues(route).Observe(time.Since(start).Seconds())
	})
}

// Route turns a path into a low-cardinality label by replacing identifiers
// with a placeholder.
//
// It works on the path rather than the matched pattern because the pattern is
// set by whichever mux matched, which is downstream of this middleware — using
// it collapses every nested route into the prefix it was mounted under. An
// unbounded label would be worse: one series per asset defeats the metrics
// system at the moment it is most needed.
func Route(path string) string {
	if path == "" {
		return "unmatched"
	}
	segments := strings.Split(path, "/")
	for i, seg := range segments {
		if looksLikeID(seg) {
			segments[i] = "{id}"
		}
	}
	return strings.Join(segments, "/")
}

// looksLikeID matches a UUID without insisting on the exact variant, since
// several id shapes end up in paths.
func looksLikeID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
			if !isHex {
				return false
			}
		}
	}
	return true
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}
