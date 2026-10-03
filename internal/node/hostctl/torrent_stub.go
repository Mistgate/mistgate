//go:build !linux

package hostctl

import "context"

var _ TorrentGuardHost = stub{}

// TorrentGuardSupported reports that this build has no Linux NFQUEUE runtime.
func TorrentGuardSupported() bool { return false }

func (stub) SetTorrentGuard(context.Context, []string, func(TorrentDetection)) error { return nil }
