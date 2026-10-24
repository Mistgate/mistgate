package agent

import (
	"context"
	"sort"
	"strconv"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/doctor"
	"github.com/mistgate/mistgate/internal/plugin"
)

const defaultLinkCounterWindow = 10 * time.Minute

type linkCounterKey struct {
	credID, inboundID string
}

type linkCounterVolume struct {
	onlineS, hellos, handshakes  uint32
	accepted, hellosForeign      uint32
	rttSamples                   uint32
	rttMSSum, pktsSent, pktsLost uint64
}

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
	var links []plugin.LinkStat
	for _, p := range a.protocols {
		e := a.engines[p]
		c, err := e.Collect(ctx)
		if err != nil {
			a.log.Warn("collect failed", "protocol", p, "err", err)
		}
		links = append(links, c.Links...)
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
	a.addLinkCounters(b, links, start, end)
	a.l3Health(ctx, b, off)
	m := a.host.Metrics()
	a.hostRing.Add(doctor.Sample{At: a.now(), CPU: m.CPUPct, Softirq: m.SoftirqPct, Load1: m.Load1})
	b.Host = &pb.HostMetrics{
		CpuPct: float32(m.CPUPct), SoftirqPct: float32(m.SoftirqPct), Load1: float32(m.Load1),
		RamUsedBytes: m.RAMUsed, RamTotalBytes: m.RAMTotal, DiskUsedBytes: m.DiskUsed, DiskTotalBytes: m.DiskTotal,
		NetRxBps: m.NetRxBps, NetTxBps: m.NetTxBps, UptimeS: m.UptimeS,
	}
	if a.journal != nil {
		b.Journal = a.journal.take(off)
	}
	return b
}

func (a *Agent) addLinkCounters(b *pb.StatsBatch, stats []plugin.LinkStat, start, end time.Time) {
	rows := make(map[linkCounterKey]*pb.LinkCounters)
	rowFor := func(key linkCounterKey) *pb.LinkCounters {
		row := rows[key]
		if row == nil {
			row = &pb.LinkCounters{CredId: key.credID, InboundId: key.inboundID}
			rows[key] = row
		}
		return row
	}
	for _, stat := range stats {
		key := linkCounterKey{credID: stat.CredID, inboundID: stat.InboundID}
		if stat.Connects|stat.Rejects|stat.Drops|stat.Stalls|stat.UnknownKeys|stat.ClientCloses|stat.Freezes|stat.Quiet != 0 {
			row := rowFor(key)
			row.Connects += stat.Connects
			row.Rejects += stat.Rejects
			row.Drops += stat.Drops
			row.Stalls += stat.Stalls
			row.UnknownKeys += stat.UnknownKeys
			row.ClientCloses += stat.ClientCloses
			row.Freezes += stat.Freezes
			row.Quiet += stat.Quiet
		}
		if stat.Hellos|stat.Handshakes|stat.Accepted|stat.HellosForeign|stat.RTTSamples != 0 || stat.RTTMSSum|stat.PktsSent|stat.PktsLost != 0 {
			a.linkCounterMu.Lock()
			a.ensureLinkVolumesLocked()
			volume := a.linkVolumes[key]
			volume.hellos += stat.Hellos
			volume.handshakes += stat.Handshakes
			volume.accepted += stat.Accepted
			volume.hellosForeign += stat.HellosForeign
			volume.rttMSSum += stat.RTTMSSum
			volume.rttSamples += stat.RTTSamples
			volume.pktsSent += stat.PktsSent
			volume.pktsLost += stat.PktsLost
			a.linkVolumes[key] = volume
			a.linkCounterMu.Unlock()
		}
	}

	interval := end.Sub(start)
	if interval < 0 {
		interval = 0
	}
	capInterval := a.linkOnlineCap()
	if interval > capInterval {
		interval = capInterval
	}
	onlineS := uint32(interval / time.Second)
	if onlineS > 0 {
		seen := make(map[linkCounterKey]struct{}, len(b.Sessions))
		a.linkCounterMu.Lock()
		a.ensureLinkVolumesLocked()
		for _, session := range b.Sessions {
			key := linkCounterKey{credID: session.CredId, inboundID: session.InboundId}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			volume := a.linkVolumes[key]
			volume.onlineS += onlineS
			a.linkVolumes[key] = volume
		}
		a.linkCounterMu.Unlock()
	}

	window := a.linkWindow
	if window <= 0 {
		window = defaultLinkCounterWindow
	}
	if end.After(start) && end.UTC().Truncate(window).After(start.UTC().Truncate(window)) {
		a.linkCounterMu.Lock()
		for key, volume := range a.linkVolumes {
			if volume.onlineS|volume.hellos|volume.handshakes|volume.accepted|volume.hellosForeign|volume.rttSamples == 0 && volume.rttMSSum|volume.pktsSent|volume.pktsLost == 0 {
				continue
			}
			row := rowFor(key)
			row.OnlineS += volume.onlineS
			row.Hellos += volume.hellos
			row.Handshakes += volume.handshakes
			row.Accepted += volume.accepted
			row.HellosForeign += volume.hellosForeign
			row.RttMsSum += volume.rttMSSum
			row.RttSamples += volume.rttSamples
			row.PktsSent += volume.pktsSent
			row.PktsLost += volume.pktsLost
			delete(a.linkVolumes, key)
		}
		a.linkCounterMu.Unlock()
	}

	keys := make([]linkCounterKey, 0, len(rows))
	for key, row := range rows {
		if row.Connects|row.Rejects|row.Drops|row.Stalls|row.OnlineS|row.UnknownKeys|row.Hellos|row.Handshakes|row.ClientCloses|row.Freezes|row.Quiet|row.Accepted|row.HellosForeign|row.RttSamples != 0 || row.RttMsSum|row.PktsSent|row.PktsLost != 0 {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		return keys[i].inboundID < keys[j].inboundID || (keys[i].inboundID == keys[j].inboundID && keys[i].credID < keys[j].credID)
	})
	for _, key := range keys {
		b.Links = append(b.Links, rows[key])
	}
}

func (a *Agent) ensureLinkVolumesLocked() {
	if a.linkVolumes == nil {
		a.linkVolumes = make(map[linkCounterKey]linkCounterVolume)
	}
}

func (a *Agent) linkOnlineCap() time.Duration {
	interval := a.statsEvery
	if interval <= 0 {
		if settings := a.settings.Load(); settings != nil {
			interval = secs(settings.StatsIntervalS, 10*time.Second)
		} else {
			interval = 10 * time.Second
		}
	}
	return 2 * interval
}
