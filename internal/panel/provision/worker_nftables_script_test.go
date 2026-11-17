package provision

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// runIsolated runs script with sh -s on a PATH that holds only the fake commands and symlinks to the named real tools,
// so a command that must be missing on the "server" (nft, apt-get) is missing whatever the machine under test has. The
// fakes find the directory they live in as ${CALLS%/*}. It needs a POSIX shell (run it in WSL on Windows).
func runIsolated(t *testing.T, script string, fakes map[string]string, tools ...string) (output string, calls []string, exit int) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	for name, body := range fakes {
		fake := "#!/bin/sh\necho \"" + name + " $*\" >> \"$CALLS\"\n" + body + "\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(fake), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, tool := range tools {
		path, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("no %s on this machine", tool)
		}
		if err := os.Symlink(path, filepath.Join(dir, tool)); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("sh", "-s")
	cmd.Stdin = strings.NewReader(script)
	cmd.Env = []string{"PATH=" + dir, "CALLS=" + log}
	out, err := cmd.CombinedOutput()
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("script did not run: %v", err)
		}
		exit = exitErr.ExitCode()
	}
	logged, _ := os.ReadFile(log)
	if s := strings.TrimSpace(string(logged)); s != "" {
		calls = strings.Split(s, "\n")
	}
	return string(out), calls, exit
}

func countPrefix(calls []string, prefix string) int {
	n := 0
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func TestPreflightReportsNft(t *testing.T) {
	command, err := preflightCommand("panel.example.com:443")
	if err != nil {
		t.Fatal(err)
	}
	// the preflight reads the system, so it runs on the real tools; only nft is the fake's to give or to withhold
	tools := []string{"id", "getconf", "awk", "df", "sed", "uname"}
	for _, tc := range []struct {
		name string
		nft  bool
		want string
	}{{"with nft", true, "nftables=yes"}, {"without nft", false, "nftables=no"}} {
		t.Run(tc.name, func(t *testing.T) {
			fakes := map[string]string{"getent": getentFake}
			if tc.nft {
				fakes["nft"] = ""
			}
			out, _, _ := runIsolated(t, command, fakes, tools...)
			if !strings.Contains(out, tc.want+"\n") || strings.Count(out, "nftables=") != 1 {
				t.Fatalf("preflight output: %q", out)
			}
		})
	}
}

// aptFake is an apt-get that records its call and then does what the scenario says: the numbered reply of its kind
// (install or update) from installs and updates, "ok" meaning it works (a working install leaves an nft behind when
// leavesNft), "lock" that another package manager holds the lock, anything else that the package is not found.
func aptFake(installs, updates string, leavesNft bool) string {
	leave := ":"
	if leavesNft {
		leave = `: > "${CALLS%/*}/nft"; chmod +x "${CALLS%/*}/nft"`
	}
	return `case "$*" in *update*) kind=update ;; *) kind=install ;; esac
echo "env DEBIAN_FRONTEND=$DEBIAN_FRONTEND NEEDRESTART_SUSPEND=$NEEDRESTART_SUSPEND" >> "$CALLS"
n=$(cat "${CALLS%/*}/$kind.n" 2>/dev/null || echo 0); n=$((n + 1)); echo $n > "${CALLS%/*}/$kind.n"
if [ "$kind" = install ]; then replies="` + installs + `"; else replies="` + updates + `"; fi
set -- $replies
reply=ok
i=1; for r in "$@"; do if [ $i -eq $n ]; then reply=$r; fi; i=$((i + 1)); done
case "$reply" in
  ok) ` + leave + `; exit 0 ;;
  lock) echo "E: Could not get lock /var/lib/dpkg/lock-frontend. It is held by process 412 (apt)" >&2; exit 100 ;;
  *) echo "E: Unable to locate package nftables" >&2; exit 100 ;;
esac`
}

func TestNftablesInstallScript(t *testing.T) {
	with := func(fakes map[string]string) map[string]string {
		fakes["sleep"] = "" // the 5 s waits cost nothing, and the log counts them
		return fakes
	}
	run := func(t *testing.T, fakes map[string]string) ([]string, int) {
		t.Helper()
		_, calls, exit := runIsolated(t, remoteNftablesInstallScript, with(fakes), "cat", "chmod") // what the fake apt-get uses
		return calls, exit
	}

	t.Run("nft is there: nothing is installed", func(t *testing.T) {
		calls, exit := run(t, map[string]string{"nft": "", "apt-get": aptFake("ok", "ok", true)})
		if exit != 0 || countPrefix(calls, "apt-get") != 0 {
			t.Fatalf("exit %d, calls %q", exit, calls)
		}
	})

	t.Run("a fresh image: installs the one package without questions", func(t *testing.T) {
		calls, exit := run(t, map[string]string{"apt-get": aptFake("ok", "ok", true)})
		if exit != 0 || countPrefix(calls, "apt-get") != 1 || countPrefix(calls, "sleep") != 0 {
			t.Fatalf("exit %d, calls %q", exit, calls)
		}
		for _, want := range []string{"install -y --no-install-recommends nftables", "--force-confdef", "--force-confold",
			"env DEBIAN_FRONTEND=noninteractive NEEDRESTART_SUSPEND=1"} {
			if !strings.Contains(strings.Join(calls, "\n"), want) {
				t.Errorf("%q is missing from %q", want, calls)
			}
		}
	})

	t.Run("lists not fetched yet: refreshes them and installs again", func(t *testing.T) {
		calls, exit := run(t, map[string]string{"apt-get": aptFake("fail ok", "ok", true)})
		if exit != 0 || countPrefix(calls, "apt-get") != 3 {
			t.Fatalf("exit %d, calls %q", exit, calls)
		}
		var last []string
		for _, c := range calls {
			if strings.HasPrefix(c, "apt-get") {
				fields := strings.Fields(c)
				last = append(last, fields[len(fields)-1])
			}
		}
		if strings.Join(last, " ") != "nftables update nftables" {
			t.Fatalf("order %q", last)
		}
	})

	t.Run("another package manager holds the lock: waits and goes on", func(t *testing.T) {
		calls, exit := run(t, map[string]string{"apt-get": aptFake("lock lock ok", "ok", true)})
		if exit != 0 || countPrefix(calls, "sleep 5") != 2 || countPrefix(calls, "apt-get") != 3 {
			t.Fatalf("exit %d, calls %q", exit, calls)
		}
	})

	t.Run("the lock never frees: gives up after the bounded wait", func(t *testing.T) {
		locked := strings.Repeat("lock ", 80)
		calls, exit := run(t, map[string]string{"apt-get": aptFake(locked, locked, true)})
		if exit != 21 {
			t.Fatalf("exit %d", exit)
		}
		if n := countPrefix(calls, "sleep 5"); n == 0 || n > 60 {
			t.Fatalf("waited %d times 5 s, the wait is bounded by 60 (5 minutes)", n)
		}
	})

	t.Run("no package manager", func(t *testing.T) {
		calls, exit := run(t, map[string]string{})
		if exit != 20 || countPrefix(calls, "apt-get") != 0 {
			t.Fatalf("exit %d, calls %q", exit, calls)
		}
	})

	t.Run("the install fails twice", func(t *testing.T) {
		calls, exit := run(t, map[string]string{"apt-get": aptFake("fail fail", "ok", true)})
		if exit != 21 || countPrefix(calls, "apt-get") != 3 {
			t.Fatalf("exit %d, calls %q", exit, calls)
		}
	})

	t.Run("the package installs but nft is still missing", func(t *testing.T) {
		_, exit := run(t, map[string]string{"apt-get": aptFake("ok", "ok", false)})
		if exit != 21 {
			t.Fatalf("exit %d", exit)
		}
	})
}

// Every script the install sends parses in a POSIX shell (dash on Debian and Ubuntu).
func TestRemoteScriptsParse(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	command, err := preflightCommand("panel.example.com:443")
	if err != nil {
		t.Fatal(err)
	}
	for name, script := range map[string]string{"preflight": command, "firewall": remoteFirewallPreparationScript, "nftables": remoteNftablesInstallScript} {
		cmd := exec.Command("sh", "-n")
		cmd.Stdin = strings.NewReader(script)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Errorf("%s script does not parse: %v: %s", name, err, out)
		}
	}
}
