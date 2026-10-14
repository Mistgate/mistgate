//go:build !js

package subs

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xtls/xray-core/infra/conf"

	"github.com/mistgate/mistgate/internal/panel/protocols/jsoncheck"
)

// Xray's own config builder is the judge of the Xray JSON array, in process (no binary): every element of every document, under
// every preset, has to unmarshal into xray-core's conf.Config, with no key the core does not know, and build.
func TestXrayCoreBuildsTheDocuments(t *testing.T) {
	lines := fixtures(t, xrayFragments)
	for _, c := range docPresets {
		t.Run(c.name, func(t *testing.T) {
			b, err := xrayProfile{lines: lines, names: jsonNames, preset: c.preset}.build()
			if err != nil {
				t.Fatal(err)
			}
			if got := jsoncheck.XrayBuildArray(t, b); len(got) != len(lines) {
				t.Errorf("%d configs", len(got))
			}
		})
	}
	for name, note := range noteSamples(testNow) {
		t.Run("placeholder_"+name, func(t *testing.T) {
			b, err := xrayProfile{note: note}.build()
			if err != nil {
				t.Fatal(err)
			}
			jsoncheck.XrayBuildArray(t, b)
		})
	}
	t.Run("hostile names", func(t *testing.T) {
		hostile := make([]string, len(hostileJSONNames))
		for i := range hostile {
			hostile[i] = lines[0]
		}
		b, _ := xrayProfile{lines: hostile, names: hostileJSONNames, preset: ruSplit()}.build()
		jsoncheck.XrayBuildArray(t, b)
	})

	// The strict decoding is a real one: the core's own types refuse a key they do not know.
	t.Run("an unknown key is refused", func(t *testing.T) {
		var c conf.Config
		dec := json.NewDecoder(strings.NewReader(`{"inbounds":[],"outbounds":[],"routing":{},"rules":{}}`))
		dec.DisallowUnknownFields()
		if dec.Decode(&c) == nil {
			t.Error("conf.Config took an unknown key")
		}
	})
}
