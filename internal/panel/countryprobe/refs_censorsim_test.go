//go:build censorsim

package countryprobe

import "testing"

func TestCensorSimReferencesStayInDocumentationRanges(t *testing.T) {
	r, ok := ReferencesFor("ru")
	if !ok || r.Control != "198.51.100.10" || len(r.Blocked) != 1 || r.Blocked[0] != "198.51.100.66" || r.ControlSNI != "www.example.com" {
		t.Fatalf("censor simulation references = %+v, %v", r, ok)
	}
}
