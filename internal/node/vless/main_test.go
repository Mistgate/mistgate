package vless

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"testing"
)

// On Windows xray reads with a blocking WSARecv (common/buf readv_windows.go), and closing that socket waits for the read.
// The tests' in-process Xray client reads the app's side that way under Vision, so when our server ends a session the
// client's Close waits forever and the app never sees the end. Nodes run Linux, where readv goes through the poller and
// Close interrupts it. xray reads the switch once at init, so the test binary runs itself again with readv off.
func TestMain(m *testing.M) {
	if runtime.GOOS == "windows" && os.Getenv("XRAY_BUF_READV") != "disable" {
		cmd := exec.Command(os.Args[0], os.Args[1:]...)
		cmd.Env = append(os.Environ(), "XRAY_BUF_READV=disable")
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		var exit *exec.ExitError
		switch err := cmd.Run(); {
		case err == nil:
			os.Exit(0)
		case errors.As(err, &exit):
			os.Exit(exit.ExitCode())
		default:
			panic(err)
		}
	}
	os.Exit(m.Run())
}
