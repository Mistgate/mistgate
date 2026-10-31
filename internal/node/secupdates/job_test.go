package secupdates

import (
	"context"
	"errors"
	"io"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/node/awgprep"
)

type reply struct {
	output string
	err    error
}

type fakeRunner struct {
	calls   [][]string
	replies map[string][]reply
	onFirst func()
}

func newFakeRunner() *fakeRunner { return &fakeRunner{replies: map[string][]reply{}} }

func (f *fakeRunner) add(key string, replies ...reply) {
	f.replies[key] = append(f.replies[key], replies...)
}

func (f *fakeRunner) run(ctx context.Context, w io.Writer, _ string, cmd ...string) (string, error) {
	call := append([]string(nil), cmd...)
	f.calls = append(f.calls, call)
	if len(f.calls) == 1 && f.onFirst != nil {
		f.onFirst()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	key := strings.Join(cmd, " ")
	queue := f.replies[key]
	if len(queue) == 0 {
		return "", errors.New("unexpected fake command: " + key)
	}
	result := queue[0]
	f.replies[key] = queue[1:]
	if result.output != "" {
		_, _ = io.WriteString(w, result.output)
	}
	return result.output, result.err
}

type fakeSys struct {
	busy   bool
	writes map[string]string
}

func newFakeSys() *fakeSys { return &fakeSys{writes: map[string]string{}} }

func (f *fakeSys) sys() awgprep.Sys {
	return awgprep.Sys{
		WriteFile: func(path, body string) error {
			f.writes[path] = body
			return nil
		},
		AptBusy: func() bool { return f.busy },
		Sleep:   func(context.Context, time.Duration) {},
	}
}

func commandKey(args ...string) string { return strings.Join(args, " ") }

func aptConfigCommandKey() string {
	return commandKey(append([]string{AptConfigCommand}, strings.Fields(AptPeriodicArgs)...)...)
}

func readStatus(t *testing.T, path string) awgprep.Status {
	t.Helper()
	status, ok, err := awgprep.ReadStatus(path)
	if err != nil || !ok {
		t.Fatalf("read status: status=%+v ok=%v err=%v", status, ok, err)
	}
	return status
}

func assertNoUpgradeOrReboot(t *testing.T, calls [][]string) {
	t.Helper()
	for _, cmd := range calls {
		if len(cmd) == 0 {
			continue
		}
		if cmd[0] == "unattended-upgrade" || cmd[0] == "reboot" || cmd[0] == "shutdown" {
			t.Errorf("forbidden command: %v", cmd)
		}
		if cmd[0] == "apt-get" && slices.Contains(cmd[1:], "upgrade") {
			t.Errorf("apt-get upgrade was run: %v", cmd)
		}
		if cmd[0] == "systemctl" && (slices.Contains(cmd[1:], "reboot") || slices.Contains(cmd[1:], "Automatic-Reboot")) {
			t.Errorf("reboot action or config was touched: %v", cmd)
		}
	}
}

func TestJobWaitsForAptLockAndWritesFailureStatus(t *testing.T) {
	runner := newFakeRunner()
	runner.add(commandKey("dpkg-query", "-W", "-f=${Status}", "unattended-upgrades"), reply{output: "deinstall ok config-files", err: errors.New("not installed")})
	sys := newFakeSys()
	sys.busy = true
	statusPath := t.TempDir() + "/security-updates.json"
	err := (Job{StatusPath: statusPath, LockWait: 3 * time.Second, Run: runner.run, Sys: sys.sys()}).Do(context.Background())
	var failure *awgprep.Failure
	if !errors.As(err, &failure) || failure.Code != awgprep.CodeAptLock {
		t.Fatalf("job error = %v, want apt_lock", err)
	}
	status := readStatus(t, statusPath)
	if status.State != awgprep.StateFailed || status.Code != awgprep.CodeAptLock || status.Finished == 0 {
		t.Fatalf("failure status = %+v", status)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("commands after busy lock = %v", runner.calls)
	}
}

func TestJobRetriesInstallAfterAptUpdateAndEnablesTimers(t *testing.T) {
	runner := newFakeRunner()
	runner.add(commandKey("dpkg-query", "-W", "-f=${Status}", "unattended-upgrades"), reply{output: "deinstall ok config-files", err: errors.New("not installed")})
	install := append([]string{"apt-get"}, installArgs()...)
	wantInstall := []string{
		"apt-get", "-o", "DPkg::Lock::Timeout=600", "-o", "Dpkg::Options::=--force-confdef",
		"-o", "Dpkg::Options::=--force-confold", "install", "-y", "--no-install-recommends", "unattended-upgrades",
	}
	if !slices.Equal(install, wantInstall) {
		t.Fatalf("install arguments = %v, want %v", install, wantInstall)
	}
	installKey := commandKey(install...)
	runner.add(installKey, reply{output: "temporary mirror error", err: errors.New("exit status 100")}, reply{})
	runner.add(commandKey("apt-get", "update"), reply{output: "unavailable third-party index", err: errors.New("exit status 100")})
	runner.add(aptConfigCommandKey(), reply{output: "E='1'; L='1'; U='1';"})
	runner.add(commandKey("systemctl", "is-enabled", "apt-daily.timer", "apt-daily-upgrade.timer"), reply{output: "masked\nenabled\n", err: errors.New("one timer is masked")})
	runner.add(commandKey("systemctl", "unmask", "apt-daily.timer"), reply{})
	runner.add(commandKey("systemctl", "enable", "--now", "apt-daily.timer"), reply{})

	sys := newFakeSys()
	statusPath := t.TempDir() + "/security-updates.json"
	runner.onFirst = func() {
		if status := readStatus(t, statusPath); status.State != awgprep.StateRunning {
			t.Errorf("first command saw status %+v, want running", status)
		}
	}
	err := (Job{StatusPath: statusPath, Run: runner.run, Sys: sys.sys()}).Do(context.Background())
	if err != nil {
		t.Fatalf("job failed: %v", err)
	}
	status := readStatus(t, statusPath)
	if status.State != awgprep.StateDone || status.Finished == 0 {
		t.Fatalf("done status = %+v", status)
	}
	if got := runner.calls[1]; !reflect.DeepEqual(got, install) {
		t.Fatalf("install args = %v, want %v", got, install)
	}
	if len(runner.calls) != 8 || runner.calls[2][0] != "apt-get" || runner.calls[2][1] != "update" {
		t.Fatalf("retry sequence = %v", runner.calls)
	}
	if runner.calls[6][0] != "systemctl" || !slices.Equal(runner.calls[6][1:], []string{"unmask", "apt-daily.timer"}) ||
		runner.calls[7][0] != "systemctl" || !slices.Equal(runner.calls[7][1:], []string{"enable", "--now", "apt-daily.timer"}) {
		t.Fatalf("timer commands = %v", runner.calls[6:])
	}
	if len(sys.writes) != 0 {
		t.Fatalf("unexpected config writes: %v", sys.writes)
	}
	assertNoUpgradeOrReboot(t, runner.calls)
}

func TestJobWritesOnlyDisabledAPTSettingsAndFailsWhenOverridden(t *testing.T) {
	for _, tc := range []struct {
		name      string
		second    string
		wantCode  string
		wantState string
	}{
		{name: "config enabled", second: "E='1'; L='1'; U='1';", wantState: awgprep.StateDone},
		{name: "later config overrides", second: "E='0'; L='0'; U='1';", wantCode: CodeOverridden, wantState: awgprep.StateFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := newFakeRunner()
			runner.add(commandKey("dpkg-query", "-W", "-f=${Status}", "unattended-upgrades"), reply{output: "install ok installed"})
			runner.add(aptConfigCommandKey(), reply{output: "E='0'; L='0'; U='1';"}, reply{output: tc.second})
			if tc.wantState == awgprep.StateDone {
				runner.add(commandKey("systemctl", "is-enabled", "apt-daily.timer", "apt-daily-upgrade.timer"), reply{output: "enabled\nenabled\n"})
			}
			sys := newFakeSys()
			err := (Job{Run: runner.run, Sys: sys.sys()}).Do(context.Background())
			if tc.wantCode == "" {
				if err != nil {
					t.Fatalf("job failed: %v", err)
				}
			} else {
				var failure *awgprep.Failure
				if !errors.As(err, &failure) || failure.Code != tc.wantCode {
					t.Fatalf("job error = %v, want %q", err, tc.wantCode)
				}
			}
			wantConfig := "# Managed by mistgate-node enable-security-updates.\n" +
				"APT::Periodic::Enable \"1\";\n" +
				"APT::Periodic::Update-Package-Lists \"1\";\n"
			if !reflect.DeepEqual(sys.writes, map[string]string{ConfigPath: wantConfig}) {
				t.Fatalf("config writes = %#v, want only %s with %q", sys.writes, ConfigPath, wantConfig)
			}
			if tc.wantState == awgprep.StateFailed {
				if len(runner.calls) != 3 || runner.calls[2][0] == "systemctl" {
					t.Fatalf("ran timer commands after overridden settings: %v", runner.calls)
				}
			}
		})
	}
}

func TestJobTimeoutWritesFailedStatus(t *testing.T) {
	statusPath := t.TempDir() + "/security-updates.json"
	runner := func(ctx context.Context, _ io.Writer, _ string, _ ...string) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}
	err := (Job{StatusPath: statusPath, Timeout: 10 * time.Millisecond, Run: runner, Sys: newFakeSys().sys()}).Do(context.Background())
	var failure *awgprep.Failure
	if !errors.As(err, &failure) || failure.Code != awgprep.CodeTimeout {
		t.Fatalf("job error = %v, want timeout", err)
	}
	status := readStatus(t, statusPath)
	if status.State != awgprep.StateFailed || status.Code != awgprep.CodeTimeout {
		t.Fatalf("timeout status = %+v", status)
	}
}
