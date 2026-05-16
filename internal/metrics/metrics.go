// Package metrics centralises the Prometheus collectors required by
// seeds/v1.yaml's /metrics acceptance criterion:
//
//	helios_case_state_total
//	helios_case_outcome_total
//	helios_queue_depth
//	helios_lumoskit_duration_seconds
//	helios_handoff_attempt_total
//	helios_notification_attempt_total
//
// Registration uses the default Prometheus registry so the standard
// promhttp.Handler() can serve /metrics without extra plumbing.
package metrics

import (
	"context"
	"database/sql"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	CaseStateTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "helios_case_state_total",
		Help: "Count of state transitions into each operational state.",
	}, []string{"state"})

	CaseOutcomeTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "helios_case_outcome_total",
		Help: "Count of terminal outcomes assigned to cases.",
	}, []string{"outcome"})

	LumoskitDurationSeconds = prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    "helios_lumoskit_duration_seconds",
		Help:    "Wall-clock duration of bin/lumoskit child process invocations.",
		Buckets: []float64{0.05, 0.25, 1, 5, 15, 30, 60, 120, 300, 600, 1800},
	})

	HandoffAttemptTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "helios_handoff_attempt_total",
		Help: "Count of downstream webhook delivery attempts by result.",
	}, []string{"result"})

	NotificationAttemptTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "helios_notification_attempt_total",
		Help: "Count of operator notification attempts by channel and result.",
	}, []string{"channel", "result"})

	registered bool
)

// Init wires the SQLite-backed queue_depth gauge and registers every collector
// exactly once with the default Prometheus registry. Safe to call from main
// after store.Open. Subsequent calls are no-ops.
func Init(db *sql.DB) {
	if registered {
		return
	}
	registered = true

	prometheus.MustRegister(
		CaseStateTotal,
		CaseOutcomeTotal,
		LumoskitDurationSeconds,
		HandoffAttemptTotal,
		NotificationAttemptTotal,
	)

	queueDepth := prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "helios_queue_depth",
		Help: "Number of cases currently in state=queued.",
	}, func() float64 {
		var n int
		if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM cases WHERE state = 'queued'`).Scan(&n); err != nil {
			return 0
		}
		return float64(n)
	})
	prometheus.MustRegister(queueDepth)
}

// ObserveCaseStateTransition is a tiny helper so the rest of the codebase
// doesn't have to import prometheus directly for every transition.
func ObserveCaseStateTransition(state string) { CaseStateTotal.WithLabelValues(state).Inc() }

// ObserveCaseOutcome records a terminal outcome assignment.
func ObserveCaseOutcome(outcome string) { CaseOutcomeTotal.WithLabelValues(outcome).Inc() }

// ObserveHandoffAttempt records the result of a single per-URL delivery.
func ObserveHandoffAttempt(result string) { HandoffAttemptTotal.WithLabelValues(result).Inc() }

// ObserveNotificationAttempt records the result of a single channel delivery.
func ObserveNotificationAttempt(channel, result string) {
	NotificationAttemptTotal.WithLabelValues(channel, result).Inc()
}
