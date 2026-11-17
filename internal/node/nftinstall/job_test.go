package nftinstall

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/node/awgprep"
	"github.com/mistgate/mistgate/internal/node/unit"
)

// fakeHost is a server: apt-get install puts nft there unless it is told to fail.
type fakeHost struct {
	nft          bool
	calls        []string
	installFails int    // how many installs fail before one works
	failOutput   string // what a failing install prints
	nftStays     bool   // the install "works" but leaves no nft
}

func (h *fakeHost) run(_ context.Context, _ io.Writer, _ string, cmd ...string) (string, error) {
	h.calls = append(h.calls, strings.Join(cmd, " "))
	if cmd[0] == "apt-get" && cmd[len(cmd)-1] == Package {
		if h.installFails > 0 {
			h.installFails--
			return h.failOutput, errors.New("exit status 100")
		}
		h.nft = h.nft || !h.nftStays
	}
	return "", nil
}

func (h *fakeHost) job(status string) Job {
	return Job{
		StatusPath: status, Run: h.run, Now: func() time.Time { return time.Unix(1_800_000_000, 0) },
		Installed: func() bool { return h.nft }, HasApt: func() bool { return true },
		Sys: awgprep.Sys{AptBusy: func() bool { return false }},
	}
}

func (h *fakeHost) installs() int {
	n := 0
	for _, c := range h.calls {
		if strings.HasPrefix(c, "apt-get ") && strings.HasSuffix(c, " "+Package) {
			n++
		}
	}
	return n
}

func status(t *testing.T, path string) awgprep.Status {
	t.Helper()
	st, ok, err := awgprep.ReadStatus(path)
	if err != nil || !ok {
		t.Fatalf("status file: %v %v", ok, err)
	}
	return st
}

func TestInstallsNftablesQuietlyAndReportsDone(t *testing.T) {
	h := &fakeHost{}
	path := filepath.Join(t.TempDir(), StatusFileName)
	if err := h.job(path).Do(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !h.nft || h.installs() != 1 || len(h.calls) != 1 {
		t.Fatalf("calls %q", h.calls)
	}
	for _, want := range []string{"install", "-y", "--no-install-recommends", "--force-confold", "DPkg::Lock::Timeout=600"} {
		if !strings.Contains(h.calls[0], want) {
			t.Errorf("%q is missing from %q", want, h.calls[0])
		}
	}
	if st := status(t, path); st.State != awgprep.StateDone {
		t.Fatalf("status %+v", st)
	}
}

func TestNothingToDoWhenNftIsThere(t *testing.T) {
	h := &fakeHost{nft: true}
	if err := h.job("").Do(context.Background()); err != nil || len(h.calls) != 0 {
		t.Fatalf("err %v calls %q", err, h.calls)
	}
}

func TestFreshImageRefreshesListsAndTriesOnce(t *testing.T) {
	h := &fakeHost{installFails: 1, failOutput: "E: Unable to locate package nftables"}
	if err := h.job("").Do(context.Background()); err != nil {
		t.Fatal(err)
	}
	if h.installs() != 2 || !h.nft {
		t.Fatalf("calls %q", h.calls)
	}
	if h.calls[1] != "apt-get update" {
		t.Fatalf("the lists were not refreshed between the tries: %q", h.calls)
	}
}

func TestFailuresAreNamedInTheStatus(t *testing.T) {
	cases := []struct {
		name string
		host *fakeHost
		edit func(*Job)
		code string
	}{
		{"install fails twice", &fakeHost{installFails: 2, failOutput: "E: Unable to locate package"}, nil, awgprep.CodeStepFailed},
		{"another package manager holds the lock", &fakeHost{installFails: 2, failOutput: "E: Could not get lock /var/lib/dpkg/lock-frontend"}, nil, awgprep.CodeAptLock},
		{"no apt-get", &fakeHost{}, func(j *Job) { j.HasApt = func() bool { return false } }, awgprep.CodeStepFailed},
		{"installed but no nft", &fakeHost{nftStays: true}, nil, awgprep.CodeStepFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), StatusFileName)
			j := tc.host.job(path)
			if tc.edit != nil {
				tc.edit(&j)
			}
			if err := j.Do(context.Background()); err == nil {
				t.Fatal("the job reported success")
			}
			st := status(t, path)
			if st.State != awgprep.StateFailed || st.Code != tc.code || st.Reason == "" {
				t.Fatalf("status %+v", st)
			}
			if tc.name == "no apt-get" && len(tc.host.calls) != 0 {
				t.Fatalf("a package manager that is not there was called: %q", tc.host.calls)
			}
		})
	}
}

func TestWaitsForTheLockAndGivesUp(t *testing.T) {
	h := &fakeHost{}
	j := h.job("")
	j.Sys.AptBusy = func() bool { return true }
	j.Sys.Sleep = func(context.Context, time.Duration) {}
	j.LockWait = 6 * time.Second
	err := j.Do(context.Background())
	var f *awgprep.Failure
	if !errors.As(err, &f) || f.Code != awgprep.CodeAptLock || len(h.calls) != 0 {
		t.Fatalf("err %v calls %q", err, h.calls)
	}
}

func TestLaunchUsesAFixedUnitAndNoShell(t *testing.T) {
	var got []string
	err := launch(context.Background(), "/usr/local/bin/mistgate-node", "/var/lib/mistgate-node/"+StatusFileName,
		func(_ context.Context, name string, args ...string) ([]byte, error) {
			got = append([]string{name}, args...)
			return nil, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Join(got, " ")
	for _, want := range []string{"systemd-run", "--unit=" + UnitName, "host install-nftables --yes", "--status-file /var/lib/mistgate-node/" + StatusFileName} {
		if !strings.Contains(line, want) {
			t.Errorf("%q is missing from %q", want, line)
		}
	}
	busy := launch(context.Background(), "x", "y", func(context.Context, string, ...string) ([]byte, error) {
		return []byte("Failed to start transient service unit: Unit mistgate-install-nftables.service already exists."), errors.New("exit status 1")
	})
	if !errors.Is(busy, unit.ErrBusy) {
		t.Fatalf("a unit that is still there must read as busy: %v", busy)
	}
}
