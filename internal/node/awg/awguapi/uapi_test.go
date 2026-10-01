package awguapi

import (
	"encoding/base64"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/node/awg/awgcfg"
)

func rng(s string) awgcfg.Range { r, _ := awgcfg.ParseRange(s); return r }

func obf31() awgcfg.Obfuscation {
	return awgcfg.Obfuscation{
		Jc: 5, Jmin: 10, Jmax: 50, S1: 24, S2: 24, S3: 24, S4: 24,
		H1: rng("1"), H2: rng("2"), H3: rng("100-200"), H4: rng("4"),
		I1:                     "<b 0xc70000000108><r 8><t><r 40>",
		HeaderProtectionKey:    base64.StdEncoding.EncodeToString(make([]byte, 32)),
		RandomTrailers:         true,
		ContentPaddingAddition: rng("2-10"), RekeyAfterTime: rng("100-120"), RekeyTimeout: rng("3-7"), RejectAfterTime: rng("150-180"),
		KeepaliveTimeout: rng("5-15"), MaxHandshakeAttempts: rng("15-20"),
	}
}

func TestDeviceSet31(t *testing.T) {
	var priv [32]byte
	priv[0], priv[31] = 0xab, 0xcd
	want := "private_key=ab000000000000000000000000000000000000000000000000000000000000cd\n" +
		"listen_port=51842\njc=5\njmin=10\njmax=50\ns1=24\ns2=24\ns3=24\ns4=24\n" +
		"h1=1\nh2=2\nh3=100-200\nh4=4\n" +
		"i1=<b 0xc70000000108><r 8><t><r 40>\n" +
		"header_protection_key=0000000000000000000000000000000000000000000000000000000000000000\n" +
		"content_padding_addition=2-10\nrekey_after_time=100-120\nrekey_timeout=3-7\nreject_after_time=150-180\n" +
		"keepalive_timeout=5-15\nmax_handshake_attempts=15-20\nrandom_trailers=true\ndisable_cookies=false\n"
	if got := DeviceSet(awgcfg.Version31, priv, 51842, obf31()); got != want {
		t.Fatalf("DeviceSet 3.1:\n%s\nwant:\n%s", got, want)
	}
}

func TestDeviceSet20HasNoThreeXKeys(t *testing.T) {
	o := obf31()
	got := DeviceSet(awgcfg.Version20, [32]byte{1}, 443, o)
	for _, k := range []string{"header_protection_key", "random_trailers", "disable_cookies", "content_padding_addition", "rekey_after_time", "keepalive_timeout"} {
		if strings.Contains(got, k+"=") {
			t.Errorf("a 2.0 device set carries %s (amneziawg-go 0.2.x rejects it):\n%s", k, got)
		}
	}
	if !strings.Contains(got, "h3=100-200\n") || !strings.Contains(got, "s4=24\n") {
		t.Errorf("a 2.0 device set must still carry S and H:\n%s", got)
	}
	// "off" is sent explicitly: zero ranges would overlap
	var o2 awgcfg.Obfuscation
	o2.H1, o2.H2, o2.H3, o2.H4 = rng("1"), rng("2"), rng("3"), rng("4")
	if got := DeviceSet(awgcfg.Version20, [32]byte{}, 1, o2); !strings.Contains(got, "h1=1\nh2=2\nh3=3\nh4=4\n") {
		t.Errorf("off headers: %s", got)
	}
}

func TestPeersSet(t *testing.T) {
	var a, b, c [32]byte
	a[0], b[0], c[0] = 1, 2, 3
	psk := [32]byte{9}
	got := PeersSet(false, []awgcfg.Peer{
		{PublicKey: a, Remove: true},
		{PublicKey: b, PSK: &psk, AllowedIPs: []netip.Prefix{netip.MustParsePrefix("10.66.4.5/32")}},
		{PublicKey: c, UpdateOnly: true, ReplaceIPs: true, AllowedIPs: []netip.Prefix{netip.MustParsePrefix("10.66.4.6/32"), netip.MustParsePrefix("fd66:66:0:1::6/128")},
			Endpoint: "203.0.113.10:51820", Keepalive: rng("25")},
	})
	want := "public_key=01" + strings.Repeat("00", 31) + "\nremove=true\n" +
		"public_key=02" + strings.Repeat("00", 31) + "\npreshared_key=09" + strings.Repeat("00", 31) + "\nallowed_ip=10.66.4.5/32\n" +
		"public_key=03" + strings.Repeat("00", 31) + "\nupdate_only=true\nendpoint=203.0.113.10:51820\npersistent_keepalive_interval=25\nreplace_allowed_ips=true\nallowed_ip=10.66.4.6/32\nallowed_ip=fd66:66:0:1::6/128\n"
	if got != want {
		t.Fatalf("PeersSet:\n%s\nwant:\n%s", got, want)
	}
	if got := PeersSet(true, nil); got != "replace_peers=true\n" {
		t.Errorf("replace: %q", got)
	}
}

func TestParseStats(t *testing.T) {
	k1, k2 := strings.Repeat("11", 32), strings.Repeat("22", 32)
	text := "private_key=" + strings.Repeat("99", 32) + "\nlisten_port=51842\njc=5\nrandom_trailers=1\ndisable_cookies=0\n" +
		"public_key=" + k1 + "\npreshared_key=" + strings.Repeat("00", 32) + "\nprotocol_version=1\nendpoint=203.0.113.7:40000\n" +
		"last_handshake_time_sec=1700000000\nlast_handshake_time_nsec=7\ntx_bytes=10\nrx_bytes=20\n" +
		"allowed_ip=10.66.4.5/32\nallowed_ip=fd66:66:0:1::5/128\n" +
		"public_key=" + k2 + "\nprotocol_version=1\nlast_handshake_time_sec=0\nlast_handshake_time_nsec=0\ntx_bytes=0\nrx_bytes=0\nallowed_ip=10.66.4.6/32\n"
	st, err := ParseStats(text)
	if err != nil {
		t.Fatal(err)
	}
	if len(st) != 2 {
		t.Fatalf("peers: %d", len(st))
	}
	p := st[0]
	if p.TxBytes != 10 || p.RxBytes != 20 || !p.LastHS.Equal(time.Unix(1700000000, 7)) || p.Endpoint != netip.MustParseAddrPort("203.0.113.7:40000") ||
		len(p.AllowedIPs) != 2 || p.AllowedIPs[1] != netip.MustParsePrefix("fd66:66:0:1::5/128") {
		t.Errorf("peer 1: %+v", p)
	}
	if !st[1].LastHS.IsZero() || len(st[1].AllowedIPs) != 1 {
		t.Errorf("peer 2: %+v", st[1])
	}
	if _, err := ParseStats("public_key=zz\n"); err == nil {
		t.Error("a malformed public key must be an error")
	}
	if _, err := ParseStats("public_key=" + k1 + "\nallowed_ip=nonsense\n"); err == nil {
		t.Error("a malformed allowed_ip must be an error")
	}
}
