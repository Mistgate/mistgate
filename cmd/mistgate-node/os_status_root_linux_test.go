//go:build linux

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/node/osupdate"
	"google.golang.org/protobuf/encoding/protojson"
)

const (
	rootReadHelperEnv = "MG_OS_STATUS_HELPER"
	rootReadMarker    = "MG-OS-STATUS "
)

// TestRootReaderHelper is the child of TestOsStatusReadInsideAgentSandbox: inside the transient unit it runs the real
// reader once and prints the snapshot after a marker. In an ordinary test run it does nothing.
func TestRootReaderHelper(t *testing.T) {
	if os.Getenv(rootReadHelperEnv) != "1" {
		t.Skip("child of the sandbox test")
	}
	status, err := osupdate.NewReader().Read(context.Background())
	if err != nil {
		fmt.Printf("%sERROR %v\n", rootReadMarker, err)
		t.Fatal(err)
	}
	body, err := protojson.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Printf("%s%s\n", rootReadMarker, body)
}

// sandboxProperties reads the hardening block of the agent unit template (renderUnit) and returns its settings as
// systemd-run --property arguments. ReadWritePaths is left out: it names the state directory and drop-in directories of
// an installed agent, which a test host need not have, and the reader writes to none of them.
func sandboxProperties(t *testing.T) []string {
	t.Helper()
	text, err := renderUnit(defaultBin, "/var/lib/mistgate-node", 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	_, hardening, ok := strings.Cut(text, "# Hardening.\n")
	if !ok {
		t.Fatal("the unit template has no hardening block")
	}
	hardening, _, _ = strings.Cut(hardening, "[Install]")
	var props []string
	scanner := bufio.NewScanner(strings.NewReader(hardening))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "ReadWritePaths=") {
			continue
		}
		props = append(props, "--property="+line)
	}
	if len(props) < 10 {
		t.Fatalf("read only %d sandbox settings from the unit template: %q", len(props), props)
	}
	return props
}

// aptTreeState records the name, size and mtime of everything under the directories apt keeps its state and logs in.
func aptTreeState(t *testing.T) map[string]string {
	t.Helper()
	state := map[string]string{}
	for _, root := range []string{"/var/log/apt", "/var/lib/apt"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil // a file that vanishes under a concurrent apt run is not this test's finding
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			state[path] = fmt.Sprintf("%d %d", info.Size(), info.ModTime().UnixNano())
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return state
}

// TestOsStatusReadInsideAgentSandbox runs the real reader in a transient systemd unit that carries the sandbox settings of
// the agent unit and checks that the read succeeds and that nothing under /var/log/apt or /var/lib/apt changes. It needs
// root, systemd and apt, so it is opt-in:
//
//	MG_ROOT_TESTS=1 go test -run OsStatusReadInsideAgentSandbox ./cmd/mistgate-node   (as root, on an idle Debian or Ubuntu host)
//
// The apt directories are compared before and after, so a package manager running at the same time can make it fail;
// run it on a quiet host. The copy of the test binary lives in /run/mgu-osread-* and is removed at the end.
func TestOsStatusReadInsideAgentSandbox(t *testing.T) {
	if os.Getenv("MG_ROOT_TESTS") != "1" {
		t.Skip("set MG_ROOT_TESTS=1 to run against the real systemd and apt (root only)")
	}
	if os.Geteuid() != 0 {
		t.Skip("needs root")
	}
	for _, name := range []string{"systemd-run", "apt-get", "dpkg-query"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skipf("%s is not installed", name)
		}
	}
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		t.Skip("systemd is not running")
	}

	// PrivateTmp and ProtectHome hide where `go test` builds, so the child binary goes to /run, which stays readable.
	dir, err := os.MkdirTemp("/run", "mgu-osread-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	self, err := os.ReadFile(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(dir, "child.test")
	if err := os.WriteFile(child, self, 0o755); err != nil {
		t.Fatal(err)
	}

	args := []string{"--quiet", "--wait", "--pipe", "--collect", "--no-ask-password",
		"--unit=" + filepath.Base(dir), "--property=RuntimeMaxSec=120s", "--setenv=" + rootReadHelperEnv + "=1"}
	args = append(args, sandboxProperties(t)...)
	args = append(args, "--", child, "-test.run=^TestRootReaderHelper$", "-test.count=1")

	before := aptTreeState(t)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "systemd-run", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("the read failed inside the sandbox: %v\n%s", err, out)
	}
	var body string
	for _, line := range strings.Split(string(out), "\n") {
		if rest, ok := strings.CutPrefix(line, rootReadMarker); ok {
			body = rest
		}
	}
	if body == "" || strings.HasPrefix(body, "ERROR") {
		t.Fatalf("no snapshot came back from the sandbox: %q\n%s", body, out)
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(body), &fields); err != nil {
		t.Fatalf("snapshot is not JSON: %v\n%s", err, body)
	}
	if fields["error"] != nil {
		t.Fatalf("the snapshot carries an error: %v", fields["error"])
	}
	if fields["measuredUnix"] == nil || fields["distroId"] == nil {
		t.Fatalf("the snapshot is empty: %s", body)
	}

	after := aptTreeState(t)
	for path, was := range before {
		if now, ok := after[path]; !ok || now != was {
			t.Errorf("%s changed during the read: %q -> %q", path, was, now)
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			t.Errorf("%s was created during the read", path)
		}
	}
}
