package health

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/node/certs"
	"github.com/mistgate/mistgate/internal/node/egress"
	"github.com/mistgate/mistgate/internal/node/engine"
	nodehy2 "github.com/mistgate/mistgate/internal/node/hysteria2"
	panelhy2 "github.com/mistgate/mistgate/internal/panel/protocols/hysteria2"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/statehash"
)

// TestMain keeps every socket of this package on loopback: a test binary that listens on all interfaces makes Windows ask
// for a firewall rule on each run. Covers the real engine (BindIP) and the panel client probing it.
func TestMain(m *testing.M) {
	nodehy2.BindIP = net.IPv4(127, 0, 0, 1)
	probeBindIP = net.IPv4(127, 0, 0, 1)
	os.Exit(m.Run())
}

// The checker against a real Hysteria2 engine on loopback (the approach of internal/node/hysteria2/
// panelcompat_test.go): the desired state the panel builds, credential included, is applied to the engine,
// and the panel's own client connects to it and probes through the tunnel.

type engineRig struct {
	*env
	e       engine.Engine
	spec    plugin.InboundSpec // what the engine runs (patched for the test), for restart
	creds   []plugin.UserCred
	udpPort int
	probe   *probeServer
}

func freeUDP(t *testing.T) int {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	return pc.LocalAddr().(*net.UDPAddr).Port
}

func newEngineRig(t *testing.T) *engineRig {
	t.Helper()
	r := &engineRig{probe: newProbeServer(t)}
	// the probe servers listen on loopback: the probes address them by IP, the node's egress is allowed to reach it
	_, port, _ := net.SplitHostPort(r.probe.srv.Listener.Addr().String())
	r.probe.url204, r.probe.urlTr = "http://127.0.0.1:"+port+"/generate_204", "http://127.0.0.1:"+port+"/cdn-cgi/trace"
	r.env = newEnv(t, func(c *Config) {
		c.Probe204URL, c.ProbeTraceURL = r.probe.url204, r.probe.urlTr
		c.HandshakeTimeout = 2 * time.Second
		c.ProbeTimeout = 3 * time.Second
	})
	direct := egress.New(func() []string { return nil }, egress.AllowPrivate())
	e, err := nodehy2.New(engine.Env{
		Certs:  certs.New(t.TempDir()),
		Egress: func(string) (engine.Egress, error) { return direct, nil },
		DNS:    func() []string { return nil },
		Now:    time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	r.e = e
	t.Cleanup(func() { e.Close(context.Background()) })
	r.udpPort = freeUDP(t)
	return r
}

// deploy creates a profile with the obfuscation, puts it on a node at 127.0.0.1 and applies the panel's desired
// state of the inbound (with the system credential) to the engine, like the agent would. It returns the inbound id.
func (r *engineRig) deploy(obfs string, withProbeCred bool) string {
	r.t.Helper()
	raw, err := panelhy2.New().DefaultSettings()
	if err != nil {
		r.t.Fatal(err)
	}
	var s panelhy2.Settings
	if err := json.Unmarshal(raw, &s); err != nil {
		r.t.Fatal(err)
	}
	s.TLSMode, s.SNI, s.Masquerade.Type, s.Obfs.Type = "self_signed", "node.example.com", "none", obfs
	if obfs == "none" {
		s.Obfs.Password = ""
	}
	if raw, err = json.Marshal(s); err != nil {
		r.t.Fatal(err)
	}
	r.exec(`INSERT INTO node (id, name, address, provider, state, created_at, last_seen_at, last_connected_at) VALUES ('nod_l', 'lo', '127.0.0.1', 'lo', 'active', 1, 1, 1)
		ON CONFLICT (id) DO NOTHING`)
	r.fl.set("nod_l", liveState{up: true, caps: []string{capDoctor}})
	p, err := r.acc.CreateProfile(r.ctx, connect.NewRequest(&adminv1.CreateProfileRequest{Protocol: "hysteria2", Name: "p-" + obfs, SettingsJson: string(raw)}))
	if err != nil {
		r.t.Fatal(err)
	}
	in, err := r.acc.CreateInbound(r.ctx, connect.NewRequest(&adminv1.CreateInboundRequest{ProfileId: p.Msg.Profile.Id, NodeId: "nod_l", PortOverride: uint32(r.udpPort)}))
	if err != nil {
		r.t.Fatal(err)
	}
	id := in.Msg.Inbound.Id

	sn, err := r.s.snapshot(r.ctx)
	if err != nil {
		r.t.Fatal(err)
	}
	spec := sn.targets[id].spec
	list := []statehash.Inbound{{Spec: spec, Creds: []plugin.UserCred{}}}
	if withProbeCred {
		list = r.s.WithProbeCreds(r.ctx, list)
		if len(list[0].Creds) != 1 {
			r.t.Fatalf("no system credential in the desired state: %+v", list[0].Creds)
		}
	}
	var doc map[string]any // the panel does not send the node's optional masquerade.tcp_port; keep the test off port 443
	if err := json.Unmarshal(spec.Settings, &doc); err != nil {
		r.t.Fatal(err)
	}
	doc["masquerade"].(map[string]any)["tcp_port"] = 0
	if spec.Settings, err = json.Marshal(doc); err != nil {
		r.t.Fatal(err)
	}
	r.spec, r.creds = spec, list[0].Creds
	rep, err := r.e.Apply(r.ctx, spec, list[0].Creds)
	if err != nil {
		r.t.Fatalf("the engine rejects the panel's spec: %v", err)
	}
	r.exec(`UPDATE inbound SET state = 'active', cert_pin_sha256 = ?, cert_not_after = ? WHERE id = ?`, rep.Cert.PinSHA256, rep.Cert.NotAfter.Unix(), id)
	r.s.invalidateSnapshot()
	return id
}

func (r *engineRig) attemptOn(id string) Result {
	r.t.Helper()
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

func TestCheckerAgainstARealEngine(t *testing.T) {
	for _, obfs := range []string{"salamander", "gecko", "none"} {
		t.Run(obfs, func(t *testing.T) {
			r := newEngineRig(t)
			id := r.deploy(obfs, true)
			res := r.attemptOn(id)
			if res.Status != cOK || res.ExitIP != "203.0.113.9" || res.ExitCountry != "DE" || res.LatencyMS == 0 || res.ErrorCode != "" {
				t.Fatalf("%+v", res)
			}
			if res.ErrorDetail != "" && !strings.Contains(res.ErrorDetail, "differs") {
				t.Fatalf("detail %q", res.ErrorDetail)
			}
			// the probe traffic is the system credential's (the engine counts by credential id)
			c, err := r.e.Collect(r.ctx)
			if err != nil {
				t.Fatal(err)
			}
			row, _ := r.st.ProbeCred(r.ctx, id)
			var got bool
			for _, tr := range c.Traffic {
				got = got || (tr.CredID == row.CredID && tr.Down > 0)
			}
			if !got {
				t.Fatalf("no traffic under the system credential %s: %+v", row.CredID, c.Traffic)
			}
		})
	}
}

func TestCheckerDiagnosesWhatWentWrong(t *testing.T) {
	r := newEngineRig(t)
	id := r.deploy("salamander", true)
	if res := r.attemptOn(id); res.Status != cOK {
		t.Fatalf("baseline: %+v", res)
	}

	t.Run("wrong certificate pin is tls", func(t *testing.T) {
		r.exec(`UPDATE inbound SET cert_pin_sha256 = ? WHERE id = ?`, strings.Repeat("ab", 32), id)
		r.s.invalidateSnapshot()
		res := r.attemptOn(id)
		if res.Status != cFail || res.ErrorCode != "tls" {
			t.Fatalf("%+v", res)
		}
	})
	t.Run("no pin yet is a skip, not a failure", func(t *testing.T) {
		r.exec(`UPDATE inbound SET cert_pin_sha256 = '' WHERE id = ?`, id)
		r.s.invalidateSnapshot()
		sn, _ := r.s.snapshot(r.ctx)
		if _, skipped := r.s.attempt(r.ctx, sn.targets[id]); !skipped {
			t.Fatal("a pinless self-signed inbound was dialled")
		}
	})

	t.Run("a credential the node does not know is auth", func(t *testing.T) {
		r2 := newEngineRig(t)
		id2 := r2.deploy("salamander", false) // the engine holds no system credential
		if _, err := r2.s.probeCred(r2.ctx, id2, "hysteria2"); err != nil {
			t.Fatal(err)
		}
		res := r2.attemptOn(id2)
		if res.Status != cFail || res.ErrorCode != "auth" {
			t.Fatalf("%+v", res)
		}
	})

	t.Run("the exit cannot reach the probe targets", func(t *testing.T) {
		r3 := newEngineRig(t)
		id3 := r3.deploy("salamander", true)
		r3.s.cfg.Probe204URL, r3.s.cfg.ProbeTraceURL = "http://127.0.0.1:1/generate_204", "http://127.0.0.1:1/cdn-cgi/trace"
		res := r3.attemptOn(id3)
		if res.Status != cFail || res.ErrorCode != "exit_unreachable" || res.LatencyMS != 0 {
			t.Fatalf("%+v", res)
		}
		r3.s.cfg.Probe204URL = r3.probe.url204 // only one probe down: the tunnel works, degraded
		res = r3.attemptOn(id3)
		if res.Status != cDeg || res.ErrorCode != "exit_unreachable" || res.ExitIP != "" || res.LatencyMS == 0 {
			t.Fatalf("one probe down: %+v", res)
		}
	})

	t.Run("a silent network is a timeout within the handshake budget", func(t *testing.T) {
		r4 := newEngineRig(t)
		id4 := r4.deploy("salamander", true)
		// the node's address now leads to a socket that swallows every packet (UDP cut by the hoster)
		hole, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { hole.Close() })
		go func() {
			buf := make([]byte, 2048)
			for {
				if _, _, err := hole.ReadFrom(buf); err != nil {
					return
				}
			}
		}()
		r4.exec(`UPDATE inbound SET port_override = ? WHERE id = ?`, hole.LocalAddr().(*net.UDPAddr).Port, id4)
		r4.s.invalidateSnapshot()
		start := time.Now()
		res := r4.attemptOn(id4)
		if res.Status != cFail || res.ErrorCode != "timeout" || !strings.Contains(res.ErrorDetail, "handshake") {
			t.Fatalf("%+v", res)
		}
		if d := time.Since(start); d > 4*time.Second {
			t.Fatalf("the attempt took %v with a 2 s handshake budget", d)
		}
	})
}

// A full round through the scheduler machinery against the real engine leaves a stored OK sample and a clean cell.
func TestRoundAgainstARealEngineIsStored(t *testing.T) {
	r := newEngineRig(t)
	id := r.deploy("salamander", true)
	res, ok := r.s.round(r.ctx, id)
	if !ok || res.Status != cOK {
		t.Fatalf("%+v ok=%v", res, ok)
	}
	rows, _ := r.st.RecentSamples(r.ctx, id, 5)
	if len(rows) != 1 || rows[0].Status != int(cOK) || rows[0].ExitCountry != "DE" {
		t.Fatalf("samples: %+v", rows)
	}
}
