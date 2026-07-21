// Tests for the Anthropic-client detection and display-name helpers used
// by /v1/models enrichment.
package proxy

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestIsAnthropicClient(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string]string
		want    bool
	}{
		{"no anthropic headers", map[string]string{"Authorization": "Bearer x"}, false},
		{"x-api-key", map[string]string{"x-api-key": "sk-ant-xxx"}, true},
		{"anthropic-version", map[string]string{"anthropic-version": "2023-06-01"}, true},
		{"anthropic-beta", map[string]string{"anthropic-beta": "tools-2024"}, true},
		{"accept anthropic", map[string]string{"Accept": "application/vnd.anthropic+json"}, true},
		{"ua claude", map[string]string{"User-Agent": "claude-code/1.0"}, true},
		{"ua anthropic", map[string]string{"User-Agent": "anthropic-sdk-python/0.7"}, true},
		{"ua curl", map[string]string{"User-Agent": "curl/8"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			req := httptest.NewRequest("GET", "/v1/models", nil)
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = req
			if got := isAnthropicClient(c); got != tc.want {
				t.Errorf("isAnthropicClient() = %v; want %v", got, tc.want)
			}
		})
	}
}

func TestDisplayNameForModel(t *testing.T) {
	cases := map[string]string{
		"grok-4.5":                "Grok 4.5",
		"grok-4.5-high":           "Grok 4.5 (High)",
		"grok-4.5-xhigh":          "Grok 4.5 (Xhigh)",
		"grok-4-fast-reasoning":   "Grok 4 (Fast Reasoning)",
		"grok-code-fast-1":        "Grok Code Fast 1",
		"cb/claude-sonnet-4.6":    "Claude Sonnet 4.6 (CodeBuddy)",
		"cb/claude-opus-4.7-1m":   "Claude Opus 4.7 1m (CodeBuddy)",
		"cb/gpt-5.1-codex-mini":   "GPT 5.1 Codex Mini (CodeBuddy)",
		"cb/glm-5.2":              "GLM 5.2 (CodeBuddy)",
		"cb/deepseek-v3.2":        "DeepSeek V3.2 (CodeBuddy)",
		"cb/kimi-k3":              "Kimi K3 (CodeBuddy)",
		"cb/default-model":        "Default Model (CodeBuddy)",
		"combo/mycombo":           "Combo: mycombo",
	}
	for id, want := range cases {
		if got := displayNameForModel(id); got != want {
			t.Errorf("displayNameForModel(%q) = %q; want %q", id, got, want)
		}
	}
}
