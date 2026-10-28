//go:build linux

package hostctl

import (
	"context"
	"fmt"
	"net/netip"
	"os/exec"
	"slices"
	"sync"
	"time"
)

// relayState is what the host owner remembers about the relay table.
type relayState struct {
	mu         sync.Mutex
	set        []Forward
	desired    []Forward // last validated request, retained while a drop policy keeps the live table closed
	hasDesired bool
	local      []netip.Addr // the node's IPv4 addresses the installed table matches against
	applied    bool         // set is what nft holds (an empty set means the table is gone)
	checkedAt  time.Time    // the last FORWARD policy look
	now        func() time.Time
	addrs      func() ([]netip.Addr, error) // nil = OwnIPv4s; a test seam
}

// relayRecheck is how often a node with forwards looks at the FORWARD policy again when nothing else changed: Docker or
// UFW may be installed later, and the forwards must then stop (fail closed) instead of hijacking ports.
const relayRecheck = 5 * time.Minute

var _ RelayHost = (*linuxHost)(nil)

func (h *linuxHost) RelaySupported() bool {
	_, err := exec.LookPath("nft")
	return err == nil
}

func (h *linuxHost) SetRelays(ctx context.Context, fs []Forward) error {
	st := &h.rly
	st.mu.Lock()
	defer st.mu.Unlock()
	st.desired, st.hasDesired = append([]Forward(nil), fs...), true
	now := time.Now()
	if st.now != nil {
		now = st.now()
	}
	// The addresses of the node are read on every call (a syscall): a forward only catches packets addressed to them, so
	// an address that appears or goes must reach the table at once, not after the five-minute look at the FORWARD policy.
	var local []netip.Addr
	if len(fs) > 0 {
		read := st.addrs
		if read == nil {
			read = OwnIPv4s
		}
		var err error
		if local, err = read(); err != nil {
			h.dropRelayTable(ctx) // fail closed: no list, no table
			return fmt.Errorf("addresses of this node: %w", err)
		}
		local = normalizeLocal(local)
	}
	if st.applied && ForwardsEqual(fs, st.set) && slices.Equal(local, st.local) && (len(fs) == 0 || now.Sub(st.checkedAt) < relayRecheck) {
		return nil
	}
	script, err := RenderRelays(fs, local) // reject before anything is sent to nft
	if err != nil {
		if len(fs) > 0 && len(local) == 0 { // no address to match: fail closed like the others
			h.dropRelayTable(ctx)
		}
		return err
	}
	if len(fs) > 0 {
		if err := h.ReconcileForwardExceptions(ctx, fs, local); err != nil {
			h.dropRelayTable(ctx)
			return fmt.Errorf("forwarding exceptions: %w", err)
		}
		if why, drops := h.forwardDrop(ctx); drops {
			h.dropRelayTable(ctx)
			return fmt.Errorf("%w: %s", ErrForwardDrop, why)
		}
		// Forwarding first: a table with DNAT but no forwarding would swallow its port.
		if err := h.enableForwarding(false); err != nil {
			h.dropRelayTable(ctx)
			return fmt.Errorf("forwarding: %w", err)
		}
	} else if err := h.ReconcileForwardExceptions(ctx, nil, nil); err != nil {
		return fmt.Errorf("forwarding exceptions: %w", err)
	}
	st.applied = false
	if err := h.nft(ctx, script, len(fs) == 0); err != nil {
		st.set, st.local = nil, nil
		return err
	}
	st.set, st.local, st.applied, st.checkedAt = append([]Forward(nil), fs...), local, true, now
	return nil
}

// dropRelayTable removes the relay table after a refusal; the caller holds rly.mu.
func (h *linuxHost) dropRelayTable(ctx context.Context) {
	script, _ := RenderRelays(nil, nil)
	h.rly.set, h.rly.local, h.rly.applied = nil, nil, h.nft(ctx, script, true) == nil
}

// cleanupRelays deletes the relay table. Quiet without nft.
func (h *linuxHost) cleanupRelays(ctx context.Context) error {
	script, _ := RenderRelays(nil, nil)
	h.rly.mu.Lock()
	defer h.rly.mu.Unlock()
	h.rly.set, h.rly.desired, h.rly.local, h.rly.applied, h.rly.hasDesired = nil, nil, nil, false, false
	return h.nft(ctx, script, true)
}
