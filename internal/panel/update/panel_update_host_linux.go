//go:build linux

package update

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
)

func panelUpdateHostSupported() bool {
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return false
	}
	systemctl, err := exec.LookPath("systemctl")
	if err != nil {
		return false
	}
	if canRunPanelUpdateHelper() {
		_, runErr := exec.LookPath("systemd-run")
		return runErr == nil
	}
	// A non-root panel can only request the fixed helper installed by the operator. Checking that systemd has
	// loaded it prevents the UI from offering an update path that will certainly fail when the button is pressed.
	out, err := exec.Command(systemctl, "show", "--property=LoadState", "--value", panelUpdateHelperService).Output()
	return err == nil && strings.TrimSpace(string(out)) == "loaded"
}

func canRunPanelUpdateHelper() bool          { return os.Geteuid() == 0 }
func panelUpdateUsesRootHelperService() bool { return !canRunPanelUpdateHelper() }
func panelUpdateRuntimeArch() string         { return runtime.GOARCH }
