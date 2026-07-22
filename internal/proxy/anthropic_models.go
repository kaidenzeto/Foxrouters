// Anthropic-client detection + display-name helpers for /v1/models.
//
// Some Anthropic clients (Claude Code, `anthropic` SDKs) probe GET /v1/models
// against ANTHROPIC_BASE_URL and reject/ignore the response when it doesn't
// carry Anthropic-shaped fields (type/display_name/created_at). We keep the
// OpenAI fields on every entry and lazily enrich them when the request looks
// Anthropic — see proxy.go where isAnthropicClient() is called.
package proxy

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// isAnthropicClient returns true when the request looks like it came from an
// Anthropic-style client. Detection is best-effort — any of:
//   - x-api-key header (Anthropic's canonical auth header)
//   - anthropic-version header (spec-mandated by Anthropic SDKs)
//   - anthropic-beta header
//   - Accept containing "anthropic"
//   - User-Agent starting with "anthropic" / "claude"
func isAnthropicClient(c *gin.Context) bool {
	if c == nil || c.Request == nil {
		return false
	}
	h := c.Request.Header
	if h.Get("x-api-key") != "" || h.Get("X-Api-Key") != "" {
		return true
	}
	if h.Get("anthropic-version") != "" || h.Get("Anthropic-Version") != "" {
		return true
	}
	if h.Get("anthropic-beta") != "" || h.Get("Anthropic-Beta") != "" {
		return true
	}
	if strings.Contains(strings.ToLower(h.Get("Accept")), "anthropic") {
		return true
	}
	ua := strings.ToLower(h.Get("User-Agent"))
	if strings.HasPrefix(ua, "anthropic") || strings.HasPrefix(ua, "claude") {
		return true
	}
	return false
}

// displayNameForModel produces a human-friendly label for a model id.
// Rules:
//   - Grok models: "grok-4.5-high" → "Grok 4.5 (High)"
//   - CB models: "cb/claude-sonnet-4.6" → "Claude Sonnet 4.6 (CodeBuddy)"
//   - combo models: "combo/foo" → "Combo: foo"
//   - Unknown: title-case the id.
func displayNameForModel(id string) string {
	if id == "" {
		return ""
	}
	lower := strings.ToLower(id)

	// combo/<name>
	if strings.HasPrefix(lower, "combo/") {
		return "Combo: " + id[len("combo/"):]
	}

	// grok-*
	if strings.HasPrefix(lower, "grok-") {
		rest := id[len("grok-"):]
		lowerRest := strings.ToLower(rest)
		// Named variants — matched on the full suffix, not just the first
		// segment (which is often a version like "4.5" but sometimes a
		// keyword like "code-fast-1").
		switch lowerRest {
		case "code-fast-1":
			return "Grok Code Fast 1"
		}
		// Split into version + optional suffix (e.g. "4.5-high" → "4.5", "high")
		parts := strings.SplitN(rest, "-", 2)
		if len(parts) == 1 {
			return "Grok " + parts[0]
		}
		suffix := parts[1]
		// Common reasoning-effort suffixes get a parenthetical.
		switch suffix {
		case "high", "medium", "low", "xhigh", "auto", "none":
			return "Grok " + parts[0] + " (" + titleCase(suffix) + ")"
		case "fast-reasoning":
			return "Grok " + parts[0] + " (Fast Reasoning)"
		}
		return "Grok " + parts[0] + " " + suffix
	}

	// cb/<something>
	if strings.HasPrefix(lower, "cb/") {
		rest := id[len("cb/"):]
		return prettyCBName(rest) + " (CodeBuddy)"
	}

	return titleCase(id)
}

// prettyCBName title-cases a CodeBuddy model name segment.
//   claude-sonnet-4.6         → Claude Sonnet 4.6
//   gpt-5.1-codex-mini        → GPT 5.1 Codex Mini
//   gemini-3.1-flash-lite     → Gemini 3.1 Flash Lite
//   glm-5.2                   → GLM 5.2
//   o3 / o4-mini              → O3 / O4 Mini
//   deepseek-v3.2             → DeepSeek v3.2
//   kimi-k3                   → Kimi K3
//   default-model             → Default Model
func prettyCBName(s string) string {
	parts := strings.Split(s, "-")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			continue
		}
		switch strings.ToLower(p) {
		case "gpt":
			out = append(out, "GPT")
		case "glm":
			out = append(out, "GLM")
		case "deepseek":
			out = append(out, "DeepSeek")
		case "claude", "sonnet", "opus", "haiku", "gemini", "flash", "pro", "lite",
			"kimi", "codex", "mini", "default", "model", "fast", "reasoning":
			out = append(out, titleCase(p))
		default:
			out = append(out, titleCase(p))
		}
	}
	return strings.Join(out, " ")
}

// titleCase upper-cases the first rune of s. ASCII-only; adequate for
// model-name segments.
func titleCase(s string) string {
	if s == "" {
		return s
	}
	if s[0] >= 'a' && s[0] <= 'z' {
		return string(s[0]-32) + s[1:]
	}
	return s
}
