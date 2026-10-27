//go:build !js

package store

import "testing"

func TestExtChecksSQLite(t *testing.T) {
	extCheckStoreProof(t, openTemp(t))
}
