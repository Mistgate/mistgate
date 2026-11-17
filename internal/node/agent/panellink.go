package agent

import (
	"context"
	"errors"
	"fmt"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

// A new panel link address (NodeSettings.panel_link_url, Meta.NextLink): the agent tries it in turn with the address it
// has, and keeps it once a session over it succeeds.

// linkURL is the link base address the agent dials (cfg.LinkURL, which follows Meta.Link once a new address works).
func (a *Agent) linkURL() string {
	a.metaMu.Lock()
	defer a.metaMu.Unlock()
	return a.cfg.LinkURL
}

func (a *Agent) nextLink() string {
	a.metaMu.Lock()
	defer a.metaMu.Unlock()
	return a.meta.NextLink
}

// applyPanelLink takes in NodeSettings.panel_link_url. reconnect: the current session should end so that the next one
// tries the new link address. An empty field changes nothing.
func (a *Agent) applyPanelLink(st *pb.NodeSettings) (reconnect bool) {
	raw := st.GetPanelLinkUrl()
	if raw == "" {
		return false
	}
	if _, err := parseLinkBase(raw); err != nil {
		a.log.Warn("panel link url rejected", "err", err) // the address itself holds a secret: it is not logged
		return false
	}
	a.metaMu.Lock()
	dirty := false
	switch raw {
	case a.meta.Link:
		if a.meta.NextLink != "" {
			a.meta.NextLink = ""
			dirty = true
		}
	case a.meta.NextLink:
	default:
		a.meta.NextLink = raw
		dirty = true
		reconnect = true
	}
	if dirty {
		if err := saveMeta(a.cfg.StateDir, a.meta); err != nil {
			a.log.Error("panel link address not saved", "err", err)
		}
	}
	a.metaMu.Unlock()
	if reconnect {
		a.log.Info("the panel sent a new link address; reconnecting")
	}
	return reconnect
}

// endForNewLink ends the current session, after what it has queued is written, so the next one tries the new link address.
func (a *Agent) endForNewLink() {
	s := a.cur.Load()
	if s == nil {
		return
	}
	a.switchPending.Store(true)
	go func() {
		wait := durOr(a.heldFlush, heldFlushWait)
		s.flush(wait)
		s.drain(wait)
		s.cancel()
	}()
}

// nextLinkSession runs a session over the new link address. Once its HelloAck arrives the address replaces the old one
// for good. A failure is an ordinary connection failure (backoff), and the next turn uses the old address.
func (a *Agent) nextLinkSession(ctx context.Context, next string) error {
	a.log.Info("trying the new panel link address")
	err := a.linkSession(ctx, func(ctx context.Context) (*websocketAgentTransport, func(), error) {
		t, cleanup, err := a.dialLinkBase(ctx, next, "")
		if err == nil {
			t.onEstablished = func() { a.commitNextLink(next) }
		}
		return t, cleanup, err
	})
	if errors.Is(err, errLinkNotEstablished) {
		return fmt.Errorf("new panel link address: %v", err) // not errLinkNotEstablished: no mTLS hold period for it
	}
	return err
}

func (a *Agent) commitNextLink(next string) {
	a.metaMu.Lock()
	defer a.metaMu.Unlock()
	if a.meta.NextLink != next {
		return
	}
	a.meta.Link, a.meta.NextLink = next, ""
	a.cfg.LinkURL = next
	if err := saveMeta(a.cfg.StateDir, a.meta); err != nil {
		a.log.Error("new panel link address not saved", "err", err)
		return
	}
	a.log.Info("new panel link address in use")
}
