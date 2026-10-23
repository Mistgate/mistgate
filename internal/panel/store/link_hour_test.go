//go:build !js

package store

import "testing"

func TestLinkHourSQLite(t *testing.T) {
	linkHourStoreProof(t, openTemp(t))
}
