// proxy_adapter.go — thin bridge from package-main call sites to internal/proxy.
// main.go still writes `proxyRequest(...)` as a route handler; that name now
// forwards into the extracted internal/proxy package.
package main

import (
	"foxrouters/internal/proxy"
)

// proxyRequest preserves the lowercase name used by main.go's routes wiring.
var proxyRequest = proxy.ProxyRequest
