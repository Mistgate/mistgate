package awg

import (
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"runtime/debug"
	"strings"

	"github.com/mistgate/mistgate/internal/node/awg/awgnl"
)

// userspacePeerBytes is what one device costs the agent in the userspace backend (amneziawg-go keeps a peer's keys,
// queues and timers in this process). A measured round figure, not a guarantee.
const userspacePeerBytes = 26 << 10

// ReasonTooManyForUserspace is BackendStatus.Reason when "kernel" could not fall back for want of memory.
const ReasonTooManyForUserspace = "too many devices for userspace on this RAM"

// probes are the two ways to learn whether a backend is usable; tests replace them.
type probes struct {
	kernel, userspace func() (awgBackend, error)
	release           func() string // the running kernel, for the event
	memLimit          func() int64  // the agent's soft memory limit (GOMEMLIMIT), math.MaxInt64 = none
}

// userspaceFits says whether peers devices fit in the userspace backend of an agent with this memory limit.
// ponytail: the third of GOMEMLIMIT is a fixed share (the other protocols and the Go heap need the rest); a per-node
// setting for it is the upgrade path if a big node ever needs the fallback above this.
func userspaceFits(peers int, limit int64) bool {
	if limit <= 0 || limit == math.MaxInt64 { // no limit set (a lab, a test): nothing to protect
		return true
	}
	return int64(peers)*userspacePeerBytes <= limit/3
}

func runningKernel() string {
	b, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func defaultMemLimit() int64 { return debug.SetMemoryLimit(-1) } // -1 only reads it

// chooseBackend picks the backend once:
//
//	auto      the kernel module when its genetlink family answers with genl version 3 (it is already loaded: the
//	          agent installs no packages and runs no modprobe), else userspace when /dev/net/tun opens
//	kernel    the module; when it does not load, userspace as a fallback (BackendStatus.Fallback) unless the node has
//	          so many devices that userspace would not fit in memory, then nothing (awg_backend_unavailable)
//	userspace amneziawg-go or nothing
//
// The fallback never switches back by itself: the engine is built once, and a rebuild (the doctor fix, a setting change,
// an agent start) is what asks the module again.
func chooseBackend(mode string, log *slog.Logger, pr probes, peers func() int) (awgBackend, BackendStatus) {
	st := BackendStatus{Mode: mode}
	reason := ""
	if mode != "userspace" {
		k, err := pr.kernel()
		switch {
		case err == nil:
			st.Name, st.Version, st.Available = k.Name(), k.Version(), true
			return k, st
		case errors.Is(err, awgnl.ErrNoModule):
			reason = "the amneziawg kernel module is not loaded"
		default:
			reason = "the amneziawg kernel module is not usable: " + err.Error()
		}
		if mode == "kernel" {
			n := 0
			if peers != nil {
				n = peers()
			}
			if !userspaceFits(n, pr.memLimit()) {
				st.Reason = reason + "; " + ReasonTooManyForUserspace
				return nil, st
			}
			st.Fallback = true
			log.Warn("awg: the kernel module is not usable, AmneziaWG runs in userspace", "reason", reason)
		} else {
			log.Info("awg: kernel module not used, falling back to userspace", "reason", reason)
		}
	}
	u, err := pr.userspace()
	if err != nil {
		st.Fallback = false
		st.Reason = err.Error()
		if reason != "" {
			st.Reason = fmt.Sprintf("%s; %s", reason, st.Reason)
		}
		return nil, st
	}
	st.Name, st.Version, st.Available = u.Name(), u.Version(), true
	if st.Fallback {
		st.Reason = reason
	}
	return u, st
}
