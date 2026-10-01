// Integration check with the real hysteria2 engine, certificate source, egress and decoy of this tree
// (owned by node-hy2): desired state flows into the real engine and the state hash of what it holds
// equals the panel's. If their constructors change, adjust this test, not the agent.

package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/decoy"
	"github.com/mistgate/mistgate/internal/node/certs"
	"github.com/mistgate/mistgate/internal/node/egress"
	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/node/hysteria2"
)

// TestMain keeps the real engine of this package on loopback: a test binary that listens on all interfaces makes Windows
// ask for a firewall rule on each run.
func TestMain(m *testing.M) {
	hysteria2.BindIP = net.IPv4(127, 0, 0, 1)
	os.Exit(m.Run())
}

func TestRealHysteria2EngineUnderAgent(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true})
	var a *Agent
	direct := egress.New(func() []string { return a.DNS() }, egress.AllowPrivate())
	var err error
	a, err = New(Config{
		StateDir: h.dir, Certs: certs.New(t.TempDir()),
		Egress:     func(string) (engine.Egress, error) { return direct, nil },
		Masquerade: func(string) http.Handler { return decoy.Handler() },
	}, map[string]engine.Factory{"hysteria2": hysteria2.Factory}, h.host)
	if err != nil {
		t.Fatal(err)
	}
	a.statsEvery, a.backoffMin, a.backoffMax = 50*time.Millisecond, 20*time.Millisecond, 100*time.Millisecond
	h.a = a
	h.start()
	h.waitConnected()

	pc, _ := net.ListenPacket("udp", "127.0.0.1:0")
	port := uint32(pc.LocalAddr().(*net.UDPAddr).Port)
	pc.Close()
	tok := func(s string) string { x := sha256.Sum256([]byte(s)); return hex.EncodeToString(x[:]) }
	mk := func(id, token string) *pb.Credential {
		return &pb.Credential{CredId: id, UserId: "u", DeviceId: "d", DataJson: `{"auth_sha256":"` + tok(token) + `"}`}
	}
	in := &pb.InboundState{InboundId: "inb_hy", CredsReplace: true, Creds: []*pb.Credential{mk("crd_a", "a"), mk("crd_b", "b")}, Spec: &pb.InboundSpec{
		InboundId: "inb_hy", Protocol: "hysteria2", ProfileId: "prf", SpecVersion: 1, Enabled: true,
		Listen: &pb.Listen{Network: "udp", Port: port}, Tls: &pb.Tls{Mode: pb.TlsMode_TLS_MODE_SELF_SIGNED, ServerName: "example.com"},
		Egress: "direct", SettingsJson: `{"masquerade":{"tcp_port":0}}`,
	}}
	ds := &pb.DesiredState{Revision: 1, Inbounds: []*pb.InboundState{in}}
	h.panel.push(ds)
	r := h.panel.nextApply()
	t.Logf("apply: %v", r)
	if r.Status != pb.ApplyStatus_APPLY_STATUS_APPLIED || r.Inbounds[0].CertPinSha256 == "" {
		t.Fatalf("not applied: %v", r)
	}
	if r.StateHash != expectedHash(t, ds) {
		t.Fatalf("hash of what the real engine holds differs from the panel's: %s vs %s", r.StateHash, expectedHash(t, ds))
	}
	d2 := &pb.DesiredState{Revision: 2, BaseRevision: 1, Inbounds: []*pb.InboundState{{InboundId: "inb_hy", Creds: []*pb.Credential{mk("crd_c", "c")}, RemovedCredIds: []string{"crd_a"}}}}
	h.panel.push(d2)
	r = h.panel.nextApply()
	if r.Status != pb.ApplyStatus_APPLY_STATUS_APPLIED || r.Inbounds[0].Restarted || r.Inbounds[0].CredCount != 2 {
		t.Fatalf("delta: %v", r)
	}
	if r.StateHash != expectedHash(t, ds, d2) {
		t.Fatalf("hash after delta differs")
	}
	h.panel.send(&pb.ConnectResponse{Message: &pb.ConnectResponse_Kick{Kick: &pb.Kick{RequestId: "k", CredIds: []string{"crd_b"}}}})
	t.Logf("kick: %v", h.panel.nextCmd())
}
