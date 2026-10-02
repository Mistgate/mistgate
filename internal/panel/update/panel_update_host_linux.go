//go:build linux

package update

import (
	"os"
	"os/exec"
	"runtime"
)

func panelUpdateHostSupported() bool {
	if os.Geteuid() != 0 {
		return false
	}
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return false
	}
	_, runErr := exec.LookPath("systemd-run")
	_, ctlErr := exec.LookPath("systemctl")
	return runErr == nil && ctlErr == nil
}

func canRunPanelUpdateHelper() bool  { return os.Geteuid() == 0 }
func panelUpdateRuntimeArch() string { return runtime.GOARCH }
