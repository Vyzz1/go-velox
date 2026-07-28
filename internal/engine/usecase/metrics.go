package usecase

import (
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	checksTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "velox",
		Subsystem: "limiter",
		Name:      "checks_total",
		Help:      "Total number of rate limit check decisions.",
	}, []string{"rule_id", "allowed"})

	checkDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: "velox",
		Subsystem: "limiter",
		Name:      "check_duration_seconds",
		Help:      "End-to-end latency of a rate limit check (including Redis).",
		Buckets:   []float64{.0001, .0005, .001, .0025, .005, .01, .025, .05, .1},
	}, []string{"allowed"})

	redisErrors = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: "velox",
		Subsystem: "limiter",
		Name:      "redis_errors_total",
		Help:      "Total number of Redis errors during rate limit checks.",
	})

	degradedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Namespace: "velox",
		Subsystem: "limiter",
		Name:      "degraded_total",
		Help:      "Checks that fell back to the fail-mode policy because the store was unreachable.",
	}, []string{"mode"})
)

func recordCheck(ruleID string, allowed bool, dur time.Duration) {
	a := strconv.FormatBool(allowed)
	checksTotal.WithLabelValues(ruleID, a).Inc()
	checkDuration.WithLabelValues(a).Observe(dur.Seconds())
}

// recordDegraded counts a fail-mode fallback ("open" or "closed").
func recordDegraded(mode string) {
	degradedTotal.WithLabelValues(mode).Inc()
}
