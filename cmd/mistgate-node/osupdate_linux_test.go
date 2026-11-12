//go:build linux

package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

// capture runs f with stdout and stderr redirected and returns what it printed.
func capture(t *testing.T, f func() int) (code int, out string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = w, w
	done := make(chan string)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	code = f()
	os.Stdout, os.Stderr = oldOut, oldErr
	_ = w.Close()
	return code, <-done
}

// None of these pass --yes: a test must never run the job on the machine it runs on.
func TestHostOSUpdatePrintsThePlanAndRunsNothingWithoutYes(t *testing.T) {
	state := t.TempDir()
	code, out := capture(t, func() int {
		return dispatch([]string{"host", "os-update", "--state-dir", state, "--action", "upgrade", "--mode", "security", "--reboot", "if-required"})
	})
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	for _, want := range []string{"unattended-upgrade", "reboot, as the very last step", "AmneziaWG module", "Nothing was run"} {
		if !strings.Contains(out, want) {
			t.Errorf("plan lacks %q:\n%s", want, out)
		}
	}
	if entries, _ := os.ReadDir(state); len(entries) != 0 {
		t.Fatalf("the dry run wrote %v", entries)
	}
}

func TestHostOSUpdateRefusesValuesOutsideTheVocabulary(t *testing.T) {
	for name, args := range map[string][]string{
		"action":         {"--action", "dist-upgrade"},
		"mode":           {"--action", "upgrade", "--mode", "everything"},
		"reboot":         {"--reboot", "now"},
		"always upgrade": {"--action", "upgrade", "--mode", "all", "--reboot", "always"},
		"refresh reboot": {"--action", "refresh", "--reboot", "if-required"},
		"reboot never":   {"--action", "reboot", "--reboot", "never"},
		"job id":         {"--job-id", "x; reboot"},
		"unknown flag":   {"--shell", "sh -c id"},
	} {
		code, out := capture(t, func() int { return dispatch(append([]string{"host", "os-update"}, args...)) })
		if code != 2 || strings.Contains(out, "Plan") {
			t.Errorf("%s: exit %d, output %q", name, code, out)
		}
	}
}

func TestHostUsageMentionsBothTasks(t *testing.T) {
	code, out := capture(t, func() int { return dispatch([]string{"host"}) })
	if code != 2 || !strings.Contains(out, "os-update") || !strings.Contains(out, "enable-security-updates") {
		t.Fatalf("exit %d: %s", code, out)
	}
}
