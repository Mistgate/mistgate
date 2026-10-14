//go:build !js

package store

import "testing"

func TestTSPUChecksSQLite(t *testing.T) {
	tspuCheckStoreProof(t, openTemp(t))
}
