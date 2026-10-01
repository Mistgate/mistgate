//go:build linux

package awg

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/mistgate/mistgate/internal/node/awg/awgnl"
)

// pickBackend chooses the backend once:
//
//	auto      the kernel module when its genetlink family answers with genl version 3 (it is already loaded: the
//	          agent installs no packages and runs no modprobe), else userspace when /dev/net/tun opens
//	kernel    the module or nothing (awg_backend_unavailable)
//	userspace amneziawg-go or nothing
func pickBackend(mode string, log *slog.Logger) (awgBackend, BackendStatus) {
	st := BackendStatus{Mode: mode}
	reason := ""
	if mode != "userspace" {
		k, err := newKernel(log)
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
			st.Reason = reason
			return nil, st
		}
		log.Info("awg: kernel module not used, falling back to userspace", "reason", reason)
	}
	u, err := newUserspace(log)
	if err != nil {
		st.Reason = err.Error()
		if reason != "" {
			st.Reason = fmt.Sprintf("%s; %s", reason, st.Reason)
		}
		return nil, st
	}
	st.Name, st.Version, st.Available = u.Name(), u.Version(), true
	return u, st
}
