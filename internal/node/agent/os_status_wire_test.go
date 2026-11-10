package agent

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/osupdate"
)

// osStatusFixtureReader is a reader on a throwaway root whose commands answer like a quiet Ubuntu host with one
// pending security update, so a test sees a real snapshot without touching the machine it runs on.
func osStatusFixtureReader(t *testing.T, simulations *atomic.Int32) *osupdate.Reader {
	t.Helper()
	root := t.TempDir()
	for path, body := range map[string]string{
		"etc/os-release":                "ID=ubuntu\nVERSION_ID=\"24.04\"\n",
		"proc/locks":                    "",
		"var/lib/dpkg/status":           "x",
		"var/lib/apt/lists/x_InRelease": "x",
	} {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return &osupdate.Reader{Root: root, Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "apt-get":
			simulations.Add(1)
			return []byte("Inst openssl [3.0.1-1] (3.0.2-1 Ubuntu:24.04/noble-security [amd64])\n"), nil
		case "dpkg-query":
			if len(args) > 2 && args[2] == "unattended-upgrades" {
				return []byte("install ok installed"), nil
			}
		case "apt-config":
			return []byte("L='1'; U='1'; R='false';\n"), nil
		case "systemctl":
			if args[0] == "is-enabled" {
				return []byte("enabled\nenabled\n"), nil
			}
			return []byte("active\nactive\n"), nil
		}
		return nil, nil
	}}
}

func nextOsStatus(h *harness, wait time.Duration) *pb.OsStatus {
	select {
	case s := <-h.panel.osStatus:
		return s
	case <-time.After(wait):
		return nil
	}
}

// The agent offers the capability, stays silent until the panel's settings ask for a snapshot, sends one when they do,
// and sends the stored one again at once on every new connection (the panel keeps one snapshot per node, and a read is
// only repeated when the host changed).
func TestOsStatusIsSentOnEnableAndOnConnect(t *testing.T) {
	asked := &pb.NodeSettings{StatsIntervalS: 10, DnsResolvers: []string{"192.0.2.53"}, OsStatus: true}
	var simulations atomic.Int32
	h := newHarness(t, harnessOpts{
		tune: func(a *Agent) { a.osStatusDelay = 0 },
		cfg: func(c *Config) {
			c.OSUpdateReader = osStatusFixtureReader(t, &simulations)
			c.OSUpdateCapability = func() bool { return true }
		},
	})
	h.waitConnected()
	if hello := h.panel.current().hello; !hasCapability(hello, osupdate.Capability) || hasCapability(hello, "os-update/1") {
		t.Fatalf("Hello capabilities = %v, want os-status/1 and not os-update/1", hello.GetCapabilities())
	}
	if s := nextOsStatus(h, 400*time.Millisecond); s != nil {
		t.Fatalf("a snapshot was sent before the panel asked for one: %+v", s)
	}

	// Enabled while connected, through a desired state.
	h.panel.settings.Store(asked) // what the next HelloAck will carry
	h.panel.push(&pb.DesiredState{Revision: 1, Settings: asked})
	first := nextOsStatus(h, 8*time.Second)
	if first == nil || first.GetDistroId() != "ubuntu" || first.GetPending() != 1 || first.GetSecurity() != 1 || first.GetError() != "" {
		t.Fatalf("snapshot after enabling = %+v", first)
	}

	// A new connection: nothing changed on the host, so there is no new read, and the stored snapshot goes out on its own.
	h.panel.dropConn()
	h.waitConnected()
	again := nextOsStatus(h, 8*time.Second)
	if again == nil || again.GetMeasuredUnix() != first.GetMeasuredUnix() || again.GetPending() != 1 {
		t.Fatalf("snapshot after reconnecting = %+v, want the stored one (%+v)", again, first)
	}
	if n := simulations.Load(); n != 1 {
		t.Fatalf("apt simulations = %d, want 1: the reconnect must resend the stored snapshot, not read again", n)
	}
}
