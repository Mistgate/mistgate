//go:build linux

package hostctl

import (
	"context"
	"errors"
	"fmt"

	torrentlinux "github.com/mistgate/mistgate/internal/node/torrentguard/linux"
)

var _ TorrentGuardHost = (*linuxHost)(nil)

// TorrentGuardSupported reports that this build includes the Linux NFQUEUE runtime.
func TorrentGuardSupported() bool { return true }

func (h *linuxHost) SetTorrentGuard(ctx context.Context, ifaces []string, onAttempt func(TorrentDetection)) error {
	script, err := RenderTorrentGuard(ifaces) // validate before opening a queue or touching nft
	if err != nil {
		return err
	}
	if len(ifaces) == 0 {
		h.torrentMu.Lock()
		defer h.torrentMu.Unlock()
		nftErr := h.nft(ctx, script, true)
		var closeErr error
		if h.torrent != nil {
			closeErr = h.torrent.Close()
			h.torrent = nil
		}
		h.torrentIfaces = nil
		return errors.Join(nftErr, closeErr)
	}

	h.torrentMu.Lock()
	defer h.torrentMu.Unlock()

	// A dead receive loop is safe (the nft queue rule has bypass), but must be
	// replaced before reapplying the guard.
	if h.torrent != nil && h.torrent.Stopped() {
		_ = h.torrent.Close()
		h.torrent = nil
	}

	callback := func(d torrentlinux.Detection) {
		if onAttempt != nil {
			onAttempt(TorrentDetection{L4Protocol: d.L4Protocol, Signature: d.Signature, TunnelIface: d.TunnelIface, TunnelIP: d.TunnelIP})
		}
	}
	newRuntime := h.torrent == nil
	runtime := h.torrent
	if newRuntime {
		runtime, err = torrentlinux.Start(torrentQueueNum, ifaces, callback)
		if err != nil {
			cleanup, _ := RenderTorrentGuard(nil)
			cleanupErr := h.nft(ctx, cleanup, true)
			return errors.Join(fmt.Errorf("start torrent guard queue: %w", err), cleanupErr)
		}
	}

	if err := h.nft(ctx, script, false); err != nil {
		if newRuntime {
			_ = runtime.Close()
		}
		return err
	}

	if !newRuntime {
		runtime.SetCallback(callback)
		if err := runtime.SetInterfaces(ifaces); err != nil {
			// The rules use queue bypass, so an interface-index lookup failure
			// must not interrupt ordinary traffic.
			return fmt.Errorf("refresh torrent guard interfaces: %w", err)
		}
	}
	h.torrent, h.torrentIfaces = runtime, append(h.torrentIfaces[:0], ifaces...)
	return nil
}
