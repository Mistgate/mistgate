//go:build !js

package app

import (
	"slices"
	"testing"
)

// Every hidden path and the host's first label are cut out of the logs: a prefix left out here would print in them.
func TestLogSecretsNameEveryHiddenPrefix(t *testing.T) {
	got := InstanceConfig{AgentSNI: "agent9.example.com", AdminPrefix: "/adm-x", SubPrefix: "/sub-x", LinkPrefix: "/lnk-x", ProbePrefix: "/prb-x"}.logSecrets()
	for _, want := range []string{"agent9.example.com", "agent9", "/adm-x", "/sub-x", "/lnk-x", "/prb-x"} {
		if !slices.Contains(got, want) {
			t.Errorf("logSecrets %v lacks %q", got, want)
		}
	}
}
