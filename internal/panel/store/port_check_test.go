//go:build !js

package store

import "testing"

func TestPortChecksSQLite(t *testing.T) {
	portCheckStoreProof(t, openTemp(t))
}

func TestUDPPathLossSQLite(t *testing.T) {
	udpPathLossStoreProof(t, openTemp(t))
}
