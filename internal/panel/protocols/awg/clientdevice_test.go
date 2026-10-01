package awg

import (
	"encoding/base64"
	"encoding/hex"
	"net/netip"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/plugin"
)

// kv reads "k=v" lines (UAPI) or "K = V" lines (.conf).
func kv(text, sep string) map[string]string {
	m := map[string]string{}
	for _, l := range strings.Split(text, "\n") {
		if k, v, ok := strings.Cut(l, sep); ok {
			m[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return m
}

func hexOfB64(t *testing.T, s string) string {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// The client device and the .conf of the same device are written from one clientConfig: every value the amneziawg-go
// client takes is the one the .conf carries, for both protocol versions.
func TestClientDeviceIsTheConfOfTheSameDevice(t *testing.T) {
	for name, s := range map[string]Settings{"3.1": fixture31(), "2.0": fixture20()} {
		t.Run(name, func(t *testing.T) {
			conf := kv(string(renderFor(t, s, plugin.FormatAWGConf, "dev_1").Data), " = ")
			in := renderInput(t, s, plugin.FormatAWGConf)
			ct, err := ClientDevice(in)
			if err != nil {
				t.Fatal(err)
			}
			ipc := kv(ct.IPC, "=")

			same := func(c, u string) {
				t.Helper()
				if want, ok := conf[c]; ok && ipc[u] != want {
					t.Errorf("%s: .conf has %q, the client device has %q", c, want, ipc[u])
				} else if !ok && ipc[u] != "" {
					t.Errorf("%s is not in the .conf but the client device has %s=%q", c, u, ipc[u])
				}
			}
			for _, k := range []string{"Jc", "Jmin", "Jmax", "S1", "S2", "S3", "S4", "H1", "H2", "H3", "H4", "I1", "I2", "I3", "I4", "I5"} {
				same(k, strings.ToLower(k))
			}
			if ipc["private_key"] != hexOfB64(t, conf["PrivateKey"]) || ipc["preshared_key"] != hexOfB64(t, conf["PresharedKey"]) ||
				ipc["public_key"] != hexOfB64(t, conf["PublicKey"]) {
				t.Errorf("keys: %v", ipc)
			}
			if ipc["endpoint"] != conf["Endpoint"] || ipc["persistent_keepalive_interval"] != conf["PersistentKeepalive"] {
				t.Errorf("endpoint %q / keepalive %q, the .conf has %q / %q", ipc["endpoint"], ipc["persistent_keepalive_interval"], conf["Endpoint"], conf["PersistentKeepalive"])
			}
			if ipc["listen_port"] != "0" || ipc["allowed_ip"] == "" {
				t.Errorf("a client takes any port and routes everything: %v", ipc)
			}
			if ct.MTU != s.MTU || ct.Server != mustKey(t, key32(0xc0)) {
				t.Errorf("mtu %d, server %x", ct.MTU, ct.Server)
			}
			if !strings.Contains(ct.IPC, "allowed_ip=0.0.0.0/0\n") || !strings.Contains(ct.IPC, "allowed_ip=::/0\n") {
				t.Errorf("a device routes everything (v4 and v6): %s", ct.IPC)
			}

			if s.Version == Version31 {
				for c, u := range map[string]string{"ContentPaddingAddition": "content_padding_addition", "RekeyAfterTime": "rekey_after_time",
					"RekeyTimeout": "rekey_timeout", "RejectAfterTime": "reject_after_time", "KeepaliveTimeout": "keepalive_timeout",
					"MaxHandshakeAttempts": "max_handshake_attempts"} {
					same(c, u)
				}
				if ipc["header_protection_key"] != hexOfB64(t, conf["HeaderProtectionKey"]) || ipc["random_trailers"] != "true" || ipc["disable_cookies"] != "true" {
					t.Errorf("3.1 keys: %v", ipc)
				}
			} else {
				for _, u := range []string{"header_protection_key", "random_trailers", "disable_cookies", "rekey_timeout", "content_padding_addition"} {
					if _, ok := ipc[u]; ok {
						t.Errorf("a 2.0 client must not carry %s (amneziawg-go 0.2 rejects it)", u)
					}
				}
			}

			want := []netip.Addr{netip.MustParseAddr("10.66.4.5")}
			if s.Subnet6 != "" {
				want = append(want, netip.MustParseAddr("fd66:66:0:1::5"))
			} else {
				want[0] = netip.MustParseAddr("10.66.8.5")
			}
			if len(ct.Addrs) != len(want) || ct.Addrs[0] != want[0] || (len(want) > 1 && ct.Addrs[1] != want[1]) {
				t.Errorf("addresses %v, want %v", ct.Addrs, want)
			}
			if len(ct.DNS) != 2 || ct.DNS[0].String() != "1.1.1.1" || ct.DNS[1].String() != "8.8.8.8" {
				t.Errorf("dns %v", ct.DNS)
			}
		})
	}
}

func mustKey(t *testing.T, b64 string) (k [32]byte) {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(b) != 32 {
		t.Fatal("bad key")
	}
	copy(k[:], b)
	return k
}

// The probe is a device: with per-device signatures the chain is the one of its own id, the same on every dial, and
// not the one of another device.
func TestClientDeviceCarriesThePerDeviceSignature(t *testing.T) {
	s := perDevice31("dns")
	chain := func(dev string) [5]string {
		in := renderInput(t, s, plugin.FormatAWGConf)
		in.DeviceID = dev
		ct, err := ClientDevice(in)
		if err != nil {
			t.Fatal(err)
		}
		m := kv(ct.IPC, "=")
		return [5]string{m["i1"], m["i2"], m["i3"], m["i4"], m["i5"]}
	}
	a, b := chain("crd_probeA"), chain("crd_probeB")
	if want, _ := deviceSignature(s.Obfuscation, "crd_probeA"); a != want || a[0] == "" || a[0] == fixtureI1 {
		t.Errorf("chain of A = %v, want %v", a, want)
	}
	if a == b || a != chain("crd_probeA") {
		t.Error("the chain must depend on the device id and on nothing else")
	}
	// the template when the profile has no per-device signatures
	in := renderInput(t, fixture31(), plugin.FormatAWGConf)
	ct, _ := ClientDevice(in)
	if kv(ct.IPC, "=")["i1"] != fixtureI1 {
		t.Error("the profile's own I1 is not used")
	}
}

func TestClientDeviceRefusesWhatItCannotConnect(t *testing.T) {
	ok := func(mut func(in *protocols.RenderInput)) error {
		in := renderInput(t, fixture31(), plugin.FormatAWGConf)
		mut(&in)
		_, err := ClientDevice(in)
		return err
	}
	if err := ok(func(*protocols.RenderInput) {}); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	for name, mut := range map[string]func(in *protocols.RenderInput){
		"a masked preview":          func(in *protocols.RenderInput) { in.MaskSecrets = true },
		"a host name for the node":  func(in *protocols.RenderInput) { in.NodeAddr = "node.example.com" },
		"no server key":             func(in *protocols.RenderInput) { in.InboundPublic = nil },
		"a damaged peer":            func(in *protocols.RenderInput) { in.Peer = []byte(`{"priv":"x"}`) },
		"a profile with no network": func(in *protocols.RenderInput) { in.Settings = []byte(`{}`) },
	} {
		if err := ok(mut); err == nil {
			t.Errorf("%s: a client device was made", name)
		}
	}
}
