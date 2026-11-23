//go:build !js

package app

import (
	"slices"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/countryreach"
)

// "Check now" asks a source through its plan only if the panel wires one: a source the table says is asked by plan and has none
// reads "off" on the page for ever.
func TestCountryNowWiresEverySourceThePlanIsAskedOf(t *testing.T) {
	var wired, planned []string
	for _, src := range countryNowSources(&core{}) {
		wired = append(wired, src.Source)
	}
	for _, src := range countryreach.NowSources() {
		if src.Now == countryreach.NowPlan {
			planned = append(planned, src.ID)
		}
	}
	slices.Sort(wired)
	slices.Sort(planned)
	if !slices.Equal(wired, planned) {
		t.Fatalf("wired %v, the table asks %v", wired, planned)
	}
}
