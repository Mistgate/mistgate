//go:build !js

package store

import "testing"

func TestIPv6EntrySQLite(t *testing.T) { ipv6EntryProof(t, openTemp(t)) }
