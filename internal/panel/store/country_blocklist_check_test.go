//go:build !js

package store

import "testing"

func TestBlocklistChecksSQLite(t *testing.T) {
	blocklistCheckStoreProof(t, openTemp(t))
}
