package release

import (
	"strings"
	"testing"
)

func TestSanitizeSourceFailuresDropsOnlyInvalidItems(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"panel:slow,github:http_404", "panel:slow,github:http_404"},
		{"panel:stalled,evil:<x>,github:HTTP", "panel:stalled"},
		{"evil:<x>,github:HTTP", ""},
	} {
		if got := SanitizeSourceFailures(tc.raw); got != tc.want {
			t.Errorf("SanitizeSourceFailures(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestSanitizeSourceFailuresCapsItems(t *testing.T) {
	const item = "panel:slow"
	raw := item
	for i := 1; i < maxSourceFailureItems+3; i++ {
		raw += "," + item
	}
	want := item
	for i := 1; i < maxSourceFailureItems; i++ {
		want += "," + item
	}
	if got := SanitizeSourceFailures(raw); got != want {
		t.Fatalf("SanitizeSourceFailures returned %d items, want %d", len(strings.Split(got, ",")), maxSourceFailureItems)
	}
}
