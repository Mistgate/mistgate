//go:build linux

package hostctl

import (
	"context"
	"fmt"
)

var _ UDPCounter = (*linuxHost)(nil)
var _ UDPCountCleaner = (*linuxHost)(nil)

// CountUDP installs (or replaces) the check table. The host keeps no state: the agent knows which tag is armed, and the
// table itself is the only thing a take or a cleanup needs, also from another process.
func (h *linuxHost) CountUDP(tag [8]byte, ports []uint16) error {
	script, err := RenderUDPCount(tag, ports)
	if err != nil {
		return err
	}
	return h.nft(context.Background(), script, false)
}

// TakeUDPCount reads the check table's counters and deletes the table.
func (h *linuxHost) TakeUDPCount() (map[uint16]Count, error) {
	counts, err := h.readCounters(context.Background(), NftUDPCheckTable, "p")
	if err != nil {
		return nil, err
	}
	if err := h.cleanupUDPCount(context.Background()); err != nil {
		return nil, err
	}
	return counts, nil
}

func (h *linuxHost) CleanupUDPCount(ctx context.Context) error { return h.cleanupUDPCount(ctx) }

// CleanupUDPCount deletes the check table without needing host state.
func CleanupUDPCount(ctx context.Context) error {
	return (&linuxHost{run: execRunner}).cleanupUDPCount(ctx)
}

func (h *linuxHost) cleanupUDPCount(ctx context.Context) error {
	script := fmt.Sprintf("add table %s %s\ndelete table %s %s\n", nftFamily, NftUDPCheckTable, nftFamily, NftUDPCheckTable)
	if err := h.nft(ctx, script, true); err != nil {
		return fmt.Errorf("remove UDP check table: %w", err)
	}
	return nil
}
