//go:build !js

package store

import "testing"

func TestCountryChecksSQLite(t *testing.T) {
	countryCheckStoreProof(t, openTemp(t))
}
