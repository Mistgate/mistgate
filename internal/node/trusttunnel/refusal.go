package trusttunnel

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/mistgate/mistgate/internal/node/egress"
	"github.com/mistgate/mistgate/internal/plugin"
)

const (
	// refusalLogEvery is the window of the journal: it hears of one reason on one inbound at most once in this time, with the
	// number of refusals since the previous record. The counters (plugin.LinkStat.Refusals) have every refusal; the journal
	// only has to say that, and why, something is wrong.
	refusalLogEvery = 10 * time.Minute
	// refusalFaultThreshold is how many connection errors, or connect timeouts, one inbound must see within the window before
	// the journal hears of them. A dead destination, a name that does not exist, a host that is down are the ordinary
	// traffic of a node; a broken uplink, a wrong route or a dead resolver make every connect fail, which is tens in minutes.
	refusalFaultThreshold = 30
)

// journalAfter is how many refusals of a reason within the window it takes to write the journal: 0, never (a counter and a debug
// record only: the requests are the client's doing, not a fault of the node); 1, the first one. A destination the egress
// forbids, one that has no IPv4 address while IPv6 is off for clients, an ICMP tunnel and an authority that does not parse are
// ordinary traffic; connection errors and timeouts are a fault only in numbers; the rest means the node itself.
func journalAfter(reason plugin.TunnelRefusal) uint32 {
	switch reason {
	case plugin.RefusedBlocked, plugin.RefusedNoIPv4, plugin.RefusedOtherICMP, plugin.RefusedOtherAuthority:
		return 0
	case plugin.RefusedConnRefused, plugin.RefusedConnUnreachable, plugin.RefusedConnDNS, plugin.RefusedConnOther, plugin.RefusedTimeout:
		return refusalFaultThreshold
	}
	return 1
}

// refusals counts the tunnel requests an authenticated credential made and the engine refused, by reason (agent.proto
// LinkCounters, capability "trusttunnel-refusals/1"). It knows no credential, person or destination: a refusal is a count
// on the inbound and, at debug level, a port.
type refusals struct {
	n [plugin.TunnelRefusalKinds]atomic.Uint32

	mu   sync.Mutex
	logs [plugin.TunnelRefusalKinds]refusalLog
}

type refusalLog struct {
	windowStart time.Time
	inWindow    uint32
	at          time.Time // of the last record
	pending     uint32    // refusals since the last record, the one being reported included
}

// refuse records one refusal. port is the destination port, 0 when the request never got as far as naming one. The client's
// answer is the caller's business and does not change with the counting.
func (in *inbound) refuse(reason plugin.TunnelRefusal, port uint16) {
	if int(reason) >= len(in.refused.n) {
		return
	}
	in.refused.n[reason].Add(1)
	log := in.e.log
	if log.Enabled(context.Background(), slog.LevelDebug) {
		if port != 0 {
			log.Debug("TrustTunnel tunnel request refused", "inbound", in.spec.ID, "reason", reason.Name(), "port", strconv.Itoa(int(port)))
		} else {
			log.Debug("TrustTunnel tunnel request refused", "inbound", in.spec.ID, "reason", reason.Name())
		}
	}
	after := journalAfter(reason)
	if after == 0 {
		return
	}
	every, after := in.e.timing.refusalLog, in.e.timing.refusalAfter(after)
	if every <= 0 {
		every = refusalLogEvery
	}
	now := in.e.now()
	in.refused.mu.Lock()
	entry := &in.refused.logs[reason]
	if entry.windowStart.IsZero() || now.Sub(entry.windowStart) >= every {
		entry.windowStart, entry.inWindow = now, 0
	}
	entry.inWindow++
	entry.pending++
	if entry.inWindow < after || (!entry.at.IsZero() && now.Sub(entry.at) < every) {
		in.refused.mu.Unlock()
		return
	}
	count := entry.pending
	entry.at, entry.pending = now, 0
	in.refused.mu.Unlock()
	log.Warn("TrustTunnel refused tunnel requests", "code", "trusttunnel_refused_"+reason.Name(), "inbound", in.spec.ID,
		"reason", reason.Name(), "count", strconv.FormatUint(uint64(count), 10))
}

// takeRefusals returns the refusals counted since the last call.
func (in *inbound) takeRefusals() plugin.TunnelRefusals {
	var out plugin.TunnelRefusals
	for i := range in.refused.n {
		out[i] = in.refused.n[i].Swap(0)
	}
	return out
}

// inboundStat is the inbound's own row of Collect: what cannot be put on a credential. false when there is nothing to say.
func (in *inbound) inboundStat() (plugin.LinkStat, bool) {
	stat := plugin.LinkStat{InboundID: in.spec.ID, UnknownKeys: in.unknownKeys.Swap(0), Refusals: in.takeRefusals()}
	return stat, !stat.Zero()
}

// classifyDial puts an error of the egress (or of the connect timeout) in a reason. The client's answer follows from it
// (warningFor), the counters and the journal use it as it is.
func classifyDial(err error) plugin.TunnelRefusal {
	if errors.Is(err, egress.ErrBlocked) {
		return plugin.RefusedBlocked
	}
	if errors.Is(err, egress.ErrNoIPv4) {
		return plugin.RefusedNoIPv4
	}
	var timeout interface{ Timeout() bool }
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout()) {
		return plugin.RefusedTimeout
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return plugin.RefusedConnDNS
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		var errno syscall.Errno
		if errors.As(err, &errno) {
			switch {
			case errnoRefused(errno):
				return plugin.RefusedConnRefused
			case errnoUnreachable(errno):
				return plugin.RefusedConnUnreachable
			}
		}
		return plugin.RefusedConnOther
	}
	return plugin.RefusedOtherDial
}

// warningFor is the X-Warning code the client gets for a reason; the table is the one the engine has always used.
func warningFor(reason plugin.TunnelRefusal) int {
	switch reason {
	case plugin.RefusedBlocked:
		return 310
	case plugin.RefusedNoIPv4, plugin.RefusedConnRefused, plugin.RefusedConnUnreachable, plugin.RefusedConnDNS, plugin.RefusedConnOther:
		return 301
	case plugin.RefusedTimeout:
		return 302
	}
	return 300
}

// refuseDatagram counts a datagram of a _udp2 stream that the egress would not take (a forbidden or unreachable destination,
// a socket that failed): the stream goes on, so unlike a TCP refusal this is one count per lost datagram. A tunnel that is
// closing is not a refusal.
func (in *inbound) refuseDatagram(err error, port uint16) {
	if errors.Is(err, net.ErrClosed) {
		return
	}
	in.refuse(classifyDial(err), port)
}
