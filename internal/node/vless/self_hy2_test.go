package vless

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"testing"

	"github.com/mistgate/mistgate/internal/node/certs"
	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/node/hysteria2"
	"github.com/mistgate/mistgate/internal/plugin"
)

// A node with Hysteria2 and a VLESS profile whose REALITY target is the node's own site, both on TCP P: Hysteria2 closes its
// site listener while VLESS holds the port (the agent's TCP claims), VLESS shows the site in its place with the same
// certificate source, and Hysteria2 gets its listener back when VLESS goes (design/vless.md sections 7.5 and 7.8).
func TestSelfSiteTakesTheHysteria2SitePortAndGivesItBack(t *testing.T) {
	oldVLESS, oldHY2 := BindIP, hysteria2.BindIP
	BindIP, hysteria2.BindIP = net.ParseIP("127.0.0.1"), net.ParseIP("127.0.0.1")
	t.Cleanup(func() { BindIP, hysteria2.BindIP = oldVLESS, oldHY2 })

	source := certs.New(t.TempDir())
	source.DirectoryURL = "http://127.0.0.1:1/directory" // a test never reaches a real CA
	source.HTTPAddr = "127.0.0.1:0"
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	site := func(who string) func(string) http.Handler {
		return func(string) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, who) })
		}
	}
	env := func(who string) engine.Env {
		return engine.Env{
			Log: log, Certs: source, Masquerade: site(who),
			Egress: func(string) (engine.Egress, error) { return localEgress{}, nil },
			DNS:    func() []string { return nil },
		}
	}
	hy2, err := hysteria2.New(env("hysteria2 site"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = hy2.Close(context.Background()) })
	vl, err := New(env(selfMarker))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = vl.Close(context.Background()) })

	port := freeTCPPort(&eng{}, nil)
	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	udpPort := uint16(udp.LocalAddr().(*net.UDPAddr).Port)
	_ = udp.Close()
	hy2Spec := plugin.InboundSpec{
		ID: "inb_hy2", Protocol: "hysteria2", Enabled: true, Egress: "direct",
		Listen:   plugin.Listen{Network: "udp", Port: udpPort},
		TLS:      plugin.TLS{Mode: plugin.TLSSelfSigned, ServerName: selfName},
		Settings: json.RawMessage(fmt.Sprintf(`{"masquerade":{"tcp_port":%d}}`, port)),
	}
	if _, err := hy2.Apply(context.Background(), hy2Spec, nil); err != nil {
		t.Fatal(err)
	}
	if body, _ := visit(t, port, "http/1.1"); body != "hysteria2 site" {
		t.Fatalf("before VLESS the port shows %q", body)
	}

	// The agent hands the claim to every engine before it applies the claimant.
	claims := map[uint16]string{port: "inb_vless"}
	hy2.(engine.TCPPortUser).TCPClaims(context.Background(), claims)
	if _, err := vl.Apply(context.Background(), selfSpec("inb_vless", port, selfName), nil); err != nil {
		t.Fatalf("VLESS could not take the port Hysteria2 let go of: %v", err)
	}
	if body, _ := visit(t, port, "http/1.1"); body != selfMarker {
		t.Fatalf("with VLESS the port shows %q, want the node's own site through REALITY", body)
	}

	// Disabling VLESS: the port is free and Hysteria2's listener is back.
	if err := vl.Remove(context.Background(), "inb_vless"); err != nil {
		t.Fatal(err)
	}
	hy2.(engine.TCPPortUser).TCPClaims(context.Background(), nil)
	if body, _ := visit(t, port, "http/1.1"); body != "hysteria2 site" {
		t.Fatalf("after VLESS the port shows %q", body)
	}
}
