//go:build linux

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// cleanup-net runs after every stop of every node, so it must leave the routing of another tool alone: warp.CleanupHost
// deletes by routing table number, and 51820 is also the table wg-quick uses. This runs the real subcommand inside a network
// namespace made for the test (mg3-n3-gate-*) in three situations: only a foreign table 51820 (nothing may change), the
// leftovers of the WARP manager (they go), and nothing at all (no error).
//
// Opt-in: MG_ROOT_TESTS=1 as root with ip.
//
//	MG_ROOT_TESTS=1 go test ./cmd/mistgate-node -run CleanupNetGate -v
func TestCleanupNetHelper(t *testing.T) {
	if os.Getenv("MG3_GATE_HELPER") == "" {
		t.Skip("runs inside the namespace of TestCleanupNetGate")
	}
	if code := cmdCleanupNet(nil); code != 0 {
		t.Fatalf("cleanup-net exited with %d", code)
	}
}

func TestCleanupNetGate(t *testing.T) {
	if os.Getenv("MG_ROOT_TESTS") == "" || os.Geteuid() != 0 {
		t.Skip("set MG_ROOT_TESTS=1 and run as root")
	}
	if _, err := exec.LookPath("ip"); err != nil {
		t.Skip("no ip")
	}
	ns := fmt.Sprintf("mg3-n3-gate-%d", os.Getpid())
	run := func(args ...string) string {
		t.Helper()
		out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	run("ip", "netns", "add", ns)
	defer exec.Command("ip", "netns", "del", ns).Run()
	ip := func(args ...string) string { return run(append([]string{"ip", "-n", ns}, args...)...) }
	cleanup := func() {
		t.Helper()
		cmd := exec.Command("ip", "netns", "exec", ns, os.Args[0], "-test.run=^TestCleanupNetHelper$", "-test.v")
		cmd.Env = append(os.Environ(), "MG3_GATE_HELPER=1")
		if out, err := cmd.CombinedOutput(); err != nil || !strings.Contains(string(out), "--- PASS: TestCleanupNetHelper") {
			t.Fatalf("cleanup-net: %v\n%s", err, out)
		}
	}

	// Nothing at all: no error.
	cleanup()

	// A foreign user of table 51820 (what wg-quick sets up): untouched, because nothing of ours is there.
	ip("link", "add", "mg3dummy0", "type", "dummy")
	ip("link", "set", "mg3dummy0", "up")
	ip("link", "set", "lo", "up")
	ip("addr", "add", "192.0.2.5/24", "dev", "mg3dummy0")
	ip("route", "add", "default", "dev", "mg3dummy0", "table", "51820")
	ip("rule", "add", "not", "fwmark", "0xca6c", "table", "51820", "pref", "32765")
	cleanup()
	if r := ip("route", "show", "table", "51820"); !strings.Contains(r, "default dev mg3dummy0") {
		t.Errorf("a foreign route of table 51820 was deleted:\n%s", r)
	}
	if r := ip("rule", "show"); !strings.Contains(r, "32765") || !strings.Contains(r, "lookup 51820") {
		t.Errorf("a foreign rule of table 51820 was deleted:\n%s", r)
	}

	// The leftovers of the WARP manager after a crash with the userspace backend: the device died with the process, the
	// client-subnet rule and the fail-closed route stay. They go.
	ip("rule", "add", "from", "10.66.4.0/22", "table", "51820", "pref", "110")
	ip("route", "add", "unreachable", "default", "table", "51820", "metric", "4096")
	cleanup()
	if r := ip("rule", "show"); strings.Contains(r, "10.66.4.0/22") {
		t.Errorf("the subnet rule of the WARP manager stayed:\n%s", r)
	}
	if r := ip("route", "show", "table", "51820"); strings.Contains(r, "unreachable") {
		t.Errorf("the fail-closed route of the WARP manager stayed:\n%s", r)
	}
}
