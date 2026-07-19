// Package upstream owns everything that talks to a real LLM provider:
// Grok (grok-*) and CodeBuddy (cb/*).  It contains the account/key
// managers, the token-refresh worker, the health checker + circuit
// breaker, and the two proxy functions used by the /v1/* handler.
//
// External deps: internal/db (persistence via DTO), internal/metrics
// (Prometheus gauges).  No dependency on internal/auth.
package upstream

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"foxrouters/internal/db"
	"foxrouters/internal/metrics"
)

// ---------------------------------------------------------------------------
// Package-level configuration + HTTP clients (moved from main package).
// ---------------------------------------------------------------------------

const (
	XAI_CLIENT_ID    = "b1a00492-073a-47ea-816f-4c329264a828"
	XAI_TOKEN_URL    = "https://auth.x.ai/oauth2/token"
	XAI_UPSTREAM_URL = "https://cli-chat-proxy.grok.com/v1"
	CB_UPSTREAM_URL  = "https://www.codebuddy.ai/v2/chat/completions"
	REFRESH_BUFFER   = 10 * time.Minute

	GROK_CLIENT_VERSION    = "0.2.93"
	GROK_CLIENT_IDENTIFIER = "grok-shell"
	CB_DEFAULT_SYSTEM      = "You are a helpful assistant."

	// Health check constants
	HEALTH_CHECK_INTERVAL = 10 * time.Minute // active LLM test every 10 min
	HEALTH_CHECK_TIMEOUT  = 30 * time.Second // LLM test timeout
	CB_OPEN_THRESHOLD     = 5                // 5 consecutive errors → circuit open
	CB_OPEN_DURATION      = 60 * time.Second // circuit stays open 60s before half-open
	CB_HALF_OPEN_PROBES   = 1                // 1 probe in half-open

	// Grok token pre-warm — background worker refreshes tokens BEFORE they expire.
	PRE_WARM_TICK          = 30 * time.Second
	PRE_WARM_WINDOW        = 30 * time.Minute
	MAX_CONCURRENT_REFRESH = 10
	REENABLE_TICK          = 1 * time.Minute

	// CB_CREDIT_LIMIT: disable key when credits used reaches this threshold.
	// Pro Trial = 250 credits. We disable at 240 to leave a small buffer.
	CB_CREDIT_LIMIT = 240.0

	// MAX_REQUEST_BODY caps incoming request bodies — kept here (upstream is
	// the primary consumer via chat/completions handler).
	MAX_REQUEST_BODY = 10 * 1024 * 1024 // 10MB
)

// upstreamClient: for Grok + CB API calls (long timeout, connection pool).
var upstreamClient = &http.Client{
	Timeout: 300 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 20,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		ForceAttemptHTTP2:   true,
		DisableCompression:  false,
	},
}

// tokenRefreshClient: for auth.x.ai token refresh (shorter timeout).
var tokenRefreshClient = &http.Client{
	Timeout: 30 * time.Second,
	Transport: &http.Transport{
		MaxIdleConns:        10,
		MaxIdleConnsPerHost: 5,
		IdleConnTimeout:     60 * time.Second,
		ForceAttemptHTTP2:   true,
	},
}

// healthCheckClient: for active health checks.
var healthCheckClient = &http.Client{
	Timeout: HEALTH_CHECK_TIMEOUT,
	Transport: &http.Transport{
		MaxIdleConns:        10,
		MaxIdleConnsPerHost: 5,
		IdleConnTimeout:     60 * time.Second,
		ForceAttemptHTTP2:   true,
	},
}

// UpstreamClient returns the shared HTTP client used for chat/completions
// proxy calls. Exported so tests and (future) siblings can reuse the
// same connection pool without reaching for the package-level var.
func UpstreamClient() *http.Client { return upstreamClient }

// truncateLog truncates text to maxLen, adding "..." suffix if truncated.
func truncateLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// TruncateLog exposes truncateLog to sibling packages (e.g. proxy).
func TruncateLog(s string, maxLen int) string { return truncateLog(s, maxLen) }

// setCircuitState wraps internal/metrics.SetCircuitState with the local
// CircuitState enum.
func setCircuitState(upstream string, state CircuitState) {
	var v int
	switch state {
	case CircuitClosed:
		v = 0
	case CircuitOpen:
		v = 1
	case CircuitHalfOpen:
		v = 2
	}
	metrics.SetCircuitState(upstream, v)
}

// SaveGrokAccount converts a GrokAccount to a db.GrokAccountDTO and persists.
// Package-internal helper (used by grok.go). Kept here so callers don't
// need to import internal/db just to save.
func saveGrokAccount(s *db.Store, acc *GrokAccount) {
	if s == nil || acc == nil {
		return
	}
	s.SaveGrokAccount(db.GrokAccountDTO{
		Email:        acc.Email,
		AccessToken:  acc.AccessToken,
		RefreshToken: acc.RefreshToken,
		IDToken:      acc.IDToken,
		ExpiresAt:    acc.expiresAt,
		ExpiresIn:    acc.ExpiresIn,
		Expired:      acc.Expired,
		LastRefresh:  acc.LastRefresh,
		Sub:          acc.Sub,
		Disabled:     acc.disabled,
		DisabledAt:   acc.disabledAt,
	})
}

// saveCBKey persists CB pool state via a db.CBKeyDTO.
func saveCBKey(s *db.Store, key string, creditsUsed float64, totalReqs int64, disabled bool, disabledAt time.Time) {
	if s == nil {
		return
	}
	s.SaveCBKey(db.CBKeyDTO{
		Key:         key,
		CreditsUsed: creditsUsed,
		TotalReqs:   totalReqs,
		Disabled:    disabled,
		DisabledAt:  disabledAt,
	})
}

// silence unused warnings in leaf builds
var _ = slog.LevelInfo
var _ = strings.TrimSpace
