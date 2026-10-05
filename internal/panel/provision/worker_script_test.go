package provision

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// runScript runs script with sh -s, the given fake commands first on PATH; each fake logs "<name> <args>" to the
// returned log. It needs a POSIX shell (run it in WSL on Windows).
func runScript(t *testing.T, script string, args []string, fakes map[string]string) (output string, calls []string) {
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
	cmd := exec.Command("sh", append([]string{"-s", "--"}, args...)...)
	cmd.Stdin = strings.NewReader(script)
	cmd.Env = append(os.Environ(), "PATH="+dir+":/usr/bin:/bin", "CALLS="+log)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("script failed: %v: %s", err, out)
	}
	logged, _ := os.ReadFile(log)
	return string(out), strings.Split(strings.TrimSpace(string(logged)), "\n")
}

func TestFirewallScriptTagsUFWRulesAndNeverReloadsFirewalld(t *testing.T) {
	_, calls := runScript(t, remoteFirewallPreparationScript, []string{"2222"}, map[string]string{
		"ufw": `case "$1" in
status) echo "Status: active" ;;
show) printf 'Added user rules (see ufw status for running firewall):\nufw allow 80/tcp\n' ;;
esac`,
		"firewall-cmd": `case "$1" in
--get-active-zones) printf 'public\n  interfaces: eth0\n' ;;
esac`,
	})
	has := func(c string) bool {
		for _, got := range calls {
			if got == c {
				return true
			}
		}
		return false
	}
	for _, want := range []string{
		"ufw allow 2222/tcp", // the SSH rule: never tagged, so retire never removes it
		"ufw allow 443/tcp comment mistgate-node-provision-v1",
		"ufw allow 443/udp comment mistgate-node-provision-v1",
	} {
		if !has(want) {
			t.Errorf("missing %q in %q", want, calls)
		}
	}
	for _, got := range calls {
		if strings.HasPrefix(got, "ufw allow 80/tcp") {
			t.Errorf("the owner's own 80/tcp rule was taken over: %q", got)
		}
		if strings.Contains(got, "--reload") {
			t.Errorf("firewalld was reloaded: %q", got)
		}
	}
	for _, rule := range []string{"2222/tcp", "80/tcp", "443/tcp", "443/udp"} {
		if !has("firewall-cmd --zone=public --add-port="+rule) || !has("firewall-cmd --zone=public --add-port="+rule+" --permanent") {
			t.Errorf("firewalld %s not added at runtime and permanently: %q", rule, calls)
		}
	}
}

func TestPanelProbeIsBoundedByTimeout(t *testing.T) {
	command, err := preflightCommand("panel.example.com:443")
	if err != nil {
		t.Fatal(err)
	}
	probe := "set -eu\n" + strings.TrimPrefix(command, remotePreflightScript)
	out, calls := runScript(t, probe, nil, map[string]string{"timeout": "exit 124"})
	if !strings.Contains(out, "panel_reachable=no") {
		t.Fatalf("probe output = %q", out)
	}
	if len(calls) != 1 || !strings.HasPrefix(calls[0], "timeout 5 bash -c ") {
		t.Fatalf("probe was not bounded by timeout: %q", calls)
	}
}
