package mtproto

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/plugin"
)

func TestParseSpec(t *testing.T) {
	base := plugin.InboundSpec{ID: "i1", Protocol: Protocol, Enabled: true, Listen: plugin.Listen{Network: "tcp", Port: 443}}
	if _, err := parseSpec(base); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*plugin.InboundSpec){
		"protocol": func(s *plugin.InboundSpec) { s.Protocol = "vless" },
		"network":  func(s *plugin.InboundSpec) { s.Listen.Network = "udp" },
		"port":     func(s *plugin.InboundSpec) { s.Listen.Port = 0 },
		"hop":      func(s *plugin.InboundSpec) { s.Listen.HopFrom, s.Listen.HopTo = 1000, 2000 },
		"egress":   func(s *plugin.InboundSpec) { s.Egress = "warp" },
	} {
		s := base
		mutate(&s)
		if _, err := parseSpec(s); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	disabled := base
	disabled.Enabled = false
	disabled.Listen.Port = 0
	if _, err := parseSpec(disabled); err != nil {
		t.Fatalf("disabled: %v", err)
	}
}

func TestEngineLifecycle(t *testing.T) {
	oldBind := BindIP
	BindIP = net.IPv4(127, 0, 0, 1)
	defer func() { BindIP = oldBind }()

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	e, err := New(engine.Env{Log: log})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close(context.Background())

	port := freePort(t)
	spec := plugin.InboundSpec{
		ID: "i1", Protocol: Protocol, Enabled: true,
		Listen: plugin.Listen{Network: "tcp", Port: port},
	}
	c1 := plugin.UserCred{CredID: "c1", Data: json.RawMessage(`{"key":"00112233445566778899aabbccddeeff"}`)}
	rep, err := e.Apply(context.Background(), spec, []plugin.UserCred{c1})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Restarted || rep.CredCount != 1 {
		t.Fatalf("apply report %+v", rep)
	}
	if h := e.Health(); len(h) != 1 || h[0].State != plugin.RunRunning {
		t.Fatalf("health %+v", h)
	}

	raw, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))), time.Second)
	if err != nil {
		t.Fatalf("listener: %v", err)
	}
	_ = raw.Close()

	c2 := plugin.UserCred{CredID: "c2", Data: json.RawMessage(`{"key":"ffeeddccbbaa99887766554433221100"}`)}
	rep, err = e.Apply(context.Background(), spec, []plugin.UserCred{c2})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Restarted {
		t.Fatal("credential-only apply restarted the listener")
	}
	observed := e.Observed()
	if len(observed) != 1 || len(observed[0].Creds) != 1 || observed[0].Creds[0].CredID != "c2" {
		t.Fatalf("observed %+v", observed)
	}
	if n, err := e.Kick(context.Background(), []string{"c2"}); err != nil || n != 0 {
		t.Fatalf("kick n=%d err=%v", n, err)
	}
	if err := e.Remove(context.Background(), "i1"); err != nil {
		t.Fatal(err)
	}
	if raw, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port))), 100*time.Millisecond); err == nil {
		_ = raw.Close()
		t.Fatal("listener survived Remove")
	}
}

func freePort(t *testing.T) uint16 {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return uint16(port)
}
