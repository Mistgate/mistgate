//go:build !js

package subs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xtls/xray-core/infra/conf"

	"github.com/mistgate/mistgate/internal/panel/protocols/jsoncheck"
)

// geoAssets points xray-core at the folder that holds geosite.dat and geoip.dat (XRAY_LOCATION_ASSET, else
// ~/.cache/mistgate-tests/geo) and skips the test when there is none: a routing rule or a dns server that names geosite:cn is
// resolved against the file while the config is built, as the client does at start. Nothing is downloaded by the tests.
func geoAssets(t *testing.T) {
	t.Helper()
	dir := os.Getenv("XRAY_LOCATION_ASSET")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".cache", "mistgate-tests", "geo")
	}
	for _, f := range []string{"geosite.dat", "geoip.dat"} {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			t.Skipf("no %s in %s: put geosite.dat (https://github.com/Loyalsoldier/v2ray-rules-dat/releases/latest/download/geosite.dat) and geoip.dat (.../geoip.dat) there, or set XRAY_LOCATION_ASSET", f, dir)
		}
	}
	t.Setenv("XRAY_LOCATION_ASSET", dir)
}

// Xray's own config builder is the judge of the Xray JSON array, in process (no binary): every element of every document, under
// every preset, has to unmarshal into xray-core's conf.Config, with no key the core does not know, and build.
func TestXrayCoreBuildsTheDocuments(t *testing.T) {
	lines := fixtures(t, xrayFragments)
	for _, c := range docPresets {
		t.Run(c.name, func(t *testing.T) {
			if c.name == "cn_split" {
				geoAssets(t) // geosite:cn and geoip:cn are looked up in the data files while the config is built
			}
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
