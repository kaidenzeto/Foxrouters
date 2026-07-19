// db_adapter.go — thin bridge from the old package-main names to
// internal/db.  Domain files that still refer to *DBStore / RequestLog /
// RefreshLog / AccountEvent / RequestStats / etc. resolve those names
// through the aliases below.
//
// Persistence-layer DTO conversion helpers moved into their owning packages:
//   - GatewayKeyInfo conversion → internal/auth
//   - GrokAccount / CBKey conversion → internal/upstream
package main

import (
	"foxrouters/internal/db"
)

// Type aliases keep existing call sites compiling.
type DBStore = db.Store
type RequestLog = db.RequestLog
type RefreshLog = db.RefreshLog
type AccountEvent = db.AccountEvent
type RequestStats = db.RequestStats
type ModelStats = db.ModelStats
type RecentRequest = db.RecentRequest
type RequestDetail = db.RequestDetail
type CustomModel = db.CustomModel

// Re-exported constants some domain files still reference by short name.
const (
	RK_GROK_ACCOUNT   = db.RK_GROK_ACCOUNT
	RK_CB_KEY         = db.RK_CB_KEY
	RK_GATEWAY_KEY    = db.RK_GATEWAY_KEY
	RK_RATE_LIMIT     = db.RK_RATE_LIMIT
	RK_CUSTOM_MODELS  = db.RK_CUSTOM_MODELS
	RK_CUSTOM_ALIASES = db.RK_CUSTOM_ALIASES
)

// NewDBStore is a thin wrapper preserving the old constructor name.
func NewDBStore() (*DBStore, error) { return db.NewStore() }
