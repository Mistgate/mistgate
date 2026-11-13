//go:build linux

package awg

import "log/slog"

// pickBackend is chooseBackend with the real probes: a genetlink lookup for the module, opening /dev/net/tun for userspace.
func pickBackend(mode string, log *slog.Logger, peers func() int) (awgBackend, BackendStatus) {
	return chooseBackend(mode, log, probes{
		kernel: func() (awgBackend, error) {
			k, err := newKernel(log)
			if err != nil {
				return nil, err
			}
			return k, nil
		},
		userspace: func() (awgBackend, error) {
			u, err := newUserspace(log)
			if err != nil {
				return nil, err
			}
			return u, nil
		},
		release:  runningKernel,
		memLimit: defaultMemLimit,
	}, peers)
}
