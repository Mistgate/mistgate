package vless

import "testing"

// The subscription host check reads REALITY targets in both forms of the settings: the profile document and the spec
// built for the node, where the node's own site is the target "self".
func TestForeignRealitySite(t *testing.T) {
	for name, tc := range map[string]struct {
		settings string
		port     int
		wantPort int
		foreign  bool
	}{
		"profile, own site":          {`{"security":"reality","port":443,"reality":{"target_kind":"self"}}`, 0, 443, false},
		"profile, another site":      {`{"security":"reality","port":443,"reality":{"target_kind":"site","target":"www.example.org:443"}}`, 0, 443, true},
		"profile before target_kind": {`{"security":"reality","port":443,"reality":{"target":"www.example.org:443"}}`, 0, 443, true},
		"node spec, own site":        {`{"security":"reality","reality":{"target":"self"}}`, 443, 443, false},
		"node spec, another site":    {`{"security":"reality","reality":{"target":"www.example.org:443"}}`, 443, 443, true},
		"port override wins":         {`{"security":"reality","port":443,"reality":{"target":"www.example.org:443"}}`, 8443, 8443, true},
		"not reality":                {`{"security":"tls","port":443}`, 0, 0, false},
	} {
		port, foreign := ForeignRealitySite([]byte(tc.settings), tc.port)
		if port != tc.wantPort || foreign != tc.foreign {
			t.Errorf("%s: port %d foreign %v, want %d %v", name, port, foreign, tc.wantPort, tc.foreign)
		}
	}
}
