package agent

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/node/update"
)

func TestStartReason(t *testing.T) {
	for _, c := range []struct {
		name      string
		prev, cur string
		updated   bool
		up        time.Duration
		want      string
	}{
		{"new version", "0.1.0-a", "0.1.0-b", false, 0, reasonUpdate},
		{"update marker, first run in this dir", "", "0.1.0-b", true, 0, reasonUpdate},
		{"update wins over a fresh boot", "0.1.0-a", "0.1.0-b", false, time.Minute, reasonUpdate},
		{"same version, host just booted", "0.1.0-a", "0.1.0-a", false, 2 * time.Minute, reasonBoot},
		{"same version, host up for days", "0.1.0-a", "0.1.0-a", false, 72 * time.Hour, reasonRestart},
		{"first start ever, uptime unknown", "", "0.1.0-a", false, 0, reasonRestart},
		{"first start ever, host just booted", "", "0.1.0-a", false, time.Minute, reasonBoot},
	} {
		if got := startReason(c.prev, c.cur, c.updated, c.up); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

func (p *fakePanel) eventsByCode(code string) []*pb.Event {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []*pb.Event
	for _, e := range p.events {
		if e.Code == code {
			out = append(out, e)
		}
	}
	return out
}

// runAgent starts one more agent process on the harness's state directory and panel.
func (h *harness) runAgent(cfg Config) (stop func()) {
	h.t.Helper()
	cfg.StateDir, cfg.Log = h.dir, slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg.DoctorEnv = h.doctorEnv
	a, err := New(cfg, map[string]engine.Factory{"fake": newFakeEngine("fake").factory()}, &fakeHost{})
	if err != nil {
		h.t.Fatal(err)
	}
	a.statsEvery, a.backoffMin, a.backoffMax = 20*time.Millisecond, 20*time.Millisecond, 100*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	return func() { cancel(); <-done }
}

func TestAgentStartedOncePerStartWithVersions(t *testing.T) {
	h := newHarness(t, harnessOpts{cfg: func(c *Config) { c.Version = "0.1.0-a" }})
	h.waitConnected()
	eventually(t, func() bool { return h.panel.hasEvent("agent_started") }, "agent_started event")
	first := h.panel.eventsByCode("agent_started")
	if len(first) != 1 {
		t.Fatalf("agent_started sent %d times by one start", len(first))
	}
	p := first[0].Params
	if p["version"] != "0.1.0-a" || p["reason"] != reasonRestart || p["prev_version"] != "" {
		t.Fatalf("first start: %v", p)
	}
	// a reconnect is not a start
	h.panel.dropConn()
	eventually(t, func() bool { return h.panel.connCount() >= 2 }, "reconnect")
	if n := len(h.panel.eventsByCode("agent_started")); n != 1 {
		t.Fatalf("a reconnect produced agent_started (%d)", n)
	}
	if err := h.stop(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(h.dir, fileLastVersion)); strings.TrimSpace(string(b)) != "0.1.0-a" {
		t.Fatalf("remembered version %q", b)
	}

	// the same dir, another version: an update, with where it came from
	stop := h.runAgent(Config{Version: "0.1.0-b"})
	defer stop()
	eventually(t, func() bool { return len(h.panel.eventsByCode("agent_started")) == 2 }, "second agent_started")
	p = h.panel.eventsByCode("agent_started")[1].Params
	if p["version"] != "0.1.0-b" || p["prev_version"] != "0.1.0-a" || p["reason"] != reasonUpdate {
		t.Fatalf("after the update: %v", p)
	}
}

func TestAgentStartedUpdateMarkerNamesPreviousVersion(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true, cfg: func(c *Config) { c.Version = "0.1.0-b" }})
	h.a.upMarker = &update.Marker{FromVersion: "0.1.0-a", ToVersion: "0.1.0-b"}
	h.start()
	h.waitConnected()
	eventually(t, func() bool { return h.panel.hasEvent("agent_started") }, "agent_started event")
	p := h.panel.eventsByCode("agent_started")[0].Params
	if p["reason"] != reasonUpdate || p["prev_version"] != "0.1.0-a" || p["version"] != "0.1.0-b" {
		t.Fatalf("%v", p)
	}
}

func TestEngineEventsCarryProtocolAndReason(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.waitConnected()
	h.panel.push(fullState(1, inb("inb_1", 0, 0, cred("crd_a"), cred("crd_b"))))
	h.panel.nextApply()
	eventually(t, func() bool { return h.panel.hasEvent("engine_started") && h.panel.hasEvent("state_applied") }, "engine_started + state_applied")
	// A profile added while the agent runs was asked for; it did not come back with the process.
	if p := h.panel.eventsByCode("engine_started")[0].Params; p["protocol"] != "fake" || p["reason"] != "profile_added" {
		t.Errorf("engine_started: %v", p)
	}
	if p := h.panel.eventsByCode("state_applied")[0].Params; p["added"] != "1" || p["removed"] != "0" || p["changed"] != "0" || p["users"] != "2" || p["inbounds"] != "1" {
		t.Errorf("state_applied: %v", p)
	}

	h.panel.send(&pb.ConnectResponse{Message: &pb.ConnectResponse_RestartInbound{RestartInbound: &pb.RestartInbound{RequestId: "req_1", InboundId: "inb_1"}}})
	h.panel.nextCmd()
	eventually(t, func() bool { return h.panel.hasEvent("engine_restarted") }, "engine_restarted")
	if p := h.panel.eventsByCode("engine_restarted")[0].Params; p["protocol"] != "fake" || p["reason"] != "restart_command" {
		t.Errorf("engine_restarted: %v", p)
	}

	// The persisted state coming back after a process start is "agent_start".
	if err := h.stop(); err != nil {
		t.Fatal(err)
	}
	stop := h.runAgent(Config{})
	defer stop()
	eventually(t, func() bool { return len(h.panel.eventsByCode("engine_started")) == 2 }, "engine_started after restart")
	if p := h.panel.eventsByCode("engine_started")[1].Params; p["reason"] != "agent_start" {
		t.Errorf("engine_started after a process start: %v", p)
	}
}

func TestDiffModels(t *testing.T) {
	cur, _ := merge(newModel(), fullState(1, inb("inb_1", 0, 0, cred("crd_a")), inb("inb_gone", 0, 0, cred("crd_x"))), func(string) bool { return true })
	next, _ := merge(newModel(), fullState(2, inb("inb_1", 0, 0, cred("crd_a"), cred("crd_b")), inb("inb_new", 0, 0)), func(string) bool { return true })
	added, removed, changed, users := diffModels(cur, next)
	if added != 1 || removed != 1 || changed != 0 || users != 2 { // crd_b added on inb_1, crd_x gone with inb_gone
		t.Fatalf("added=%d removed=%d changed=%d users=%d", added, removed, changed, users)
	}
}
