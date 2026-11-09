package agent

import (
	"context"
	"math"
	"sort"
	"strconv"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/doctor"
	"github.com/mistgate/mistgate/internal/plugin"
	"github.com/mistgate/mistgate/internal/sysstat"
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
	// links/3
	hsUnfinished, tlsAlertsSent, tlsAlertsRecv, tlsCertRefused uint32
	realityFallback, authFailed                                uint32
	initiations, responses, responsesUnmatched                 uint32
}

// add folds the volume counters of one engine delta in.
func (v *linkCounterVolume) add(stat plugin.LinkStat) {
	v.hellos += stat.Hellos
	v.handshakes += stat.Handshakes
	v.accepted += stat.Accepted
	v.hellosForeign += stat.HellosForeign
	v.rttMSSum += stat.RTTMSSum
	v.rttSamples += stat.RTTSamples
	v.pktsSent += stat.PktsSent
	v.pktsLost += stat.PktsLost
	v.hsUnfinished += stat.HsUnfinished
	v.tlsAlertsSent += stat.TLSAlertsSent
	v.tlsAlertsRecv += stat.TLSAlertsRecv
	v.tlsCertRefused += stat.TLSCertRefused
	v.realityFallback += stat.RealityFallback
	v.authFailed += stat.AuthFailed
	v.initiations += stat.Initiations
	v.responses += stat.Responses
	v.responsesUnmatched += stat.ResponsesUnmatched
}

func (v linkCounterVolume) zero() bool {
	return v.onlineS|v.hellos|v.handshakes|v.accepted|v.hellosForeign|v.rttSamples|v.hsUnfinished|v.tlsAlertsSent|v.tlsAlertsRecv|
		v.tlsCertRefused|v.realityFallback|v.authFailed|v.initiations|v.responses|v.responsesUnmatched == 0 &&
		v.rttMSSum|v.pktsSent|v.pktsLost == 0
}

// hasLinkVolume reports a delta with a volume counter (kept until the 10-minute boundary).
func hasLinkVolume(stat plugin.LinkStat) bool {
	return stat.Hellos|stat.Handshakes|stat.Accepted|stat.HellosForeign|stat.RTTSamples|stat.HsUnfinished|stat.TLSAlertsSent|
		stat.TLSAlertsRecv|stat.TLSCertRefused|stat.RealityFallback|stat.AuthFailed|stat.Initiations|stat.Responses|
		stat.ResponsesUnmatched != 0 || stat.RTTMSSum|stat.PktsSent|stat.PktsLost != 0
}

// linkRowEmpty reports a row with no counter at all (it is not sent).
func linkRowEmpty(row *pb.LinkCounters) bool {
	return row.Connects|row.Rejects|row.Drops|row.Stalls|row.OnlineS|row.UnknownKeys|row.Hellos|row.Handshakes|row.ClientCloses|
		row.Freezes|row.Quiet|row.Accepted|row.HellosForeign|row.RttSamples|row.HsUnfinished|row.TlsAlertsSent|row.TlsAlertsRecv|
		row.TlsCertRefused|row.RealityFallback|row.AuthFailed|row.TcpStalls|row.TcpStalls_16K|row.Initiations|row.Responses|
		row.ResponsesUnmatched == 0 && row.RttMsSum|row.PktsSent|row.PktsLost == 0
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
	var networksSHA string
	if a.nets != nil {
		networksSHA = a.nets.LoadedSHA()
	}
	b := &pb.StatsBatch{IntervalStartUnix: start.Unix(), IntervalEndUnix: end.Unix(), NetworksDbSha256: networksSHA}
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
	window := a.linkWindow
	if window <= 0 {
		window = defaultLinkCounterWindow
	}
	if end.After(start) && end.UTC().Truncate(window).After(start.UTC().Truncate(window)) {
		b.Networks, b.PersonNetworks = a.nets.Take()
	}
	a.l3Health(ctx, b, off)
	m := a.host.Metrics()
	a.hostRing.Add(doctor.Sample{At: a.now(), CPU: m.CPUPct, Softirq: m.SoftirqPct, Load1: m.Load1, Steal: m.StealPct})
	b.Host = &pb.HostMetrics{
		CpuPct: float32(m.CPUPct), SoftirqPct: float32(m.SoftirqPct), Load1: float32(m.Load1),
		RamUsedBytes: m.RAMUsed, RamTotalBytes: m.RAMTotal, DiskUsedBytes: m.DiskUsed, DiskTotalBytes: m.DiskTotal,
		NetRxBps: m.NetRxBps, NetTxBps: m.NetTxBps, UptimeS: m.UptimeS,
		StealPct: float32(m.StealPct), IowaitPct: float32(m.IOWaitPct), SwapUsedBytes: m.SwapUsed, SwapTotalBytes: m.SwapTotal,
		ConntrackCount: m.ConntrackCount, ConntrackMax: m.ConntrackMax, TcpSockets: m.TCPSockets, UdpSockets: m.UDPSockets,
		NicSpeedMbps: m.NICSpeedMbps, Agent: a.selfUsage(),
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
		if stat.Connects|stat.Rejects|stat.Drops|stat.Stalls|stat.UnknownKeys|stat.ClientCloses|stat.Freezes|stat.Quiet|stat.TCPStalls|stat.TCPStalls16K != 0 {
			row := rowFor(key)
			row.Connects += stat.Connects
			row.Rejects += stat.Rejects
			row.Drops += stat.Drops
			row.Stalls += stat.Stalls
			row.UnknownKeys += stat.UnknownKeys
			row.ClientCloses += stat.ClientCloses
			row.Freezes += stat.Freezes
			row.Quiet += stat.Quiet
			row.TcpStalls += stat.TCPStalls
			row.TcpStalls_16K += stat.TCPStalls16K
		}
		if hasLinkVolume(stat) {
			a.linkCounterMu.Lock()
			a.ensureLinkVolumesLocked()
			volume := a.linkVolumes[key]
			volume.add(stat)
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
			if volume.zero() {
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
			row.HsUnfinished += volume.hsUnfinished
			row.TlsAlertsSent += volume.tlsAlertsSent
			row.TlsAlertsRecv += volume.tlsAlertsRecv
			row.TlsCertRefused += volume.tlsCertRefused
			row.RealityFallback += volume.realityFallback
			row.AuthFailed += volume.authFailed
			row.Initiations += volume.initiations
			row.Responses += volume.responses
			row.ResponsesUnmatched += volume.responsesUnmatched
			delete(a.linkVolumes, key)
		}
		a.linkCounterMu.Unlock()
	}

	keys := make([]linkCounterKey, 0, len(rows))
	for key, row := range rows {
		if !linkRowEmpty(row) {
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

// selfUsage is the agent process's own CPU, memory, threads and descriptors. Every engine runs inside it, so this is what
// Mistgate costs the machine.
func (a *Agent) selfUsage() *pb.ProcessUsage {
	p := a.self.Sample()
	g := sysstat.ReadGoRuntime()
	u32 := func(v uint64) uint32 { return uint32(min(v, math.MaxUint32)) }
	return &pb.ProcessUsage{
		CpuPct: float32(p.CPUPct), RssBytes: p.RSS, GoTotalBytes: g.TotalBytes, GoHeapBytes: g.HeapBytes,
		Goroutines: u32(g.Goroutines), Threads: p.Threads, OpenFds: u32(p.FDs), FdLimit: u32(p.FDLimit),
	}
}
