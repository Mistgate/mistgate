//go:build !js

package store

import "testing"

func TestPortChecksSQLite(t *testing.T) {
	portCheckStoreProof(t, openTemp(t))
}
