//go:build linux

package warp

import (
	"context"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// A default route of another tool in the WARP table (wg-quick uses 51820 too) is a clash: Preflight reports it,
// Reassert refuses and leaves it alone. Only "default dev <iface>" without a gateway is ours. The outer test makes a
// network namespace (mgf-warp-foreign) and runs the inner one in it; the namespace takes everything along when it is
// deleted. Needs root and `ip`, `nft`; skipped without MG_ROOT_TESTS=1.
//
//	MG_ROOT_TESTS=1 go test ./internal/node/warp -run '^TestForeignDefaultRoute$' -v
const nsForeign = "mgf-warp-foreign"

func TestForeignDefaultRoute(t *testing.T) {
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
	run("ip", "netns", "del", nsForeign)
	mustRun(t, "ip", "netns", "add", nsForeign)
	t.Cleanup(func() { run("ip", "netns", "del", nsForeign) })
	cmd := exec.Command("ip", "netns", "exec", nsForeign, os.Args[0], "-test.run", "^TestForeignDefaultRouteInner$", "-test.v")
	cmd.Env = append(os.Environ(), "MGF_WARP_INNER=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("inner: %v\n%s", err, out)
	}
}

func TestForeignDefaultRouteInner(t *testing.T) {
	if os.Getenv("MGF_WARP_INNER") == "" {
		t.Skip("runs inside the namespace of TestForeignDefaultRoute")
	}
	for _, c := range [][]string{
		{"link", "set", "lo", "up"},
		{"link", "add", DefaultIface, "type", "dummy"},
		{"link", "set", DefaultIface, "up"},
		{"link", "add", "mgf-other0", "type", "dummy"},
		{"link", "set", "mgf-other0", "up"},
		{"addr", "add", "192.0.2.1/24", "dev", "mgf-other0"},
	} {
		mustRun(t, "ip", c...)
	}
	ctx := context.Background()
	p := newPlane(Settings{}.withDefaults(), slog.New(slog.NewTextHandler(io.Discard, nil))).(*linuxPlane)
	rs := routeSpec{Configured: true, LinkUp: true, Subnets: []netip.Prefix{netip.MustParsePrefix("10.66.0.0/24")}}
	table := func() string { return mustRun(t, "ip", "route", "show", "table", "51820") }
	clashes := func() bool {
		for _, f := range p.Preflight(ctx) {
			if f.ID == "table_in_use" {
				return true
			}
		}
		return false
	}

	// a foreign default (with a gateway) with another metric: refused, left alone, ours installed next to it
	mustRun(t, "ip", "route", "add", "default", "via", "192.0.2.254", "dev", "mgf-other0", "table", "51820", "metric", "200")
	if !clashes() {
		t.Fatal("a foreign default route in the table passed the preflight")
	}
	if _, err := p.Reassert(ctx, rs); err == nil || !strings.Contains(err.Error(), "not ours") {
		t.Fatalf("Reassert must refuse a table with a foreign route: %v", err)
	}
	if !strings.Contains(table(), "default via 192.0.2.254 dev mgf-other0 metric 200") {
		t.Fatalf("the foreign default was deleted:\n%s", table())
	}

	// a foreign default with the very key of ours (metric 0): not overwritten either
	mustRun(t, "ip", "route", "flush", "table", "51820")
	mustRun(t, "ip", "route", "add", "default", "via", "192.0.2.254", "dev", "mgf-other0", "table", "51820")
	if !clashes() {
		t.Fatal("a foreign default route in the table passed the preflight")
	}
	if _, err := p.Reassert(ctx, rs); err == nil {
		t.Fatal("Reassert overwrote or ignored a foreign default with the same key")
	}
	if tab := table(); !strings.Contains(tab, "default via 192.0.2.254 dev mgf-other0") || strings.Contains(tab, "dev "+DefaultIface) {
		t.Fatalf("the foreign default was replaced:\n%s", tab)
	}

	// the table holds only ours: no clash, and a second Reassert changes nothing
	mustRun(t, "ip", "route", "flush", "table", "51820")
	if _, err := p.Reassert(ctx, rs); err != nil {
		t.Fatal(err)
	}
	if clashes() {
		t.Fatalf("our own routes are reported as a clash:\n%s", table())
	}
	if changed, err := p.Reassert(ctx, rs); err != nil || changed {
		t.Fatalf("second Reassert: changed=%v err=%v", changed, err)
	}
	tab := table()
	for _, want := range []string{"default dev " + DefaultIface, "unreachable default", "throw 10.66.0.0/24"} {
		if !strings.Contains(tab, want) {
			t.Fatalf("%q missing:\n%s", want, tab)
		}
	}
}
