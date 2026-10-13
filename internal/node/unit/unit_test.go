package unit

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSystemdRunRefreshArgs(t *testing.T) {
	var gotName string
	var gotArgs []string
	err := systemdRun(context.Background(), "/usr/local/bin/mistgate-node", "/var/lib/mistgate-node", 2, "0123456789abcdef0123456789abcdef", func(_ context.Context, name string, args ...string) ([]byte, error) {
		gotName, gotArgs = name, args
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"--quiet", "--collect", "--no-ask-password", "--unit=" + RefreshUnitName,
		"--property=RuntimeMaxSec=300",
		"--property=TimeoutStopSec=150",
		"--description=Refresh Mistgate node unit", "--",
		"/usr/local/bin/mistgate-node", "install", "--refresh-unit", "--state-dir", "/var/lib/mistgate-node",
		"--bin", "/usr/local/bin/mistgate-node", "--from-generation", "2", "--ack-token", "0123456789abcdef0123456789abcdef",
	}
	if gotName != "systemd-run" || !reflect.DeepEqual(gotArgs, want) {
		t.Fatalf("systemd-run %v %v, want systemd-run %v", gotName, gotArgs, want)
	}
}

func TestSystemdRunMapsExistingUnitToBusy(t *testing.T) {
	err := systemdRun(context.Background(), "/usr/local/bin/mistgate-node", "/var/lib/mistgate-node", 2, "0123456789abcdef0123456789abcdef", func(_ context.Context, _ string, _ ...string) ([]byte, error) {
		return []byte("Unit mistgate-unit-refresh.service already exists."), errors.New("exit status 1")
	})
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("systemd-run error = %v, want ErrBusy", err)
	}
}

func TestSystemdRunRequiresCollect(t *testing.T) {
	err := supportsCollect(context.Background(), func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "systemd-run" || strings.Join(args, " ") != "--help" {
			t.Fatalf("collect probe ran %s %v", name, args)
		}
		return []byte("systemd-run options: --unit --property"), nil
	})
	if err == nil || !strings.Contains(err.Error(), "does not support --collect") {
		t.Fatalf("collect probe error = %v", err)
	}
}

func TestDetectSystemdServiceRequiresExactComponent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cgroup  string
		managed bool
		matches bool
	}{
		{"service", "0::/system.slice/mistgate-node.service\n", true, true},
		{"nested under service", "0::/system.slice/mistgate-node.service/worker.scope\n", true, true},
		{"similar service", "0::/system.slice/mistgate-node.service-old\n", true, false},
		{"other systemd service", "1:name=systemd:/system.slice/other.service\n", true, false},
		{"container cgroup", "0::/docker/012345\n", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			managed, matches := DetectSystemdService([]byte(tc.cgroup), ServiceName)
			if managed != tc.managed || matches != tc.matches {
				t.Fatalf("detected managed=%v matches=%v, want %v/%v", managed, matches, tc.managed, tc.matches)
			}
		})
	}
}

func TestUnitReplacementKeepsAndRestoresPreviousFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ServiceName)
	if err := os.WriteFile(path, []byte("old unit"), 0o644); err != nil {
		t.Fatal(err)
	}
	changed, err := WriteUnitWithPrevious(path, []byte("new unit"))
	if err != nil || !changed {
		t.Fatalf("replace changed=%v err=%v", changed, err)
	}
	if got, _ := os.ReadFile(path); string(got) != "new unit" {
		t.Fatalf("new unit = %q", got)
	}
	if got, _ := os.ReadFile(path + ".prev"); string(got) != "old unit" {
		t.Fatalf("previous unit = %q", got)
	}
	if err := RestorePreviousUnit(path); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "old unit" {
		t.Fatalf("restored unit = %q", got)
	}
}

func TestRefreshOutcomeAndAckFiles(t *testing.T) {
	dir := t.TempDir()
	outcomePath := filepath.Join(dir, RefreshOutcomeFile)
	want := RefreshOutcome{At: time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC), FromGen: 2, ToGen: 3, Result: RefreshRolledBack, Reason: "service did not stay active"}
	if err := WriteRefreshOutcome(outcomePath, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadRefreshOutcome(outcomePath)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("outcome = %+v, err=%v", got, err)
	}
	ackPath := filepath.Join(dir, RefreshAckFile)
	token := "0123456789abcdef0123456789abcdef"
	if err := WriteRefreshAck(ackPath, token); err != nil {
		t.Fatal(err)
	}
	if err := WaitRefreshAck(context.Background(), ackPath, token); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ackPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ack file remains: %v", err)
	}
}

func TestRefreshInstallWaitsForStableService(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ServiceName)
	if err := os.WriteFile(path, []byte("generation 2"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	states := []string{"active", "active", "active"}
	var calls []string
	out, err := RefreshInstall(context.Background(), RefreshConfig{
		UnitPath: path, Binary: "/usr/local/bin/mistgate-node", StateDir: "/var/lib/mistgate-node", FromGen: 2, ToGen: 3,
		Render: func(string, string, uint64) (string, error) { return "generation 3", nil },
		Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			calls = append(calls, name+" "+strings.Join(args, " "))
			if len(args) > 0 && args[0] == "is-active" {
				state := states[0]
				states = states[1:]
				if state == "active" {
					return []byte(state), nil
				}
				return []byte(state), errors.New(state)
			}
			return nil, nil
		},
		Now: func() time.Time { return now }, Sleep: func(_ context.Context, d time.Duration) { now = now.Add(d) },
		WaitTimeout: 4 * time.Second, StableFor: 2 * time.Second, PollInterval: time.Second,
	})
	if err != nil || out.Result != RefreshOK {
		t.Fatalf("refresh = %+v, err=%v", out, err)
	}
	if got, _ := os.ReadFile(path); string(got) != "generation 3" {
		t.Fatalf("unit file = %q", got)
	}
	if got, _ := os.ReadFile(path + ".prev"); string(got) != "generation 2" {
		t.Fatalf("previous unit = %q", got)
	}
	if !reflect.DeepEqual(calls[:2], []string{"systemctl daemon-reload", "systemctl restart " + ServiceName}) {
		t.Fatalf("systemctl calls = %v", calls)
	}
}

func TestRefreshInstallRollsBackWhenServiceDoesNotStayActive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ServiceName)
	if err := os.WriteFile(path, []byte("generation 2"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	states := []string{"active", "inactive", "inactive", "inactive", "inactive", "active", "active", "active"}
	out, err := RefreshInstall(context.Background(), RefreshConfig{
		UnitPath: path, Binary: "/usr/local/bin/mistgate-node", StateDir: "/var/lib/mistgate-node", FromGen: 2, ToGen: 3,
		Render: func(string, string, uint64) (string, error) { return "generation 3", nil },
		Run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if len(args) > 0 && args[0] == "is-active" {
				state := states[0]
				states = states[1:]
				if state == "active" {
					return []byte(state), nil
				}
				return []byte(state), errors.New(state)
			}
			return nil, nil
		},
		Now: func() time.Time { return now }, Sleep: func(_ context.Context, d time.Duration) { now = now.Add(d) },
		WaitTimeout: 4 * time.Second, StableFor: 2 * time.Second, PollInterval: time.Second,
	})
	if err == nil || out.Result != RefreshRolledBack || out.Reason == "" {
		t.Fatalf("failed refresh = %+v, err=%v", out, err)
	}
	if got, _ := os.ReadFile(path); string(got) != "generation 2" {
		t.Fatalf("unit file after rollback = %q", got)
	}
}

func TestRefreshInstallRollsBackAfterCallerCancellation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ServiceName)
	if err := os.WriteFile(path, []byte("generation 2"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	now := time.Unix(1_800_000_000, 0)
	var calls []string
	out, err := RefreshInstall(ctx, RefreshConfig{
		UnitPath: path, Binary: "/usr/local/bin/mistgate-node", StateDir: "/var/lib/mistgate-node", FromGen: 2, ToGen: 3,
		Render: func(string, string, uint64) (string, error) { return "generation 3", nil },
		Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			calls = append(calls, name+" "+strings.Join(args, " "))
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if len(args) > 0 && args[0] == "is-active" {
				return []byte("active"), nil
			}
			return nil, nil
		},
		Now: func() time.Time { return now }, Sleep: func(_ context.Context, d time.Duration) { now = now.Add(d) },
		WaitTimeout: 4 * time.Second, CommandTimeout: time.Second, StableFor: 2 * time.Second, PollInterval: time.Second,
	})
	if err == nil || out.Result != RefreshRolledBack {
		t.Fatalf("cancelled refresh = %+v, err=%v", out, err)
	}
	if got, _ := os.ReadFile(path); string(got) != "generation 2" {
		t.Fatalf("unit file after cancelled refresh = %q", got)
	}
	if len(calls) < 5 || calls[0] != "systemctl daemon-reload" || calls[1] != "systemctl daemon-reload" || calls[2] != "systemctl restart "+ServiceName {
		t.Fatalf("cancelled refresh calls = %v", calls)
	}
}

func TestRefreshBusyCheckDetectsLoadedUnit(t *testing.T) {
	calls := 0
	err := refreshBusy(context.Background(), func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls++
		if name != "systemctl" || strings.Join(args, " ") != "show --property=LoadState --value "+RefreshUnitName+".service" {
			t.Fatalf("busy check ran %s %v", name, args)
		}
		return []byte("loaded\n"), nil
	})
	if !errors.Is(err, ErrBusy) || calls != 1 {
		t.Fatalf("busy check = %v after %d calls", err, calls)
	}
}
