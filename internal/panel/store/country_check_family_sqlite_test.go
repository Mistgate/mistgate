//go:build !js

package store

import (
	"testing"
)

func TestCountryCheckFamiliesSQLite(t *testing.T) {
	countryCheckFamilyProof(t, openTemp(t))
}
