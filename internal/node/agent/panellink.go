package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

// A new panel link address (NodeSettings.panel_link_url, Meta.NextLink): the agent tries it in turn with the address it
// has, and keeps it once a session over it succeeds. The last proven address stays as the previous one (Meta.PrevLink):
// consecutive switches cannot replace it until the current link passes healthyAfter, with proof persisted for restart.
// After a turn on which the current address did not hold, the agent tries the previous one and returns on HelloAck.
//
// "Did not hold" means no HelloAck, or less than healthyAfter elapsed since HelloAck. Dialing and authentication time
// never count as health. The direct path and bridge use the same rule, including the session that commits a new address.
//
// Only a link node (one enrolled over the link: Meta.Link set) follows a new address. An mTLS node ignores it: following
// it would make the node link-only, with no way back to mTLS once the previous address expires.
//
// The previous address is not kept for ever: its domain may later belong to someone else, and the node would keep
// knocking on it. Once the new address has worked for prevAddressKeep the agent forgets it. "Has worked for" is
// approximated as: a session over the current address stays up healthyAfter past its HelloAck (a healthy session)
// prevAddressKeep or more after the address took over (Meta.LinkSince). The previous address is then dropped at
// once, in memory and in agent.json, so the fallback never dials an expired one. An address that has had no healthy
// session since that point (one that died on day 1, however long ago it was committed, or one that answers and drops
// every session) keeps its previous address, and the fallback to it still works. agent.json without link_since (written
// before the field) counts as committed long ago.

// prevAddressKeep is how long a new panel address must have worked before the agent forgets the previous one.
const prevAddressKeep = 7 * 24 * time.Hour

// linkNow is the clock of Meta.LinkSince.
func (a *Agent) linkNow() time.Time {
	if a.linkClock != nil {
		return a.linkClock()
	}
	return a.now()
}

// linkURL is the link base address the agent dials (cfg.LinkURL, which follows Meta.Link once a new address works).
func (a *Agent) linkURL() string {
	a.metaMu.Lock()
	defer a.metaMu.Unlock()
	return a.cfg.LinkURL
}

// nextLink is the new link address to try, "" for none. An mTLS node has none, whatever agent.json says.
func (a *Agent) nextLink() string {
	a.metaMu.Lock()
	defer a.metaMu.Unlock()
	if a.meta.Link == "" {
		return ""
	}
	return a.meta.NextLink
}

// prevLink is the previous link address, "" for none.
func (a *Agent) prevLink() string {
	a.metaMu.Lock()
	defer a.metaMu.Unlock()
	return a.meta.PrevLink
}

// applyPanelLink takes in NodeSettings.panel_link_url, from a HelloAck or a DesiredState. A new address is saved as
// Meta.NextLink and tried at the next reconnect; the session it came in goes on. fresh: a new address was saved (a
// HelloAck session then ends at once, before it started any work: runSession). An empty field changes nothing, and an
// mTLS node ignores the address.
func (a *Agent) applyPanelLink(st *pb.NodeSettings) (fresh bool) {
	raw := st.GetPanelLinkUrl()
	if raw == "" {
		return false
	}
	if _, err := parseLinkBase(raw); err != nil {
		a.log.Warn("panel link url rejected", "err", err) // the address itself holds a secret: it is not logged
		return false
	}
	a.metaMu.Lock()
	if a.meta.Link == "" {
		a.metaMu.Unlock()
		if a.panelLinkIgnored.CompareAndSwap(false, true) {
			a.log.Info("the panel sent a link address; an mTLS node does not follow it", "code", "panel_link_ignored")
		}
		return false
	}
	if raw != a.leftLink {
		a.leftLink = "" // the panel moved on to another address: the one this run left may be tried again
	}
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
		fresh = raw != a.leftLink // the address this run rolled back from is kept as pending, but must not end the session
	}
	if dirty {
		if err := saveMeta(a.cfg.StateDir, &a.meta); err != nil {
			a.log.Error("panel link address not saved", "err", err)
			fresh = false // no reconnect loop when the address cannot be saved
		}
	}
	a.metaMu.Unlock()
	if fresh {
		a.log.Info("the panel sent a new link address; the next session tries it")
	}
	return fresh
}

// linkNodeSession chooses the next, current or previous address with the same policy on the direct path and bridge.
func (a *Agent) linkNodeSession(ctx context.Context, via string) error {
	triedPrev := a.triedPrev
	a.triedPrev = false
	if a.currentFailed {
		a.currentFailed = false
		if prev := a.prevLink(); prev != "" && (!triedPrev || a.nextLink() == "") {
			a.triedPrev = true
			epoch := a.sessionEpoch.Load()
			err := a.prevSession(ctx, prev, via)
			if via == "" || ctx.Err() != nil || a.sessionEpoch.Load() != epoch {
				return err
			}
			// A failed candidate is not a failed bridge. Retry the current address in this turn without consuming
			// the pending next address's schedule (or a direct-probe hint).
			return a.linkAddressSession(ctx, a.linkURL(), via, nil)
		}
	}
	nextAnswered := via == "" && a.nextAnswered.Swap(false)
	// Favor fallback after a failed current session, but never twice in a row ahead of a pending next address.
	// The previous candidate already took a turn, so next is due even if an earlier next attempt failed.
	if next := a.nextLink(); next != "" && (!a.triedNext || nextAnswered || triedPrev) {
		a.triedNext = true
		epoch := a.sessionEpoch.Load()
		err := a.nextLinkSession(ctx, next, via)
		if via == "" || ctx.Err() != nil || a.sessionEpoch.Load() != epoch {
			return err
		}
		// An unreachable next address must not cost the node its working bridge.
	} else {
		a.triedNext = false
	}
	return a.linkAddressSession(ctx, a.linkURL(), via, nil)
}

// linkAddressSession measures health from HelloAck, never from dialing. The same measurement records current-address
// failures, confirms a new address and expires a previous address, whether direct or through a bridge.
func (a *Agent) linkAddressSession(ctx context.Context, raw, via string, commit func() bool) error {
	established := false
	var healthy *time.Timer
	done := make(chan struct{})
	err := a.linkSessionOn(ctx, func(sctx context.Context) (*websocketAgentTransport, func(), error) {
		t, cleanup, err := a.dialLinkBase(sctx, raw, via)
		if err == nil {
			t.onEstablished = func() error {
				deadline := a.sessionAckAt.Add(a.healthyAfter())
				ended := a.sessionEndedAt
				if commit != nil && !commit() {
					return errSwitchTransport // a newer operator intent superseded this connection
				}
				established = true
				healthy = time.AfterFunc(time.Until(deadline), func() {
					defer close(done)
					if sctx.Err() == nil && ended.Load() == nil {
						a.confirmHealthyLink(raw)
					}
				})
				return nil
			}
		}
		return t, cleanup, err
	}, via)
	if healthy != nil && !healthy.Stop() {
		<-done // finish the health write before the next turn can change addresses
	}
	healthySession := established && a.sessionHealthy()
	if healthySession {
		// Promotion or timer scheduling may have used the remaining deadline. A completed healthy lifetime is still
		// proof, even if cancellation stopped the timer before it could persist that proof.
		a.confirmHealthyLink(raw)
	}
	if a.linkURL() == raw {
		a.currentFailed = ctx.Err() == nil && !errors.Is(err, errSwitchTransport) &&
			!healthySession
	}
	return err
}

// nextLinkSession commits a new address at HelloAck; a short session then triggers fallback on the next turn.
func (a *Agent) nextLinkSession(ctx context.Context, next, via string) error {
	a.log.Info("trying the new panel link address", "via", via)
	a.metaMu.Lock()
	revision := a.meta.NextLinkRevision
	pending := a.meta.NextLink == next
	a.metaMu.Unlock()
	if !pending {
		return errSwitchTransport
	}
	err := a.linkAddressSession(ctx, next, via, func() bool { return a.commitNextLink(next, revision) })
	if errors.Is(err, errLinkNotEstablished) {
		return fmt.Errorf("new panel link address: %v", err) // no mTLS hold period for it
	}
	return err
}

func (a *Agent) commitNextLink(next, revision string) bool {
	a.metaMu.Lock()
	defer a.metaMu.Unlock()
	if a.meta.Link == "" {
		return false
	}
	promoted, err := promoteNextLink(a.cfg.StateDir, &a.meta, next, revision, a.linkNow().Unix())
	if err != nil {
		a.log.Error("new panel link address not saved", "err", err)
		return false
	}
	a.triedNext = false // a superseding intent gets its own first turn
	a.leftLink = ""
	if !promoted {
		return false
	}
	a.cfg.LinkURL = next
	a.log.Info("new panel link address in use")
	return true
}

// confirmHealthyLink persists proof of the current address, so a later switch may replace the fallback even after a
// restart. An unconfirmed intermediate address must never replace the last known-good fallback.
func (a *Agent) confirmHealthyLink(link string) {
	a.metaMu.Lock()
	defer a.metaMu.Unlock()
	if a.meta.Link != link || a.meta.PrevLink == "" {
		return
	}
	dirty := !a.meta.LinkHealthy
	a.meta.LinkHealthy = true
	expired := a.linkNow().Sub(time.Unix(a.meta.LinkSince, 0)) >= prevAddressKeep
	if expired {
		a.meta.PrevLink, a.meta.LinkSince = "", 0
		dirty = true
	}
	if !dirty {
		return
	}
	if err := saveMeta(a.cfg.StateDir, &a.meta); err != nil {
		a.log.Error("panel link health not saved", "err", err)
		return
	}
	if expired {
		a.log.Info("the new panel address has worked long enough; previous address forgotten")
	}
}

// prevSession runs a session over the previous link address prev. Once its HelloAck arrives the previous address is
// the current one again. A failure is an ordinary connection failure (backoff), and the next turn uses the current
// address.
func (a *Agent) prevSession(ctx context.Context, prev, via string) error {
	a.log.Info("the panel address does not answer; trying the previous one", "via", via)
	err := a.linkAddressSession(ctx, prev, via, func() bool { return a.commitPrevLink(prev) })
	if errors.Is(err, errLinkNotEstablished) {
		return fmt.Errorf("previous panel link address: %v", err) // not errLinkNotEstablished: no mTLS hold period for it
	}
	return err
}

// commitPrevLink makes the previous link address current again and drops the one that stopped answering.
func (a *Agent) commitPrevLink(prev string) bool {
	a.metaMu.Lock()
	defer a.metaMu.Unlock()
	rolledBack, err := rollbackPreviousLink(a.cfg.StateDir, &a.meta, prev)
	if err != nil {
		a.log.Error("previous panel address not saved", "err", err)
		return false
	}
	if !rolledBack {
		return false
	}
	a.leftLink = a.cfg.LinkURL
	a.cfg.LinkURL = prev
	a.log.Info("back on the previous panel address")
	return true
}
