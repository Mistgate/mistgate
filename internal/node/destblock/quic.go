package destblock

import (
	"encoding/binary"
	"errors"
	"sync"

	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/protocol/quic"
)

const (
	maxQUICFlows   = 64 // destinations followed at once; the rest pass unchecked (ponytail: a session that talks to more is not a browser)
	maxQUICPackets = 3  // datagrams a ClientHello may use before the check gives up
)

// QUICGuard reads the name from the first datagrams a client sends to each IP destination. One guard serves one UDP
// session (a session may name several destinations). Safe for concurrent use.
type QUICGuard struct {
	mu    sync.Mutex
	flows map[flowKey]*flow
}

type flowKey struct {
	host string
	port uint16
}

type flow struct {
	pkts [][]byte // the datagrams of a ClientHello not yet complete
	done bool
	drop bool
}

// Drop reports whether the datagram b, the next one for host:port, must not be sent: the QUIC Initial in it (with the
// ones before it) names something in list, or an earlier datagram of the flow did. A flow whose first datagram is not
// a QUIC Initial is never looked at again. A ClientHello in two datagrams lets the first one out: without the second the
// server cannot read it.
func (g *QUICGuard) Drop(list *List, host string, port uint16, b []byte) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	k := flowKey{host, port}
	f := g.flows[k]
	if f == nil {
		if len(g.flows) >= maxQUICFlows {
			return false
		}
		if g.flows == nil {
			g.flows = map[flowKey]*flow{}
		}
		f = &flow{done: !quicInitial(b)}
		g.flows[k] = f
	}
	if f.drop {
		return true
	}
	if f.done {
		return false
	}
	f.pkts = append(f.pkts, append([]byte(nil), b...))
	var all []byte // SniffQUIC unprotects its input in place
	for _, p := range f.pkts {
		all = append(all, p...)
	}
	h, err := quic.SniffQUIC(all)
	if errors.Is(err, protocol.ErrProtoNeedMoreData) && len(f.pkts) < maxQUICPackets {
		return false
	}
	f.done, f.pkts = true, nil
	if err == nil && list.Match(h.Domain()) {
		f.drop = true
	}
	return f.drop
}

// quicInitial: a long-header packet of QUIC v1, v2 or draft 29, the versions SniffQUIC reads.
func quicInitial(b []byte) bool {
	if len(b) < 5 || b[0]&0xC0 != 0xC0 {
		return false
	}
	switch binary.BigEndian.Uint32(b[1:5]) {
	case 1, 0x6b3343cf, 0xff00001d:
		return true
	}
	return false
}
