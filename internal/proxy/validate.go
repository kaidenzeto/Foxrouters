package proxy

import (
	"fmt"
	"regexp"
	"strings"
)

// validNameRegex allows alphanumeric, dot, underscore, slash, hyphen. Max 64 chars.
// Blocks: <, >, ", ', \, control chars, whitespace, ../, etc.
var validNameRegex = regexp.MustCompile(`^[A-Za-z0-9._/\-]{1,64}$`)

// validateName validates a custom model id, alias, or combo name.
// Returns error if the name contains invalid characters or is too long.
// This prevents XSS payloads (e.g. <script>), Redis key injection
// (e.g. \r\n SET evil), path traversal (../), and Redis key pollution.
func validateName(name, field string) error {
	if name == "" {
		return fmt.Errorf("%s required", field)
	}
	if len(name) > 64 {
		return fmt.Errorf("%s too long (max 64 chars)", field)
	}
	if strings.Contains(name, "..") {
		return fmt.Errorf("%s must not contain '..'", field)
	}
	if !validNameRegex.MatchString(name) {
		return fmt.Errorf("%s contains invalid characters (allowed: A-Z, a-z, 0-9, . _ / -)", field)
	}
	return nil
}
