//go:build !js

package health

import (
	"encoding/json"
	"net"
	"net/http"
	"net/netip"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/net/dns/dnsmessage"

	"github.com/amnezia-vpn/amneziawg-go/v3/conn"
	"github.com/amnezia-vpn/amneziawg-go/v3/conn/bindtest"
	"github.com/amnezia-vpn/amneziawg-go/v3/device"
	"github.com/amnezia-vpn/amneziawg-go/v3/tun/netstack"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/node/awg/awgcfg"
	"github.com/mistgate/mistgate/internal/node/awg/awguapi"
	"github.com/mistgate/mistgate/internal/panel/protocols/awg"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/statehash"
)

// The AmneziaWG probe against a real amneziawg-go server running in the test process: the server is configured the way the
// node's engine configures an interface (awguapi.DeviceSet of the spec the panel built, one peer per credential of the
// desired state, the system credential included), on a netstack TUN with an HTTP server behind it, and the two ends talk
// through amneziawg-go's in-memory bind pair. The client is the panel's own dialAWG, unchanged.

// pairClientBind is the client end of the in-memory pair. The endpoint the config names does not matter: the pair is the
// only way out.
type pairClientBind struct{ conn.Bind }

func (pairClientBind) ParseEndpoint(string) (conn.Endpoint, error) {
	return bindtest.ChannelEndpoint(1), nil
}

type awgRig struct {
	*env
	binds       [2]conn.Bind
	gw          netip.Addr // the node's address inside the tunnel, where the probe server listens
	srv         *device.Device
	dnsQueries  atomic.Int32
	lastAttempt time.Time
}

const awgNode = "nod_l"

func newAWGRig(t *testing.T) *awgRig {
	t.Helper()
	r := &awgRig{binds: bindtest.NewChannelBinds()}
	r.env = newEnv(t, func(c *Config) {
		c.HandshakeTimeout = 2 * time.Second
		c.ProbeTimeout = 3 * time.Second
		c.Now = time.Now // the latency of a round is measured against this clock: the tests look at it
	})
	old := awgBind
	awgBind = func() conn.Bind { return pairClientBind{r.binds[0]} }
	t.Cleanup(func() { awgBind = old })
	r.exec(`INSERT INTO node (id, name, address, provider, state, created_at, last_seen_at, last_connected_at) VALUES (?, 'lo', '127.0.0.1', 'lo', 'active', 1, 1, 1)`, awgNode)
	r.fl.set(awgNode, liveState{up: true, caps: []string{capDoctor}})
	return r
}

// deploy puts a new AWG profile (settings "" = the defaults) on the node, marks the inbound active as the node would,
// and starts the server for it. withProbe: the system credential is part of what the server is given. It returns the
// inbound id.
func (r *awgRig) deploy(name, settings string, withProbe bool) string {
	r.t.Helper()
	p, err := r.acc.CreateProfile(r.ctx, connect.NewRequest(&adminv1.CreateProfileRequest{Protocol: "awg", Name: name, SettingsJson: settings}))
	if err != nil {
		r.t.Fatal(err)
	}
	in, err := r.acc.CreateInbound(r.ctx, connect.NewRequest(&adminv1.CreateInboundRequest{ProfileId: p.Msg.Profile.Id, NodeId: awgNode}))
	if err != nil {
		r.t.Fatal(err)
	}
	id := in.Msg.Inbound.Id
	r.exec(`UPDATE inbound SET state = 'active' WHERE id = ?`, id)
	r.s.invalidateSnapshot()
	sn, err := r.s.snapshot(r.ctx)
	if err != nil {
		r.t.Fatal(err)
	}
	tg := sn.targets[id]
	if tg.err != nil {
		r.t.Fatalf("the panel cannot build the inbound: %v", tg.err)
	}
	list := []statehash.Inbound{{Spec: tg.spec, Creds: []plugin.UserCred{}}}
	if withProbe {
		list = r.s.WithProbeCreds(r.ctx, list)
		if len(list[0].Creds) != 1 {
			r.t.Fatalf("no system credential in the desired state: %+v", list[0].Creds)
		}
	}
	r.startServer(tg.spec, list[0].Creds)
	return id
}

// startServer runs the node side of one inbound.
func (r *awgRig) startServer(spec plugin.InboundSpec, creds []plugin.UserCred) {
	t := r.t
	ns, err := awgcfg.ParseSettings(spec.Settings)
	if err != nil {
		t.Fatal(err)
	}
	priv, ok := awgcfg.DecodeKey(ns.PrivateKey)
	if !ok {
		t.Fatal("the spec has no server key")
	}
	r.gw = spec.Tunnel.AddrV4.Addr()
	// the node also answers as 1.1.1.1: the DNS of a profile without a user's preset, reached through the tunnel
	tdev, tnet, err := netstack.CreateNetTUN([]netip.Addr{r.gw, netip.MustParseAddr("1.1.1.1")}, nil, int(spec.Tunnel.MTU))
	if err != nil {
		t.Fatal(err)
	}
	// the node is a long-lived process whose first packet is long gone: the same gate as the client's makes the test node
	// one that has already carried traffic, so that no round pays for the quirk the gate documents
	gate := newGatedTun(tdev)
	dev := device.NewDevice(gate, r.binds[1], device.NewLogger(device.LogLevelSilent, ""))
	t.Cleanup(dev.Close)
	err = dev.IpcSet(awguapi.DeviceSet(ns.Version, priv, spec.Listen.Port, ns.Obfuscation))
	gate.release()
	if err != nil {
		t.Fatal(err)
	}
	var peers []awgcfg.Peer
	for _, c := range creds {
		var nd awg.NodeData
		if err := json.Unmarshal(c.Data, &nd); err != nil {
			t.Fatal(err)
		}
		pub, ok1 := awgcfg.DecodeKey(nd.PublicKey)
		psk, ok2 := awgcfg.DecodeKey(nd.PSK)
		if !ok1 || !ok2 {
			t.Fatalf("credential %s has a bad key", c.CredID)
		}
		p := awgcfg.Peer{PublicKey: pub, PSK: &psk}
		for _, a := range nd.AllowedIPs {
			pf := netip.MustParsePrefix(a)
			// what the node's parsePeers insists on: one address, inside the client network, not the node's own
			if pf.Bits() != pf.Addr().BitLen() || (pf.Addr().Is4() && (!spec.Tunnel.AddrV4.Masked().Contains(pf.Addr()) || pf.Addr() == r.gw)) {
				t.Fatalf("credential %s: allowed ip %s is not a peer address of %s", c.CredID, a, spec.Tunnel.AddrV4)
			}
			p.AllowedIPs = append(p.AllowedIPs, pf)
		}
		peers = append(peers, p)
	}
	if len(peers) > 0 {
		if err := dev.IpcSet(awguapi.PeersSet(false, peers)); err != nil {
			t.Fatal(err)
		}
	}
	if err := dev.Up(); err != nil {
		t.Fatal(err)
	}
	r.srv = dev

	ln, err := tnet.ListenTCP(&net.TCPAddr{Port: 80})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/generate_204", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("/cdn-cgi/trace", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("fl=1\nip=203.0.113.9\nloc=de\nts=1\n")) })
	hs := &http.Server{Handler: mux}
	go hs.Serve(ln)
	t.Cleanup(func() { hs.Close() })

	pc, err := tnet.ListenUDP(&net.UDPAddr{IP: net.IPv4(1, 1, 1, 1), Port: 53})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() { // every name is the node itself
		buf := make([]byte, 512)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			var q dnsmessage.Message
			if q.Unpack(buf[:n]) != nil || len(q.Questions) != 1 {
				continue
			}
			r.dnsQueries.Add(1)
			a := dnsmessage.Message{Header: dnsmessage.Header{ID: q.ID, Response: true, Authoritative: true}, Questions: q.Questions}
			if q.Questions[0].Type == dnsmessage.TypeA {
				a.Answers = []dnsmessage.Resource{{Header: dnsmessage.ResourceHeader{Name: q.Questions[0].Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60},
					Body: &dnsmessage.AResource{A: r.gw.As4()}}}
			}
			if out, err := a.Pack(); err == nil {
				pc.WriteTo(out, from)
			}
		}
	}()
	r.s.cfg.Probe204URL, r.s.cfg.ProbeTraceURL = "http://"+r.gw.String()+"/generate_204", "http://"+r.gw.String()+"/cdn-cgi/trace"
}

func (r *awgRig) attemptOn(id string) Result {
	r.t.Helper()
	// WireGuard drops a handshake initiation that follows another one of the same peer by less than 20 ms (flood protection).
	// The rounds of an inbound are 15 s apart at the least; tests that attempt back to back keep that much apart.
	if d := 40*time.Millisecond - time.Since(r.lastAttempt); d > 0 {
		time.Sleep(d)
	}
	defer func() { r.lastAttempt = time.Now() }()
	sn, err := r.s.snapshot(r.ctx)
	if err != nil {
		r.t.Fatal(err)
	}
	res, skipped := r.s.attempt(r.ctx, sn.targets[id])
	if skipped {
		r.t.Fatal("skipped")
	}
	return res
}

// peerStat is what the server saw of the probe peer.
func (r *awgRig) peerStat(inboundID string) awgcfg.PeerStat {
	r.t.Helper()
	row, err := r.st.ProbeCred(r.ctx, inboundID)
	if err != nil {
		r.t.Fatal(err)
	}
	var nd awg.NodeData
	if err := json.Unmarshal([]byte(row.DataJSON), &nd); err != nil {
		r.t.Fatal(err)
	}
	pub, _ := awgcfg.DecodeKey(nd.PublicKey)
	text, err := r.srv.IpcGet()
	if err != nil {
		r.t.Fatal(err)
	}
	stats, err := awguapi.ParseStats(text)
	if err != nil {
		r.t.Fatal(err)
	}
	for _, s := range stats {
		if s.PublicKey == pub {
			return s
		}
	}
	r.t.Fatal("the server does not hold the probe peer")
	return awgcfg.PeerStat{}
}

const awgV20 = `{"version":"2.0","obfuscation":{"preset":"stun","h1":"1","h2":"2","h3":"3","h4":"4","random_trailers":false,"content_padding_addition":"","rekey_after_time":"","rekey_timeout":"","reject_after_time":"","keepalive_timeout":"","max_handshake_attempts":"","persistent_keepalive":"25"}}`

func TestAWGProbeAgainstARealServer(t *testing.T) {
	for name, settings := range map[string]string{
		"defaults (3.1)":          "",
		"2.0":                     awgV20,
		"per-device signatures":   `{"obfuscation":{"preset":"dns","per_device_signature":true}}`,
		"no keepalive in profile": `{"obfuscation":{"persistent_keepalive":""}}`,
	} {
		t.Run(name, func(t *testing.T) {
			r := newAWGRig(t)
			id := r.deploy("awg", settings, true)
			res := r.attemptOn(id)
			if res.Status != cOK || res.ExitIP != "203.0.113.9" || res.ExitCountry != "DE" || res.LatencyMS == 0 || res.ErrorCode != "" {
				t.Fatalf("%+v", res)
			}
			// handshake and both probes in a few tens of milliseconds: a first packet lost to the padding quirk (gatedTun) would
			// cost a TCP retransmission, one to two seconds
			if res.LatencyMS > 500 {
				t.Fatalf("latency %d ms: the first connection lost a packet", res.LatencyMS)
			}
			if res.ErrorDetail != "" && !strings.Contains(res.ErrorDetail, "differs") { // the exit is not the node's literal address: only noted
				t.Fatalf("detail %q", res.ErrorDetail)
			}
			// the traffic really went through the probe's own peer
			st := r.peerStat(id)
			if st.LastHS.IsZero() || st.RxBytes == 0 || st.TxBytes == 0 {
				t.Fatalf("the server's view of the probe peer: %+v", st)
			}
		})
	}
}

// The AWG row of the checks table stops being "n/a": the inbound is probed, a round is stored, and it is the same kind
// of row as a Hysteria2 one.
func TestAWGRoundIsStoredAndShownAsARealRow(t *testing.T) {
	r := newAWGRig(t)
	id := r.deploy("awg", "", true)
	r.s.invalidateSnapshot()
	sn, _ := r.s.snapshot(r.ctx)
	if why := r.s.skipReason(sn.targets[id], store.NodeLiveRow{Connected: true}); why != "" {
		t.Fatalf("skip reason %q: the inbound would still be n/a", why)
	}
	res, ok := r.s.round(r.ctx, id)
	if !ok || res.Status != cOK {
		t.Fatalf("%+v ok=%v", res, ok)
	}
	rows, _ := r.st.RecentSamples(r.ctx, id, 5)
	if len(rows) != 1 || rows[0].Status != int(cOK) || rows[0].ExitCountry != "DE" || rows[0].LatencyMS == 0 {
		t.Fatalf("samples: %+v", rows)
	}
	got, err := rpc{r.s}.GetChecks(r.ctx, connect.NewRequest(&adminv1.GetChecksRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	var cell *adminv1.CheckCell
	for _, row := range got.Msg.Rows {
		for _, c := range row.Cells {
			if c.InboundId == id {
				cell = c
			}
		}
	}
	if cell == nil || cell.Last == nil || cell.Last.Status != cOK || cell.Last.ExitCountry != "DE" || cell.Last.ErrorCode == "client_unsupported" {
		t.Fatalf("cell: %+v", cell)
	}
	if len(got.Msg.Columns) != 1 || got.Msg.Columns[0].Protocol != "awg" {
		t.Fatalf("columns: %+v", got.Msg.Columns)
	}
}

func TestAWGProbeDiagnosesWhatWentWrong(t *testing.T) {
	t.Run("a node that does not know the probe peer: no handshake, within the budget", func(t *testing.T) {
		r := newAWGRig(t)
		id := r.deploy("awg", "", false) // the server holds no system credential
		if _, err := r.s.probeCred(r.ctx, id, "awg"); err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		res := r.attemptOn(id)
		if res.Status != cFail || res.ErrorCode != "timeout" || !strings.Contains(res.ErrorDetail, "handshake") {
			t.Fatalf("%+v", res)
		}
		if d := time.Since(start); d > 4*time.Second {
			t.Fatalf("the attempt took %v with a 2 s handshake budget", d)
		}
	})
	t.Run("the exit cannot reach the probe targets", func(t *testing.T) {
		r := newAWGRig(t)
		id := r.deploy("awg", "", true)
		ok204 := r.s.cfg.Probe204URL
		r.s.cfg.Probe204URL, r.s.cfg.ProbeTraceURL = "http://"+r.gw.String()+":81/generate_204", "http://"+r.gw.String()+":81/cdn-cgi/trace"
		res := r.attemptOn(id)
		if res.Status != cFail || res.ErrorCode != "exit_unreachable" || res.LatencyMS != 0 {
			t.Fatalf("%+v", res)
		}
		r.s.cfg.Probe204URL = ok204 // one probe down: the tunnel works, degraded
		res = r.attemptOn(id)
		if res.Status != cDeg || res.ErrorCode != "exit_unreachable" || res.ExitIP != "" || res.LatencyMS == 0 {
			t.Fatalf("one probe down: %+v", res)
		}
	})
	t.Run("a hostname is resolved inside the tunnel, with the DNS of a real config", func(t *testing.T) {
		r := newAWGRig(t)
		id := r.deploy("awg", "", true)
		// ".test" resolves nowhere but at the node: the answer can only have come through the tunnel from 1.1.1.1, the
		// first of the pair a config of a profile without a user preset carries
		r.s.cfg.Probe204URL, r.s.cfg.ProbeTraceURL = "http://probe.test/generate_204", "http://probe.test/cdn-cgi/trace"
		res := r.attemptOn(id)
		if res.Status != cOK || res.ExitIP != "203.0.113.9" {
			t.Fatalf("%+v", res)
		}
		if n := r.dnsQueries.Load(); n < 2 {
			t.Fatalf("the node answered %d DNS queries", n)
		}
	})
	t.Run("a profile the panel cannot make a client of is skipped, not failed", func(t *testing.T) {
		r := newAWGRig(t)
		id := r.deploy("awg", "", true)
		r.exec(`UPDATE inbound SET plugin_public_json = '{}' WHERE id = ?`, id) // no server key to present to
		r.s.invalidateSnapshot()
		sn, _ := r.s.snapshot(r.ctx)
		if _, skipped := r.s.attempt(r.ctx, sn.targets[id]); !skipped {
			t.Fatal("an unusable inbound was reported as a failure of the node")
		}
	})
}

// A round opens a device and closes it: a hundred rounds must not leave a hundred devices' goroutines behind.
func TestAWGProbeRoundsDoNotLeakDevices(t *testing.T) {
	r := newAWGRig(t)
	id := r.deploy("awg", "", true)
	settled := func() int {
		n := 0
		for i := 0; i < 40; i++ { // the goroutines of a closed device end a moment after Close returns
			time.Sleep(50 * time.Millisecond)
			if m := runtime.NumGoroutine(); i > 0 && m == n {
				return m
			} else {
				n = m
			}
		}
		return n
	}
	if res := r.attemptOn(id); res.Status != cOK { // warm up: the first round starts the shared machinery
		t.Fatalf("%+v", res)
	}
	base := settled()
	const rounds = 8
	for i := range rounds {
		if res := r.attemptOn(id); res.Status != cOK {
			t.Fatalf("round %d: %+v", i, res)
		}
	}
	if after := settled(); after > base+4 { // a leaking device is dozens of goroutines each
		t.Fatalf("goroutines: %d before %d rounds, %d after", base, rounds, after)
	}
}
