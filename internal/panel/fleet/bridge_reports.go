package fleet

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/store"
)

var bridgeReportError = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

const (
	// bridgeReportRefresh is the longest the panel goes without writing what an agent says of its bridges while it says the same:
	// the stored report carries the time the stage's freshness is read from (90 seconds), and a stats batch comes every few
	// seconds. A change of a connection count, an error, the pin or the pool's people in steps of 10 is written at once.
	bridgeReportRefresh = 20 * time.Second
	// bridgePoolRefresh is how often the time the reason for four connections last held is written while it keeps holding.
	bridgePoolRefresh = 5 * time.Minute
)

// bridgeSeen is what the panel last wrote of a node's bridge reports, to tell a report that changes nothing from one that does.
type bridgeSeen struct {
	sig string
	at  time.Time
}

// bridgeReportSig is what in a node's bridge snapshot matters without its clock: who, how many connections, which error, the
// site pin and the people carried in steps of ten (a person leaving is not news).
func bridgeReportSig(rows []*agentv1.BridgeState) string {
	parts := make([]string, 0, len(rows))
	for _, row := range rows {
		if row == nil {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s|%d|%s|%d|%s", row.PeerNodeId, row.Conns, row.Error, row.ClientsNow/10, row.SiteCertPinSha256))
	}
	sort.Strings(parts)
	return strings.Join(parts, ";")
}

// recordBridgeReports consumes the agent's complete bridge snapshot. The pin stays in memory; the status, the people and the
// pool enter relay.check_json, where the entry and the exit have separate members. It runs on every stats batch and every apply
// result of every node, so a node that has never reported a bridge costs nothing (no reads), and one that reports the same as
// before is skipped for 20 seconds.
func (f *Fleet) recordBridgeReports(ctx context.Context, nodeID string, rows []*agentv1.BridgeState, now time.Time) {
	sig := bridgeReportSig(rows)
	f.bridgeSeenMu.Lock()
	prev, seen := f.bridgeSeen[nodeID]
	switch {
	case !seen && sig == "":
		f.bridgeSeenMu.Unlock()
		return
	case seen && prev.sig == sig && !now.Before(prev.at) && now.Sub(prev.at) < bridgeReportRefresh:
		f.bridgeSeenMu.Unlock()
		return
	case sig == "":
		delete(f.bridgeSeen, nodeID)
	default:
		f.bridgeSeen[nodeID] = bridgeSeen{sig: sig, at: now}
	}
	f.bridgeSeenMu.Unlock()

	links, err := f.st.RelayLinks(ctx, store.RelayFilter{NodeID: nodeID})
	if err != nil {
		f.log.Warn("read bridge report relays", "code", "bridge_relay_read_failed", "node", nodeID, "err", err)
		return
	}
	byPeer := make(map[string]*agentv1.BridgeState, len(rows))
	validPeers := make(map[string]bool, len(links))
	for _, l := range links {
		if l.Network == "tcp" && (l.ViaNodeID == nodeID || l.ExitNodeID == nodeID) {
			peer := l.ViaNodeID
			if l.ViaNodeID == nodeID {
				peer = l.ExitNodeID
			}
			validPeers[peer] = true
		}
	}
	var sitePin string
	var sitePort uint16
	for _, row := range rows {
		if row == nil || !validPeers[row.PeerNodeId] {
			continue
		}
		byPeer[row.PeerNodeId] = row
		if sitePin == "" && row.SiteCertPinSha256 != "" {
			sitePin, sitePort = f.bridgeSitePinOf(ctx, nodeID, row.SiteCertPinSha256)
		}
	}

	for _, link := range links {
		if link.Network != "tcp" || !link.Enabled && !link.BridgeServed() {
			continue
		}
		peer, side := "", ""
		switch nodeID {
		case link.ViaNodeID:
			peer, side = link.ExitNodeID, "entry"
		case link.ExitNodeID:
			peer, side = link.ViaNodeID, "exit"
		}
		if side == "" {
			continue
		}
		var report *store.RelayBridgeSideCheck
		row := byPeer[peer]
		previous, hasBridge := link.Bridge()
		previousSide := hasBridge && (side == "entry" && previous.Entry != nil || side == "exit" && previous.Exit != nil)
		if row != nil {
			errText := strings.ToLower(row.Error)
			if errText != "" && !bridgeReportError.MatchString(errText) {
				errText = "reported_error"
			}
			since := row.SinceUnix
			if since < 0 || since > now.Add(time.Minute).Unix() {
				since = 0
			}
			report = &store.RelayBridgeSideCheck{Connected: row.Conns > 0, Conns: row.Conns, RTTMS: row.RttMs,
				SinceUnix: since, Error: errText, AtUnix: now.Unix()}
			if side == "entry" {
				report.Clients, report.SitePin, report.SitePort = row.ClientsNow, sitePin, sitePort
			}
		}
		if report != nil || previousSide {
			if err := f.st.SetRelayBridgeSide(ctx, link.ID, side, report); err != nil && !errors.Is(err, store.ErrNotFound) {
				f.log.Warn("store bridge report", "code", "bridge_report_store_failed", "relay", link.ID, "side", side, "err", err)
				continue
			}
		}
		if side == "entry" && report != nil {
			if report.SitePin != "" && (previous.Entry == nil || previous.Entry.SitePin != report.SitePin || previous.Entry.SitePort != report.SitePort) {
				f.StateChanged() // the exits that dial this entry are told the pin the site has now
			}
			f.chooseBridgePool(ctx, link, previous, report.Clients, now)
		}
		if link.Enabled {
			f.controlRelayLater(link.ID)
		}
	}
}

// bridgeSitePinOf checks the pin an entry reported for the bridge site it runs itself, and the port it is for (the node's
// bridge port now): "" when the node has no separate site, or the pin is not a SHA-256.
func (f *Fleet) bridgeSitePinOf(ctx context.Context, nodeID, pin string) (string, uint16) {
	n, err := f.st.Node(ctx, nodeID)
	if err != nil {
		f.log.Warn("read bridge report node", "code", "bridge_node_read_failed", "node", nodeID, "err", err)
		return "", 0
	}
	if n.BridgePort == 0 {
		return "", 0
	}
	normalized, ok := protocols.NormalizePin(pin)
	if !ok {
		f.log.Warn("ignore malformed bridge site certificate pin", "code", "invalid_bridge_certificate_pin", "node", nodeID)
		return "", 0
	}
	return normalized, n.BridgePort
}

// chooseBridgePool decides how many TLS connections the exit holds for this relay (store.BridgePool: busy forward or nearly full
// bridge) and writes it when it changes, or now and then while the reason for four keeps holding. A new pool is a new desired state.
func (f *Fleet) chooseBridgePool(ctx context.Context, link store.RelayLink, previous store.RelayBridgeCheck, clients uint32, now time.Time) {
	bps := uint64(0)
	if check, ok := link.Check(); ok && check.Counters != nil {
		bps = check.Counters.BPS(now)
	}
	pool, at := store.BridgePool(previous.Pool, previous.PoolAt, clients, bps, now)
	if pool == store.BridgePoolMin && previous.Pool == 0 {
		return // nothing was decided and nothing is: the default is two
	}
	if pool == previous.Pool && at-previous.PoolAt < int64(bridgePoolRefresh/time.Second) {
		return
	}
	if err := f.st.SetRelayBridgePool(ctx, link.ID, pool, at); err != nil && !errors.Is(err, store.ErrNotFound) {
		f.log.Warn("store bridge pool", "code", "bridge_pool_store_failed", "relay", link.ID, "err", err)
		return
	}
	if pool != previous.Pool && !(previous.Pool == 0 && pool == store.BridgePoolMin) {
		f.StateChanged()
	}
}
