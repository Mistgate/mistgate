package agent

import (
	"context"
	"encoding/json"
	"errors"
	mrand "math/rand/v2"
	"sort"
	"strconv"
	"strings"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/doctor"
	"github.com/mistgate/mistgate/internal/node/hostctl"
	"github.com/mistgate/mistgate/internal/plugin"
)

// The doctor (agent.proto "DOCTOR") is internal/node/doctor; this file is the wiring:
// the capability string, the periodic DoctorReport, RunDoctor and ApplyFix handling, and the agent-side facts
// the checks need. Runs and fixes happen in their own goroutines, so the stream reader never waits for them.

// capDoctor is listed in Hello.capabilities: the panel sends RunDoctor and ApplyFix only to agents that have it.
const capDoctor = "doctor/1"

const (
	// First report 30 s after every connect (engines need a moment to settle), then every 10 min +-60 s.
	doctorFirstDelay = 30 * time.Second
	doctorEvery      = 10 * time.Minute
	doctorJitterMax  = 60 * time.Second
	// A fix may restart an inbound or vacuum a big journal; it gets this long.
	fixTimeout = 2 * time.Minute
	// After a fix the re-check waits this many seconds for a run that is still going.
	recheckTries = 15
)

// doctorEnv builds the doctor's view of this machine and this agent.
func (a *Agent) doctorEnv() doctor.Env {
	e := doctor.DefaultEnv()
	f := a.host.Facts(context.Background())
	e.Virt, e.CPUs = f.Virt, f.CPUCount
	e.StateDir = a.cfg.StateDir
	e.Now = a.now
	e.Settings = func() doctor.Settings {
		st := a.settings.Load()
		return doctor.Settings{Country: st.CountryCode, Resolvers: st.DnsResolvers, ClientIPv6Disabled: st.GetClientIpv6Disabled()}
	}
	e.Inbounds = a.doctorInbounds
	e.Samples = func(w time.Duration) []doctor.Sample { return a.hostRing.Since(a.now(), w) }
	e.Offset = a.offsetNow
	e.AgentCertNotAfter = func() time.Time { return a.id.Load().notAfter() }
	e.UnitGen = a.cfg.UnitGen
	e.UnderSystemd = a.cfg.UnitUnderSystemd
	e.UnitSkipReason = a.cfg.UnitSkipReason
	e.UnitRefreshBusy = a.cfg.UnitRefreshBusy
	e.UnitRefreshReserve = a.cfg.UnitRefreshReserve
	e.LaunchUnitRefresh = a.cfg.UnitRefreshLauncher
	e.AgentStartedAt = a.startedAt
	e.ApplyBaseline = a.host.ApplyBaseline
	e.RestartInbound = a.doctorRestart
	e.ReconnectWarp = a.doctorReconnectWarp
	e.UnitGen = a.cfg.UnitGen
	e.OwnIface = func(name string) bool {
		return strings.HasPrefix(name, hostctl.TunnelIfacePrefix) || name == hostctl.WarpIface
	}
	if bs, ok := a.awgStatuser(); ok {
		e.AwgBackend = func() doctor.AwgBackend {
			st := bs.BackendStatus() // only called when the node has an awg inbound (the check skips otherwise)
			return doctor.AwgBackend{Mode: st.Mode, Name: st.Name, Version: st.Version, Available: st.Available, Reason: st.Reason}
		}
	}
	if w := a.cfg.Warp; w != nil {
		e.Warp = func(ctx context.Context) doctor.WarpInfo {
			h, ok := w.Health()
			info := doctor.WarpInfo{Configured: w.Configured()}
			if ok {
				info.State, info.Backend, info.Endpoint, info.Colo = h.State.String(), h.Backend, h.Endpoint, h.Colo
				info.LastError, info.LastHandshake = h.LastError, h.LastHandshake
			}
			return info
		}
		e.RecheckWarp = func(ctx context.Context) (bool, bool, error) { return w.CheckNow(ctx) }
		e.HostPath = func(ctx context.Context) []doctor.PathFinding {
			var out []doctor.PathFinding
			for _, f := range w.Preflight(ctx) {
				out = append(out, doctor.PathFinding{ID: f.ID, Detail: f.Detail})
			}
			return out
		}
	}
	if fx, ok := a.host.(hostctl.Fixer); ok {
		e.Fixer = fx
	}
	if a.cfg.DoctorEnv != nil {
		e = a.cfg.DoctorEnv(e)
	}
	return e
}

// doctorInbounds describes the applied inbounds: spec from the model, run state from the engines' Health
// (safe to read from any goroutine), certificate expiry from the last apply.
func (a *Agent) doctorInbounds() []doctor.Inbound {
	m := a.snapshotModel()
	health := map[string]plugin.EngineHealth{}
	for _, p := range a.protocols {
		for _, h := range a.engines[p].Health() {
			health[h.InboundID] = h
		}
	}
	var na map[string]time.Time
	if p := a.certNA.Load(); p != nil {
		na = *p
	}
	out := make([]doctor.Inbound, 0, len(m.inbounds))
	for _, id := range m.ids() {
		sp := m.inbounds[id].spec
		in := doctor.Inbound{
			ID: id, Protocol: sp.Protocol, Network: sp.Listen.Network, Port: int(sp.Listen.Port),
			HopFrom: int(sp.Listen.HopFrom), HopTo: int(sp.Listen.HopTo), Enabled: sp.Enabled, Egress: sp.Egress,
			TLSMode: tlsModeName(sp.TLS.Mode), ServerName: sp.TLS.ServerName, State: "stopped", CertNotAfter: na[id],
		}
		in.TLSPort = tlsPortOf(sp)
		if h, ok := health[id]; ok {
			if h.CertNotAfter.After(in.CertNotAfter) { // an ACME leaf issued or renewed after the last apply
				in.CertNotAfter = h.CertNotAfter
			}
			in.State = runStateName(h.State)
			if h.State == plugin.RunFailed {
				in.Error = h.Detail
			}
			if hasPrefix(h.Detail, "masq_tcp:") { // the engine could not bind its HTTPS listener
				in.TLSDown = h.Detail
			}
		}
		out = append(out, in)
	}
	return out
}

func hasPrefix(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }

func tlsModeName(m plugin.TLSMode) string {
	switch m {
	case plugin.TLSAcmeDomain:
		return "acme_domain"
	case plugin.TLSAcmeIP:
		return "acme_ip"
	case plugin.TLSSelfSigned:
		return "self_signed"
	}
	return ""
}

func runStateName(s plugin.RunState) string {
	switch s {
	case plugin.RunStarting:
		return "starting"
	case plugin.RunRunning:
		return "running"
	case plugin.RunFailed:
		return "failed"
	}
	return "stopped"
}

// tlsPortOf is the TCP port whose served certificate the doctor can read, 0 = none. Hysteria2: its HTTPS masquerade
// listener, settings.masquerade.tcp_port, 443 when absent, 0 = none (hysteria2/settings.go). VLESS with security tls: the
// inbound's own port. VLESS with REALITY, whatever its target: nothing, because the certificate of a "self" site is behind
// REALITY (a visitor of the port gets it, but the doctor's own handshake is not one of them), and before the first
// issuance that site shows a self-signed certificate that would read as a healthy 10-year one; the engine reports the
// issued certificate instead (Inbound.CertNotAfter). The agent peeks into the engines' settings; an engine-provided
// accessor replaces this when a third protocol has a TLS listener.
func tlsPortOf(sp plugin.InboundSpec) int {
	if sp.TLS.Mode == 0 {
		return 0
	}
	switch sp.Protocol {
	case "hysteria2":
		var j struct {
			Masquerade struct {
				TCPPort *int `json:"tcp_port"`
			} `json:"masquerade"`
		}
		if len(sp.Settings) > 0 && json.Unmarshal(sp.Settings, &j) != nil {
			return 0
		}
		if p := j.Masquerade.TCPPort; p != nil {
			return max(*p, 0)
		}
		return 443
	case "vless":
		var j struct {
			Security string `json:"security"`
		}
		if json.Unmarshal(sp.Settings, &j) != nil || j.Security != "tls" {
			return 0
		}
		return int(sp.Listen.Port)
	}
	return 0
}

// noteCerts keeps the certificate expiry of the last apply for the doctor (the engines do not expose it later).
func (a *Agent) noteCerts(results []*pb.InboundResult) {
	m := make(map[string]time.Time, len(results))
	for _, r := range results {
		if r.CertNotAfterUnix > 0 {
			m[r.InboundId] = time.Unix(r.CertNotAfterUnix, 0)
		}
	}
	a.certNA.Store(&m)
}

// doctorRestart restarts one inbound through the worker (which owns every engine mutation) and waits.
func (a *Agent) doctorRestart(ctx context.Context, inboundID string) (uint32, error) {
	ch := make(chan *pb.CommandResult, 1)
	select {
	case a.jobs <- job{restart: &pb.RestartInbound{InboundId: inboundID}, res: ch}:
	case <-ctx.Done():
		return 0, ctx.Err()
	}
	select {
	case r := <-ch:
		if r.Error != "" {
			return r.Affected, errors.New(r.Error)
		}
		return r.Affected, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

// doctorReconnectWarp runs the manager mutation on the worker alongside normal desired-state reconciliation.
func (a *Agent) doctorReconnectWarp(ctx context.Context) (bool, error) {
	ch := make(chan *pb.CommandResult, 1)
	select {
	case a.jobs <- job{ctx: ctx, reconnectWarp: true, res: ch}:
	case <-ctx.Done():
		return false, ctx.Err()
	}
	select {
	case r := <-ch:
		if r.Error != "" {
			return false, errors.New(r.Error)
		}
		return r.Affected > 0, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

func statusCount(rep *doctor.Report) (warn, fail int) {
	for _, r := range rep.Results {
		switch r.Status {
		case doctor.Warn:
			warn++
		case doctor.Fail:
			fail++
		}
	}
	return
}

func reportMsg(requestID string, rep *doctor.Report, errText string) *pb.ConnectRequest {
	m := &pb.DoctorReport{RequestId: requestID, Error: errText}
	if rep != nil {
		m.Partial = rep.Partial
		m.DurationMs = uint32(rep.Duration.Milliseconds())
		for _, r := range rep.Results {
			m.Results = append(m.Results, &pb.DoctorResult{
				Id: r.ID, Status: pb.DoctorStatus(r.Status), TitleKey: r.TitleKey(), Detail: r.Detail,
				Params: r.Params, FixId: r.FixID, MeasuredUnix: r.Measured.Unix(), DetailCode: r.Code,
			})
		}
	}
	return &pb.ConnectRequest{Message: &pb.ConnectRequest_DoctorReport{DoctorReport: m}}
}

func (a *Agent) doctorReportMsg(ctx context.Context, requestID string, rep *doctor.Report, errText string) *pb.ConnectRequest {
	if rep == nil {
		return reportMsg(requestID, nil, errText)
	}
	counters := relayCounterResults(ctx, a.doctorRun, a.snapshotModel().relays, a.now())
	if len(counters) == 0 {
		return reportMsg(requestID, rep, errText)
	}
	copy := *rep
	copy.Results = append(append([]doctor.Result(nil), rep.Results...), counters...)
	return reportMsg(requestID, &copy, errText)
}

type relayNftCounter struct {
	Family  string `json:"family"`
	Table   string `json:"table"`
	Name    string `json:"name"`
	Packets uint64 `json:"packets"`
	Bytes   uint64 `json:"bytes"`
}

type relayNftDocument struct {
	Nftables []struct {
		Counter *relayNftCounter `json:"counter"`
	} `json:"nftables"`
}

// relayCounterResults reads only the agent-owned relay table through the doctor's existing command runner. Each forward is a
// separate DoctorResult with id "relay"; the panel treats these as counter reports, not doctor warnings.
func relayCounterResults(ctx context.Context, run func(context.Context, string, ...string) ([]byte, error), forwards []plugin.RelayForward, now time.Time) []doctor.Result {
	if run == nil || len(forwards) == 0 {
		return nil
	}
	out, err := run(ctx, "nft", "-j", "list", "counters", "table", "inet", hostctl.NftRelayTable)
	if err != nil {
		return nil
	}
	var doc relayNftDocument
	if json.Unmarshal(out, &doc) != nil {
		return nil
	}
	counts := make(map[string]relayNftCounter, len(doc.Nftables))
	for _, item := range doc.Nftables {
		if c := item.Counter; c != nil && c.Family == "inet" && c.Table == hostctl.NftRelayTable {
			counts[c.Name] = *c
		}
	}
	items := append([]plugin.RelayForward(nil), forwards...)
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	results := make([]doctor.Result, 0, len(items))
	seen := map[string]bool{}
	for _, f := range items {
		if f.ID == "" || (f.Network != "tcp" && f.Network != "udp") || f.Port == 0 {
			continue
		}
		name := f.Network + "_" + strconv.FormatUint(uint64(f.Port), 10)
		counter, ok := counts[name]
		if !ok || seen[name] {
			continue
		}
		seen[name] = true
		results = append(results, doctor.Result{ID: "relay", Status: doctor.OK, Measured: now,
			Params: map[string]string{"relay": f.ID, "network": f.Network, "port": strconv.FormatUint(uint64(f.Port), 10),
				"bytes": strconv.FormatUint(counter.Bytes, 10), "packets": strconv.FormatUint(counter.Packets, 10)}})
	}
	return results
}

// clientIPv6SettingsChanged schedules just the IPv6 check. A one-bit pending flag coalesces repeated setting changes,
// and Run's busy response is retried once a second so a settings change during the periodic report is not lost.
func (a *Agent) clientIPv6SettingsChanged(s *session) {
	a.clientIPv6DoctorPending.Store(true)
	a.startClientIPv6Doctor(s)
}

func (a *Agent) startClientIPv6Doctor(s *session) {
	if s == nil || s.ctx.Err() != nil || !a.clientIPv6DoctorRunning.CompareAndSwap(false, true) {
		return
	}
	a.goBusy(func() {
		defer func() {
			a.clientIPv6DoctorRunning.Store(false)
			// A reconnect can start while the previous stream's check is finishing. Keep a queued refresh for it.
			if a.clientIPv6DoctorPending.Load() {
				a.startClientIPv6Doctor(a.cur.Load())
			}
		}()
		for a.clientIPv6DoctorPending.Swap(false) {
			rep, err := a.doc.Run(s.ctx, []string{doctor.CheckIPv6})
			if err != nil {
				a.clientIPv6DoctorPending.Store(true)
				if !errors.Is(err, doctor.ErrBusy) || s.ctx.Err() != nil {
					return
				}
				sleepCtx(s.ctx, time.Second)
				if s.ctx.Err() != nil {
					return
				}
				continue
			}
			if s.ctx.Err() != nil {
				a.clientIPv6DoctorPending.Store(true)
				return
			}
			s.send(a.doctorReportMsg(s.ctx, "", rep, ""))
		}
	})
}

// runDoctor answers a RunDoctor with one DoctorReport echoing its request_id ("busy" while another run is on).
// "not_root" is never reported; a check that needs root (nft) skips itself with its own reason.
func (a *Agent) runDoctor(s *session, r *pb.RunDoctor) {
	rep, err := a.doc.Run(s.ctx, r.Checks)
	if err != nil {
		s.send(reportMsg(r.RequestId, nil, "busy")) // ErrBusy is the only error Run returns
		return
	}
	s.send(a.doctorReportMsg(s.ctx, r.RequestId, rep, ""))
}

// applyFix answers an ApplyFix with one CommandResult and, after a real successful fix, sends a partial
// DoctorReport (request_id "") with the re-run of the checks that offer it.
func (a *Agent) applyFix(s *session, r *pb.ApplyFix) {
	ctx, cancel := context.WithTimeout(s.ctx, fixTimeout)
	defer cancel()
	replied := false
	var beforeRefresh func(doctor.FixOutcome)
	if r.FixId == doctor.FixRefreshUnit && !r.DryRun {
		beforeRefresh = func(out doctor.FixOutcome) {
			replied = true
			s.sendSync(cmdResult(&pb.CommandResult{
				RequestId: r.RequestId, Ok: out.OK, Error: out.Err, Affected: out.Affected, Detail: out.Detail, Params: out.Params,
			}), 3*time.Second)
		}
	}
	out := a.doc.ApplyWithRefreshAck(ctx, r.FixId, r.DryRun, r.Params, beforeRefresh)
	if !replied {
		s.send(cmdResult(&pb.CommandResult{
			RequestId: r.RequestId, Ok: out.OK, Error: out.Err, Affected: out.Affected, Detail: out.Detail, Params: out.Params,
		}))
	}
	a.log.Info("doctor fix", "fix", r.FixId, "dry_run", r.DryRun, "ok", out.OK, "affected", out.Affected, "error", out.Err)
	if r.FixId == doctor.FixRefreshUnit {
		return // the helper writes the outcome after restart; the new process reports it on its next doctor run
	}
	if !out.OK || r.DryRun {
		return
	}
	for try := 0; try < recheckTries; try++ {
		rep, err := a.doc.Run(s.ctx, doctor.RecheckIDs(r.FixId))
		if err == nil {
			s.send(a.doctorReportMsg(s.ctx, "", rep, ""))
			return
		}
		sleepCtx(s.ctx, time.Second) // a periodic run is in progress
	}
}

// doctorSchedule sends the unsolicited reports of one stream: the first 30 s after connect, then every
// 10 min +-60 s. It ends with the stream.
func (a *Agent) doctorSchedule(s *session) {
	first, every := a.doctorFirst, a.doctorEvery
	if first == 0 {
		first = doctorFirstDelay
	}
	if every == 0 {
		every = doctorEvery
	}
	span := min(doctorJitterMax, every/10)
	wait := first
	for {
		t := time.NewTimer(wait)
		select {
		case <-s.ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		if rep, err := a.doc.Run(s.ctx, nil); err == nil { // busy: a RunDoctor is on, it reports by itself
			w, f := statusCount(rep)
			a.log.Debug("doctor report", "warn", w, "fail", f, "took", rep.Duration.Round(time.Millisecond))
			s.send(a.doctorReportMsg(s.ctx, "", rep, ""))
		}
		wait = every + time.Duration((mrand.Float64()*2-1)*float64(span))
	}
}
