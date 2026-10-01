package awg

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/node/awg/awgcfg"
	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/plugin"
)

// fakeBackend is an in-memory awgBackend that records every call, so the engine's diff logic is tested without a
// network. Its SetPeers follows the real semantics: remove, update_only, add-or-merge, replace_allowed_ips.
type fakeBackend struct {
	mu   sync.Mutex
	name string
	is31 bool

	devs  map[string]*fakeDev
	calls []string // "create:mgawg51842", "destroy:...", "peers:mgawg51842:-01,+02,~03", "mtu:..."

	createErr error
	peersErr  error
	unknown   uint32
}

type fakeDev struct {
	cfg   deviceConfig
	mtu   int
	up    bool
	peers map[[32]byte]*fakePeer
}

type fakePeer struct {
	psk     *[32]byte
	allowed []netip.Prefix
	rx, tx  uint64
	hs      time.Time
	ep      netip.AddrPort
}

func newFake() *fakeBackend {
	return &fakeBackend{name: "userspace", is31: true, devs: map[string]*fakeDev{}}
}

func (f *fakeBackend) Name() string    { return f.name }
func (f *fakeBackend) Version() string { return "fake " + f.name }
func (f *fakeBackend) Is31() bool      { return f.is31 }

func (f *fakeBackend) Create(_ context.Context, name string, d deviceConfig) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "create:"+name)
	if f.createErr != nil {
		return f.createErr
	}
	f.devs[name] = &fakeDev{cfg: d, mtu: int(d.Tunnel.MTU), up: true, peers: map[[32]byte]*fakePeer{}}
	return nil
}

func (f *fakeBackend) SetPeers(_ context.Context, name string, replace bool, peers []awgcfg.Peer) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.peersErr != nil {
		return f.peersErr
	}
	d := f.devs[name]
	if d == nil {
		return errors.New("no such interface")
	}
	var ops []string
	if replace {
		d.peers = map[[32]byte]*fakePeer{}
	}
	for _, p := range peers {
		cur := d.peers[p.PublicKey]
		switch {
		case p.Remove:
			delete(d.peers, p.PublicKey)
			ops = append(ops, fmt.Sprintf("-%02x", p.PublicKey[0]))
		case p.UpdateOnly && cur == nil:
			// update_only never creates
		case cur == nil:
			d.peers[p.PublicKey] = &fakePeer{psk: p.PSK, allowed: append([]netip.Prefix(nil), p.AllowedIPs...)}
			ops = append(ops, fmt.Sprintf("+%02x", p.PublicKey[0]))
		default:
			if p.PSK != nil {
				cur.psk = p.PSK
				if *p.PSK == zeroKey {
					cur.psk = nil
				}
			}
			if p.ReplaceIPs {
				cur.allowed = nil
			}
			cur.allowed = append(cur.allowed, p.AllowedIPs...)
			ops = append(ops, fmt.Sprintf("~%02x", p.PublicKey[0]))
		}
	}
	f.calls = append(f.calls, "peers:"+name+":"+strings.Join(ops, ","))
	return nil
}

func (f *fakeBackend) Stats(_ context.Context, name string) ([]awgcfg.PeerStat, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.devs[name]
	if d == nil {
		return nil, errors.New("no such interface")
	}
	var out []awgcfg.PeerStat
	for k, p := range d.peers {
		out = append(out, awgcfg.PeerStat{PublicKey: k, RxBytes: p.rx, TxBytes: p.tx, LastHS: p.hs, Endpoint: p.ep, AllowedIPs: append([]netip.Prefix(nil), p.allowed...)})
	}
	return out, nil
}

func (f *fakeBackend) SetMTU(_ context.Context, name string, mtu int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf("mtu:%s:%d", name, mtu))
	f.devs[name].mtu = mtu
	return nil
}

func (f *fakeBackend) LinkUp(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.devs[name]
	return d != nil && d.up
}

func (f *fakeBackend) Destroy(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "destroy:"+name)
	delete(f.devs, name)
	return nil
}

func (f *fakeBackend) Close() error {
	f.mu.Lock()
	f.calls = append(f.calls, "close")
	f.mu.Unlock()
	return nil
}

func (f *fakeBackend) TakeUnknownPeers(string) uint32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := f.unknown
	f.unknown = 0
	return n
}

// ---- test helpers on the fake ----

func (f *fakeBackend) take() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.calls
	f.calls = nil
	return c
}

func (f *fakeBackend) peer(t *testing.T, name string, b byte) *fakePeer {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.devs[name]
	if d == nil {
		t.Fatalf("no interface %s", name)
	}
	p := d.peers[pk(b)]
	if p == nil {
		t.Fatalf("no peer %02x on %s", b, name)
	}
	return p
}

func (f *fakeBackend) hasPeer(name string, b byte) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.devs[name]
	return d != nil && d.peers[pk(b)] != nil
}

func (f *fakeBackend) mutate(name string, fn func(d *fakeDev)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f.devs[name])
}

// ---- fixtures ----

func pk(b byte) [32]byte {
	var k [32]byte
	for i := range k {
		k[i] = b
	}
	return k
}

func b64(b byte) string { k := pk(b); return base64.StdEncoding.EncodeToString(k[:]) }

// cred is a credential whose public key is 32 x b, psk 32 x (b+100), at 10.66.4.<host>.
func cred(id string, b byte, host int) plugin.UserCred {
	d, _ := json.Marshal(credJSON{PublicKey: b64(b), PSK: b64(b + 100), AllowedIPs: []string{fmt.Sprintf("10.66.4.%d/32", host), fmt.Sprintf("fd66:66:0:1::%x/128", host)}})
	return plugin.UserCred{CredID: id, UserID: "usr_" + id, DeviceID: "dev_" + id, Data: d}
}

func credData(c plugin.UserCred, mut func(*credJSON)) plugin.UserCred {
	var j credJSON
	_ = json.Unmarshal(c.Data, &j)
	mut(&j)
	c.Data, _ = json.Marshal(j)
	return c
}

func testObf() awgcfg.Obfuscation {
	return awgcfg.Obfuscation{
		Jc: 6, Jmin: 10, Jmax: 50, S1: 24, S2: 24, S3: 24, S4: 24,
		H1: rng("1"), H2: rng("2"), H3: rng("3"), H4: rng("4"),
		HeaderProtectionKey: b64(7), RandomTrailers: true, ContentPaddingAddition: rng("2-10"),
	}
}

func rng(s string) awgcfg.Range { r, _ := awgcfg.ParseRange(s); return r }

func settingsJSON(t testing.TB, mut func(*awgcfg.Settings)) json.RawMessage {
	t.Helper()
	s := awgcfg.Settings{Version: awgcfg.Version31, PrivateKey: b64(1), Obfuscation: testObf()}
	if mut != nil {
		mut(&s)
	}
	b, err := s.NodeJSON()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func spec(t testing.TB, id string, port uint16, mut func(*awgcfg.Settings)) plugin.InboundSpec {
	return plugin.InboundSpec{
		ID: id, Protocol: Protocol, ProfileID: "prf_" + id, Version: 1, Enabled: true,
		Listen: plugin.Listen{Network: "udp", Port: port}, Egress: "direct",
		Settings: settingsJSON(t, mut),
		Tunnel:   plugin.Tunnel{AddrV4: netip.MustParsePrefix("10.66.4.1/22"), AddrV6: netip.MustParsePrefix("fd66:66:0:1::1/64"), MTU: 1280},
	}
}

// clock is a settable time source for Env.Now.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newTestEngine(t *testing.T) (*Engine, *fakeBackend, *clock) {
	t.Helper()
	fb := newFake()
	clk := &clock{t: time.Unix(1_800_000_000, 0)}
	e := newEngine(engine.Env{Now: clk.Now}, fb, BackendStatus{Mode: "auto", Name: fb.name, Version: fb.Version(), Available: true})
	e.portListening = func(uint16) bool { return true }
	t.Cleanup(func() { _ = e.Close(context.Background()) })
	return e, fb, clk
}

func mustApply(t *testing.T, e *Engine, s plugin.InboundSpec, creds ...plugin.UserCred) engine.ApplyReport {
	t.Helper()
	rep, err := e.Apply(context.Background(), s, creds)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return rep
}

func sorted(s []string) []string { out := append([]string(nil), s...); sort.Strings(out); return out }
