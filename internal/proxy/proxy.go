// Package proxy wires the HTTP entrypoint (/v1/chat/completions, /v1/models)
// to the correct upstream — Grok or CodeBuddy — and emits Prometheus metrics
// + async ClickHouse audit rows for every proxied call.
//
// Dependencies:
//   - internal/upstream  (isGrokModel, expandGrokAlias, proxyGrok, proxyCodeBuddy, MAX_REQUEST_BODY)
//   - internal/db        (RequestLog DTO, Store.LogRequest)
//   - internal/auth      (Manager.Get / IsModelAllowed / IncrementTokens / IncrementRequests)
//   - internal/metrics   (RequestsTotal, RequestDuration)
package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"foxrouters/internal/auth"
	"foxrouters/internal/db"
	"foxrouters/internal/metrics"
	"foxrouters/internal/upstream"

	"github.com/gin-gonic/gin"
)

// ============================================================================
// UTILITY FUNCTIONS
// ============================================================================

// extractInputText: extract last user message from request body for logging.
// Truncates to 500 chars to avoid bloating the DB.
func extractInputText(bodyMap map[string]any) string {
	msgs, ok := bodyMap["messages"].([]any)
	if !ok || len(msgs) == 0 {
		return ""
	}
	// Find last user message
	for i := len(msgs) - 1; i >= 0; i-- {
		msg, ok := msgs[i].(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		if role != "user" {
			continue
		}
		content := msg["content"]
		switch v := content.(type) {
		case string:
			return upstream.TruncateLog(v, 500)
		case []any:
			// Array of content parts (vision etc.) — extract text parts
			var parts []string
			for _, p := range v {
				pm, ok := p.(map[string]any)
				if !ok {
					continue
				}
				if pt, _ := pm["type"].(string); pt == "text" {
					if txt, ok := pm["text"].(string); ok {
						parts = append(parts, txt)
					}
				}
			}
			if len(parts) > 0 {
				return upstream.TruncateLog(strings.Join(parts, " "), 500)
			}
		}
	}
	return ""
}

// toInt safely converts interface{} from c.Get() to int.
func toInt(v interface{}) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

// toString safely converts interface{} from c.Get() to string.
func toString(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// ============================================================================
// MAIN HANDLER
// ============================================================================

// ProxyRequest routes /v1/chat/completions to Grok or CodeBuddy based on the
// requested model, expands Grok aliases, enforces per-key model whitelists,
// records Prometheus metrics, updates per-key token quotas, and emits an
// async RequestLog to ClickHouse for chat completions only.
//
// The optional `registry` argument (may be nil) resolves runtime-configured
// custom models + aliases (see internal/proxy/custom.go). Aliases are
// rewritten in-body before routing; custom models override the default
// grok-* / cb/* prefix routing.
func ProxyRequest(grokAM *upstream.GrokAccountManager, cbKM *upstream.CBKeyManager, hc *upstream.HealthChecker, authMgr *auth.Manager, registry *CustomRegistry) gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path

		// /v1/models — local
		if path == "/v1/models" || path == "/models" {
			models := []gin.H{
				// Grok models
				{"id": "grok-4.5", "object": "model", "owned_by": "xai"},
				{"id": "grok-4.5-high", "object": "model", "owned_by": "xai"},
				{"id": "grok-4.5-medium", "object": "model", "owned_by": "xai"},
				{"id": "grok-4.5-low", "object": "model", "owned_by": "xai"},
				{"id": "grok-4.5-xhigh", "object": "model", "owned_by": "xai"},
				{"id": "grok-4.5-auto", "object": "model", "owned_by": "xai"},
				{"id": "grok-4.5-none", "object": "model", "owned_by": "xai"},
				{"id": "grok-4", "object": "model", "owned_by": "xai"},
				{"id": "grok-4-fast-reasoning", "object": "model", "owned_by": "xai"},
				{"id": "grok-code-fast-1", "object": "model", "owned_by": "xai"},
				// CodeBuddy — GPT
				{"id": "cb/gpt-5.6-sol", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/gpt-5.6-terra", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/gpt-5.6-luna", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/gpt-5.5", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/gpt-5.4", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/gpt-5.2", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/gpt-5.1", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/gpt-5", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/gpt-4.1", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/gpt-5.3-codex", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/gpt-5.1-codex", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/gpt-5.1-codex-mini", "object": "model", "owned_by": "codebuddy"},
				// CodeBuddy — Claude
				{"id": "cb/claude-opus-4.7-1m", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/claude-opus-4.6", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/claude-sonnet-4.6", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/claude-haiku-4.5", "object": "model", "owned_by": "codebuddy"},
				// CodeBuddy — Gemini
				{"id": "cb/gemini-3.1-pro", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/gemini-3.5-flash", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/gemini-3.0-flash", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/gemini-2.5-pro", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/gemini-2.5-flash", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/gemini-3.1-flash-lite", "object": "model", "owned_by": "codebuddy"},
				// CodeBuddy — OpenAI Reasoning
				{"id": "cb/o3", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/o4-mini", "object": "model", "owned_by": "codebuddy"},
				// CodeBuddy — GLM
				{"id": "cb/glm-5.2", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/glm-5.1", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/glm-5.0", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/glm-4.6", "object": "model", "owned_by": "codebuddy"},
				// CodeBuddy — DeepSeek
				{"id": "cb/deepseek-v3", "object": "model", "owned_by": "codebuddy"},
				{"id": "cb/deepseek-v3.2", "object": "model", "owned_by": "codebuddy"},
				// CodeBuddy — Kimi
				{"id": "cb/kimi-k2.5", "object": "model", "owned_by": "codebuddy"},
				// CodeBuddy — Default
				{"id": "cb/default-model", "object": "model", "owned_by": "codebuddy"},
			}
			// Append runtime-registered custom models.
			if registry != nil {
				for _, entry := range registry.ListModels() {
					models = append(models, gin.H{"id": entry.ID, "object": "model", "owned_by": entry.OwnedBy})
				}
			}
			c.JSON(200, gin.H{"object": "list", "data": models})
			return
		}

		// Only handle chat completions
		if path != "/v1/chat/completions" && path != "/chat/completions" {
			c.JSON(404, gin.H{"error": "not found: " + path})
			return
		}

		// Cap request body to prevent OOM / DoS via multi-GB uploads.
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, upstream.MAX_REQUEST_BODY)
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			// MaxBytesReader returns *http.MaxBytesError when limit exceeded
			if _, ok := err.(*http.MaxBytesError); ok {
				c.JSON(413, gin.H{"error": "request body too large", "limit_bytes": upstream.MAX_REQUEST_BODY})
				return
			}
			c.JSON(400, gin.H{"error": "read body failed"})
			return
		}

		var bodyMap map[string]any
		if err := json.Unmarshal(body, &bodyMap); err != nil {
			c.JSON(400, gin.H{"error": "invalid JSON"})
			return
		}

		model, _ := bodyMap["model"].(string)
		if model == "" {
			model = "grok-4.5"
			bodyMap["model"] = model
			body, _ = json.Marshal(bodyMap)
		}

		// Custom alias + custom model resolution (runtime-configured).
		// 1) Alias rewrite (single hop): "my-claude" → "cb/claude-sonnet-4.6"
		// 2) Custom-model lookup: if resolved id is registered, override the
		//    routing upstream + the model_name that goes over the wire.
		var customUpstream, customModelName string
		if registry != nil {
			resolved, up, mn := registry.Resolve(model)
			if resolved != model {
				model = resolved
				bodyMap["model"] = model
				body, _ = json.Marshal(bodyMap)
			}
			customUpstream = up
			customModelName = mn
		}

		// Model alias expansion: grok-4.5-{high,medium,low,auto,none} → grok-4.5 + reasoning_effort
		// Mirrors 9router's grok-cli provider (upstreamModelId + thinking level).
		// Skip when a custom model has already routed us — the custom model_name
		// is authoritative in that case.
		if customUpstream == "" {
			if effort, ok := upstream.ExpandGrokAlias(model); ok {
				model = "grok-4.5"
				bodyMap["model"] = model
				// Only set reasoning_effort if client didn't specify one already
				if _, has := bodyMap["reasoning_effort"]; !has {
					if _, has2 := bodyMap["reasoning"]; !has2 {
						bodyMap["reasoning_effort"] = effort
					}
				}
				body, _ = json.Marshal(bodyMap)
			}
		}

		// Per-key model whitelist check.
		// If the key has allowed_models set, reject models not on the list.
		// Supports glob: "grok-*", "cb/*", or exact "cb/gpt-5.5".
		fullKey := c.GetString("client_key")
		if fullKey != "" && authMgr != nil {
			if info := authMgr.Get(fullKey); info != nil {
				if !info.IsModelAllowed(model) {
					c.JSON(403, gin.H{
						"error": "model not allowed for this API key",
						"model": model,
						"hint":  "this key is restricted to specific models — contact the gateway operator",
					})
					c.Set("error_msg", "model not allowed: "+model)
					errJSON, _ := json.Marshal(gin.H{"error": "model not allowed", "model": model})
					c.Set("response_body", json.RawMessage(errJSON))
					return
				}
			}
		}

		clientStream := false
		if s, ok := bodyMap["stream"].(bool); ok && s {
			clientStream = true
		}
		if c.GetHeader("Accept") == "text/event-stream" {
			clientStream = true
		}

		startTime := time.Now()
		upstreamName := "codebuddy"

		// Routing decision:
		//   1. Custom model → routes to its declared upstream. If a ModelName
		//      override is set, we rewrite bodyMap[model] to that name so the
		//      upstream sees the "real" model. cbTransform strips the cb/
		//      prefix, so for CodeBuddy we prepend one to keep its stripCBPrefix
		//      happy; grok upstream sees the model_name as-is.
		//   2. Fall through to prefix routing (grok-* vs cb/*).
		switch customUpstream {
		case "grok":
			upstreamName = "grok"
			effectiveModel := model
			if customModelName != "" {
				effectiveModel = customModelName
				bodyMap["model"] = effectiveModel
				body, _ = json.Marshal(bodyMap)
			}
			upstream.ProxyGrok(c, body, grokAM, clientStream, hc, effectiveModel)
		case "codebuddy":
			upstreamName = "codebuddy"
			if customModelName != "" {
				// cbTransform will TrimPrefix("cb/") on this — prepend so the
				// upstream sees exactly customModelName.
				bodyMap["model"] = "cb/" + customModelName
				body, _ = json.Marshal(bodyMap)
			}
			upstream.ProxyCodeBuddy(c, body, bodyMap, cbKM, clientStream, hc)
		default:
			if upstream.IsGrokModel(model) {
				upstreamName = "grok"
				upstream.ProxyGrok(c, body, grokAM, clientStream, hc, model)
			} else {
				upstream.ProxyCodeBuddy(c, body, bodyMap, cbKM, clientStream, hc)
			}
		}

		// Record Prometheus metrics for this proxied request. Bucket status by
		// 3-digit HTTP code (200, 429, 500). Duration in seconds for the
		// standard histogram buckets. Cheap: label lookups + atomic increments.
		elapsed := time.Since(startTime).Seconds()
		status := strconv.Itoa(c.Writer.Status())
		metrics.RequestsTotal.WithLabelValues(upstreamName, status).Inc()
		metrics.RequestDuration.WithLabelValues(upstreamName).Observe(elapsed)

		// Per-key token quota tracking
		fullKey = c.GetString("client_key")
		if fullKey != "" && authMgr != nil {
			tokensIn, _ := c.Get("tokens_in")
			tokensOut, _ := c.Get("tokens_out")
			totalTokens := int64(toInt(tokensIn) + toInt(tokensOut))
			if totalTokens > 0 {
				authMgr.IncrementTokens(fullKey, totalTokens)
			} else {
				authMgr.IncrementRequests(fullKey)
			}
		}

		// Async log to ClickHouse — only for chat completion endpoint,
		// not for probes to /v1/models, /health, /props, etc.
		if grokAM.DB() != nil && path == "/v1/chat/completions" {
			inputText := extractInputText(bodyMap)
			outputText, _ := c.Get("output_text")
			tokensIn, _ := c.Get("tokens_in")
			tokensOut, _ := c.Get("tokens_out")
			responseBody, _ := c.Get("response_body")

			// Full request/response JSON stored in ClickHouse (ZSTD) — unlimited.
			rl := db.RequestLog{
				Timestamp:  startTime,
				RequestID:  c.GetString("request_id"),
				ClientKey:  c.GetString("client_key_masked"),
				Model:      model,
				Upstream:   upstreamName,
				AccountID:  c.GetString("upstream_account"),
				StatusCode: c.Writer.Status(),
				LatencyMs:  int(time.Since(startTime).Milliseconds()),
				TokensIn:   toInt(tokensIn),
				TokensOut:  toInt(tokensOut),
				InputText:  inputText,
				OutputText: toString(outputText),
			}
			// Capture error message for non-2xx responses (audit trail)
			if errMsg, exists := c.Get("error_msg"); exists {
				rl.ErrorMsg = toString(errMsg)
			}
			if len(body) > 0 {
				rl.RequestBody = json.RawMessage(body)
			}
			if rb, ok := responseBody.(json.RawMessage); ok && len(rb) > 0 {
				rl.ResponseBody = rb
			} else if errMsg, exists := c.Get("response_body"); exists {
				// Fallback: error branches set response_body directly
				if rb, ok := errMsg.(json.RawMessage); ok && len(rb) > 0 {
					rl.ResponseBody = rb
				}
			}
			grokAM.DB().LogRequest(rl)
		}
	}
}
