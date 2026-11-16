//go:build !js

package store

import "testing"

func TestOwnProbeStoreSQLite(t *testing.T) { ownProbeStoreProof(t, openTemp(t)) }
