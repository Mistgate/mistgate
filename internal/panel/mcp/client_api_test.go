//go:build !js

package mcp

import (
	"strings"
	"testing"
)

func TestClientAPISettingsReadView(t *testing.T) {
	e := newTestEnv(t)
	e.w.clientAPIEnabled = true
	_, secret := e.token(ProfileReadonly)
	s := e.session(secret)
	if out := mustOK(t, s, "client_api_settings", map[string]any{}); !strings.Contains(out, `"enabled":true`) {
		t.Fatalf("client_api_settings = %s", out)
	}
}
