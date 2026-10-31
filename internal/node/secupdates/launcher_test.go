package secupdates

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/mistgate/mistgate/internal/node/unit"
)

func TestLaunchSystemdRunUsesExactArgv(t *testing.T) {
	var gotName string
	var gotArgs []string
	err := launchSystemdRun(context.Background(), "/usr/lib/mistgate/mistgate-node", "/var/lib/mistgate-node/security-updates.json",
		func(_ context.Context, name string, args ...string) ([]byte, error) {
			gotName, gotArgs = name, args
			return nil, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"--quiet", "--collect", "--no-ask-password", "--unit=mistgate-security-updates",
		"--property=RuntimeMaxSec=1800", "--", "/usr/lib/mistgate/mistgate-node", "host", "enable-security-updates", "--yes",
		"--status-file", "/var/lib/mistgate-node/security-updates.json", "--timeout", "20m",
	}
	if gotName != "systemd-run" || !reflect.DeepEqual(gotArgs, want) {
		t.Fatalf("command = %q %v, want systemd-run %v", gotName, gotArgs, want)
	}
}

func TestLaunchSystemdRunMapsExistingUnitToBusy(t *testing.T) {
	err := launchSystemdRun(context.Background(), "/mistgate-node", "/state/security-updates.json",
		func(context.Context, string, ...string) ([]byte, error) {
			return []byte("Unit mistgate-security-updates.service already exists."), errors.New("exit status 1")
		})
	if !errors.Is(err, unit.ErrBusy) {
		t.Fatalf("launch error = %v, want unit.ErrBusy", err)
	}
}
