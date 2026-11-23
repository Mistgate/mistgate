package countryreach

import (
	"slices"
	"testing"
)

// The order the sources are shown and combined in is a fixed one: the screens, the alert text and Combine.Sources follow it.
func TestCompareSourcesFixedOrder(t *testing.T) {
	want := []string{"globalping", "tspu", "cheburcheck", "cheburcheck_own", "bsbord", "bsbord_tunnel", "bsbord_geo", "lists", "probes"}
	got := slices.Clone(want)
	slices.Reverse(got)
	slices.SortFunc(got, CompareSources)
	if !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
	// a name the panel does not know comes after every known one, by name among its kind
	got = []string{"zeta", "probes", "alpha", "globalping"}
	slices.SortFunc(got, CompareSources)
	if !slices.Equal(got, []string{"globalping", "probes", "alpha", "zeta"}) {
		t.Fatalf("with unknown names = %v", got)
	}
}
