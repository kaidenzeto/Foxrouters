// metrics.go — Prometheus metrics registration + helpers.
//
// Exposed at /metrics (public, no auth) so Prometheus scrapers can consume
// them. Metrics are instrumented from:
//   - proxy handlers (proxyGrok, proxyCodeBuddy) → requestsTotal + duration
//   - pool managers (grok_account.go, codebuddy.go) → active/disabled gauges
//   - circuit breaker (health.go) → circuitState
package main

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

var (
	// requestsTotal counts proxied requests bucketed by upstream + response
	// status class. Status is a 3-digit HTTP code as a string (e.g. "200", "429").
	requestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "foxrouters_requests_total",
		Help: "Total proxied requests by upstream and status code",
	}, []string{"upstream", "status"})

	// requestDuration observes end-to-end proxy latency in seconds.
	requestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name:    "foxrouters_request_duration_seconds",
		Help:    "Proxy request duration (seconds) by upstream",
		Buckets: prometheus.DefBuckets,
	}, []string{"upstream"})

	// activeKeys tracks the number of usable keys/accounts by pool type.
	// Type: "grok" (accounts), "codebuddy" (API keys), "auth" (gateway keys).
	activeKeys = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "foxrouters_active_keys",
		Help: "Number of currently active (usable) keys/accounts by pool",
	}, []string{"type"})

	// disabledKeys tracks disabled/cooldown entries by pool type.
	disabledKeys = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "foxrouters_disabled_keys",
		Help: "Number of disabled/cooldown keys/accounts by pool",
	}, []string{"type"})

	// circuitState reports the current circuit-breaker state per upstream.
	// 0 = closed (healthy), 1 = open (blocked), 2 = half-open (probing).
	circuitState = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "foxrouters_circuit_state",
		Help: "Circuit breaker state per upstream (0=closed, 1=open, 2=half-open)",
	}, []string{"upstream"})
)

// updatePoolGauges snapshots pool sizes into active/disabled gauges.
// Called periodically from the pool managers' background workers so the
// gauges stay eventually-consistent without adding hot-path overhead.
func updatePoolGauges(grokAM *GrokAccountManager, cbKM *CBKeyManager, authMgr *AuthManager) {
	if grokAM != nil {
		grokAM.mu.RLock()
		var gActive, gDisabled int
		for _, a := range grokAM.accounts {
			if a.IsDisabled() {
				gDisabled++
			} else {
				gActive++
			}
		}
		grokAM.mu.RUnlock()
		activeKeys.WithLabelValues("grok").Set(float64(gActive))
		disabledKeys.WithLabelValues("grok").Set(float64(gDisabled))
	}
	if cbKM != nil {
		cbKM.mu.RLock()
		var cActive, cDisabled int
		for _, k := range cbKM.keys {
			_, _, disabled := k.Stats()
			if disabled {
				cDisabled++
			} else {
				cActive++
			}
		}
		cbKM.mu.RUnlock()
		activeKeys.WithLabelValues("codebuddy").Set(float64(cActive))
		disabledKeys.WithLabelValues("codebuddy").Set(float64(cDisabled))
	}
	if authMgr != nil {
		authMgr.mu.RLock()
		var aActive, aDisabled int
		for _, info := range authMgr.keys {
			if info.Disabled {
				aDisabled++
			} else {
				aActive++
			}
		}
		authMgr.mu.RUnlock()
		activeKeys.WithLabelValues("auth").Set(float64(aActive))
		disabledKeys.WithLabelValues("auth").Set(float64(aDisabled))
	}
}

// setCircuitState maps CircuitState → gauge value.
func setCircuitState(upstream string, state CircuitState) {
	var v float64
	switch state {
	case CircuitClosed:
		v = 0
	case CircuitOpen:
		v = 1
	case CircuitHalfOpen:
		v = 2
	}
	circuitState.WithLabelValues(upstream).Set(v)
}
