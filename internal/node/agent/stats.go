package agent

import (
	"context"
	"strconv"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/doctor"
)

// collectLoop produces one StatsBatch per stats interval, even when it is empty (it doubles as the
// heartbeat), and queues it as a reliable message. It runs for the agent's whole life: while the panel is
// unreachable the batches wait in the outbox. Engine.Collect resets the engine's counters, so every batch
// taken here is ours to deliver.
func (a *Agent) collectLoop(ctx context.Context) {
	last := a.now()
	for {
		d := a.statsEvery
		if d == 0 {
			d = secs(a.settings.Load().StatsIntervalS, 10*time.Second)
		}
		t := time.NewTimer(d)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		end := a.now()
		a.push(&pb.ConnectRequest{Message: &pb.ConnectRequest_Stats{Stats: a.buildBatch(ctx, last, end)}})
		last = end
		if n := a.out.takeDropped(); n > 0 {
			a.log.Warn("unacked statistics dropped", "batches", n)
			a.event(pb.Severity_SEVERITY_WARNING, "stats_dropped", "", map[string]string{"batches": strconv.FormatUint(uint64(n), 10)})
		}
	}
}

func (a *Agent) buildBatch(ctx context.Context, start, end time.Time) *pb.StatsBatch {
	b := &pb.StatsBatch{IntervalStartUnix: start.Unix(), IntervalEndUnix: end.Unix()}
	off := time.Duration(a.offset.Load()) * time.Second
	for _, p := range a.protocols {
		e := a.engines[p]
		c, err := e.Collect(ctx)
		if err != nil {
			a.log.Warn("collect failed", "protocol", p, "err", err)
		}
		for _, t := range c.Traffic {
			b.Traffic = append(b.Traffic, &pb.TrafficDelta{CredId: t.CredID, InboundId: t.InboundID, BytesUp: t.Up, BytesDown: t.Down})
		}
		for _, s := range c.Sessions {
			b.Sessions = append(b.Sessions, &pb.Session{
				CredId: s.CredID, InboundId: s.InboundID, ConnectedAtUnix: s.Since.Add(off).Unix(),
			})
		}
		for _, h := range e.Health() {
			ih := &pb.InboundHealth{
				InboundId: h.InboundID, State: pb.InboundRunState(h.State), Detail: h.Detail, RestartCount: h.Restarts,
			}
			if !h.Since.IsZero() {
				ih.StartedAtUnix = h.Since.Add(off).Unix()
			}
			ih.CertPinSha256 = h.CertPinSHA256
			if !h.CertNotAfter.IsZero() {
				ih.CertNotAfterUnix = h.CertNotAfter.Unix()
			}
			b.Health = append(b.Health, ih)
		}
	}
	a.l3Health(ctx, b, off)
	m := a.host.Metrics()
	a.hostRing.Add(doctor.Sample{At: a.now(), CPU: m.CPUPct, Softirq: m.SoftirqPct, Load1: m.Load1})
	b.Host = &pb.HostMetrics{
		CpuPct: float32(m.CPUPct), SoftirqPct: float32(m.SoftirqPct), Load1: float32(m.Load1),
		RamUsedBytes: m.RAMUsed, RamTotalBytes: m.RAMTotal, DiskUsedBytes: m.DiskUsed, DiskTotalBytes: m.DiskTotal,
		NetRxBps: m.NetRxBps, NetTxBps: m.NetTxBps, UptimeS: m.UptimeS,
	}
	return b
}
