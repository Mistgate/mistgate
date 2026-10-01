package awg

import (
	"encoding/base64"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/plugin"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

func key32(seed byte) string {
	b := make([]byte, 32)
	for i := range b {
		b[i] = seed + byte(i)
	}
	return base64.StdEncoding.EncodeToString(b)
}

const fixtureI1 = "<r 2><b 0x858000010001000000000669636c6f756403636f6d0000010001c00c000100010000105a00044d583737>"

// fixture31 is a complete 3.1 profile with fixed values (no randomness) for the goldens.
func fixture31() Settings {
	return Settings{
		Version: Version31, Port: 51842, MTU: 1280, Egress: "direct",
		Subnet4: "10.66.4.0/22", Subnet6: "fd66:66:0:1::/64",
		Obfuscation: Obfuscation{
			Preset: "webrtc", Jc: 6, Jmin: 10, Jmax: 50, S1: 24, S2: 24, S3: 24, S4: 24,
			H1: "1", H2: "2", H3: "3", H4: "4", I1: fixtureI1,
			HeaderProtectionKey: key32(0x40), RandomTrailers: true,
			ContentPaddingAddition: "2-10", RekeyAfterTime: "100-120", RekeyTimeout: "3-7",
			RejectAfterTime: "150-180", KeepaliveTimeout: "5-15", MaxHandshakeAttempts: "15-20",
			PersistentKeepalive: "25-35", // Amnezia's range: the goldens, and the subscription goldens that read them, keep it
		},
	}
}

// clean31 is fixture31 without anything to remark on (a keepalive under the NAT timeout, a preset with no port).
func clean31() Settings {
	s := fixture31()
	s.Obfuscation.PersistentKeepalive = "22-30"
	return s
}

// fixture20 is the 2.0 counterpart: padding and header ranges, none of the 3.x keys.
func fixture20() Settings {
	return Settings{
		Version: Version20, Port: 43210, MTU: 1280, Egress: "direct",
		Subnet4: "10.66.8.0/22", Subnet6: "",
		Obfuscation: Obfuscation{
			Preset: "dns", Jc: 5, Jmin: 12, Jmax: 60, S1: 40, S2: 60, S3: 25, S4: 18,
			H1: "100000-200000", H2: "300000-400000", H3: "500000-600000", H4: "700000-800000",
			I1: fixtureI1, PersistentKeepalive: "25",
		},
	}
}

func raw(t testing.TB, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// renderInput builds what the subscription assembler passes for device idx of the fixture profile.
func renderInput(t testing.TB, s Settings, f plugin.ClientFormat) protocols.RenderInput {
	t.Helper()
	v4, v6, err := peerAddrs(s, 5)
	if err != nil {
		t.Fatal(err)
	}
	sec := peerSecret{Priv: key32(0x01), PSK: key32(0x80), IP4: v4.String()}
	if v6.IsValid() {
		sec.IP6 = v6.String()
	}
	return protocols.RenderInput{
		Format:        f,
		Settings:      raw(t, s),
		Inbound:       protocols.InboundView{ID: "inb_1", Node: protocols.NodeView{ID: "nod_1", Name: "de1", Address: "203.0.113.10", CountryCode: "DE"}, Port: uint16(s.Port)},
		UserID:        "usr_1",
		UserName:      "alice",
		DeviceID:      "dev_1",
		Label:         "laptop",
		DisplayName:   "de1 · AWG " + s.Version,
		Peer:          raw(t, sec),
		InboundPublic: json.RawMessage(`{"public_key":"` + key32(0xc0) + `"}`),
		DNS:           []string{"1.1.1.1", "8.8.8.8"},
		NodeAddr:      "203.0.113.10",
	}
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -update)", err)
	}
	if string(want) != string(got) {
		t.Errorf("%s differs from the golden file\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}
