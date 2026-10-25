// Package metrics exposes Prometheus metrics and the health endpoints. Every method is nil-safe,
// so components work without metrics (tests, one-shot subcommands).
package metrics

import (
	"context"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics are the service's signals (section 12, observability).
type Metrics struct {
	reg           *prometheus.Registry
	requests      *prometheus.CounterVec
	duration      *prometheus.HistogramVec
	published     prometheus.Counter
	outboxAge     prometheus.Gauge
	notifications prometheus.Counter
	indexerLag    prometheus.Gauge
	indexed       prometheus.Counter
	locks         prometheus.Gauge

	historyPruned      prometheus.Counter
	historyBytes       prometheus.Counter
	historyDuration    prometheus.Gauge
	historyRuns        *prometheus.CounterVec
	historyLastSuccess prometheus.Gauge
	historyPending     prometheus.Gauge
	historyPendingAge  prometheus.Gauge
}

// New registers the metrics.
func New() *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		reg: reg,
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "jiku_docs_requests_total", Help: "Requests by plane, method and error code (empty = success).",
		}, []string{"plane", "method", "code"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "jiku_docs_request_duration_seconds", Help: "Request latency by plane and method.",
			Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
		}, []string{"plane", "method"}),
		published: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "jiku_docs_outbox_published_total", Help: "Events published to JIKU_DOCS_EVENTS."}),
		outboxAge: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "jiku_docs_outbox_oldest_unpublished_seconds", Help: "Age of the oldest unpublished changelog row (alert above 60)."}),
		notifications: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "jiku_docs_notifications_total", Help: "Per-person notifications sent (fan-out)."}),
		indexerLag: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "jiku_docs_indexer_lag_seconds", Help: "Age of the last event the indexer processed (alert above 300)."}),
		indexed: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "jiku_docs_indexer_documents_total", Help: "Documents (re)indexed."}),
		locks: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "jiku_docs_locks_active", Help: "Live edit locks."}),
	}
	m.historyPruned = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "docs_history_versions_pruned_total", Help: "Versions removed by the history compaction."})
	m.historyBytes = prometheus.NewCounter(prometheus.CounterOpts{
		Name: "docs_history_bytes_freed_total", Help: "Bytes of the versions removed by the history compaction."})
	m.historyDuration = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "docs_history_run_duration_seconds", Help: "Duration of the last history compaction run."})
	m.historyRuns = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "docs_history_runs_total", Help: "History compaction runs by result (success, failure)."}, []string{"result"})
	m.historyLastSuccess = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "docs_history_last_success_timestamp_seconds", Help: "Unix time of the last successful compaction (alert when older than 48 h)."})
	m.historyPending = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "docs_history_pruned_objects_pending", Help: "Rows in pruned_objects: objects of compacted versions still in S3."})
	m.historyPendingAge = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "docs_history_pruned_objects_oldest_seconds", Help: "Age of the oldest row in pruned_objects (alert above 86400)."})
	reg.MustRegister(m.requests, m.duration, m.published, m.outboxAge, m.notifications, m.indexerLag, m.indexed, m.locks,
		m.historyPruned, m.historyBytes, m.historyDuration, m.historyRuns, m.historyLastSuccess, m.historyPending, m.historyPendingAge,
		collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	return m
}

func (m *Metrics) Request(plane, method, code string, d time.Duration) {
	if m == nil {
		return
	}
	m.requests.WithLabelValues(plane, method, code).Inc()
	m.duration.WithLabelValues(plane, method).Observe(d.Seconds())
}

func (m *Metrics) Published(n int) {
	if m != nil {
		m.published.Add(float64(n))
	}
}

func (m *Metrics) OutboxAge(d time.Duration) {
	if m != nil {
		m.outboxAge.Set(d.Seconds())
	}
}

func (m *Metrics) Notified(n int) {
	if m != nil {
		m.notifications.Add(float64(n))
	}
}

func (m *Metrics) IndexerLag(d time.Duration) {
	if m != nil {
		m.indexerLag.Set(d.Seconds())
	}
}

func (m *Metrics) Indexed() {
	if m != nil {
		m.indexed.Inc()
	}
}

func (m *Metrics) Locks(n int) {
	if m != nil {
		m.locks.Set(float64(n))
	}
}

// HistoryRun records one compaction run (dry runs and skipped runs are not recorded).
func (m *Metrics) HistoryRun(pruned int, bytes int64, d time.Duration, ok bool) {
	if m == nil {
		return
	}
	m.historyPruned.Add(float64(pruned))
	m.historyBytes.Add(float64(bytes))
	m.historyDuration.Set(d.Seconds())
	if ok {
		m.historyRuns.WithLabelValues("success").Inc()
		m.historyLastSuccess.SetToCurrentTime()
	} else {
		m.historyRuns.WithLabelValues("failure").Inc()
	}
}

// HistoryPending records the backlog of pruned_objects.
func (m *Metrics) HistoryPending(n int, oldest time.Duration) {
	if m != nil {
		m.historyPending.Set(float64(n))
		m.historyPendingAge.Set(oldest.Seconds())
	}
}

// Check is a readiness probe.
type Check func(ctx context.Context) error

// Serve runs the HTTP endpoints: /healthz (process alive), /readyz (dependencies), /metrics.
func Serve(ctx context.Context, addr string, m *Metrics, ready map[string]Check) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		cctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		for name, check := range ready {
			if err := check(cctx); err != nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(name + ": not ready\n"))
				return
			}
		}
		_, _ = w.Write([]byte("ready\n"))
	})
	if m != nil {
		mux.Handle("/metrics", promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{}))
	}
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
