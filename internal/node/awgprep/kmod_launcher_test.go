package awgprep

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestKmodSystemdArgsBoundBootAndRepairWithoutWaiting(t *testing.T) {
	exe, status := "/usr/local/bin/mistgate-node", "/var/lib/mistgate-node/awg-kmod.json"
	boot, err := KmodSystemdArgs(exe, status, KmodBoot)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(boot, "--unit="+KmodUnitName) || !slices.Contains(boot, "--collect") ||
		!slices.Contains(boot, "--property=RuntimeMaxSec=240") || slices.Contains(boot, "--wait") ||
		!slices.Contains(boot, status) || !slices.Contains(boot, "--boot") {
		t.Fatalf("boot systemd-run args = %v", boot)
	}
	repair, err := KmodSystemdArgs(exe, status, KmodRepair)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(repair, "--wait") || !slices.Contains(repair, "--property=RuntimeMaxSec=3600") || !slices.Contains(repair, "--repair") {
		t.Fatalf("repair systemd-run args = %v", repair)
	}
	if _, err := KmodSystemdArgs("/tmp/bad path/mistgate-node", status, KmodBoot); err == nil {
		t.Fatal("unsafe systemd command path was accepted")
	}
}

func TestKmodControllerDoesNotDuplicateRunningUnit(t *testing.T) {
	status := filepath.Join(t.TempDir(), KmodStatusFileName)
	now := time.Now().Unix()
	if err := WriteKmodStatus(status, KmodStatus{Mode: KmodBoot, State: StateRunning, Started: now, Updated: now}); err != nil {
		t.Fatal(err)
	}
	launches := 0
	ctl := NewKmodController(status, func() bool { return true }, func(context.Context) error { launches++; return nil })
	ctl.Now = func() time.Time { return time.Unix(now, 0) }
	if ctl.StartBoot(context.Background()) || launches != 0 {
		t.Fatalf("duplicate unit was started: launches=%d", launches)
	}
}

func TestKmodControllerDoesNotLaunchWhenUnneeded(t *testing.T) {
	status := filepath.Join(t.TempDir(), KmodStatusFileName)
	launches := 0
	ctl := NewKmodController(status, func() bool { return false }, func(context.Context) error { launches++; return nil })
	if ctl.StartBoot(context.Background()) || launches != 0 {
		t.Fatalf("unneeded unit was started: launches=%d", launches)
	}
}

func TestKmodControllerRecordsLaunchFailure(t *testing.T) {
	status := filepath.Join(t.TempDir(), KmodStatusFileName)
	ctl := NewKmodController(status, func() bool { return true }, func(context.Context) error { return errors.New("launch failed") })
	if ctl.StartBoot(context.Background()) {
		t.Fatal("failed unit launch was reported as started")
	}
	st, ok, err := ReadKmodStatus(status)
	if err != nil || !ok || st.Mode != KmodBoot || st.State != StateFailed {
		t.Fatalf("status=%+v ok=%v err=%v", st, ok, err)
	}
}
