//go:build !js

package store

import "testing"

func TestRelaysSQLite(t *testing.T) {
	relayStoreProof(t, openTemp(t))
}

func TestRelayCountersSQLite(t *testing.T) {
	relayCounterStoreProof(t, openTemp(t))
}

func TestRelayRaceSQLite(t *testing.T) {
	relayRaceProof(t, openTemp(t))
}

func TestRelayAudienceSQLite(t *testing.T) {
	relayAudienceProof(t, openTemp(t))
}
