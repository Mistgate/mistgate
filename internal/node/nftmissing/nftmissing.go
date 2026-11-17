// Package nftmissing names one failure the agent meets on a server without the nft command (a cloud image of Debian 13
// ships without the nftables package): what hostctl and warp return for it, how the agent says it once instead of every
// cycle, and where to look for the command.
package nftmissing

import (
	"errors"
	"os"
	"os/exec"
	"sync"
)

// ErrMissing is wrapped by every failure that comes from the nft command not being there. Its text is the one the
// agent always used, so a log search for it keeps working.
var ErrMissing = errors.New("nft is not installed")

// Is reports whether err, or any error joined into it, is the missing-nft failure.
func Is(err error) bool { return errors.Is(err, ErrMissing) }

// Rest is err without the missing-nft failures: what is left to log when the missing command is reported on its own.
// It is nil when nothing else failed.
func Rest(err error) error {
	if err == nil {
		return nil
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var rest []error
		for _, e := range joined.Unwrap() {
			if r := Rest(e); r != nil {
				rest = append(rest, r)
			}
		}
		return errors.Join(rest...)
	}
	if Is(err) {
		return nil
	}
	return err
}

// sbinDirs are where the package puts the command; a root shell started without them on PATH (su without "-") does not
// see it, though the agent's systemd service does.
var sbinDirs = []string{"/usr/sbin/nft", "/sbin/nft", "/usr/local/sbin/nft", "/usr/bin/nft"}

// Installed reports whether the nft command exists: on PATH, else in the usual system directories.
func Installed() bool {
	if _, err := exec.LookPath("nft"); err == nil {
		return true
	}
	for _, p := range sbinDirs {
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return true
		}
	}
	return false
}

// InstallHint is the one command that puts the package on the server, in the words of the owner's package manager.
func InstallHint() string {
	for _, c := range []struct{ tool, command string }{
		{"apt-get", "apt-get install -y nftables"},
		{"dnf", "dnf install -y nftables"},
		{"yum", "yum install -y nftables"},
		{"zypper", "zypper install -y nftables"},
		{"pacman", "pacman -S --noconfirm nftables"},
		{"apk", "apk add nftables"},
	} {
		if _, err := exec.LookPath(c.tool); err == nil {
			return c.command
		}
	}
	return "install the nftables package with your package manager"
}

// Watch turns a failure that repeats every cycle into two facts: it appeared, it is gone.
type Watch struct {
	mu      sync.Mutex
	missing bool
}

// Appeared records a failure caused by the missing command. first is true only for the first of a run of them.
func (w *Watch) Appeared() (first bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	first = !w.missing
	w.missing = true
	return first
}

// Gone records that nft worked again. It is true only when the failure had been recorded.
func (w *Watch) Gone() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	was := w.missing
	w.missing = false
	return was
}
