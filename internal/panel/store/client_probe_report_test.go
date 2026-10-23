//go:build !js

package store

import "testing"

func TestClientProbeReportsSQLite(t *testing.T) {
	clientProbeStoreProof(t, openTemp(t))
}
