//go:build !linux

package awg

import "log/slog"

// AmneziaWG runs on Linux nodes only. Elsewhere the package builds so the whole tree does, and every awg inbound
// fails with awg_backend_unavailable.
func pickBackend(mode string, _ *slog.Logger) (awgBackend, BackendStatus) {
	return nil, BackendStatus{Mode: mode, Reason: "AmneziaWG is supported on Linux only"}
}

func portListening(uint16) bool { return true }
