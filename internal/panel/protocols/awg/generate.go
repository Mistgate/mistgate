package awg

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	mrand "math/rand/v2"
	"net/netip"
	"slices"
	"strconv"

	"github.com/mistgate/mistgate/internal/panel/protocols/awg/mimicry"
)

// Every default is drawn per profile: defaults that are the same for everybody become a fingerprint of their
// own. The structure follows the Amnezia 5.0.x installer, the values are random.

// defaultPreset is the mimicry of a new profile. DNS is a preset whose packets differ for every device and every
// profile (STUN, WebRTC and DTLS are the same bytes for everybody, see PresetVaries) and stay short: the A/AAAA/HTTPS
// triplet is 225-351 characters, so the device .conf fits a QR code (qrMaxBytes in web/src/sub/qr.ts is 2331 at ECC M).
// QUIC and curl_quic are real Initials of about 2406 characters in I1 and a .conf of 3.3 KB: they stay selectable
// but are not the default, a phone could not scan them. DNS wants port 53, so a new profile starts with the
// preset_port remark. One word to change.
const defaultPreset = mimicry.DNS

// PresetVaries: the chain of the preset depends on the rng, so per-device signatures give every device another
// one. The other presets are the same I1..I5 whatever the seed (all their randomness is in tags): per-device
// signatures do nothing for them, and the score does not count them. awg_test.go checks this list against the
// generator.
func PresetVaries(preset string) bool {
	switch preset {
	case mimicry.STUN, mimicry.WebRTC, mimicry.DTLS, mimicry.Custom:
		return false
	}
	return true
}

// GenerateObfuscation returns a complete, valid `obfuscation` object for a version and a mimicry preset: what
// the editor's "Generate" button fills in. mtu bounds Jmax (the junk must stay below the MTU) and S4 (the data
// packet must fit 1500 with its headers, see maxS4). domain is the host name of the UsesDomain presets, "" draws
// one from the pool. The header protection key and the signature seed always come from crypto/rand.
func GenerateObfuscation(version, preset string, mtu int, domain string) (Obfuscation, error) {
	rng, err := newRand()
	if err != nil {
		return Obfuscation{}, err
	}
	return generate(version, preset, mtu, domain, rng, rand.Reader)
}

// GenerateObfuscationSeeded is GenerateObfuscation with a fixed seed, for tests and goldens. NEVER for a real
// profile: its header protection key comes from the seed, not from crypto/rand.
func GenerateObfuscationSeeded(version, preset string, mtu int, seed uint64, domain string) (Obfuscation, error) {
	rng := mrand.New(mrand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	return generate(version, preset, mtu, domain, rng, rngReader{rng})
}

// GenerateSignature is the "pick a mimicry card" call: a fresh Preset, Domain and I1..I5 and nothing else, so
// nothing the peers must agree on changes and no issued config goes stale. The preset must have a generator.
func GenerateSignature(preset, domain string) (Obfuscation, error) {
	if preset == mimicry.Custom {
		return Obfuscation{}, fmt.Errorf("the custom preset has no generator: write I1-I5 by hand")
	}
	rng, err := newRand()
	if err != nil {
		return Obfuscation{}, err
	}
	return signature(preset, domain, rng)
}

func signature(preset, domain string, rng *mrand.Rand) (o Obfuscation, err error) {
	if !mimicry.Valid(preset) {
		return o, fmt.Errorf("unknown mimicry preset %q", preset)
	}
	if domain != "" {
		if domain, err = mimicry.NormalizeDomain(domain); err != nil {
			return o, err
		}
	}
	chain, err := mimicry.GenerateChain(preset, mimicry.Options{Domain: domain}, rng)
	if err != nil {
		return o, err
	}
	o.Preset, o.Domain = preset, domain
	o.I1, o.I2, o.I3, o.I4, o.I5 = chain[0], chain[1], chain[2], chain[3], chain[4]
	return o, nil
}

// deviceSignature is the I1..I5 one device's client config carries when the profile has per-device signatures:
// the preset's chain drawn from an rng seeded by sha256(signature_seed, device id). Same device, same seed: same
// chain at every render (the config the user imported matches the page that shows it again); another device, a
// different chain. false: the profile has none of it (or the preset's chain does not vary, see PresetVaries: custom,
// which is hand-written, and the looks that are the same bytes for everybody), use the profile's own I1..I5.
func deviceSignature(o Obfuscation, deviceID string) ([5]string, bool) {
	if deviceID == "" || !perDeviceInEffect(o) {
		return [5]string{}, false
	}
	h := sha256.New()
	h.Write([]byte(o.SignatureSeed))
	h.Write([]byte{0}) // "ab"+"c" and "a"+"bc" are not the same pair
	h.Write([]byte(deviceID))
	chain, err := mimicry.GenerateChain(o.Preset, mimicry.Options{Domain: o.Domain}, pcgFrom(h.Sum(nil)))
	return chain, err == nil
}

func newRand() (*mrand.Rand, error) {
	var seed [16]byte
	if _, err := rand.Read(seed[:]); err != nil {
		return nil, err
	}
	return pcgFrom(seed[:]), nil
}

func pcgFrom(b []byte) *mrand.Rand {
	return mrand.New(mrand.NewPCG(binary.LittleEndian.Uint64(b[:8]), binary.LittleEndian.Uint64(b[8:16])))
}

type rngReader struct{ r *mrand.Rand }

func (r rngReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(r.r.Uint32())
	}
	return len(p), nil
}

// maxS4 is how much padding a data packet may carry at this MTU: MTU + WG header and tag + S4 + UDP + IPv6 must
// fit 1500. ContentPaddingAddition and random trailers are not added: both engines cut them to the UDP window,
// the largest data packet seen so far (amneziawg-go send.go randomPaddingAddition, module peer.h
// wg_peer_skb_randomize_padding_addition), so they never take a packet above MTU + 32 + S4.
func maxS4(mtu int) int { return max(0, pathMTU-wireOverhead-mtu) }

// suggestedMTU is the largest MTU that leaves room for s4 (maxS4 inverted), kept inside the legal range.
func suggestedMTU(s4 int) int { return min(max(pathMTU-wireOverhead-s4, minMTU), maxMTU) }

func generate(version, preset string, mtu int, domain string, rng *mrand.Rand, keys io.Reader) (Obfuscation, error) {
	if version != Version31 && version != Version20 {
		return Obfuscation{}, fmt.Errorf("unknown AmneziaWG version %q", version)
	}
	if mtu < minMTU || mtu > maxMTU {
		return Obfuscation{}, fmt.Errorf("MTU must be %d-%d", minMTU, maxMTU)
	}
	between := func(lo, hi int) int { return lo + rng.IntN(hi-lo+1) }
	span := func(lo, hi int) string { return strconv.Itoa(lo) + "-" + strconv.Itoa(hi) }

	jc := between(4, 7)
	jmin := between(10, 40)
	jmax := min(jmin+between(20, 80), 1200, mtu-1)
	if jmax < jmin { // a tiny MTU: keep the pair legal
		jmin = jmax
	}

	sig, err := signature(preset, domain, rng)
	if err != nil {
		return Obfuscation{}, err
	}
	o := sig
	o.Jc, o.Jmin, o.Jmax = jc, jmin, jmax

	var seed [16]byte
	if _, err := io.ReadFull(keys, seed[:]); err != nil {
		return Obfuscation{}, err
	}
	o.SignatureSeed = hex.EncodeToString(seed[:])

	if version == Version31 {
		// equal S: the advice for random trailers; sizes 148+S, 92+S, 64+S, 32+S differ anyway. S4 stays within the
		// MTU's headroom when the key allows it (it needs at least 12): above that the validator says mtu_headroom.
		s := between(12, max(12, min(32, maxS4(mtu))))
		o.S1, o.S2, o.S3, o.S4 = s, s, s, s
		o.H1, o.H2, o.H3, o.H4 = "1", "2", "3", "4"
		var key [hpkBytes]byte
		if _, err := io.ReadFull(keys, key[:]); err != nil {
			return Obfuscation{}, err
		}
		o.HeaderProtectionKey = base64.StdEncoding.EncodeToString(key[:])
		o.RandomTrailers = true
		// Ranges around the WireGuard constants (rekey 120 s, reject 180 s, keepalive 10 s, retry 5 s) and not the
		// constants themselves: a value every profile shares is a signature (awg-multi-script awg2.sh does the same).
		// The retry timeout stays below 10 s and the attempts at 20: 9 s x 20 is the three minutes a handshake may
		// take to give up. RekeyAfterTime ends at 160, RejectAfterTime starts at 175: rekey always comes first.
		o.ContentPaddingAddition = span(between(8, 24), between(48, 96))
		o.RekeyAfterTime = span(between(110, 125), between(140, 160))
		rt := between(5, 6)
		o.RekeyTimeout = span(rt, rt+between(2, 3))
		o.RejectAfterTime = span(between(175, 190), between(200, 215))
		o.KeepaliveTimeout = span(between(9, 14), between(20, 30))
		ma := between(14, 17)
		o.MaxHandshakeAttempts = span(ma, min(ma+between(3, 5), 20))
		// under the 30 s UDP timeout of a NAT, which a keepalive of 25-35 straddles
		o.PersistentKeepalive = span(between(22, 25), between(27, 30))
		return o, nil
	}

	// 2.0: S1, S2 in [15,150], S3 in [8,55], S4 in [4,27] (less when the MTU leaves no room); the four packet
	// sizes must differ.
	hr := maxS4(mtu)
	for {
		o.S1, o.S2, o.S3, o.S4 = between(15, 150), between(15, 150), between(8, 55), between(min(4, hr), min(27, hr))
		if a, b, c, d := 148+o.S1, 92+o.S2, 64+o.S3, 32+o.S4; a != b && a != c && a != d && b != c && b != d && c != d {
			break
		}
	}
	// Four disjoint ranges in 5..2^31-1: eight distinct sorted points, taken in pairs, in random order.
	var pts [8]int
	for {
		for i := range pts {
			pts[i] = between(5, 1<<31-1)
		}
		sortInts(pts[:])
		ok := true
		for i := 1; i < len(pts); i++ {
			if pts[i] == pts[i-1] {
				ok = false
			}
		}
		if ok {
			break
		}
	}
	order := rng.Perm(4)
	hs := [4]string{}
	for i, k := range order {
		hs[i] = fmt.Sprintf("%d-%d", pts[2*k], pts[2*k+1])
	}
	o.H1, o.H2, o.H3, o.H4 = hs[0], hs[1], hs[2], hs[3]
	o.PersistentKeepalive = "25"
	return o, nil
}

func sortInts(a []int) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j] < a[j-1]; j-- {
			a[j], a[j-1] = a[j-1], a[j]
		}
	}
}

// DefaultSettings implements protocols.Protocol: a fresh 3.1 profile with everything random and the secret
// generated. Port: random in 10000-60000 without knownPorts (the WireGuard and Amnezia defaults, the first ports
// a scanner tries). Networks: the first slot (SubnetsFor(1)); access replaces them with the first slot
// that no other profile uses. Per-device signatures are on: a new profile has no devices to surprise.
func (*Protocol) DefaultSettings() (json.RawMessage, error) { return defaultSettings() }

func defaultSettings() (json.RawMessage, error) {
	ob, err := GenerateObfuscation(Version31, defaultPreset, defaultMTU, "")
	if err != nil {
		return nil, err
	}
	ob.PerDeviceSignature = true
	port, err := randomPort()
	if err != nil {
		return nil, err
	}
	s4, s6, err := SubnetsFor(1)
	if err != nil {
		return nil, err
	}
	return json.Marshal(Settings{
		Version: Version31, Port: port, MTU: defaultMTU, Egress: "direct",
		Subnet4: s4, Subnet6: s6, Obfuscation: ob,
	})
}

// knownPorts are the ports of WireGuard (51820, 51821) and of Amnezia (55424): the first a scanner tries, and a
// port the score marks down.
var knownPorts = []int{51820, 51821, 55424}

func randomPort() (int, error) {
	for {
		var b [2]byte
		if _, err := rand.Read(b[:]); err != nil {
			return 0, err
		}
		p := 10000 + int(binary.BigEndian.Uint16(b[:]))%50001
		if !slices.Contains(knownPorts, p) {
			return p, nil
		}
	}
}

// RandomPort is the "random" button next to the port field: 10000-60000 without knownPorts.
func RandomPort() (int, error) { return randomPort() }

// SubnetsFor returns the client networks of the n-th profile slot (0-63): 10.66.(4n).0/22 and
// fd66:66:0:n::/64. 10.66.0.0/16 holds 64 of them. access picks the first n no other profile uses.
func SubnetsFor(n int) (v4, v6 string, err error) {
	if n < 0 || n > 63 {
		return "", "", fmt.Errorf("profile slot must be 0-63, got %d", n)
	}
	p4 := netip.PrefixFrom(netip.AddrFrom4([4]byte{10, 66, byte(4 * n), 0}), 22)
	a6 := [16]byte{0xfd, 0x66, 0x00, 0x66, 0, 0, 0, byte(n)} // fd66:66:0:n::
	return p4.String(), netip.PrefixFrom(netip.AddrFrom16(a6), 64).String(), nil
}
