package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

// A new panel link address (NodeSettings.panel_link_url, Meta.NextLink): the agent tries it in turn with the address it
// has, and keeps it once a session over it succeeds. The address it had stays as the previous one (Meta.PrevLink, or
// Meta.PrevMTLS for mTLS): while the new one does not answer the agent tries the previous one every other turn, and
// goes back to it once a session over it succeeds, so a wrong address does not cut the node off.
//
// The previous address is not kept for ever: its domain may later belong to someone else, and the node would keep
// knocking on it. Once the new address has worked for prevAddressKeep the agent forgets it. "Has worked for" is
// approximated as: a direct session over the current address establishes (HelloAck) prevAddressKeep or more after the
// address took over (Meta.LinkSince). The previous address is then dropped at once, in memory and in agent.json, so the
// fallback never dials an expired one. An address that has not established a session since that point (one that died
// on day 1, however long ago it was committed) keeps its previous address, and the fallback to it still works.
// agent.json without link_since (written before the field) counts as committed long ago.

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

func (a *Agent) nextLink() string {
	a.metaMu.Lock()
	defer a.metaMu.Unlock()
	return a.meta.NextLink
}

// prevAddress is the previous panel address: a link base, or mtls for the pinned mTLS endpoint ("", false: none).
func (a *Agent) prevAddress() (link string, mtls bool) {
	a.metaMu.Lock()
	defer a.metaMu.Unlock()
	return a.meta.PrevLink, a.meta.PrevMTLS
}

// applyPanelLink takes in NodeSettings.panel_link_url, from a HelloAck or a DesiredState. A new address is saved as
// Meta.NextLink and tried at the next reconnect; the session it came in goes on. fresh: a new address was saved (a
// HelloAck session then ends at once, before it started any work: runSession). An empty field changes nothing.
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
		fresh = true
	}
	if dirty {
		if err := saveMeta(a.cfg.StateDir, a.meta); err != nil {
			a.log.Error("panel link address not saved", "err", err)
		}
	}
	a.metaMu.Unlock()
	if fresh {
		a.log.Info("the panel sent a new link address; the next session tries it")
	}
	return fresh
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
	a.meta.PrevLink, a.meta.PrevMTLS = a.meta.Link, a.meta.Link == "" // the address in use until now; "" = mTLS
	a.meta.LinkSince = a.linkNow().Unix()
	a.meta.Link, a.meta.NextLink = next, ""
	a.cfg.LinkURL = next
	if err := saveMeta(a.cfg.StateDir, a.meta); err != nil {
		a.log.Error("new panel link address not saved", "err", err)
		return
	}
	a.log.Info("new panel link address in use")
}

// dialCurrentLink dials the current link address. Once a session over it establishes, the previous panel address may
// expire (forgetExpiredPrev).
func (a *Agent) dialCurrentLink(ctx context.Context) (*websocketAgentTransport, func(), error) {
	link := a.linkURL()
	t, cleanup, err := a.dialLinkBase(ctx, link, "")
	if err == nil {
		t.onEstablished = func() { a.forgetExpiredPrev(link) }
	}
	return t, cleanup, err
}

// forgetExpiredPrev drops the previous panel address after a session over the current link address link established,
// when link took over prevAddressKeep or more ago. Without a previous address it changes nothing (agent.json is not
// rewritten).
func (a *Agent) forgetExpiredPrev(link string) {
	a.metaMu.Lock()
	defer a.metaMu.Unlock()
	if a.meta.Link != link || (a.meta.PrevLink == "" && !a.meta.PrevMTLS) {
		return
	}
	if a.linkNow().Sub(time.Unix(a.meta.LinkSince, 0)) < prevAddressKeep {
		return
	}
	a.meta.PrevLink, a.meta.PrevMTLS, a.meta.LinkSince = "", false, 0
	if err := saveMeta(a.cfg.StateDir, a.meta); err != nil {
		a.log.Error("forgotten previous panel address not saved", "err", err)
		return
	}
	a.log.Info("the new panel address has worked long enough; previous address forgotten")
}

// prevSession runs a session over the previous panel address: the link base prev, or mTLS when mtls. Once its HelloAck
// arrives the previous address is the current one again. A failure is an ordinary connection failure (backoff), and
// the next turn uses the current address.
func (a *Agent) prevSession(ctx context.Context, prev string, mtls bool) error {
	a.log.Info("the panel address does not answer; trying the previous one", "mtls", mtls)
	commit := func() { a.commitPrevAddress(prev, mtls) }
	if mtls {
		return a.mtlsSessionOn(ctx, "", commit)
	}
	err := a.linkSession(ctx, func(ctx context.Context) (*websocketAgentTransport, func(), error) {
		t, cleanup, err := a.dialLinkBase(ctx, prev, "")
		if err == nil {
			t.onEstablished = commit
		}
		return t, cleanup, err
	})
	if errors.Is(err, errLinkNotEstablished) {
		return fmt.Errorf("previous panel link address: %v", err) // not errLinkNotEstablished: no mTLS hold period for it
	}
	return err
}

// commitPrevAddress makes the previous panel address current again and drops the one that stopped answering. For mTLS
// the agent is no longer link-only, and its configured link (--link-url) is its optional link again.
func (a *Agent) commitPrevAddress(prev string, mtls bool) {
	a.metaMu.Lock()
	defer a.metaMu.Unlock()
	if a.meta.PrevLink != prev || a.meta.PrevMTLS != mtls {
		return
	}
	a.meta.Link, a.meta.PrevLink, a.meta.PrevMTLS, a.meta.LinkSince = prev, "", false, 0
	a.cfg.LinkURL = prev
	if mtls {
		a.cfg.LinkURL = a.configLink
	}
	if err := saveMeta(a.cfg.StateDir, a.meta); err != nil {
		a.log.Error("previous panel address not saved", "err", err)
		return
	}
	a.log.Info("back on the previous panel address", "mtls", mtls)
}
