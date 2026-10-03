//go:build linux

package hostctl

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// scriptedRun answers sshd -T and systemctl show with canned output; a nil sshd means "not installed".
func scriptedRun(h *linuxHost, sshd, socket *string) *[]call {
	var calls []call
	h.run = func(_ context.Context, stdin, name string, args ...string) ([]byte, error) {
		calls = append(calls, call{stdin, name, strings.Join(args, " ")})
		switch {
		case name == "ufw" && len(args) == 1 && args[0] == "status":
			return []byte("Status: inactive\n"), nil
		case name == "sshd" || name == "/usr/sbin/sshd":
			if sshd == nil {
				return nil, &exec.Error{Name: name, Err: exec.ErrNotFound}
			}
			return []byte(*sshd), nil
		case name == "systemctl" && len(args) > 0 && args[0] == "show":
			if socket == nil {
				return nil, nil
			}
			return []byte(*socket), nil
		}
		return nil, nil
	}
	return &calls
}

func str(s string) *string { return &s }

func TestDetectSSHPorts(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		sshd, socket *string
		conf         map[string]string
		want         []uint16
	}{
		"sshd -T":                {sshd: str("port 2200\nport 22\n"), want: []uint16{22, 2200}},
		"sshd -T wins over conf": {sshd: str("port 2200\n"), conf: map[string]string{"sshd_config": "Port 5\n"}, want: []uint16{2200}},
		"no sshd binary: files":  {conf: map[string]string{"sshd_config": "Port 2022\n", "sshd_config.d/50-x.conf": "Port 2023\n"}, want: []uint16{2022, 2023}},
		"socket activation":      {sshd: str("port 22\n"), socket: str("Listen=[::]:2222 (Stream)\nListen=0.0.0.0:2222 (Stream)\n"), want: []uint16{22, 2222}},
		"nothing found":          {want: []uint16{22}},
	} {
		h, _ := testHost(t)
		h.sshdConfDir = t.TempDir()
		for rel, body := range tc.conf {
			p := filepath.Join(h.sshdConfDir, rel)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		scriptedRun(h, tc.sshd, tc.socket)
		if got := h.detectSSHPorts(ctx); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
}

// The ports found by ApplyBaseline drive both the guard and the hop validation; Cleanup removes it all.
func TestSSHGuardLifecycle(t *testing.T) {
	ctx := context.Background()
	h, _ := testHost(t)
	h.sshdConfDir = t.TempDir()
	calls := scriptedRun(h, str("port 2222\n"), nil)
	if err := h.ApplyBaseline(ctx); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(h.SSHPorts(), []uint16{2222}) {
		t.Fatalf("SSHPorts: %v", h.SSHPorts())
	}
	lastNft := func() string {
		for i := len(*calls) - 1; i >= 0; i-- {
			if (*calls)[i].name == "nft" {
				return (*calls)[i].stdin
			}
		}
		return ""
	}
	if s := lastNft(); !strings.Contains(s, "tcp dport { 2222 } ct state new add @ssh_v4") || strings.Contains(s, "chain hop") {
		t.Fatalf("baseline ruleset:\n%s", s)
	}

	// Hops are added to the same table and the guard stays.
	if err := h.SetPortHops(ctx, []Hop{{InboundID: "inb_a", Network: "udp", From: 20000, To: 29999, Port: 443}}); err != nil {
		t.Fatal(err)
	}
	if s := lastNft(); !strings.Contains(s, "chain ssh") || !strings.Contains(s, `udp dport 20000-29999 redirect to :443 comment "hop:inb_a"`) {
		t.Fatalf("ruleset with hops:\n%s", s)
	}
	// A hop over the sshd port never reaches nft, and the previous hops stay in force.
	n := len(*calls)
	if err := h.SetPortHops(ctx, []Hop{{InboundID: "inb_b", Network: "udp", From: 2000, To: 3000, Port: 443}}); err == nil {
		t.Fatal("hop over the sshd port accepted")
	}
	if len(*calls) != n || len(h.hops) != 1 {
		t.Fatalf("a rejected hop changed the firewall: %d calls (was %d), hops %+v", len(*calls), n, h.hops)
	}
	// No hops left: the guard is still there.
	if err := h.SetPortHops(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if s := lastNft(); !strings.Contains(s, "chain ssh") || strings.Contains(s, "chain hop") {
		t.Fatalf("ruleset without hops:\n%s", s)
	}
	// Retire: the whole table goes (guard included).
	if err := h.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if s := lastNft(); s != "add table inet mistgate_node\ndelete table inet mistgate_node\n" {
		t.Fatalf("cleanup script: %q", s)
	}
	if len(h.SSHPorts()) != 0 {
		t.Fatalf("ports after cleanup: %v", h.SSHPorts())
	}
}

// TestSSHGuardLoadsInPrivateNetns checks the full ruleset with a real nft inside a throw-away user+net
// namespace: `nft -c` (syntax and kernel support, nothing installed), then the real load twice (the replace
// path), the listing, and the cleanup. Skipped where nft, unshare or unprivileged namespaces are missing.
func TestSSHGuardLoadsInPrivateNetns(t *testing.T) {
	for _, bin := range []string{"nft", "unshare"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s not installed", bin)
		}
	}
	script, err := RenderRuleset([]Hop{{InboundID: "inb_a", Network: "udp", From: 20000, To: 29999, Port: 443}}, []uint16{22, 2222})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	load, cleanup := filepath.Join(dir, "load.nft"), filepath.Join(dir, "cleanup.nft")
	clean, _ := RenderRuleset(nil, nil)
	for p, body := range map[string]string{load: script, cleanup: clean} {
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sh := "nft -c -f " + load + " && echo CHECK-OK && nft -f " + load + " && nft -f " + load +
		" && nft list ruleset && echo AFTER-CLEANUP && nft -f " + cleanup + " && nft list ruleset"
	out, err := execRunner(context.Background(), "", "unshare", "-Urn", "bash", "-c", sh)
	if err != nil {
		if strings.Contains(string(out), "not permitted") || strings.Contains(string(out), "unshare failed") {
			t.Skipf("no unprivileged namespaces: %s", out)
		}
		t.Fatalf("%v\n%s", err, out)
	}
	if !strings.Contains(string(out), "CHECK-OK") {
		t.Fatalf("nft -c did not pass:\n%s", out)
	}
	before, after, _ := strings.Cut(string(out), "AFTER-CLEANUP")
	for _, want := range []string{
		"tcp dport { 22, 2222 } ct state new add @ssh_v4 { ip saddr limit rate over 6/minute burst 10 packets } drop",
		"ip6 saddr & ffff:ffff:ffff:ffff:: limit rate over 6/minute burst 10 packets } drop",
		`iifname "lo" accept`,
		`udp dport 20000-29999 redirect to :443 comment "hop:inb_a"`,
	} {
		if !strings.Contains(before, want) {
			t.Errorf("listed ruleset lacks %q:\n%s", want, before)
		}
	}
	if strings.Contains(after, "mistgate_node") {
		t.Errorf("table survived cleanup:\n%s", after)
	}
}
