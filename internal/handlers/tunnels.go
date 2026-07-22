// Package handlers — tunnel API endpoints.
//
// All handlers are admin-only (wired under AdminMiddleware). Mutations
// are CSRF-guarded upstream at the router. Persistence lives in Redis
// via *tunnel.Manager.
//
// Endpoints:
//
//	GET    /api/tunnel/status    → runtime status (redacted)
//	POST   /api/tunnel/enable    → apply mode + credentials + start
//	POST   /api/tunnel/disable   → stop all + persist mode=none
//	POST   /api/tunnel/restart   → re-apply current persisted mode
package handlers

import (
	"strings"

	"foxrouters/internal/tunnel"

	"github.com/gin-gonic/gin"
)

// tunnelEnableInput is the wire schema for POST /api/tunnel/enable.
// All fields are optional except mode; credentials only need to be
// re-sent if they've changed (empty string = keep existing Redis
// value). This lets the dashboard show the current mode without
// echoing credentials back to the browser.
type tunnelEnableInput struct {
	Mode                string `json:"mode"`
	CloudflareAPIToken  string `json:"cloudflare_api_token,omitempty"`
	CloudflareAccountID string `json:"cloudflare_account_id,omitempty"`
	CloudflareZoneID    string `json:"cloudflare_zone_id,omitempty"`
	TunnelDomain        string `json:"tunnel_domain,omitempty"`
}

// HandleTunnelStatus: GET /api/tunnel/status.
func HandleTunnelStatus(mgr *tunnel.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		if mgr == nil {
			c.JSON(500, gin.H{"error": "tunnel manager not initialised"})
			return
		}
		st, err := mgr.Status()
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		c.JSON(200, gin.H{"ok": true, "status": st})
	}
}

// HandleTunnelEnable: POST /api/tunnel/enable.
// Empty credential fields on the input mean "keep existing" so the
// operator can flip modes without re-entering the CF token every time.
func HandleTunnelEnable(mgr *tunnel.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		if mgr == nil {
			c.JSON(500, gin.H{"error": "tunnel manager not initialised"})
			return
		}
		var in tunnelEnableInput
		if err := c.ShouldBindJSON(&in); err != nil {
			c.JSON(400, gin.H{"error": "invalid JSON: " + err.Error()})
			return
		}
		mode := tunnel.Mode(strings.TrimSpace(strings.ToLower(in.Mode)))
		if mode == "" {
			mode = tunnel.ModeQuick
		}
		if !mode.IsValid() {
			c.JSON(400, gin.H{"error": "invalid mode; must be none|quick|named|hybrid"})
			return
		}

		// Merge with existing config so credentials aren't lost.
		cfg, err := mgr.LoadConfig()
		if err != nil {
			c.JSON(500, gin.H{"error": "load config: " + err.Error()})
			return
		}
		cfg.Mode = mode
		if v := strings.TrimSpace(in.CloudflareAPIToken); v != "" {
			cfg.CloudflareAPIToken = v
		}
		if v := strings.TrimSpace(in.CloudflareAccountID); v != "" {
			cfg.CloudflareAccountID = v
		}
		if v := strings.TrimSpace(in.CloudflareZoneID); v != "" {
			cfg.CloudflareZoneID = v
		}
		if v := strings.TrimSpace(in.TunnelDomain); v != "" {
			cfg.TunnelDomain = v
		}

		// Validate for named/hybrid: all four credential fields required.
		if mode == tunnel.ModeNamed || mode == tunnel.ModeHybrid {
			missing := []string{}
			if cfg.CloudflareAPIToken == "" {
				missing = append(missing, "cloudflare_api_token")
			}
			if cfg.CloudflareAccountID == "" {
				missing = append(missing, "cloudflare_account_id")
			}
			if cfg.CloudflareZoneID == "" {
				missing = append(missing, "cloudflare_zone_id")
			}
			if cfg.TunnelDomain == "" {
				missing = append(missing, "tunnel_domain")
			}
			if len(missing) > 0 {
				c.JSON(400, gin.H{"error": "missing required fields for " + string(mode) + " mode", "missing": missing})
				return
			}
		}

		if err := mgr.Enable(cfg); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		st, _ := mgr.Status()
		c.JSON(200, gin.H{"ok": true, "status": st})
	}
}

// HandleTunnelDisable: POST /api/tunnel/disable.
func HandleTunnelDisable(mgr *tunnel.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		if mgr == nil {
			c.JSON(500, gin.H{"error": "tunnel manager not initialised"})
			return
		}
		if err := mgr.Disable(); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		st, _ := mgr.Status()
		c.JSON(200, gin.H{"ok": true, "status": st})
	}
}

// HandleTunnelRestart: POST /api/tunnel/restart.
func HandleTunnelRestart(mgr *tunnel.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		if mgr == nil {
			c.JSON(500, gin.H{"error": "tunnel manager not initialised"})
			return
		}
		if err := mgr.Restart(); err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		st, _ := mgr.Status()
		c.JSON(200, gin.H{"ok": true, "status": st})
	}
}
