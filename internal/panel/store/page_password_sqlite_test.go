//go:build !js

package store

import "testing"

func TestPagePasswordSQLite(t *testing.T) { pagePasswordStoreProof(t, openTemp) }
