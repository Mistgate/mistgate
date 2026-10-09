//go:build linux

package hostctl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// enableUFW gives the test host a ufw.conf saying ENABLED=<value>.
func enableUFW(t *testing.T, h *linuxHost, value string) {
	t.Helper()
	h.ufwConf = filepath.Join(t.TempDir(), "ufw.conf")
	if err := os.WriteFile(h.ufwConf, []byte("# /etc/ufw/ufw.conf\nENABLED="+value+"\nLOGLEVEL=low\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ufwArgs returns the ufw arguments of a command the host ran, which must go through systemd-run (outside the agent's
// sandbox, waiting for and returning ufw's result, bounded by systemd).
func ufwArgs(t *testing.T, name string, args []string) ([]string, bool) {
	t.Helper()
	if name == "ufw" {
		t.Fatalf("ufw ran inside the agent's sandbox: %q", args)
	}
	if name != "systemd-run" {
		return nil, false
	}
	i := slices.Index(args, "--")
	if i < 0 || i+1 >= len(args) || args[i+1] != "ufw" {
		t.Fatalf("systemd-run without ufw: %q", args)
	}
	for _, flag := range []string{"--wait", "--pipe", "--collect"} {
		if !slices.Contains(args[:i], flag) {
			t.Fatalf("systemd-run without %s: %q", flag, args)
		}
	}
	if !slices.ContainsFunc(args[:i], func(a string) bool { return strings.HasPrefix(a, "--property=RuntimeMaxSec=") }) {
		t.Fatalf("systemd-run without a runtime limit: %q", args)
	}
	return args[i+2:], true
}

// fakeUFW is an active UFW holding rules (as `ufw show added` prints them, without "ufw ").
type fakeUFW struct {
	t     *testing.T
	rules []string
	fail  error // returned by every mutation
	calls []string
}

func (f *fakeUFW) run(ctx context.Context, _ string, name string, args ...string) ([]byte, error) {
	if _, ok := ctx.Deadline(); !ok {
		f.t.Fatalf("%s %q ran without a timeout", name, args)
	}
	if name == "firewall-cmd" {
		f.calls = append(f.calls, "firewall-cmd "+strings.Join(args, " "))
		return nil, &exec.Error{Name: name, Err: exec.ErrNotFound}
	}
	a, ok := ufwArgs(f.t, name, args)
	if !ok {
		return nil, fmt.Errorf("unexpected command %s", name)
	}
	cmd := strings.Join(a, " ")
	f.calls = append(f.calls, cmd)
	switch {
	case cmd == "status":
		return []byte("Status: active\n"), nil
	case cmd == "show added":
		lines := []string{"Added user rules (see 'ufw status' for running firewall):"}
		for _, r := range f.rules {
			lines = append(lines, "ufw "+r)
		}
		return []byte(strings.Join(lines, "\n")), nil
	case f.fail != nil:
		return []byte("ERROR: " + f.fail.Error()), f.fail
	case len(a) == 5 && a[0] == "delete" && a[1] == "allow" && a[3] == "comment":
		rule := fmt.Sprintf("allow %s comment '%s'", a[2], a[4])
		f.rules = slices.DeleteFunc(f.rules, func(r string) bool { return r == rule })
		return []byte("Rule deleted"), nil
	case len(a) == 4 && a[0] == "allow" && a[2] == "comment":
		f.rules = append(f.rules, fmt.Sprintf("allow %s comment '%s'", a[1], a[3]))
		return []byte("Rule added"), nil
	}
	return nil, fmt.Errorf("unexpected ufw args %q", a)
}

func (f *fakeUFW) mutations() (n int) {
	for _, c := range f.calls {
		if c != "status" && c != "show added" && !strings.HasPrefix(c, "firewall-cmd") {
			n++
		}
	}
	return n
}

func TestSyncInboundPortsOwnsExactUFWRules(t *testing.T) {
	h, _ := testHost(t)
	enableUFW(t, h, "yes")
	fw := &fakeUFW{t: t, rules: []string{
		"allow 443/tcp",
		fmt.Sprintf("allow 20000:30000/udp comment '%s20000:30000'", ufwInboundCommentPrefix),
	}}
	h.run = fw.run

	want := []UDPInboundPort{{Port: 51820}, {From: 20000, To: 20010}}
	if err := h.SyncInboundPorts(context.Background(), want, nil); err != nil {
		t.Fatal(err)
	}
	for _, r := range []string{
		fmt.Sprintf("allow 51820/udp comment '%s51820'", ufwInboundCommentPrefix),
		fmt.Sprintf("allow 20000:20010/udp comment '%s20000:20010'", ufwInboundCommentPrefix),
	} {
		if !slices.Contains(fw.rules, r) {
			t.Fatalf("rule %q missing after sync: %v", r, fw.rules)
		}
	}
	if len(fw.rules) != 3 || fw.rules[0] != "allow 443/tcp" {
		t.Fatalf("UFW rules after sync: %v", fw.rules)
	}
	if fw.mutations() != 3 { // remove the old range, then add the exact listener and current hop range
		t.Fatalf("calls: %q", fw.calls)
	}

	// The same desired set again does nothing at all: no Python start on every reconcile and sweep.
	before := len(fw.calls)
	if err := h.SyncInboundPorts(context.Background(), want, nil); err != nil {
		t.Fatal(err)
	}
	if got := fw.calls[before:]; len(got) != 0 {
		t.Fatalf("an unchanged sync ran commands: %q", got)
	}

	if err := h.SyncInboundPorts(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(fw.rules) != 1 || fw.rules[0] != "allow 443/tcp" {
		t.Fatalf("removal left or touched rules: %v", fw.rules)
	}
}

func TestSyncInboundPortsOwnsUDPAndTCPAndKeepsProvisioned443TCP(t *testing.T) {
	h, _ := testHost(t)
	enableUFW(t, h, "yes")
	provisioned := fmt.Sprintf("allow 443/tcp comment '%s'", ProvisionUFWTag)
	oldUDP := fmt.Sprintf("allow 20000:30000/udp comment '%s20000:30000'", ufwInboundCommentPrefix)
	fw := &fakeUFW{t: t, rules: []string{provisioned, oldUDP}}
	h.run = fw.run

	udp := []UDPInboundPort{{Port: 51820}, {From: 20000, To: 20010}}
	tcp := []uint16{443, 8443}
	if err := h.SyncInboundPorts(context.Background(), udp, tcp); err != nil {
		t.Fatal(err)
	}
	// Rules are added in a fixed order: tcp before udp, then by port expression.
	want := []string{
		provisioned,
		"allow 8443/tcp comment 'mistgate-node-managed-tcp-v1-8443'",
		fmt.Sprintf("allow 20000:20010/udp comment '%s20000:20010'", ufwInboundCommentPrefix),
		fmt.Sprintf("allow 51820/udp comment '%s51820'", ufwInboundCommentPrefix),
	}
	if !slices.Equal(fw.rules, want) {
		t.Fatalf("UFW rules after sync = %v, want %v", fw.rules, want)
	}

	if err := h.SyncInboundPorts(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fw.rules, []string{provisioned}) {
		t.Fatalf("removal changed the provisioned rule or left managed rules: %v", fw.rules)
	}
}

// A rule for the same port that is not ours is never rewritten ("Rule updated" turns the owner's deny into our allow)
// and therefore never deleted later.
func TestSyncInboundPortsLeavesForeignRulesForTheSamePort(t *testing.T) {
	h, _ := testHost(t)
	enableUFW(t, h, "yes")
	fw := &fakeUFW{t: t, rules: []string{"allow 443/udp", "deny 51820/udp comment 'blocked by the owner'", "limit log 52000/udp"}}
	h.run = fw.run

	err := h.SyncInboundPorts(context.Background(), []UDPInboundPort{{Port: 443}, {Port: 51820}, {Port: 52000}, {Port: 53000}}, nil)
	if err == nil || !strings.Contains(err.Error(), "deny 51820/udp") || strings.Contains(err.Error(), "443") {
		t.Fatalf("result: %v", err)
	}
	if fw.mutations() != 1 || !slices.Contains(fw.rules, fmt.Sprintf("allow 53000/udp comment '%s53000'", ufwInboundCommentPrefix)) {
		t.Fatalf("calls %q, rules %v", fw.calls, fw.rules)
	}
	h.inboundSynced = false // force a fresh run
	if err := h.SyncInboundPorts(context.Background(), nil, nil); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(fw.rules, []string{"allow 443/udp", "deny 51820/udp comment 'blocked by the owner'", "limit log 52000/udp"}) {
		t.Fatalf("removal touched the owner's rules: %v", fw.rules)
	}
}

func TestSyncInboundPortsReportsForeignTCPDeny(t *testing.T) {
	h, _ := testHost(t)
	enableUFW(t, h, "yes")
	fw := &fakeUFW{t: t, rules: []string{"deny 8443/tcp"}}
	h.run = fw.run

	err := h.SyncInboundPorts(context.Background(), nil, []uint16{8443})
	if err == nil || !strings.Contains(err.Error(), "deny 8443/tcp") {
		t.Fatalf("result = %v, want the foreign TCP deny reported", err)
	}
	if fw.mutations() != 0 {
		t.Fatalf("foreign TCP deny was modified: %q", fw.calls)
	}
}

func TestSyncInboundPortsTCP443IgnoresForeignUDPAllow(t *testing.T) {
	h, _ := testHost(t)
	enableUFW(t, h, "yes")
	fw := &fakeUFW{t: t, rules: []string{"allow 443/udp"}}
	h.run = fw.run

	if err := h.SyncInboundPorts(context.Background(), nil, []uint16{443}); err != nil {
		t.Fatal(err)
	}
	want := "allow 443/tcp comment 'mistgate-node-managed-tcp-v1-443'"
	if !slices.Contains(fw.rules, want) {
		t.Fatalf("TCP rule %q missing beside foreign UDP rule: %v", want, fw.rules)
	}
}

func TestSyncInboundPortsAddsTCP443WithoutProvisionRule(t *testing.T) {
	h, _ := testHost(t)
	enableUFW(t, h, "yes")
	fw := &fakeUFW{t: t}
	h.run = fw.run

	if err := h.SyncInboundPorts(context.Background(), nil, []uint16{443}); err != nil {
		t.Fatal(err)
	}
	want := "allow 443/tcp comment 'mistgate-node-managed-tcp-v1-443'"
	if !slices.Equal(fw.rules, []string{want}) {
		t.Fatalf("UFW rules = %v, want %q", fw.rules, want)
	}
}

func TestSyncInboundPortsLeavesInactiveFirewallAlone(t *testing.T) {
	for _, conf := range []string{"no", ""} {
		h, calls := testHost(t)
		if conf != "" {
			enableUFW(t, h, conf)
		} // else: no ufw.conf at all (UFW not installed)
		h.run = func(_ context.Context, _ string, name string, args ...string) ([]byte, error) {
			*calls = append(*calls, call{name: name, args: strings.Join(args, " ")})
			if name == "firewall-cmd" {
				return nil, &exec.Error{Name: name, Err: exec.ErrNotFound}
			}
			return nil, errors.New("unexpected command")
		}
		if err := h.SyncInboundPorts(context.Background(), []UDPInboundPort{{Port: 443}}, nil); err != nil {
			t.Fatal(err)
		}
		if len(*calls) != 1 || (*calls)[0].name != "firewall-cmd" || (*calls)[0].args != "--state" {
			t.Fatalf("ENABLED=%q: UFW was run: %+v", conf, *calls)
		}
	}
}

// A failing sync (for example EROFS inside the sandbox) is retried rarely, not on every reconcile.
func TestSyncInboundPortsRetriesAFailureRarely(t *testing.T) {
	h, _ := testHost(t)
	enableUFW(t, h, "yes")
	fw := &fakeUFW{t: t, fail: errors.New("[Errno 30] Read-only file system: '/etc/ufw/user.rules'")}
	h.run = fw.run
	want := []UDPInboundPort{{Port: 443}}
	first := h.SyncInboundPorts(context.Background(), want, nil)
	if first == nil || !strings.Contains(first.Error(), "Read-only") {
		t.Fatalf("first sync: %v", first)
	}
	before := len(fw.calls)
	if err := h.SyncInboundPorts(context.Background(), want, nil); err == nil || err.Error() != first.Error() || len(fw.calls) != before {
		t.Fatalf("retry within the interval: err=%v calls=%q", err, fw.calls[before:])
	}
	h.inboundAt = h.inboundAt.Add(-firewallRetry)
	fw.fail = nil
	if err := h.SyncInboundPorts(context.Background(), want, nil); err != nil || len(fw.calls) == before {
		t.Fatalf("retry after the interval: err=%v calls=%q", err, fw.calls[before:])
	}
	// The owner enabling UFW later is a new state, not a cached one.
	enableUFW(t, h, "no")
	if err := h.SyncInboundPorts(context.Background(), want, nil); err != nil {
		t.Fatal(err)
	}
	enableUFW(t, h, "yes")
	before = len(fw.calls)
	if err := h.SyncInboundPorts(context.Background(), want, nil); err != nil || len(fw.calls) == before {
		t.Fatalf("UFW enabled again was not synced: err=%v", err)
	}
}

func TestSyncInboundPortsReportsOnlyPortsFirewalldDoesNotOpen(t *testing.T) {
	h, calls := testHost(t)
	h.run = func(ctx context.Context, _ string, name string, args ...string) ([]byte, error) {
		*calls = append(*calls, call{name: name, args: strings.Join(args, " ")})
		if _, ok := ctx.Deadline(); !ok {
			t.Fatalf("%s %q ran without a timeout", name, args)
		}
		switch {
		case name == "firewall-cmd" && strings.Join(args, " ") == "--state":
			return []byte("running\n"), nil
		case name == "firewall-cmd" && strings.Join(args, " ") == "--query-port=443/udp":
			return []byte("yes\n"), nil
		case name == "firewall-cmd" && strings.Join(args, " ") == "--query-port=20000-20010/udp":
			return []byte("no\n"), errors.New("exit status 1")
		default:
			return nil, errors.New("unexpected mutating command")
		}
	}
	err := h.SyncInboundPorts(context.Background(), []UDPInboundPort{{Port: 443}, {From: 20000, To: 20010}}, nil)
	if err == nil || !strings.Contains(err.Error(), "firewalld is active") || strings.Contains(err.Error(), "443/udp") || !strings.Contains(err.Error(), "20000-20010/udp") {
		t.Fatalf("firewalld result: %v", err)
	}
	if len(*calls) != 3 {
		t.Fatalf("firewalld calls: %+v", *calls)
	}
}

func TestSyncInboundPortsRejectsInvalidRangesBeforeCommands(t *testing.T) {
	h, calls := testHost(t)
	enableUFW(t, h, "yes")
	err := h.SyncInboundPorts(context.Background(), []UDPInboundPort{{From: 10000, To: 60000}}, nil)
	if err == nil {
		t.Fatal("wide hop range accepted")
	}
	if len(*calls) != 0 {
		t.Fatalf("commands ran for invalid rules: %+v", *calls)
	}
}

func TestUFWEnabled(t *testing.T) {
	dir := t.TempDir()
	for body, want := range map[string]bool{
		"ENABLED=yes\n":          true,
		"# c\nENABLED=\"yes\"\n": true,
		"ENABLED=no\n":           false,
		"LOGLEVEL=low\n":         false,
		"  ENABLED=YES  \nX=1\n": true,
	} {
		p := filepath.Join(dir, "ufw.conf")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := ufwEnabled(p); got != want {
			t.Errorf("ufwEnabled(%q) = %v, want %v", body, got, want)
		}
	}
	if ufwEnabled(filepath.Join(dir, "missing")) || ufwEnabled("") {
		t.Error("a missing ufw.conf counts as enabled")
	}
}
