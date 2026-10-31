//go:build !js

package subs

import (
	"testing"

	"github.com/mistgate/mistgate/internal/panel/protocols/jsoncheck"
)

// The sing-box binaries are the judge of the profile: `sing-box check` on every document with each binary in
// ~/.cache/mistgate-tests/ (1.12.x, the oldest the schema is written for, and 1.14.x, the current one) or SINGBOX_BIN. The test is
// skipped when there is none, like the Mihomo core test without MIHOMO_BIN.
func TestSingboxCoreAcceptsTheDocuments(t *testing.T) {
	if len(jsoncheck.SingboxBins()) == 0 {
		t.Skip("no sing-box binary: set SINGBOX_BIN or put sing-box-<version> into ~/.cache/mistgate-tests/")
	}
	lines := fixtures(t, singboxFragments)
	for _, c := range docPresets {
		t.Run(c.name, func(t *testing.T) {
			b, err := singboxProfile{title: "Example VPN", lines: lines, names: jsonNames, preset: c.preset}.build()
			if err != nil {
				t.Fatal(err)
			}
			jsoncheck.SingboxCheck(t, b)
		})
	}
	for _, c := range adsDocPresets { // the predefined DNS rule and the reject rule of BlockAds (sing-box 1.12+)
		t.Run(c.name, func(t *testing.T) {
			b, err := singboxProfile{title: "Example VPN", lines: lines, names: jsonNames, preset: c.preset}.build()
			if err != nil {
				t.Fatal(err)
			}
			jsoncheck.SingboxCheck(t, b)
		})
	}
	for name, note := range noteSamples(testNow) {
		t.Run("placeholder_"+name, func(t *testing.T) {
			b, err := singboxProfile{title: "Example VPN", note: note}.build()
			if err != nil {
				t.Fatal(err)
			}
			jsoncheck.SingboxCheck(t, b)
		})
	}
	t.Run("hostile names", func(t *testing.T) {
		hostile := make([]string, len(hostileJSONNames))
		for i := range hostile {
			hostile[i] = lines[0]
		}
		b, _ := singboxProfile{title: "Brand, \"Inc\" #1", lines: hostile, names: hostileJSONNames, preset: ruSplit()}.build()
		jsoncheck.SingboxCheck(t, b)
	})
}
