//go:build !js

package store

import "testing"

func TestNodeRuntimeSQLite(t *testing.T) {
	nodeRuntimeStoreProof(t, openTemp(t))
}
