//go:build linux

package awg

// The WSL stand (AWG userspace): our engine runs in a network namespace as a re-executed copy of
// this test binary, real amneziawg-go clients (built from the pinned source, awgtest.ClientBin) run in another
// namespace, a third one plays the internet. Needs root and MG_ROOT_TESTS=1 and is skipped otherwise:
//
//	MSYS_NO_PATHCONV=1 wsl -d Ubuntu -u root -- bash -c 'cd <repo> && MG_ROOT_TESTS=1 go test ./internal/node/awg/ -run TestStand -count=1 -v'
//
// Everything it creates is named mg3-awg-*; nothing else in the WSL is touched. Addresses are documentation ranges.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/node/awg/awgcfg"
	"github.com/mistgate/mistgate/internal/node/awg/awgtest"
	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/statehash"
)

// ---- the helper process: the engine under test, inside the server namespace ----

const helperEnv = "MG3_AWG_HELPER_SOCK"

type ctlReq struct {
	Op      string
	Spec    *plugin.InboundSpec
	Creds   []plugin.UserCred
	ID      string
	CredIDs []string
	Key     [32]byte
}

type obsInbound struct {
	ID      string
	CredIDs []string
}

type ctlResp struct {
	Err       string
	Report    engine.ApplyReport
	Collected engine.Collected
	State     string
	Observed  []obsInbound
	Health    []plugin.EngineHealth
	Awg       []InboundHealth
	Peers     []awgcfg.PeerStat
	Backend   BackendStatus
	Version   string
	Kicked    int
}

// TestStandHelper is the entry point of the helper process; a normal run skips it.
func TestStandHelper(t *testing.T) {
	sock := os.Getenv(helperEnv)
	if sock == "" {
		t.Skip("entry point of the stand's server process")
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	e, err := NewWithOptions(engine.Env{Log: log}, Options{Backend: "userspace"})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	go func() { // the parent closing our stdin (or dying) ends the helper
		_, _ = io.Copy(io.Discard, os.Stdin)
		ln.Close()
	}()
	for {
		c, err := ln.Accept()
		if err != nil {
			break
		}
		go serveCtl(e, c)
	}
	_ = e.Close(context.Background())
}

func serveCtl(e *Engine, c net.Conn) {
	defer c.Close()
	dec, enc := json.NewDecoder(c), json.NewEncoder(c)
	ctx := context.Background()
	for {
		var q ctlReq
		if err := dec.Decode(&q); err != nil {
			return
		}
		var r ctlResp
		var err error
		switch q.Op {
		case "apply":
			r.Report, err = e.Apply(ctx, *q.Spec, q.Creds)
		case "remove":
			err = e.Remove(ctx, q.ID)
		case "collect":
			r.Collected, err = e.Collect(ctx)
		case "kick":
			r.Kicked, err = e.Kick(ctx, q.CredIDs)
		case "observed":
			obs := e.Observed()
			r.State = statehash.State(obs)
			for _, o := range obs {
				oi := obsInbound{ID: o.Spec.ID}
				for _, cr := range o.Creds {
					oi.CredIDs = append(oi.CredIDs, cr.CredID)
				}
				r.Observed = append(r.Observed, oi)
			}
		case "health":
			r.Health = e.Health()
		case "awghealth":
			r.Awg = e.AwgHealth()
		case "peers", "drop":
			e.mu.Lock()
			in := e.inbounds[q.ID]
			e.mu.Unlock()
			if in == nil {
				err = fmt.Errorf("no inbound %q", q.ID)
				break
			}
			if q.Op == "drop" { // behind the engine's back
				err = e.backend.SetPeers(ctx, in.cfg.name, false, []awgcfg.Peer{{PublicKey: q.Key, Remove: true}})
			} else {
				r.Peers, err = e.backend.Stats(ctx, in.cfg.name)
			}
		case "backend":
			r.Backend, r.Version = e.BackendStatus(), e.Version()
		}
		if err != nil {
			r.Err = err.Error()
		}
		if enc.Encode(&r) != nil {
			return
		}
	}
}

// ---- the stand ----

type stand struct {
	t              *testing.T
	srv, cli, inet awgtest.NS
	enc            *json.Encoder
	dec            *json.Decoder
	helper         *awgtest.Proc
}

// masqRules is what the agent's hostctl installs for tunnels, as a stand-in for it.
const masqRules = `
table inet mg3awg_stand {
  chain forward {
    type filter hook forward priority filter; policy accept;
    iifname "mgawg*" tcp flags syn tcp option maxseg size set rt mtu
    oifname "mgawg*" tcp flags syn tcp option maxseg size set rt mtu
  }
  chain postrouting {
    type nat hook postrouting priority srcnat; policy accept;
    ip  saddr 10.66.0.0/16 oifname != "mgawg*" masquerade
    ip6 saddr fd66:66::/48 oifname != "mgawg*" masquerade
  }
}
`

const (
	srvAddr   = "203.0.113.10" // where the clients send their UDP
	siteAddr  = "198.51.100.2" // the "internet" host
	siteAddr6 = "2001:db8:2::2"
)

func newStand(t *testing.T) *stand {
	t.Helper()
	awgtest.Require(t)
	s := &stand{t: t}
	s.srv, s.cli, s.inet = awgtest.NewNS(t, "s"), awgtest.NewNS(t, "c"), awgtest.NewNS(t, "i")
	awgtest.Veth(t, s.cli, "c0", []string{"203.0.113.2/24", "2001:db8:1::2/64"}, s.srv, "s0", []string{srvAddr + "/24", "2001:db8:1::10/64"})
	awgtest.Veth(t, s.srv, "s1", []string{"198.51.100.1/24", "2001:db8:2::1/64"}, s.inet, "i1", []string{siteAddr + "/24", siteAddr6 + "/64"})
	s.srv.Must(t, "sysctl", "-qw", "net.ipv4.ip_forward=1", "net.ipv6.conf.all.forwarding=1")
	nft := s.srv.Cmd("nft", "-f", "-")
	nft.Stdin = strings.NewReader(masqRules)
	if out, err := nft.CombinedOutput(); err != nil {
		t.Skipf("nft is not usable here: %v: %s", err, out)
	}
	awgtest.StartWhoami(t, s.inet, siteAddr, 8080)
	awgtest.StartWhoami(t, s.inet, siteAddr6, 8080)
	s.startHelper()
	return s
}

func (s *stand) startHelper() {
	t := s.t
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(os.TempDir(), fmt.Sprintf("mg3-awg-ctl-%d-%d.sock", os.Getpid(), time.Now().UnixNano()%100000))
	_ = os.Remove(sock)
	s.helper = s.srv.Start(t, []string{helperEnv + "=" + sock}, exe, "-test.run=^TestStandHelper$", "-test.timeout=0", "-test.count=1")
	var conn net.Conn
	if !awgtest.Wait(15*time.Second, func() bool {
		conn, err = net.Dial("unix", sock)
		return err == nil
	}) {
		t.Fatalf("the engine helper did not start: %s", s.helper.Output())
	}
	s.enc, s.dec = json.NewEncoder(conn), json.NewDecoder(conn)
	t.Cleanup(func() {
		conn.Close()
		_ = s.helper.Stdin.Close() // the helper closes its engine and exits: its interfaces go away cleanly
		if !s.helper.Exited(10 * time.Second) {
			t.Errorf("the engine helper did not stop")
		}
		if out := s.helper.Output(); strings.Contains(out, "panic:") || strings.Contains(out, "DATA RACE") {
			t.Errorf("the engine helper crashed:\n%s", out)
		}
		if t.Failed() {
			t.Logf("engine helper log:\n%s", s.helper.Output())
		}
		_ = os.Remove(sock)
	})
}

func (s *stand) call(q ctlReq) ctlResp {
	s.t.Helper()
	if err := s.enc.Encode(q); err != nil {
		s.t.Fatalf("ctl %s: %v", q.Op, err)
	}
	var r ctlResp
	if err := s.dec.Decode(&r); err != nil {
		s.t.Fatalf("ctl %s: %v\n%s", q.Op, err, s.helper.Output())
	}
	return r
}

func (s *stand) applyErr(sp plugin.InboundSpec, creds ...plugin.UserCred) (engine.ApplyReport, error) {
	s.t.Helper()
	r := s.call(ctlReq{Op: "apply", Spec: &sp, Creds: creds})
	if r.Err != "" {
		return r.Report, fmt.Errorf("%s", r.Err)
	}
	return r.Report, nil
}

func (s *stand) apply(sp plugin.InboundSpec, creds ...plugin.UserCred) engine.ApplyReport {
	s.t.Helper()
	rep, err := s.applyErr(sp, creds...)
	if err != nil {
		s.t.Fatalf("Apply: %v", err)
	}
	return rep
}

func (s *stand) collect() engine.Collected {
	s.t.Helper()
	r := s.call(ctlReq{Op: "collect"})
	if r.Err != "" {
		s.t.Fatalf("Collect: %s", r.Err)
	}
	return r.Collected
}

func (s *stand) awgHealth() []InboundHealth    { return s.call(ctlReq{Op: "awghealth"}).Awg }
func (s *stand) health() []plugin.EngineHealth { return s.call(ctlReq{Op: "health"}).Health }
func (s *stand) peers(id string) []awgcfg.PeerStat {
	r := s.call(ctlReq{Op: "peers", ID: id})
	if r.Err != "" {
		s.t.Fatalf("peers: %s", r.Err)
	}
	return r.Peers
}

func (s *stand) observed() ctlResp { return s.call(ctlReq{Op: "observed"}) }

// desired is the state hash the agent would compute from what it asked for.
func desired(sp plugin.InboundSpec, creds ...plugin.UserCred) string {
	return statehash.State([]statehash.Inbound{{Spec: sp, Creds: creds}})
}

// ---- profiles, devices, clients ----

type profile struct {
	version  string
	obf      awgcfg.Obfuscation
	priv     [32]byte
	pub      [32]byte
	port     uint16
	n        int // subnet number: 10.66.(4n).0/22 and fd66:66:0:n::/64
	clientI1 string
}

func newProfile(t *testing.T, version string, port uint16, n int, obf awgcfg.Obfuscation) profile {
	t.Helper()
	priv, pub := awgtest.NewKey(t)
	p := profile{version: version, obf: obf, priv: priv, pub: pub, port: port, n: n, clientI1: obf.I1}
	if r := awgcfg.Validate(p.settings(), awgcfg.Options{MTU: 1280}); !r.OK() {
		t.Fatalf("the stand's own profile is invalid: %v", r.Errors)
	}
	return p
}

func (p profile) settings() awgcfg.Settings {
	return awgcfg.Settings{Version: p.version, PrivateKey: awgtest.B64(p.priv), Obfuscation: p.obf}
}

func (p profile) addr4() string { return fmt.Sprintf("10.66.%d.1/22", 4*p.n) }
func (p profile) addr6() string { return fmt.Sprintf("fd66:66:0:%d::1/64", p.n) }

func (p profile) spec(t testing.TB, id string, mut ...func(*plugin.InboundSpec)) plugin.InboundSpec {
	nj, err := p.settings().NodeJSON()
	if err != nil {
		t.Fatal(err)
	}
	sp := plugin.InboundSpec{
		ID: id, Protocol: Protocol, ProfileID: "prf_" + id, Version: 1, Enabled: true,
		Listen: plugin.Listen{Network: "udp", Port: p.port}, Egress: "direct", Settings: nj,
		Tunnel: plugin.Tunnel{AddrV4: netip.MustParsePrefix(p.addr4()), AddrV6: netip.MustParsePrefix(p.addr6()), MTU: 1280},
	}
	for _, m := range mut {
		m(&sp)
	}
	return sp
}

type tdev struct {
	priv, pub, psk [32]byte
	host           int
	p              profile
}

func newDevice(t *testing.T, p profile, host int) tdev {
	t.Helper()
	priv, pub := awgtest.NewKey(t)
	psk, _ := awgtest.NewKey(t)
	return tdev{priv: priv, pub: pub, psk: psk, host: host, p: p}
}

func (d tdev) ip4() string { return fmt.Sprintf("10.66.%d.%d", 4*d.p.n, d.host) }
func (d tdev) ip6() string { return fmt.Sprintf("fd66:66:0:%d::%x", d.p.n, d.host) }

func (d tdev) cred(id string) plugin.UserCred {
	data, _ := json.Marshal(credJSON{PublicKey: awgtest.B64(d.pub), PSK: awgtest.B64(d.psk), AllowedIPs: []string{d.ip4() + "/32", d.ip6() + "/128"}})
	return plugin.UserCred{CredID: id, UserID: "usr_" + id, DeviceID: "dev_" + id, Data: data}
}

func (s *stand) client(flavor string, p profile, d tdev, mut ...func(*awgtest.ClientCfg)) *awgtest.Client {
	s.t.Helper()
	return s.clientIn(s.cli, srvAddr, flavor, p, d, mut...)
}

func (s *stand) clientIn(ns awgtest.NS, server string, flavor string, p profile, d tdev, mut ...func(*awgtest.ClientCfg)) *awgtest.Client {
	s.t.Helper()
	cfg := awgtest.ClientCfg{
		Flavor: flavor, Addrs: []string{d.ip4() + "/22", d.ip6() + "/64"},
		Routes: []string{"198.51.100.0/24", "2001:db8:2::/64"},
		Priv:   d.priv, ServerPub: p.pub, PSK: &d.psk, Endpoint: fmt.Sprintf("%s:%d", server, p.port),
		Obf: p.obf, MTU: 1280, Keepalive: "25",
	}
	for _, m := range mut {
		m(&cfg)
	}
	return awgtest.StartClient(s.t, ns, cfg)
}

// secondClientNS adds a second client namespace with its own link to the node (same subnet as the first client would
// make its routes ambiguous).
func (s *stand) secondClientNS() (ns awgtest.NS, server string) {
	s.t.Helper()
	ns = awgtest.NewNS(s.t, "c2")
	awgtest.Veth(s.t, ns, "c0", []string{"203.0.114.2/24"}, s.srv, "s2", []string{"203.0.114.10/24"})
	return ns, "203.0.114.10"
}

func (s *stand) mustHandshake(c *awgtest.Client) {
	s.t.Helper()
	if !awgtest.Wait(10*time.Second, c.Handshaken) {
		s.t.Fatalf("no handshake.\nclient: %s\nserver: %s", c.Output(), s.helper.Output())
	}
}

func (s *stand) noHandshake(c *awgtest.Client, node string) {
	s.t.Helper()
	if loss, out := awgtest.Ping(s.cli, node, 3, 0, false, 0); loss != 100 {
		s.t.Fatalf("traffic passes although the parameters differ: %s", out)
	}
	if c.Handshaken() {
		s.t.Fatal("the client completed a handshake although the parameters differ")
	}
}

func (s *stand) mustPing(target string, n int) {
	s.t.Helper()
	if loss, out := awgtest.Ping(s.cli, target, n, 0, false, 200*time.Millisecond); loss != 0 {
		s.t.Fatalf("ping %s lost %d%%: %s", target, loss, out)
	}
}

// ---- parameter sets (the rows of the compatibility matrix) ----

func r(s string) awgcfg.Range { v, _ := awgcfg.ParseRange(s); return v }

func withH(o awgcfg.Obfuscation, a, b, c, d string) awgcfg.Obfuscation {
	o.H1, o.H2, o.H3, o.H4 = r(a), r(b), r(c), r(d)
	return o
}

func full31(t *testing.T) awgcfg.Obfuscation {
	hpk, _ := awgtest.NewKey(t)
	return awgcfg.Obfuscation{
		Jc: 6, Jmin: 10, Jmax: 50, S1: 24, S2: 24, S3: 24, S4: 24,
		H1: r("1"), H2: r("2"), H3: r("3"), H4: r("4"),
		I1:                  "<b 0xc70000000108><r 8><t><r 40>",
		HeaderProtectionKey: awgtest.B64(hpk), RandomTrailers: true,
		ContentPaddingAddition: r("2-10"), RekeyAfterTime: r("100-120"), RekeyTimeout: r("3-7"), RejectAfterTime: r("150-180"),
		KeepaliveTimeout: r("5-15"), MaxHandshakeAttempts: r("15-20"),
	}
}

func style30() awgcfg.Obfuscation {
	return withH(awgcfg.Obfuscation{
		Jc: 5, Jmin: 10, Jmax: 50, S1: 31, S2: 47, S3: 23, S4: 17, ContentPaddingAddition: r("2-10"),
		I1: "<b 0xc70000000108><r 8><t><r 40>",
	}, "100-200", "300-400", "500-600", "700-800")
}

func style20() awgcfg.Obfuscation {
	return withH(awgcfg.Obfuscation{Jc: 4, Jmin: 10, Jmax: 50, S1: 20, S2: 30, S3: 10, S4: 5, I1: "<r 2><b 0x858000010001>"},
		"1000-2000", "3000-4000", "5000-6000", "7000-8000")
}

func noObf() awgcfg.Obfuscation { return withH(awgcfg.Obfuscation{}, "1", "2", "3", "4") }

// ---- scenarios ----

func TestStandBackend(t *testing.T) {
	s := newStand(t)
	r := s.call(ctlReq{Op: "backend"})
	if !r.Backend.Available || r.Backend.Name != "userspace" || !strings.HasPrefix(r.Version, "amneziawg-go v3.1.") {
		t.Fatalf("backend %+v version %q", r.Backend, r.Version)
	}
	t.Logf("backend: %s", r.Version)
}

// Handshake, ping, NAT, traffic accounting, sessions and the observed state for every version of the protocol.
func TestStandInterop(t *testing.T) {
	cases := []struct {
		name, flavor, version string
		obf                   func(*testing.T) awgcfg.Obfuscation
		iperf                 bool
	}{
		{"3.1 full", awgtest.Flavor31, awgcfg.Version31, full31, true},
		{"3.0 style with a 3.1 client", awgtest.Flavor31, awgcfg.Version31, func(*testing.T) awgcfg.Obfuscation { return style30() }, false},
		{"3.0 style with a real 3.0 client", awgtest.Flavor30, awgcfg.Version31, func(*testing.T) awgcfg.Obfuscation { return style30() }, false},
		{"2.0 with a real 2.0 client (v0.2.17)", awgtest.Flavor20, awgcfg.Version20, func(*testing.T) awgcfg.Obfuscation { return style20() }, true},
		{"no obfuscation", awgtest.Flavor31, awgcfg.Version31, func(*testing.T) awgcfg.Obfuscation { return noObf() }, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newStand(t)
			p := newProfile(t, c.version, 51842, 1, c.obf(t))
			d := newDevice(t, p, 2)
			sp, cr := p.spec(t, "inb1"), d.cred("cred1")
			s.apply(sp, cr)
			cl := s.client(c.flavor, p, d)
			s.mustHandshake(cl)

			s.mustPing("10.66.4.1", 5)
			s.mustPing(siteAddr, 5)
			if got := awgtest.Whoami(s.cli, siteAddr, 8080); got != "198.51.100.1" {
				t.Errorf("v4 whoami = %q, want the node's address behind masquerade", got)
			}
			s.mustPing("fd66:66:0:1::1", 3)
			s.mustPing(siteAddr6, 3)
			if got := awgtest.Whoami(s.cli, siteAddr6, 8080); got != "2001:db8:2::1" {
				t.Errorf("v6 whoami = %q (NAT66)", got)
			}
			if c.iperf {
				srv := s.inet.Start(t, nil, "iperf3", "-s", "-1", "-B", siteAddr)
				_ = srv
				time.Sleep(300 * time.Millisecond)
				out, err := s.cli.Run("iperf3", "-c", siteAddr, "-t", "2", "-J")
				if err != nil {
					t.Fatalf("iperf3: %v: %.400s", err, out)
				}
				var res struct {
					End struct {
						Recv struct {
							Bps float64 `json:"bits_per_second"`
						} `json:"sum_received"`
					} `json:"end"`
				}
				if json.Unmarshal([]byte(out), &res) != nil || res.End.Recv.Bps < 5e6 {
					t.Errorf("iperf3 through the tunnel: %.0f bit/s", res.End.Recv.Bps)
				} else {
					t.Logf("iperf3 through the tunnel: %.0f Mbit/s", res.End.Recv.Bps/1e6)
				}
			}

			h := s.awgHealth()
			if len(h) != 1 || !h[0].IfaceUp || h[0].Peers != 1 || h[0].PeersHandshaken != 1 || h[0].PeersOnline != 1 || h[0].NewestHandshakeUnix == 0 || h[0].Backend != "userspace" {
				t.Errorf("AwgHealth %+v", h)
			}
			col := s.collect()
			if len(col.Traffic) != 1 || col.Traffic[0].CredID != "cred1" || col.Traffic[0].Up == 0 || col.Traffic[0].Down == 0 {
				t.Errorf("traffic %+v", col.Traffic)
			}
			if len(col.Sessions) != 1 || col.Sessions[0].CredID != "cred1" || col.Sessions[0].RemoteIP != netip.MustParseAddr("203.0.113.2") {
				t.Errorf("sessions %+v", col.Sessions)
			}
			if o := s.observed(); o.State != desired(sp, cr) {
				t.Errorf("observed state differs from the desired one: %+v", o.Observed)
			}
			if hl := s.health(); len(hl) != 1 || hl[0].State != plugin.RunRunning {
				t.Errorf("health %+v", hl)
			}
		})
	}
}

// Parameters that differ make the handshake silently fail: the engine must not pretend otherwise, and AwgHealth
// shows "peer configured, never shook hands" for the doctor.
func TestStandMismatchNeverHandshakes(t *testing.T) {
	cases := []struct {
		name string
		mut  func(o *awgcfg.Obfuscation)
	}{
		{"hpk differs", func(o *awgcfg.Obfuscation) { k, _ := awgtest.NewKey(t); o.HeaderProtectionKey = awgtest.B64(k) }},
		{"no hpk at the client", func(o *awgcfg.Obfuscation) { o.HeaderProtectionKey = "" }},
		{"s1 off by one", func(o *awgcfg.Obfuscation) { o.S1++ }},
		{"random trailers only at the server", func(o *awgcfg.Obfuscation) { o.RandomTrailers = false }},
		{"h2 does not overlap", func(o *awgcfg.Obfuscation) { o.H2 = r("20-30") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newStand(t)
			p := newProfile(t, awgcfg.Version31, 51842, 1, full31(t))
			d := newDevice(t, p, 2)
			s.apply(p.spec(t, "inb1"), d.cred("cred1"))
			cl := s.client(awgtest.Flavor31, p, d, func(cfg *awgtest.ClientCfg) { c.mut(&cfg.Obf) })
			s.noHandshake(cl, "10.66.4.1")
			h := s.awgHealth()
			if len(h) != 1 || h[0].Peers != 1 || h[0].PeersHandshaken != 0 || h[0].PeersOnline != 0 {
				t.Errorf("AwgHealth must say \"configured, never connected\": %+v", h)
			}
			if col := s.collect(); len(col.Sessions) != 0 {
				t.Errorf("sessions %+v", col.Sessions)
			}
		})
	}
	t.Run("random trailers only at the client", func(t *testing.T) {
		s := newStand(t)
		o := full31(t)
		o.RandomTrailers = false
		p := newProfile(t, awgcfg.Version31, 51842, 1, o)
		d := newDevice(t, p, 2)
		s.apply(p.spec(t, "inb1"), d.cred("cred1"))
		cl := s.client(awgtest.Flavor31, p, d, func(cfg *awgtest.ClientCfg) { cfg.Obf.RandomTrailers = true })
		s.noHandshake(cl, "10.66.4.1")
	})
	t.Run("a 2.0 client against a 3.1 server", func(t *testing.T) {
		s := newStand(t)
		p := newProfile(t, awgcfg.Version31, 51842, 1, full31(t))
		d := newDevice(t, p, 2)
		s.apply(p.spec(t, "inb1"), d.cred("cred1"))
		cl := s.client(awgtest.Flavor20, p, d, func(cfg *awgtest.ClientCfg) { cfg.Obf = style20() })
		s.noHandshake(cl, "10.66.4.1")
	})
}

// update_only on a live peer: the session is not touched and a ping running during the change loses nothing.
func TestStandLiveUpdateKeepsTheSession(t *testing.T) {
	s := newStand(t)
	p := newProfile(t, awgcfg.Version31, 51842, 1, full31(t))
	a, b := newDevice(t, p, 2), newDevice(t, p, 3)
	sp := p.spec(t, "inb1")
	s.apply(sp, a.cred("a"))
	cl := s.client(awgtest.Flavor31, p, a)
	s.mustHandshake(cl)
	hs := cl.LastHandshake()

	type result struct {
		loss int
		out  string
	}
	done := make(chan result, 1)
	go func() {
		loss, out := awgtest.Ping(s.cli, "10.66.4.1", 150, 0, false, 40*time.Millisecond) // about 6 s
		done <- result{loss, out}
	}()
	time.Sleep(1500 * time.Millisecond)

	// a new peer arrives and a's allowed ips shrink (the v6 address goes): update_only + replace_allowed_ips
	shrunk := credData(a.cred("a"), func(j *credJSON) { j.AllowedIPs = []string{a.ip4() + "/32"} })
	sp2 := sp
	sp2.Version = 2
	rep := s.apply(sp2, shrunk, b.cred("b"))
	if rep.Restarted {
		t.Error("a users-only change restarted the interface")
	}
	got := <-done
	if got.loss != 0 {
		t.Errorf("a ping running during the change lost %d%%:\n%s", got.loss, got.out)
	}
	if after := cl.LastHandshake(); !after.Equal(hs) {
		t.Errorf("the session was renegotiated (%v -> %v)", hs, after)
	}
	for _, ps := range s.peers("inb1") {
		if ps.PublicKey == a.pub && len(ps.AllowedIPs) != 1 {
			t.Errorf("allowed ips of a after the update: %v", ps.AllowedIPs)
		}
	}
	if len(s.peers("inb1")) != 2 {
		t.Error("the new peer is missing")
	}
	// and the new peer works right away
	cl.Stop()
	clb := s.client(awgtest.Flavor31, p, b)
	s.mustHandshake(clb)
	s.mustPing("10.66.4.1", 3)
}

// Removing a peer cuts its traffic in well under 2 s, and its last bytes are still reported.
func TestStandRevokeCutsTrafficFast(t *testing.T) {
	s := newStand(t)
	p := newProfile(t, awgcfg.Version31, 51842, 1, full31(t))
	a := newDevice(t, p, 2)
	sp := p.spec(t, "inb1")
	s.apply(sp, a.cred("a"))
	cl := s.client(awgtest.Flavor31, p, a)
	s.mustHandshake(cl)
	s.mustPing("10.66.4.1", 3)

	sp2 := sp
	sp2.Version = 2
	start := time.Now()
	s.apply(sp2)
	for {
		if loss, _ := awgtest.Ping(s.cli, "10.66.4.1", 1, 0, false, 0); loss == 100 {
			break
		}
		if time.Since(start) > 5*time.Second {
			t.Fatal("traffic still flows 5 s after the peer was removed")
		}
		time.Sleep(50 * time.Millisecond)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("revocation took %v, want < 2 s", d)
	} else {
		t.Logf("revocation cut traffic in %v", d.Round(10*time.Millisecond))
	}
	col := s.collect()
	if len(col.Traffic) != 1 || col.Traffic[0].CredID != "a" || col.Traffic[0].Up == 0 {
		t.Errorf("the revoked peer's last bytes must be reported: %+v", col.Traffic)
	}
	if len(col.Sessions) != 0 {
		t.Errorf("a revoked peer has no session: %+v", col.Sessions)
	}
}

// Key rotation: the old public key leaves and the new one takes the same address in one Apply.
func TestStandRotationKeepsTheAddress(t *testing.T) {
	s := newStand(t)
	p := newProfile(t, awgcfg.Version31, 51842, 1, full31(t))
	old := newDevice(t, p, 2)
	sp := p.spec(t, "inb1")
	s.apply(sp, old.cred("dev"))
	cl1 := s.client(awgtest.Flavor31, p, old)
	s.mustHandshake(cl1)
	s.mustPing("10.66.4.1", 3)

	fresh := newDevice(t, p, 2) // same address, new keys
	sp2 := sp
	sp2.Version = 2
	s.apply(sp2, fresh.cred("dev-rotated"))
	if loss, _ := awgtest.Ping(s.cli, "10.66.4.1", 3, 0, false, 0); loss != 100 {
		t.Error("the old key must die at once")
	}
	cl1.Stop()
	cl2 := s.client(awgtest.Flavor31, p, fresh)
	s.mustHandshake(cl2)
	s.mustPing("10.66.4.1", 5)
	s.mustPing(siteAddr, 3)
	if o := s.observed(); o.State != desired(sp2, fresh.cred("dev-rotated")) {
		t.Errorf("observed %+v", o.Observed)
	}
}

// Kick removes the peer and puts it back after a second; the client recovers by its own timer (up to ~15 s).
func TestStandKickRecovers(t *testing.T) {
	s := newStand(t)
	p := newProfile(t, awgcfg.Version31, 51842, 1, full31(t))
	a := newDevice(t, p, 2)
	s.apply(p.spec(t, "inb1"), a.cred("a"))
	cl := s.client(awgtest.Flavor31, p, a)
	s.mustHandshake(cl)
	s.mustPing("10.66.4.1", 3)

	r := s.call(ctlReq{Op: "kick", CredIDs: []string{"a", "nobody"}})
	if r.Err != "" || r.Kicked != 1 {
		t.Fatalf("Kick = %d %q", r.Kicked, r.Err)
	}
	time.Sleep(1500 * time.Millisecond)
	if !func() bool {
		for _, ps := range s.peers("inb1") {
			if ps.PublicKey == a.pub {
				return true
			}
		}
		return false
	}() {
		t.Fatal("the kicked peer was not put back")
	}
	start := time.Now()
	ok := awgtest.Wait(30*time.Second, func() bool {
		loss, _ := awgtest.Ping(s.cli, "10.66.4.1", 1, 0, false, 0)
		return loss == 0
	})
	if !ok {
		t.Fatalf("the client never recovered after Kick\nclient: %s", cl.Output())
	}
	t.Logf("client recovered %v after the kick", time.Since(start).Round(100*time.Millisecond))
}

// Thousands of peers in one Apply, Observed after a peer is removed behind the engine's back, and the repair.
func TestStandManyPeers(t *testing.T) {
	s := newStand(t)
	p := newProfile(t, awgcfg.Version31, 51842, 1, full31(t))
	sp := p.spec(t, "inb1", func(sp *plugin.InboundSpec) { sp.Tunnel.AddrV4 = netip.MustParsePrefix("10.66.0.1/16") })
	const n = 5000
	creds := make([]plugin.UserCred, n)
	var first [32]byte
	for i := range creds {
		_, pub := awgtest.NewKey(t)
		if i == 17 {
			first = pub
		}
		data, _ := json.Marshal(credJSON{PublicKey: awgtest.B64(pub), AllowedIPs: []string{fmt.Sprintf("10.66.%d.%d/32", 1+i/250, 2+i%250)}})
		creds[i] = plugin.UserCred{CredID: fmt.Sprintf("c%d", i), Data: data}
	}
	start := time.Now()
	s.apply(sp, creds...)
	t.Logf("%d peers applied in %v", n, time.Since(start).Round(time.Millisecond))
	if h := s.awgHealth(); len(h) != 1 || h[0].Peers != n {
		t.Fatalf("AwgHealth %+v", h)
	}
	want := desired(sp, creds...)
	if o := s.observed(); o.State != want {
		t.Fatal("observed differs right after Apply")
	}
	if r := s.call(ctlReq{Op: "drop", ID: "inb1", Key: first}); r.Err != "" {
		t.Fatal(r.Err)
	}
	if o := s.observed(); o.State == want {
		t.Fatal("a peer deleted behind the engine's back must show as a hash difference")
	}
	s.apply(sp, creds...)
	if o := s.observed(); o.State != want {
		t.Fatal("Apply did not put the missing peer back")
	}
	start = time.Now()
	s.collect()
	t.Logf("Collect with %d peers: %v", n, time.Since(start).Round(time.Millisecond))
}

// Changing a critical parameter recreates the interface: old clients are cut, new parameters work.
func TestStandCriticalChangeRecreatesTheInterface(t *testing.T) {
	s := newStand(t)
	p := newProfile(t, awgcfg.Version31, 51842, 1, full31(t))
	a := newDevice(t, p, 2)
	sp, cr := p.spec(t, "inb1"), a.cred("a")
	s.apply(sp, cr)
	cl := s.client(awgtest.Flavor31, p, a)
	s.mustHandshake(cl)
	s.mustPing("10.66.4.1", 3)

	o2 := p.obf
	o2.S1, o2.S2, o2.S3, o2.S4 = 30, 30, 30, 30
	p2 := p
	p2.obf = o2
	sp2 := p2.spec(t, "inb1")
	rep := s.apply(sp2, cr)
	if !rep.Restarted {
		t.Fatal("a changed S1..S4 must recreate the interface")
	}
	if loss, _ := awgtest.Ping(s.cli, "10.66.4.1", 3, 0, false, 0); loss != 100 {
		t.Error("the old client must not get through with the new parameters")
	}
	cl.Stop()
	cl2 := s.client(awgtest.Flavor31, p2, a)
	s.mustHandshake(cl2)
	s.mustPing("10.66.4.1", 3)
	if hl := s.health(); len(hl) != 1 || hl[0].Restarts != 1 || hl[0].State != plugin.RunRunning {
		t.Errorf("health %+v", hl)
	}
	col := s.collect()
	if len(col.Traffic) == 0 || col.Traffic[0].Up == 0 {
		t.Errorf("traffic across the restart: %+v", col.Traffic)
	}

	// an MTU change alone is live
	sp3 := p2.spec(t, "inb1", func(sp *plugin.InboundSpec) { sp.Tunnel.MTU = 1380 })
	if rep := s.apply(sp3, cr); rep.Restarted {
		t.Error("an MTU change must not recreate the interface")
	}
	s.mustPing("10.66.4.1", 3) // the session survived
}

func linkMTU(t *testing.T, ns awgtest.NS, iface string) string {
	out := ns.Must(t, "ip", "-o", "link", "show", iface)
	f := strings.Fields(out)
	for i, w := range f {
		if w == "mtu" && i+1 < len(f) {
			return f[i+1]
		}
	}
	t.Fatalf("no mtu in %q", out)
	return ""
}

func TestStandMTU(t *testing.T) {
	s := newStand(t)
	p := newProfile(t, awgcfg.Version31, 51842, 1, full31(t))
	a := newDevice(t, p, 2)
	sp, cr := p.spec(t, "inb1"), a.cred("a")
	s.apply(sp, cr)
	if got := linkMTU(t, s.srv, "mgawg51842"); got != "1280" {
		t.Fatalf("server MTU %s", got)
	}
	cl := s.client(awgtest.Flavor31, p, a, func(c *awgtest.ClientCfg) { c.MTU = 1420 })
	s.mustHandshake(cl)
	ping := func(size int) int {
		loss, _ := awgtest.Ping(s.cli, "10.66.4.1", 2, size, true, 0)
		return loss
	}
	if ping(1252) != 0 { // 1252 + 28 = 1280
		t.Error("a 1280-byte packet must pass")
	}
	before := ping(1392) // 1420 bytes: too big for the node's 1280 interface
	t.Logf("1420-byte DF packet with the node at MTU 1280: loss %d%%", before)

	sp2 := p.spec(t, "inb1", func(sp *plugin.InboundSpec) { sp.Tunnel.MTU = 1420 })
	sp2.Version = 2
	if rep := s.apply(sp2, cr); rep.Restarted {
		t.Fatal("a live MTU change restarted the interface")
	}
	if got := linkMTU(t, s.srv, "mgawg51842"); got != "1420" {
		t.Errorf("server MTU after the live change: %s", got)
	}
	if loss := ping(1392); loss != 0 {
		t.Errorf("a 1420-byte DF packet must pass after the MTU was raised live: loss %d%%", loss)
	}
	if before == 0 {
		t.Log("note: the node already carried 1420-byte packets at MTU 1280 (the TUN accepts them); the link MTU itself is what the engine sets")
	}
}

// Two profiles on one node: two ports, two subnets, two interfaces; parameters of one never work on the other.
func TestStandTwoProfiles(t *testing.T) {
	s := newStand(t)
	p1 := newProfile(t, awgcfg.Version31, 51842, 1, full31(t))
	p2 := newProfile(t, awgcfg.Version20, 51843, 2, style20())
	d1, d2 := newDevice(t, p1, 2), newDevice(t, p2, 2)
	sp1, sp2 := p1.spec(t, "inb1"), p2.spec(t, "inb2")
	s.apply(sp1, d1.cred("c1"))
	s.apply(sp2, d2.cred("c2"))
	cl1 := s.client(awgtest.Flavor31, p1, d1)
	cl2 := s.client(awgtest.Flavor20, p2, d2, func(c *awgtest.ClientCfg) { c.Routes = nil })
	s.mustHandshake(cl1)
	s.mustHandshake(cl2)
	s.mustPing("10.66.4.1", 3)
	s.mustPing("10.66.8.1", 3)
	loss, _ := awgtest.Ping(s.cli, "10.66.8.1", 2, 0, false, 0, "-I", cl1.Iface)
	{
		t.Logf("FINDING: a client of profile 1 reaches the node's address of profile 2 (%s) through its own tunnel: loss %d%% (0 = reachable, the weak host model; the firewall layer decides)", "10.66.8.1", loss)
	}
	for id, want := range map[string]int{"inb1": 1, "inb2": 1} {
		if got := len(s.peers(id)); got != want {
			t.Errorf("%s has %d peers", id, got)
		}
	}
	h := s.awgHealth()
	if len(h) != 2 || h[0].PeersOnline != 1 || h[1].PeersOnline != 1 {
		t.Errorf("AwgHealth %+v", h)
	}
	// a 3.1 client pointed at the 2.0 profile's port never gets in
	cl3 := s.client(awgtest.Flavor31, p1, d1, func(c *awgtest.ClientCfg) { c.Endpoint = fmt.Sprintf("%s:%d", srvAddr, p2.port) })
	if awgtest.Wait(6*time.Second, cl3.Handshaken) {
		t.Error("parameters of profile 1 worked on profile 2's port")
	}
	if o := s.observed(); len(o.Observed) != 2 {
		t.Errorf("observed %+v", o.Observed)
	}
}

// What the firewall layer must still decide (not the engine's job): can a tunnel client reach another client?
// The answer is logged for the agent's hostctl.SetTunnels; the test asserts nothing about it.
func TestStandFindingClientToClient(t *testing.T) {
	s := newStand(t)
	p := newProfile(t, awgcfg.Version31, 51842, 1, full31(t))
	a, b := newDevice(t, p, 2), newDevice(t, p, 3)
	s.apply(p.spec(t, "inb1"), a.cred("a"), b.cred("b"))
	ns2, server2 := s.secondClientNS()
	ca := s.client(awgtest.Flavor31, p, a)
	cb := s.clientIn(ns2, server2, awgtest.Flavor31, p, b)
	s.mustHandshake(ca)
	s.mustHandshake(cb)
	loss, _ := awgtest.Ping(s.cli, b.ip4(), 3, 0, false, 0)
	t.Logf("FINDING: client a -> client b (%s) through the node, same profile: loss %d%% (0 = reachable; hostctl must drop iifname mgawg* oifname mgawg*)", b.ip4(), loss)
}

// A busy UDP port fails the inbound without leaving an interface behind; freeing it lets the retry through.
func TestStandPortInUse(t *testing.T) {
	s := newStand(t)
	p := newProfile(t, awgcfg.Version31, 51842, 1, full31(t))
	sp := p.spec(t, "inb1")
	squat := s.srv.Start(t, nil, "python3", "-c", "import socket,time\ns=socket.socket(socket.AF_INET,socket.SOCK_DGRAM)\ns.bind(('0.0.0.0',51842))\ntime.sleep(600)")
	time.Sleep(500 * time.Millisecond)
	_, err := s.applyErr(sp)
	if err == nil {
		t.Fatal("Apply succeeded on a busy port")
	}
	t.Logf("busy port: %v", err)
	if out := s.srv.Must(t, "ip", "-o", "link", "show"); strings.Contains(out, "mgawg") {
		t.Errorf("a failed inbound left an interface behind:\n%s", out)
	}
	if hl := s.health(); len(hl) != 1 || hl[0].State != plugin.RunFailed {
		t.Errorf("health %+v", hl)
	}
	squat.Kill()
	time.Sleep(200 * time.Millisecond)
	s.apply(sp) // the same spec again: a retry rebuilds it
	if hl := s.health(); hl[0].State != plugin.RunRunning {
		t.Errorf("health after the retry %+v", hl)
	}
}

// Closing the engine (the helper's exit) removes every interface.
func TestStandShutdownLeavesNothing(t *testing.T) {
	s := newStand(t)
	p := newProfile(t, awgcfg.Version31, 51842, 1, full31(t))
	s.apply(p.spec(t, "inb1"))
	if out := s.srv.Must(t, "ip", "-o", "link", "show"); !strings.Contains(out, "mgawg51842") {
		t.Fatalf("no interface:\n%s", out)
	}
	_ = s.helper.Stdin.Close()
	if !s.helper.Exited(10 * time.Second) {
		t.Fatal("the helper did not stop")
	}
	if out := s.srv.Must(t, "ip", "-o", "link", "show"); strings.Contains(out, "mgawg") {
		t.Errorf("interfaces left behind after Close:\n%s", out)
	}
}

var _ = base64.StdEncoding
