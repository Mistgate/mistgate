//go:build linux

package warp

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The cleanup must not take another tool's share of the WARP table (51820 is wg-quick's default too). The outer test
// makes a network namespace (mgf-warp-clean) and runs the inner one in it; the namespace takes everything along when
// it is deleted. Needs root and `ip`, `nft`; skipped without MG_ROOT_TESTS=1.
//
//	MG_ROOT_TESTS=1 go test ./internal/node/warp -run '^TestCleanupHostOwnOnly$' -v
const nsClean = "mgf-warp-clean"

func TestCleanupHostOwnOnly(t *testing.T) {
	if os.Getenv("MGF_WARP_INNER") != "" {
		t.Skip("inner process")
	}
	if os.Getenv("MG_ROOT_TESTS") != "1" || os.Geteuid() != 0 {
		t.Skip("needs root and MG_ROOT_TESTS=1")
	}
	for _, tool := range []string{"ip", "nft"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	run("ip", "netns", "del", nsClean)
	mustRun(t, "ip", "netns", "add", nsClean)
	t.Cleanup(func() { run("ip", "netns", "del", nsClean) })
	cmd := exec.Command("ip", "netns", "exec", nsClean, os.Args[0], "-test.run", "^TestCleanupHostOwnOnlyInner$", "-test.v")
	cmd.Env = append(os.Environ(), "MGF_WARP_INNER=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("inner: %v\n%s", err, out)
	}
}

func TestCleanupHostOwnOnlyInner(t *testing.T) {
	if os.Getenv("MGF_WARP_INNER") == "" {
		t.Skip("runs inside the namespace of TestCleanupHostOwnOnly")
	}
	for _, c := range [][]string{
		{"link", "set", "lo", "up"},
		{"link", "add", DefaultIface, "type", "dummy"},
		{"link", "set", DefaultIface, "up"},
		{"link", "add", "mgf-other0", "type", "dummy"},
		{"link", "set", "mgf-other0", "up"},
		// ours
		{"route", "add", "default", "dev", DefaultIface, "table", "51820"},
		{"route", "add", "unreachable", "default", "table", "51820", "metric", "4096"},
		{"-6", "route", "add", "unreachable", "default", "table", "51820", "metric", "4096"},
		{"route", "add", "throw", "10.66.0.0/24", "table", "51820"},
		{"rule", "add", "oif", DefaultIface, "table", "51820", "pref", "90"},
		{"rule", "add", "from", "10.66.0.0/24", "table", "51820", "pref", "110"},
		// a foreign tool: same table, a wg-quick style rule, a route on its own device, a throw route and an
		// unreachable route that are not our floor, and a rule that shares our preference but not our shape
		{"route", "add", "198.51.100.0/24", "dev", "mgf-other0", "table", "51820"},
		{"route", "add", "throw", "203.0.113.0/24", "table", "51820"},
		{"route", "add", "unreachable", "default", "table", "51820", "metric", "100"},
		{"rule", "add", "not", "fwmark", "0xca6c", "table", "51820", "pref", "32765"},
		{"rule", "add", "from", "198.18.0.0/16", "table", "51820", "pref", "90"},
	} {
		mustRun(t, "ip", c...)
	}
	mustRun(t, "nft", "add", "table", "inet", NftTable)
	mustRun(t, "nft", "add", "table", "inet", "mgf_other")

	if err := CleanupHost(context.Background(), Settings{}); err != nil {
		t.Fatal(err)
	}

	routes := mustRun(t, "ip", "route", "show", "table", "51820")
	rules := mustRun(t, "ip", "rule", "show")
	for _, gone := range []string{"dev " + DefaultIface, "metric 4096", "10.66.0.0/24"} {
		if strings.Contains(routes, gone) || strings.Contains(rules, gone) {
			t.Errorf("%q is ours and must be gone:\nroutes:\n%s\nrules:\n%s", gone, routes, rules)
		}
	}
	for _, keep := range []string{"198.51.100.0/24 dev mgf-other0", "throw 203.0.113.0/24", "unreachable default"} {
		if !strings.Contains(routes, keep) {
			t.Errorf("a foreign route %q was deleted:\n%s", keep, routes)
		}
	}
	for _, keep := range []string{"32765:", "from 198.18.0.0/16 lookup 51820"} {
		if !strings.Contains(rules, keep) {
			t.Errorf("a foreign rule %q was deleted:\n%s", keep, rules)
		}
	}
	if out, err := run("ip", "link", "show", DefaultIface); err == nil {
		t.Errorf("the device is still there:\n%s", out)
	}
	if out, err := run("nft", "list", "table", "inet", NftTable); err == nil {
		t.Errorf("our nft table is still there:\n%s", out)
	}
	mustRun(t, "nft", "list", "table", "inet", "mgf_other")
	if err := CleanupHost(context.Background(), Settings{}); err != nil {
		t.Fatalf("a second cleanup must be a no-op: %v", err)
	}
}
