package subs

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/access"
)

// A key's config goes out with its code, and one too long for a QR code goes out without (the page says so and leaves the
// file and the key).
func TestConfigsCarryTheirCode(t *testing.T) {
	short := "[Interface]\nPrivateKey = AAAA\nAddress = 10.8.0.2/32\n"
	out := configsOf([]access.DeviceConfig{{Conf: short}, {Conf: strings.Repeat("x", 4000)}, {Conf: ""}})
	if out[0].QR == nil || out[0].QR.N < 21 || out[0].QR.Window <= 0 {
		t.Fatalf("no code for a short config: %+v", out[0].QR)
	}
	if out[1].QR != nil || out[2].QR != nil {
		t.Errorf("a code for a config too long for one, or for an empty one: %+v %+v", out[1].QR, out[2].QR)
	}
	b, err := json.Marshal(out[0])
	if err != nil || !strings.Contains(string(b), `"qr":{"n":`) {
		t.Errorf("json: %s %v", b, err)
	}
	b, _ = json.Marshal(out[1])
	if !strings.Contains(string(b), `"qr":null`) {
		t.Errorf("a missing code is null in the json: %s", b)
	}
}
