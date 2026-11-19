package bridge

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"time"
)

// Errors of Attach and DialExit. Neither touches the connection when it returns one of these: the caller decides what the
// person gets (a subscription host sends it to the node's site).
var (
	ErrNoPair       = errors.New("no bridge pair in time")           // the exit has no free pair (down, or all busy)
	ErrEntryBusy    = errors.New("bridge entry carries its maximum") // the entry's client limits
	ErrExitNotKnown = errors.New("bridge exit not served")           // exitID is not one of the entry's exits
)

// Client is a connection a caller of Attach accepted itself (a subscription host takes them from a route of its front, not
// from a RelayListener the entry binds).
type Client struct {
	Conn net.Conn
	// RelayID is the exit's target for it.
	RelayID string
	// Idle is how long the stream may carry nothing before it is closed; 0 = BridgeStreamIdleTimeout.
	Idle time.Duration
	// Addr is the person's address, told to the exit when the entry passes it (ServeConfig.PassClient).
	Addr netip.AddrPort
}

// Attach hands c to a free pair of exit exitID, as a RelayListener does with a person it accepted, and blocks until the
// person's stream ends (nil). It returns ErrNoPair when no pair came within ClientWaitForStream, ErrEntryBusy over the entry's
// client limits, ErrExitNotKnown for an exit the entry does not serve, or ctx's error before the stream began; in each of
// these cases c is untouched. Once attached, c belongs to the stream, which closes it; ctx ending then cuts the stream.
func (e *Entry) Attach(ctx context.Context, exitID string, c Client) error {
	cfg := e.serveSnapshot()
	if !relayIDPattern.MatchString(c.RelayID) || !slicesContains(cfg.ExitNodeIDs, exitID) {
		return ErrExitNotKnown
	}
	peer := e.peer(exitID)
	if peer == nil {
		return ErrExitNotKnown
	}
	if !e.acquireClient(peer) {
		return ErrEntryBusy
	}
	defer e.releaseClient(peer)
	var client netip.AddrPort
	if cfg.PassClient {
		client = c.Addr
	}
	attached := false
	e.attachClient(ctx, peer, RelayListener{RelayID: c.RelayID, ExitNodeID: exitID}, c.Conn, client, c.Idle, func() { attached = true })
	switch {
	case attached:
		return nil
	case ctx.Err() != nil:
		return ctx.Err()
	}
	return ErrNoPair
}

// Connected says whether exit exitID holds a connection to this entry now (it may still have no free pair).
func (e *Entry) Connected(exitID string) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	p := e.peers[exitID]
	return p != nil && len(p.connections) > 0
}

// DialExit is the entry's own connection to exit exitID's target relayID, through a free pair, as a person's would be: a
// subscription host's agent reaches the panel this way. ctx bounds the wait for a pair only; the connection then lives until
// either side closes it or it carries nothing for idle (0 = BridgeStreamIdleTimeout).
func (e *Entry) DialExit(ctx context.Context, exitID, relayID string, idle time.Duration) (net.Conn, error) {
	local, remote := net.Pipe()
	attachCtx, cancel := context.WithCancel(context.Background())
	stop := context.AfterFunc(ctx, cancel)
	ready := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		defer cancel()
		peer := e.peer(exitID)
		if peer == nil || !slicesContains(e.serveSnapshot().ExitNodeIDs, exitID) || !relayIDPattern.MatchString(relayID) {
			result <- ErrExitNotKnown
			return
		}
		if !e.acquireClient(peer) {
			result <- ErrEntryBusy
			return
		}
		defer e.releaseClient(peer)
		attached := false
		e.attachClient(attachCtx, peer, RelayListener{RelayID: relayID, ExitNodeID: exitID}, remote, netip.AddrPort{}, idle, func() {
			attached = true
			stop() // from now on only the stream ends it
			close(ready)
		})
		if !attached {
			if err := ctx.Err(); err != nil {
				result <- err
			} else {
				result <- ErrNoPair
			}
		}
	}()
	select {
	case <-ready:
		return local, nil
	case err := <-result:
		stop()
		_ = local.Close()
		_ = remote.Close()
		return nil, err
	}
}
