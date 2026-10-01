//go:build linux

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestUnitPassesSystemdVerify feeds the rendered unit to `systemd-analyze verify`, which parses every
// directive and rejects unknown or malformed ones. It does not start anything. Skipped without systemd.
func TestUnitPassesSystemdVerify(t *testing.T) {
	if _, err := exec.LookPath("systemd-analyze"); err != nil {
		t.Skip("systemd-analyze not installed")
	}
	self, err := os.Executable() // any existing executable satisfies the ExecStart check
	if err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	u, err := renderUnit(self, state, 2<<30)
	if err != nil {
		t.Skipf("test paths are not unit-safe: %v", err)
	}
	path := filepath.Join(t.TempDir(), unitName)
	if err := os.WriteFile(path, []byte(u), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("systemd-analyze", "verify", path).CombinedOutput()
	// Warnings about units that are absent in a container are not ours; an unknown-directive or parse
	// complaint about the unit file itself is.
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, unitName) && (strings.Contains(line, "Unknown") || strings.Contains(line, "Invalid") || strings.Contains(line, "Failed to parse") || strings.Contains(line, "Unknown key")) {
			t.Errorf("systemd rejected the unit: %s", line)
		}
	}
	if err != nil && strings.Contains(string(out), "Unknown") {
		t.Fatalf("systemd-analyze: %v\n%s", err, out)
	}
	t.Logf("systemd-analyze verify: %v\n%s", err, out)
}
