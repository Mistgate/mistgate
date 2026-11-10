//go:build linux

package hostctl

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// fixHost is testHost plus resolver paths in the temp dir and a scriptable command runner.
func fixHost(t *testing.T, resolvedActive bool) (*linuxHost, *[]call, string) {
	t.Helper()
	h, calls := testHost(t)
	dir := t.TempDir()
	h.resolvedFile = filepath.Join(dir, "resolved.conf.d", "90-mistgate.conf")
	h.resolvConfFile = filepath.Join(dir, "resolv.conf")
	h.run = func(_ context.Context, stdin, name string, args ...string) ([]byte, error) {
		line := strings.Join(args, " ")
		*calls = append(*calls, call{stdin, name, line})
		switch {
		case name == "ufw" && line == "status":
			return []byte("Status: inactive\n"), nil
		case name == "systemctl" && line == "is-active --quiet systemd-resolved":
			if resolvedActive {
				return nil, nil
			}
			return nil, errors.New("exit status 3")
		case name == "resolvectl":
			return []byte("Global:\nLink 2 (eth0): 10.0.0.1\n"), nil
		}
		return nil, nil
	}
	return h, calls, dir
}

func count(calls *[]call, name, args string) (n int) {
	for _, c := range *calls {
		if c.name == name && c.args == args {
			n++
		}
	}
	return
}

func TestVacuumJournal(t *testing.T) {
	h, calls, _ := fixHost(t, false)
	if err := h.VacuumJournal(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || (*calls)[0].name != "journalctl" || (*calls)[0].args != "--vacuum-size=200M --vacuum-time=7d" {
		t.Fatalf("calls = %+v", *calls)
	}
	h.run = func(context.Context, string, string, ...string) ([]byte, error) {
		return []byte("Failed to vacuum: Permission denied\n"), errors.New("exit status 1")
	}
	if err := h.VacuumJournal(context.Background()); err == nil || !strings.Contains(err.Error(), "Permission denied") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolverPlanChangesNothing(t *testing.T) {
	for _, resolved := range []bool{false, true} {
		h, calls, dir := fixHost(t, resolved)
		if err := os.WriteFile(h.resolvConfFile, []byte("nameserver 10.0.0.1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		plan, err := h.ResolverPlan(context.Background(), []string{"1.1.1.1", "dns.example.com", "8.8.8.8"})
		if err != nil {
			t.Fatal(err)
		}
		wantMode := ResolverModeResolvConf
		if resolved {
			wantMode = ResolverModeResolved
		}
		if plan.Mode != wantMode || strings.Join(plan.Before, ",") != "10.0.0.1" || strings.Join(plan.After, ",") != "1.1.1.1,8.8.8.8" {
			t.Errorf("resolved=%v plan = %+v", resolved, plan)
		}
		for _, c := range *calls { // only read-only commands
			if !(c.name == "systemctl" && c.args == "is-active --quiet systemd-resolved") && c.name != "resolvectl" {
				t.Errorf("plan ran %+v", c)
			}
		}
		ents, _ := os.ReadDir(dir)
		if len(ents) != 1 { // resolv.conf only: no backup, no drop-in directory
			t.Errorf("plan wrote files: %v", ents)
		}
		if b, _ := os.ReadFile(h.resolvConfFile); string(b) != "nameserver 10.0.0.1\n" {
			t.Errorf("plan changed resolv.conf: %q", b)
		}
	}
}

func TestSetResolverRewritesResolvConfAndCleanupRestoresIt(t *testing.T) {
	h, _, dir := fixHost(t, false)
	orig := "# from dhcp\nnameserver 10.0.0.1\nsearch lan\n"
	if err := os.WriteFile(h.resolvConfFile, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.SetResolver(context.Background(), []string{"77.88.8.8", "77.88.8.1"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(h.resolvConfFile); string(b) != "# Managed by mistgate-node.\nnameserver 77.88.8.8\nnameserver 77.88.8.1\n" {
		t.Fatalf("resolv.conf = %q", b)
	}
	if b, _ := os.ReadFile(h.resolvConfFile + ".mistgate.bak"); string(b) != orig {
		t.Fatalf("backup = %q", b)
	}
	// A second fix must not overwrite the backup with our own file.
	if err := h.SetResolver(context.Background(), []string{"9.9.9.9"}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(h.resolvConfFile + ".mistgate.bak"); string(b) != orig {
		t.Fatalf("backup overwritten: %q", b)
	}
	if err := h.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(h.resolvConfFile); string(b) != orig {
		t.Fatalf("after Cleanup resolv.conf = %q", b)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Errorf("Cleanup left files behind: %v", ents)
	}
}

func TestSetResolverUsesTransientUnitForResolvConfWrites(t *testing.T) {
	t.Setenv("MISTGATE_UNIT_GEN", "3")
	h, calls, _ := fixHost(t, false)
	orig := "nameserver 10.0.0.1\n"
	if err := os.WriteFile(h.resolvConfFile, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.SetResolver(context.Background(), []string{"9.9.9.9"}); err != nil {
		t.Fatal(err)
	}
	var writes int
	for _, call := range *calls {
		if call.name != "systemd-run" {
			continue
		}
		writes++
		if !strings.Contains(call.args, "mistgate-resolver-write") || !strings.Contains(call.args, h.resolvConfFile) {
			t.Errorf("unexpected privileged resolver command: %+v", call)
		}
	}
	if writes != 2 {
		t.Fatalf("systemd-run writes = %d, calls = %+v", writes, *calls)
	}
	if b, err := os.ReadFile(h.resolvConfFile); err != nil || string(b) != orig {
		t.Fatalf("the sandbox wrote resolv.conf directly: %q, %v", b, err)
	}
}

func TestWriteResolverFileUsesTransientUnitBesideTarget(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the transient resolver writer is Linux-only")
	}
	if os.Geteuid() != 0 {
		t.Skip("a system transient unit needs root on this runner")
	}
	if _, err := exec.LookPath("systemd-run"); err != nil {
		t.Skip("systemd-run is unavailable")
	}
	if _, err := exec.Command("systemctl", "show-environment").CombinedOutput(); err != nil {
		t.Skip("the system systemd manager is unavailable")
	}
	t.Setenv("MISTGATE_UNIT_GEN", "3")
	for _, script := range []string{resolverWriteScript, resolverLinkScript} {
		if !strings.Contains(script, " EXIT HUP INT TERM") {
			t.Fatalf("transient resolver script does not clean up after unit termination: %s", script)
		}
		for _, positional := range []string{"$1", "$2", "$3", "$4"} {
			if !strings.Contains(script, positional) {
				t.Fatalf("transient resolver script lacks positional argument %s: %s", positional, script)
			}
		}
		for _, expansion := range []string{"${", "$$", "$target", "$link", "$tmp", "$dir", "$mode"} {
			if strings.Contains(script, expansion) {
				t.Fatalf("transient resolver script contains systemd-expanded text %q: %s", expansion, script)
			}
		}
	}
	dir := t.TempDir()
	target := filepath.Join(dir, "resolved.conf.d", "resolved.conf")
	if _, err := os.Stat(filepath.Dir(target)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("test target directory should start absent, stat error = %v", err)
	}
	tmp, err := resolverTempPath(target)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(tmp) != filepath.Dir(target) {
		t.Fatalf("temporary file %q is not beside target %q", tmp, target)
	}
	h := &linuxHost{run: execRunner}
	body := bytes.Repeat([]byte("DNS=192.0.2.53\n"), 1<<16)
	if err := h.writeResolverFileAtTemp(context.Background(), target, tmp, body, 0o640); err != nil {
		t.Fatalf("transient systemd write: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, body) {
		t.Fatalf("target contents did not land atomically: equal=%v err=%v", bytes.Equal(got, body), err)
	}
	if _, err := os.Stat(filepath.Dir(target)); err != nil {
		t.Fatalf("transient unit did not create the target directory: %v", err)
	}
	if fi, err := os.Stat(target); err != nil || fi.Mode().Perm() != 0o640 {
		t.Fatalf("target mode = %v, err=%v", fi, err)
	}
	if _, err := os.Lstat(tmp); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary file remains after the rename: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(target))
	if err != nil || len(entries) != 1 || entries[0].Name() != filepath.Base(target) {
		t.Fatalf("temporary files were not cleaned up: entries=%v err=%v", entries, err)
	}
}

func TestSetResolverKeepsASymlinkResolvConf(t *testing.T) {
	h, _, dir := fixHost(t, false)
	target := filepath.Join(dir, "stub-resolv.conf")
	if err := os.WriteFile(target, []byte("nameserver 127.0.0.53\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, h.resolvConfFile); err != nil {
		t.Fatal(err)
	}
	if err := h.SetResolver(context.Background(), []string{"1.1.1.1"}); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(h.resolvConfFile); fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("resolv.conf is still a link: the new content went into the link's target")
	}
	if b, _ := os.ReadFile(target); string(b) != "nameserver 127.0.0.53\n" {
		t.Fatalf("the stub file was modified: %q", b)
	}
	if err := h.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, err := os.Readlink(h.resolvConfFile); err != nil || got != target {
		t.Fatalf("link not restored: %q %v", got, err)
	}
}

func TestCleanupLeavesAResolvConfSomeoneElseChanged(t *testing.T) {
	h, _, _ := fixHost(t, false)
	_ = os.WriteFile(h.resolvConfFile, []byte("nameserver 10.0.0.1\n"), 0o644)
	if err := h.SetResolver(context.Background(), []string{"1.1.1.1"}); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(h.resolvConfFile, []byte("nameserver 192.0.2.1\n"), 0o644) // the admin took over afterwards
	if err := h.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(h.resolvConfFile); string(b) != "nameserver 192.0.2.1\n" {
		t.Fatalf("Cleanup overwrote the admin's file: %q", b)
	}
	if _, err := os.Stat(h.resolvConfFile + ".mistgate.bak"); err == nil {
		t.Error("the backup is still there")
	}
}

func TestSetResolverWithSystemdResolved(t *testing.T) {
	h, calls, _ := fixHost(t, true)
	if err := h.SetResolver(context.Background(), []string{"77.88.8.8", "10.0.0.9:5353"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(h.resolvedFile)
	if err != nil || !strings.Contains(string(b), "DNS=77.88.8.8 10.0.0.9:5353\n") || !strings.Contains(string(b), "Domains=~.") {
		t.Fatalf("drop-in = %q %v", b, err)
	}
	if count(calls, "systemctl", "restart systemd-resolved") != 1 {
		t.Fatalf("calls = %+v", *calls)
	}
	if _, err := os.Stat(h.resolvConfFile + ".mistgate.bak"); err == nil {
		t.Error("resolved mode must not touch resolv.conf")
	}
	if err := h.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(h.resolvedFile); err == nil {
		t.Error("Cleanup left the drop-in")
	}
	if count(calls, "systemctl", "restart systemd-resolved") != 2 { // restarted again so the change is undone live
		t.Fatalf("calls = %+v", *calls)
	}
}

func TestSetResolverUsesTransientUnitForResolvedDropIn(t *testing.T) {
	t.Setenv("MISTGATE_UNIT_GEN", "3")
	h, calls, _ := fixHost(t, true)
	if err := h.SetResolver(context.Background(), []string{"77.88.8.8"}); err != nil {
		t.Fatal(err)
	}
	var write *call
	for i := range *calls {
		if (*calls)[i].name == "systemd-run" {
			write = &(*calls)[i]
		}
	}
	if write == nil || !strings.Contains(write.args, "mistgate-resolver-write") || !strings.Contains(write.args, h.resolvedFile) {
		t.Fatalf("resolved drop-in did not use the transient writer: %+v", *calls)
	}
}

func TestCleanupWithoutAnyResolverFixIsQuiet(t *testing.T) {
	h, calls, _ := fixHost(t, true)
	if err := h.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if count(calls, "systemctl", "restart systemd-resolved") != 0 {
		t.Error("restarted resolved although no drop-in existed")
	}
}

func TestSetResolverRejectsUnusableInput(t *testing.T) {
	h, calls, _ := fixHost(t, false)
	if err := h.SetResolver(context.Background(), []string{"dns.example.com"}); err == nil {
		t.Fatal("a name was accepted")
	}
	if _, err := os.Stat(h.resolvConfFile); err == nil {
		t.Error("resolv.conf written for a rejected request")
	}
	_ = calls
	// Unconfigured paths (a host built without the fix) say so instead of writing to "".
	bare := &linuxHost{run: h.run}
	if _, err := bare.ResolverPlan(context.Background(), []string{"1.1.1.1"}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("err = %v", err)
	}
	if err := bare.undoResolver(context.Background()); err != nil {
		t.Fatal(err)
	}
}
