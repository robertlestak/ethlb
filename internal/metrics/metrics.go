package metrics

import (
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	// Metrics
	HTTPRequests = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: os.Getenv("PROMETHEUS_NAMESPACE"),
			Name:      "http_requests_total",
			Help:      "Total number of HTTP requests by url, code, and method",
		},
		[]string{"url", "code", "method"},
	)
	CacheHit = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: os.Getenv("PROMETHEUS_NAMESPACE"),
			Name:      "cache_hit_total",
			Help:      "Total number of HTTP requests by chain, code, and method that hit the cache",
		},
		[]string{"chain", "code", "method"},
	)
	CacheMiss = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: os.Getenv("PROMETHEUS_NAMESPACE"),
			Name:      "cache_miss_total",
			Help:      "Total number of HTTP requests by chain, code, and method that miss the cache",
		},
		[]string{"chain", "code", "method"},
	)
	Cooldowns = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: os.Getenv("PROMETHEUS_NAMESPACE"),
			Name:      "cooldown_until",
			Help:      "Unix timestamp until when the cooldown is active",
		},
		[]string{"endpoint"},
	)
	EndpointEnabled = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: os.Getenv("PROMETHEUS_NAMESPACE"),
			Name:      "endpoint_enabled",
			Help:      "A boolean indicating whether the endpoint is enabled",
		},
		[]string{"chain", "endpoint"},
	)
	EndpointBlockHead = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: os.Getenv("PROMETHEUS_NAMESPACE"),
			Name:      "endpoint_block_head",
			Help:      "The block head of the endpoint",
		},
		[]string{"chain", "endpoint"},
	)
	responseTimeHistogram = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: os.Getenv("PROMETHEUS_NAMESPACE"),
		Name:      "http_server_request_duration_seconds",
		Help:      "Histogram of response time for handler in seconds",
		Buckets:   []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
	}, []string{"route", "method", "status_code"})
)

func registerMetrics() error {
	l := log.WithFields(log.Fields{
		"module": "metrics",
		"action": "registerMetrics",
	})
	l.Info("registering metrics")
	prometheus.MustRegister(
		HTTPRequests,
		responseTimeHistogram,
		CacheHit,
		CacheMiss,
		Cooldowns,
		EndpointEnabled,
		EndpointBlockHead,
	)
	return nil
}

type statusRecorder struct {
	http.ResponseWriter
	statusCode int
}

func (rec *statusRecorder) WriteHeader(statusCode int) {
	rec.statusCode = statusCode
	rec.ResponseWriter.WriteHeader(statusCode)
}

func MeasureResponseDuration(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := statusRecorder{w, 200}
		next.ServeHTTP(&rec, r)
		duration := time.Since(start)
		statusCode := strconv.Itoa(rec.statusCode)
		responseTimeHistogram.WithLabelValues(r.URL.String(), r.Method, statusCode).Observe(duration.Seconds())
	})
}

// StartExporter starts the prometheus exporter to export
func StartExporter() error {
	l := log.WithFields(log.Fields{
		"component": "metrics",
		"action":    "start",
	})
	l.Info("starting metrics exporter")
	if rerr := registerMetrics(); rerr != nil {
		l.WithError(rerr).Error("error registering metrics")
		return rerr
	}
	http.Handle("/metrics", promhttp.Handler())
	http.HandleFunc("/statusz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "ok")
	})
	var promPort = "9090"
	if os.Getenv("PROMETHEUS_PORT") != "" {
		promPort = os.Getenv("PROMETHEUS_PORT")
	}
	l.Infof("starting metrics exporter on port %s", promPort)
	http.ListenAndServe(":"+promPort, nil)
	return nil
}
