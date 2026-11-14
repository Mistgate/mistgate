package awgprep

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// failFirstRun is recRun whose command that starts with failFirst fails the first `times` times it runs, with the output of
// a DKMS build that does not compile.
type failFirstRun struct {
	recRun
	failFirst string
	times     int
}

func (r *failFirstRun) run(ctx context.Context, w io.Writer, dir string, cmd ...string) (string, error) {
	line := strings.Join(cmd, " ")
	if strings.HasPrefix(line, r.failFirst) && r.times > 0 {
		r.times--
		r.ran = append(r.ran, line)
		return dkmsBuildOutput, errors.New("exit status 100")
	}
	return r.recRun.run(ctx, w, dir, cmd...)
}

func (r *failFirstRun) count(prefix string) (n int) {
	for _, c := range r.ran {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// compatSys is goodSys with the module source on a memFS.
func compatSys(m *memFS, wrote *string) Sys {
	s, ms := goodSys(wrote), m.sys()
	s.ReadFile, s.SourceDirs = ms.ReadFile, ms.SourceDirs
	other := s.WriteFile
	s.WriteFile = func(p, body string) error {
		if _, ok := m.files[p]; ok {
			return ms.WriteFile(p, body)
		}
		return other(p, body)
	}
	return s
}

const (
	installModule = aptOpts + "amneziawg"
	configureCmd  = "dpkg --force-confdef --force-confold --configure -a"
)

// The build fails, the source is a known file: it is repaired, dpkg finishes, the step goes once more and the run completes.
func TestRunRepairsAKnownSourceAndRunsTheFailedStepAgain(t *testing.T) {
	m := newMemFS(fixture(t, "ppa-1.0.0.h", hashPPA))
	r := &failFirstRun{failFirst: installModule, times: 1}
	var wrote string
	var out strings.Builder
	if err := Run(context.Background(), &out, ubuntuPlanFor(t, "7.0.0-38-generic"), "/tmp/src", r.run, compatSys(m, &wrote), JobTimeout, nil); err != nil {
		t.Fatalf("err = %v\n%s", err, out.String())
	}
	if got := sha256Hex([]byte(m.files[compatPath(srcDir)])); got != hashFixed {
		t.Errorf("the source hashes to %s", got)
	}
	if r.count(installModule) != 2 || r.count(configureCmd) != 1 || r.count("apt-get -o DPkg::Lock::Timeout=1800 purge") != 0 {
		t.Fatalf("ran %v", r.ran)
	}
	// order: failed install, configure, install again, then the rest, ending with modprobe
	first, conf := indexOfPrefix(r.ran, installModule), indexOfPrefix(r.ran, configureCmd)
	if !(first < conf && conf < lastIndexOfPrefix(r.ran, installModule) && r.ran[len(r.ran)-1] == "modprobe amneziawg") {
		t.Errorf("order: %v", r.ran)
	}
	if !strings.Contains(out.String(), "the module source was repaired") {
		t.Errorf("the journal does not say so:\n%s", out.String())
	}
}

// The source is a file Mistgate does not know: it is not touched, the step is not repeated, the removal of the half-installed
// packages is the same as before, and the reason carries the start of the hash.
func TestRunLeavesAnUnknownSourceAndKeepsThePurgePath(t *testing.T) {
	body := fixture(t, "ppa-1.0.0.h", hashPPA) + "/* local */\n"
	m := newMemFS(body)
	r := &failFirstRun{failFirst: installModule, times: 1}
	var wrote string
	err := Run(context.Background(), io.Discard, ubuntuPlanFor(t, "7.0.0-38-generic"), "/tmp/src", r.run, compatSys(m, &wrote), JobTimeout, nil)
	code, detail := Short(err)
	sha := sha256Hex([]byte(body))[:12]
	if code != CodeBuildFailed || !strings.Contains(detail, sha) || !strings.Contains(detail, "nothing was changed") {
		t.Fatalf("short = %q %q", code, detail)
	}
	if m.files[compatPath(srcDir)] != body || m.writes != 0 {
		t.Error("an unknown file was written")
	}
	if r.count(installModule) != 1 || r.count(configureCmd) != 0 || r.ran[len(r.ran)-1] != purgeModule {
		t.Fatalf("ran %v", r.ran)
	}
}

// A repaired source whose build still fails: the step went twice, and then the existing removal applies.
func TestRunPurgesWhenTheBuildStillFailsAfterTheRepair(t *testing.T) {
	m := newMemFS(fixture(t, "handfixed.h", hashHand))
	r := &failFirstRun{failFirst: installModule, times: 2}
	var wrote string
	err := Run(context.Background(), io.Discard, ubuntuPlanFor(t, "7.0.0-38-generic"), "/tmp/src", r.run, compatSys(m, &wrote), JobTimeout, nil)
	if code, detail := Short(err); code != CodeBuildFailed || !strings.Contains(detail, "removed again") || strings.Contains(detail, "not one Mistgate knows") {
		t.Fatalf("short = %q %q", code, detail)
	}
	if r.count(installModule) != 2 || r.count(configureCmd) != 1 || r.ran[len(r.ran)-1] != purgeModule {
		t.Fatalf("ran %v", r.ran)
	}
}

// A failure that is not the module's own build does not look at the source.
func TestRunDoesNotTouchTheSourceForAnotherFailure(t *testing.T) {
	m := newMemFS(fixture(t, "ppa-1.0.0.h", hashPPA))
	r := &recRun{fail: installModule, out: map[string]string{installModule: "E: Unable to locate package amneziawg\n"}}
	var wrote string
	if err := Run(context.Background(), io.Discard, ubuntuPlanFor(t, "7.0.0-38-generic"), "/tmp/src", r.run, compatSys(m, &wrote), JobTimeout, nil); err == nil {
		t.Fatal("expected a failure")
	}
	if m.writes != 0 {
		t.Error("the source was written")
	}
}

// The Debian recipe repairs the clone before make.
func TestRunDebianRepairsTheCloneBeforeMake(t *testing.T) {
	src := t.TempDir()
	file := filepath.Join(src, "src", "compat", "compat.h")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(fixture(t, "ppa-1.0.0.h", hashPPA)), 0o644); err != nil {
		t.Fatal(err)
	}
	var atMake string
	r := &recRun{out: map[string]string{"git -C": pinnedModuleCommit + "\n"}}
	run := func(ctx context.Context, w io.Writer, dir string, cmd ...string) (string, error) {
		if cmd[0] == "make" {
			b, _ := os.ReadFile(file)
			atMake = sha256Hex(b)
		}
		return r.run(ctx, w, dir, cmd...)
	}
	var wrote string
	if err := Run(context.Background(), io.Discard, debianPlan(t), src, run, goodSys(&wrote), JobTimeout, nil); err != nil {
		t.Fatal(err)
	}
	if atMake != hashFixed {
		t.Errorf("make saw a source with sha256 %s, want the repaired one", atMake)
	}
}

func indexOfPrefix(list []string, prefix string) int {
	for i, c := range list {
		if strings.HasPrefix(c, prefix) {
			return i
		}
	}
	return -1
}

func lastIndexOfPrefix(list []string, prefix string) int {
	for i := len(list) - 1; i >= 0; i-- {
		if strings.HasPrefix(list[i], prefix) {
			return i
		}
	}
	return -1
}
