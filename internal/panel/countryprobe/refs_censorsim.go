//go:build censorsim

package countryprobe

import "strings"

// censorSimReferences replaces the production references only in the explicitly tagged lab binary. This keeps the real
// TestTspuProbe API, node command, and result storage path while ensuring every probe target stays inside the namespace lab.
func censorSimReferences(country string) (References, bool) {
	if !strings.EqualFold(country, "RU") {
		return References{}, false
	}
	return References{
		Control: "198.51.100.10",
		Blocked: []string{"198.51.100.66"},
	}, true
}
