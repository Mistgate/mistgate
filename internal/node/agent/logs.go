package agent

import (
	"context"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

const (
	maxLogTail      = 1000
	maxLogFollow    = 600 * time.Second
	logChunkLines   = 100
	maxLogStreams   = 8 // concurrent LogRequests per stream
	followFlushTick = 500 * time.Millisecond
)

// startLog serves one LogRequest in its own goroutine, so a follow never blocks the stream reader.
func (s *session) startLog(req *pb.LogRequest) {
	ctx, cancel := context.WithCancel(s.ctx)
	s.mu.Lock()
	if old := s.logs[req.RequestId]; old != nil {
		old() // a repeated request id replaces the earlier stream
	}
	if len(s.logs) >= maxLogStreams && s.logs[req.RequestId] == nil {
		s.mu.Unlock()
		cancel()
		s.sendLog(&pb.LogChunk{RequestId: req.RequestId, Eof: true, Error: "too many concurrent log requests"})
		return
	}
	s.logs[req.RequestId] = cancel
	s.mu.Unlock()
	go func() {
		defer cancel()
		s.serveLog(ctx, req)
		s.mu.Lock()
		delete(s.logs, req.RequestId)
		s.mu.Unlock()
	}()
}

func (s *session) cancelLog(id string) {
	s.mu.Lock()
	cancel := s.logs[id]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *session) sendLog(c *pb.LogChunk) {
	s.send(&pb.ConnectRequest{Message: &pb.ConnectRequest_LogChunk{LogChunk: c}})
}

func (s *session) serveLog(ctx context.Context, req *pb.LogRequest) {
	tail := int(min(req.TailLines, maxLogTail))
	if tail == 0 && !req.Follow {
		tail = 200
	}
	keep := logFilter(req)
	recs, f := s.a.ring.tail(tail, keep, req.Follow)
	if f != nil {
		defer s.a.ring.unfollow(f)
	}
	lines := make([]*pb.LogLine, len(recs))
	for i, r := range recs {
		lines[i] = r.line()
	}
	if !req.Follow {
		s.chunks(req.RequestId, lines, 0, true)
		return
	}
	s.chunks(req.RequestId, lines, 0, false)

	limit := maxLogFollow
	if n := time.Duration(req.FollowMaxSeconds) * time.Second; n > 0 && n < limit {
		limit = n
	}
	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	tick := time.NewTicker(followFlushTick)
	defer tick.Stop()
	var pend []*pb.LogLine
	flush := func(eof bool) {
		s.chunks(req.RequestId, pend, s.a.ring.takeDropped(f), eof)
		pend = nil
	}
	for {
		select {
		case r := <-f.ch:
			if keep(r) {
				pend = append(pend, r.line())
				if len(pend) >= logChunkLines {
					flush(false)
				}
			}
		case <-tick.C:
			if len(pend) > 0 {
				flush(false)
			}
		case <-deadline.C:
			flush(true)
			return
		case <-ctx.Done():
			if s.ctx.Err() == nil { // LogCancel, not a dead stream
				flush(true)
			}
			return
		}
	}
}

// chunks sends lines in pieces of at most logChunkLines; eof marks the last piece. An empty list still
// produces one chunk when it carries eof or a dropped count.
func (s *session) chunks(id string, lines []*pb.LogLine, dropped uint32, eof bool) {
	for {
		n := min(len(lines), logChunkLines)
		last := n == len(lines)
		if n == 0 && !eof && dropped == 0 {
			return
		}
		s.sendLog(&pb.LogChunk{RequestId: id, Lines: lines[:n], Eof: eof && last, Dropped: dropped})
		lines, dropped = lines[n:], 0
		if last {
			return
		}
	}
}
