package vless

import (
	"context"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	xtlsreality "github.com/xtls/reality"
	"github.com/xtls/xray-core/app/proxyman"
	xreality "github.com/xtls/xray-core/transport/internet/reality"
)

// keyShareOf is one key_share entry of a hello: the group and the key bytes.
type keyShareOf struct {
	group uint16
	key   []byte
}

const groupKyberDraft = 0x6399 // X25519Kyber768Draft00, what uTLS parrots with _PQ in their name send

var shKyberDraft = share{groupKyberDraft, 1216}

// helloWithShares is buildHello with real key material: X25519 shares carry a valid public key (the server's ECDH rejects the
// all-zero point before the decision is visible), an ML-KEM share carries random bytes followed by one. It returns the shares as
// they sit in the hello.
func helloWithShares(t *testing.T, shares []share) ([]byte, []keyShareOf) {
	t.Helper()
	hello := buildHello(shares, true)
	body := hello[5+4:]
	p := 2 + 32 + 1 + 32 + 2 + 4 + 2 // version, random, session id, cipher suites, compression
	extLen := int(body[p])<<8 | int(body[p+1])
	exts := body[p+2 : p+2+extLen]
	off := 7 + 4 + 2 // supported_versions, key_share header, list length
	var out []keyShareOf
	for _, s := range shares {
		off += 4
		key := exts[off : off+s.n]
		_, _ = rand.Read(key)
		if (s.group == groupX25519 || s.group == groupX25519MLKEM) && s.n >= 32 {
			priv, err := ecdh.X25519().GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			copy(key[s.n-32:], priv.PublicKey().Bytes())
		}
		out = append(out, keyShareOf{s.group, append([]byte(nil), key...)})
		off += s.n
	}
	return hello, out
}

// rule2603 is the key-share rule of xtls/reality 9234c772, the library of Xray 26.3.27 (tls.go lines 214-228 there), copied as it
// was: the first 32-byte X25519 share anywhere, else the X25519 half of the first ML-KEM share of the right size.
func rule2603(shares []keyShareOf) []byte {
	var peerPub []byte
	for _, ks := range shares {
		if ks.group == groupX25519 && len(ks.key) == 32 {
			peerPub = ks.key
			break
		}
	}
	if peerPub == nil {
		for _, ks := range shares {
			if ks.group == groupX25519MLKEM && len(ks.key) == x25519MLKEMKeyLen {
				peerPub = ks.key[len(ks.key)-32:]
				break
			}
		}
	}
	return peerPub
}

// ruleUpstream is the rule of xtls/reality 4bb46b2a (the library of Xray 26.11.9) as upstream has it: nil when it turns the
// hello away.
func ruleUpstream(shares []keyShareOf) []byte {
	var peerPub, peerPub2 []byte
	for _, ks := range shares {
		if ks.group == groupX25519MLKEM && len(ks.key) == x25519MLKEMKeyLen {
			if peerPub2 != nil {
				peerPub2 = nil // ensure fail
				break          // ensure once
			}
			peerPub2 = ks.key[len(ks.key)-32:]
			continue
		}
		if ks.group == groupX25519 && len(ks.key) == 32 {
			if peerPub != nil {
				peerPub2 = nil
				break
			}
			peerPub = ks.key
			break // ensure order
		}
	}
	if peerPub2 == nil {
		return nil
	}
	if peerPub == nil {
		peerPub = peerPub2
	}
	return peerPub
}

// realityTakesHello feeds the hello to reality.Server of the vendored library and reports the first 16 bytes of the auth key
// REALITY derived from it ("" when the hello was turned away before that: the library prints that line when its Show flag is on;
// the hello is not authentic, so it ends in the fallback either way). The target is a socket that never answers.
func realityTakesHello(t *testing.T, hello []byte) string {
	t.Helper()
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	go func() {
		for {
			c, err := target.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(io.Discard, c); _ = c.Close() }()
		}
	}()
	front, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer front.Close()
	cfg := &xtlsreality.Config{
		DialContext: (&net.Dialer{}).DialContext, Show: true, Type: "tcp", Dest: target.Addr().String(),
		ServerNames: map[string]bool{"": true}, PrivateKey: realityTestKey, ShortIds: map[[8]byte]bool{{1}: true},
	}

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldStdout := os.Stdout
	os.Stdout = writer
	captured := make(chan string, 1)
	go func() { b, _ := io.ReadAll(reader); captured <- string(b) }()

	client, err := net.Dial("tcp", front.Addr().String())
	if err != nil {
		os.Stdout = oldStdout
		t.Fatal(err)
	}
	server, err := front.Accept()
	if err != nil {
		os.Stdout = oldStdout
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = xtlsreality.Server(context.Background(), server, cfg)
	}()
	_, _ = client.Write(hello)
	time.Sleep(300 * time.Millisecond) // the decision is made when the first record is read
	_ = client.Close()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Error("reality.Server did not return")
	}
	os.Stdout = oldStdout
	_ = writer.Close()
	for _, line := range strings.Split(<-captured, "\n") {
		if _, rest, ok := strings.Cut(line, "AuthKey[:16]: "); ok {
			key, _, _ := strings.Cut(rest, "\t")
			return key
		}
	}
	return ""
}

var realityTestKey = func() *ecdh.PrivateKey {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	return k
}()

// authKeyPrefix is what REALITY prints of the auth key it derives from the client's share (hkdf over the X25519 secret, salt the
// first 20 bytes of the hello's random, which buildHello leaves zero).
func authKeyPrefix(t *testing.T, peerPub []byte) string {
	t.Helper()
	pub, err := ecdh.X25519().NewPublicKey(peerPub)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := realityTestKey.ECDH(pub)
	if err != nil {
		t.Fatal(err)
	}
	key, err := hkdf.Key(sha256.New, secret, make([]byte, 20), "REALITY", 32)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprint(key[:16])
}

// For every shape of key_share list the vendored library with the compatibility on takes exactly the hellos Xray 26.3.27 took
// and derives the key from the same share; with it turned off it does what upstream 26.11.9 does.
func TestRealityKeyShareRuleAgainst2603(t *testing.T) {
	defer xtlsreality.SetRejectClassical(nil)
	cases := []struct {
		name   string
		shares []share
	}{
		{"X25519", []share{shX25519}},
		{"GREASE, X25519", []share{shGrease, shX25519}},
		{"X25519, P-256", []share{shX25519, shP256}},
		{"GREASE, Kyber draft, X25519", []share{shGrease, shKyberDraft, shX25519}},
		{"PQ, X25519", []share{shMLKEM, shX25519}},
		{"GREASE, PQ, X25519", []share{shGrease, shMLKEM, shX25519}},
		{"GREASE, PQ, X25519, P-256", []share{shGrease, shMLKEM, shX25519, shP256}},
		{"PQ alone", []share{shMLKEM}},
		{"PQ, X25519, X25519", []share{shMLKEM, shX25519, shX25519}},
		{"X25519, PQ", []share{shX25519, shMLKEM}},
		{"X25519, P-256, PQ", []share{shX25519, shP256, shMLKEM}},
		{"X25519, X25519", []share{shX25519, shX25519}},
		{"X25519, X25519, PQ", []share{shX25519, shX25519, shMLKEM}},
		{"PQ, PQ, X25519", []share{shMLKEM, shMLKEM, shX25519}},
		{"PQ, PQ", []share{shMLKEM, shMLKEM}},
		{"PQ of a wrong size, X25519", []share{shMLKEMBad, shX25519}},
		{"X25519 of a wrong size, PQ", []share{shX25519Bd, shMLKEM}},
		{"X25519 of a wrong size", []share{shGrease, shX25519Bd}},
		{"no X25519, no PQ", []share{shGrease, shP256}},
	}
	rows := []string{"hello (key shares)                    26.3.27     with the patch   patch off (= 26.11.9)"}
	for _, c := range cases {
		hello, shares := helloWithShares(t, c.shares)
		want2603, wantUpstream := rule2603(shares), ruleUpstream(shares)
		if wantUpstream != nil && string(wantUpstream) != string(want2603) {
			t.Fatalf("%s: upstream takes a share the 26.3.27 rule does not: the equivalence this patch rests on is broken", c.name)
		}
		show := func(taken bool) string {
			if taken {
				return "takes"
			}
			return "turned away"
		}

		xtlsreality.SetRejectClassical(nil) // the default: on
		onGot := realityTakesHello(t, hello)
		xtlsreality.SetRejectClassical(func(*xtlsreality.Config) bool { return true })
		offGot := realityTakesHello(t, hello)

		onWant, offWant := "", ""
		if want2603 != nil {
			onWant = authKeyPrefix(t, want2603)
		}
		if wantUpstream != nil {
			offWant = authKeyPrefix(t, wantUpstream)
		}
		if onGot != onWant {
			t.Errorf("%s: with the patch the auth key is %q, the 26.3.27 rule gives %q", c.name, onGot, onWant)
		}
		if offGot != offWant {
			t.Errorf("%s: with the patch off the auth key is %q, upstream gives %q", c.name, offGot, offWant)
		}
		rows = append(rows, fmt.Sprintf("%-37s %-11s %-16s %s", c.name, show(want2603 != nil), show(onGot != ""), show(offGot != "")))
	}
	t.Log("\n" + strings.Join(rows, "\n"))
}

// A policy that says "strict" for one inbound and "compatible" for another is asked per hello with that inbound's config.
func TestRealityRejectClassicalIsPerConfig(t *testing.T) {
	defer xtlsreality.SetRejectClassical(nil)
	hello, shares := helloWithShares(t, []share{shGrease, shX25519})
	want := authKeyPrefix(t, rule2603(shares))
	xtlsreality.SetRejectClassical(func(c *xtlsreality.Config) bool { return c.Dest == "" })
	if got := realityTakesHello(t, hello); got != want { // the test's config has a Dest: not strict
		t.Fatalf("a config the policy does not call strict turned a classical hello away: %q", got)
	}
	xtlsreality.SetRejectClassical(func(c *xtlsreality.Config) bool { return c.Dest != "" })
	if got := realityTakesHello(t, hello); got != "" {
		t.Fatalf("a config the policy calls strict took a classical hello: %q", got)
	}
}

// The node starts with the compatibility on without anyone setting it: a classical hello is taken by the library as it is.
func TestClassicalHelloIsTakenByDefault(t *testing.T) {
	xtlsreality.SetRejectClassical(nil)
	hello, shares := helloWithShares(t, []share{shGrease, shX25519})
	if got, want := realityTakesHello(t, hello), authKeyPrefix(t, rule2603(shares)); got != want {
		t.Fatalf("a classical hello is not taken by default: %q, want %q", got, want)
	}
}

// minClientVer stays empty: REALITY would otherwise turn away every client that reports an older version (sing-box claims
// 1.8.1, Mihomo 1.8.2). Upstream sets it only in its JSON builder, which this node does not use.
func TestRealityConfigLeavesClientVersionsOpen(t *testing.T) {
	spec := settingsSpec("tcp", "reality")
	cfg, err := parseSpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	coreConfig := buildCoreConfig(spec, cfg, nil, nil, "")
	message, err := coreConfig.Inbound[0].ReceiverSettings.GetInstance()
	if err != nil {
		t.Fatal(err)
	}
	stream := message.(*proxyman.ReceiverConfig).StreamSettings
	if len(stream.SecuritySettings) != 1 {
		t.Fatalf("security settings = %d", len(stream.SecuritySettings))
	}
	inst, err := stream.SecuritySettings[0].GetInstance()
	if err != nil {
		t.Fatal(err)
	}
	rc := inst.(*xreality.Config)
	if len(rc.MinClientVer) != 0 || len(rc.MaxClientVer) != 0 || rc.MaxTimeDiff != 0 {
		t.Fatalf("reality config limits clients: min %x max %x diff %d", rc.MinClientVer, rc.MaxClientVer, rc.MaxTimeDiff)
	}
}
