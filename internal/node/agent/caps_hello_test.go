package agent

import (
	"context"
	"flag"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/node/awg"
	nodebridge "github.com/mistgate/mistgate/internal/node/bridge"
	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/node/hostctl"
)

var updateHelloCaps = flag.Bool("update-hello-caps", false, "rewrite testdata/hello_caps.golden")

// allCapsHost is a host that can forward, count UDP and hold IPv6 addresses: every optional capability of a host is listed.
type allCapsHost struct{ *fakeHost }

func (allCapsHost) RelaySupported() bool                               { return true }
func (allCapsHost) SetRelays(context.Context, []hostctl.Forward) error { return nil }
func (allCapsHost) CountUDP([8]byte, []uint16) error                   { return nil }
func (allCapsHost) TakeUDPCount() (map[uint16]hostctl.Count, error)    { return nil, nil }
func (allCapsHost) KeepOwnedIPv6(context.Context) (hostctl.OwnedIPv6Result, error) {
	return hostctl.OwnedIPv6Result{}, nil
}
func (allCapsHost) CleanupOwnedIPv6(context.Context) error { return nil }
func (allCapsHost) SyncOwnedIPv6(context.Context, []netip.Addr) (hostctl.OwnedIPv6Result, error) {
	return hostctl.OwnedIPv6Result{}, nil
}

// helloCapsAgent is an agent with every engine and every option that adds a capability to its Hello.
func helloCapsAgent(t *testing.T) *Agent {
	t.Helper()
	var cfg Config
	cfg.StateDir = t.TempDir()
	newUpdFix(t).cfg(1000, "0.1.0", nil)(&cfg)
	cfg.Certs = &markCerts{}
	cfg.Warp = newFakeWarp(&order{})
	cfg.UnitGen = 3
	cfg.UnitRefreshLauncher = func(context.Context, func()) error { return nil }
	cfg.OSUpdateCapability = func() bool { return true }
	cfg.OSUpdate = newOsNode().controller(cfg.StateDir)
	cfg.AwgPrepare = (&prepNode{env: okPrepEnv()}).controller(cfg.StateDir)
	shares := engine.Capabilities{RateLimitPerCred: true, RateLimitPerUser: true}
	engines := map[string]engine.Engine{awg.Protocol: capsEngine{newFakeEngine(awg.Protocol), engine.Capabilities{}}}
	for _, p := range []string{"vless", "trusttunnel", "mtproto", "hysteria2"} {
		engines[p] = capsEngine{newFakeEngine(p), shares}
	}
	a := &Agent{cfg: cfg, host: allCapsHost{&fakeHost{}}, upd: cfg.Updater, engines: engines, bridgeEntry: nodebridge.NewEntry()}
	a.country.supported = func() bool { return true }
	return a
}

// A wire value of Hello.capabilities is never renamed: an agent in the field lists the string it was built with, and a panel asks
// for the exact string. The golden holds, per configuration of a build, the list in the order it is sent. A host that cannot hold
// the kernel debug source or the torrent guard does not list debug/2 and torrentguard/1; the golden is written on Linux.
func TestHelloCapabilitiesAreTheWireValues(t *testing.T) {
	scenarios := []struct {
		name  string
		agent func(*testing.T) *Agent
	}{
		{"an agent with nothing: no engine, no host", func(*testing.T) *Agent {
			a := &Agent{engines: map[string]engine.Engine{}}
			a.cfg.OSUpdateCapability = func() bool { return false }
			a.country.supported = func() bool { return false }
			return a
		}},
		{"a VPS agent with every engine and option", helloCapsAgent},
		{"the same, without the certificate source, the updater, the OS update and unit 3", func(t *testing.T) *Agent {
			a := helloCapsAgent(t)
			a.cfg.Certs, a.cfg.Updater, a.upd, a.cfg.OSUpdate, a.cfg.UnitRefreshLauncher, a.cfg.UnitGen = nil, nil, nil, nil, nil, 2
			a.cfg.OSUpdateCapability = func() bool { return false }
			return a
		}},
		{"the same, a unit of generation 1 and no WARP", func(t *testing.T) *Agent {
			a := helloCapsAgent(t)
			a.cfg.Warp, a.cfg.AwgPrepare, a.bridgeEntry, a.cfg.UnitGen = nil, nil, nil, 1
			return a
		}},
		{"a link-only agent with every engine and option", func(t *testing.T) *Agent {
			a := helloCapsAgent(t)
			a.meta.Link = "wss://de1.example.com/" + strings.Repeat("p", 24) + "/"
			return a
		}},
	}
	var got strings.Builder
	for _, sc := range scenarios {
		got.WriteString("[" + sc.name + "]\n")
		for _, c := range sc.agent(t).capabilities() {
			got.WriteString(c + "\n")
		}
	}
	golden := filepath.Join("testdata", "hello_caps.golden")
	if *updateHelloCaps {
		if err := os.WriteFile(golden, []byte(got.String()), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	// the two values a platform may not list are cut from both sides when this one does not support them
	cut := map[string]bool{}
	if !kernelDebugSupported() {
		cut["debug/2"] = true
	}
	if !hostctl.TorrentGuardSupported() {
		cut["torrentguard/1"] = true
	}
	trim := func(s string) string {
		var out []string
		for _, l := range strings.Split(s, "\n") {
			if !cut[l] {
				out = append(out, l)
			}
		}
		return strings.Join(out, "\n")
	}
	if trim(got.String()) != trim(strings.ReplaceAll(string(want), "\r\n", "\n")) {
		t.Fatalf("Hello.capabilities differ from testdata/hello_caps.golden:\n%s\nwant:\n%s", got.String(), want)
	}
}
