//go:build censorsim && !js

package app

import "time"

// These endpoints exist only in scripts/e2e/censor.sh's private namespaces. The tagged lab panel cannot call a public
// Globalping or Bsbord service; the local adapters perform their requested TCP measurements from lab vantage namespaces.
func censorSimGlobalping() (string, time.Duration) {
	return "http://198.51.100.100:9001", 5 * time.Millisecond
}
func censorSimBsbord() string { return "http://198.51.100.100:9002" }
