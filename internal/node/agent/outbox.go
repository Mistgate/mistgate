package agent

import (
	"sync"

	"google.golang.org/protobuf/proto"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

// outbox holds the reliable messages (StatsBatch, Event) until the panel acks them. seq is strictly
// increasing per agent instance and starts at 1; the panel deduplicates on it, so resending after a
// reconnect never double-counts (agent.proto, RELIABLE MESSAGES).
//
// The bound is a message count plus a byte budget; when exceeded the OLDEST StatsBatch is dropped
// and the agent reports it with a stats_dropped event. The protocol's hourly coalescing (merge batches of
// the same UTC hour, 6 h cap) is not implemented: upgrade to it when nodes with thousands of users are
// expected to survive multi-hour panel outages without losing counters. Events are never dropped.
type outbox struct {
	mu       sync.Mutex
	next     uint64 // next seq to assign
	q        []*pb.ConnectRequest
	bytes    int
	maxCount int
	maxBytes int
	dropped  uint32
}

func newOutbox(maxCount, maxBytes int) *outbox {
	return &outbox{next: 1, maxCount: maxCount, maxBytes: maxBytes}
}

// push assigns the next seq and queues the message (msg.Seq must be left zero).
func (o *outbox) push(msg *pb.ConnectRequest) uint64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	msg.Seq = o.next
	o.next++
	o.q = append(o.q, msg)
	o.bytes += proto.Size(msg)
	// Only stats are dropped, and only for a stats push: events stay and the stats_dropped event itself
	// cannot trigger another round of dropping.
	if msg.GetStats() != nil {
		for (len(o.q) > o.maxCount || o.bytes > o.maxBytes) && len(o.q) > 1 {
			i := o.oldestStats()
			if i < 0 || i == len(o.q)-1 {
				break
			}
			o.bytes -= proto.Size(o.q[i])
			o.q = append(o.q[:i], o.q[i+1:]...)
			o.dropped++
		}
	}
	return msg.Seq
}

func (o *outbox) oldestStats() int {
	for i, m := range o.q {
		if m.GetStats() != nil {
			return i
		}
	}
	return -1
}

// ack drops every message with seq <= upTo.
func (o *outbox) ack(upTo uint64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	n := 0
	for n < len(o.q) && o.q[n].Seq <= upTo {
		o.bytes -= proto.Size(o.q[n])
		n++
	}
	if n > 0 {
		o.q = append(o.q[:0:0], o.q[n:]...) // do not keep acked messages alive in the backing array
	}
}

// after returns the pending messages with seq > seq, in order. The messages are shared and read-only.
func (o *outbox) after(seq uint64) []*pb.ConnectRequest {
	o.mu.Lock()
	defer o.mu.Unlock()
	for i, m := range o.q {
		if m.Seq > seq {
			return append([]*pb.ConnectRequest(nil), o.q[i:]...)
		}
	}
	return nil
}

// hello returns (next_seq, first_unacked_seq) for the Hello message.
func (o *outbox) hello() (next, firstUnacked uint64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.q) > 0 {
		firstUnacked = o.q[0].Seq
	}
	return o.next, firstUnacked
}

func (o *outbox) takeDropped() uint32 {
	o.mu.Lock()
	defer o.mu.Unlock()
	d := o.dropped
	o.dropped = 0
	return d
}
