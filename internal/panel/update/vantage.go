package update

import (
	"context"
	"time"

	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/tcpcheck"
)

// The gate's second opinion. After a reboot or an agent update the panel's own network path to the node may lose packets
// while the node is fine and every other node reaches it: the synthetic probes then time out and the gate would fail a healthy
// node. healthGate (rollout.go) therefore lets a step pass on a relaxed rule, when all of these hold:
//
//	P  the only thing missing is probe_failed (no hard miss, no other soft miss);
//	T  every probeable inbound has a round since the gate window began, every failed round is a timeout (not refused, auth,
//	   tls, exit_unreachable) and the failures have lasted at least vantageMinFailing;
//	S  the node reports itself well: the job is done / the update committed (the callers' own check), every enabled inbound is
//	   running, and the doctor's resolver check is OK and was measured after the window began;
//	V  the system port check, run after the window began from another node (never the panel, never the target), sees every UDP
//	   port ok and every TCP/TLS port ours.
//
// V takes 6-30 s and the worker holds s.mu, so it runs in its own goroutine: one run per (node, window), repeated only when it
// errored (at most vantageMaxRuns times), never when it said "bad". Its result lives in memory, so a restart of the panel
// asks again. V proves reachability (and the TLS listeners); with S it is not a full client round through the tunnel.
const (
	vantageMinFailing = 60 * time.Second
	vantageMaxRuns    = 3
	vantageRunTimeout = 75 * time.Second
)

// Verdicts of the second vantage.
const (
	vantageOK   = "ok"   // another node sees every port: the lossy part is the panel's own path
	vantageBad  = "bad"  // another node does not see them either
	vantageNone = "none" // nobody could check (no other node, same host), or every run errored
	vantageErr  = "err"  // one run did not finish: asked again
)

type vantageKey struct {
	node  string
	since int64 // the window's start, UnixNano
}

// vantageRun is the second vantage of one gate window.
type vantageRun struct {
	running bool
	runs    int    // runs that ended, the errored ones included
	verdict string // "" until decided: vantageOK, vantageBad, vantageNone
	sender  string // the name of the node that checked (ok, bad)
	noOther bool   // none, because there is no other node that could check
}

// params are the words the step keeps about it (the SPA's notes in "updates.step.note.*").
func (r vantageRun) params() map[string]string {
	switch r.verdict {
	case vantageOK:
		return map[string]string{"vantage": r.sender, "panel_path": "lossy"}
	case vantageBad:
		return map[string]string{"vantage": r.sender, "vantage_verdict": vantageBad}
	case vantageNone:
		out := map[string]string{"vantage_verdict": vantageNone}
		if r.noOther {
			out["no_vantage"] = "1"
		}
		return out
	}
	return nil
}

// vantageOf returns what is known about the window and starts the run when none is running or decided. Callers hold s.mu
// (it guards runCtx); the run itself never takes it.
func (s *Service) vantageOf(nodeID string, since time.Time) vantageRun {
	key := vantageKey{nodeID, since.UnixNano()}
	s.vantageMu.Lock()
	defer s.vantageMu.Unlock()
	r := s.vantage[key]
	if r == nil {
		for k := range s.vantage { // an older window of the same node is over
			if k.node == nodeID {
				delete(s.vantage, k)
			}
		}
		r = &vantageRun{}
		s.vantage[key] = r
	}
	if r.verdict == "" && !r.running {
		r.running = true
		ctx := s.runCtx
		s.wg.Add(1)
		go s.runVantage(ctx, key, since)
	}
	return *r
}

func (s *Service) runVantage(ctx context.Context, key vantageKey, since time.Time) {
	defer s.wg.Done()
	rctx, cancel := context.WithTimeout(ctx, vantageRunTimeout)
	sender, udp, tcp, code := s.fl.CheckPortsForGate(rctx, key.node)
	cancel()
	verdict := vantageVerdict(key.node, since, sender, udp, tcp, code)
	name := sender
	if verdict == vantageOK || verdict == vantageBad {
		if n, err := s.st.Node(ctx, sender); err == nil && n.Name != "" {
			name = n.Name
		}
	}
	s.vantageMu.Lock()
	r := s.vantage[key]
	if r == nil { // a newer window of the node replaced it
		s.vantageMu.Unlock()
		return
	}
	r.running = false
	r.runs++
	switch {
	case verdict == vantageErr && r.runs < vantageMaxRuns: // the next pass asks again
	case verdict == vantageErr:
		r.verdict = vantageNone
	default:
		r.verdict, r.sender = verdict, name
		r.noOther = verdict == vantageNone
	}
	s.vantageMu.Unlock()
	if verdict == vantageOK {
		ev := store.EventRow{Time: s.now(), Severity: 1, Code: "panel_path_lossy", Source: "panel", NodeID: key.node, Params: map[string]string{"vantage": name}}
		if err := s.st.InsertEvent(ctx, ev); err != nil {
			s.log.Warn("update: panel path event", "code", "update_panel_path_event_failed", "node", key.node, "err", err)
		}
	}
}

// vantageVerdict reads one port check run: ok, bad, none (nobody could check) or err (it did not finish; ask again).
func vantageVerdict(nodeID string, since time.Time, sender string, udp []store.PortCheck, tcp []store.TCPCheck, code string) string {
	switch code {
	case "":
	case "no_sender", "same_host", "agent_too_old":
		return vantageNone
	default:
		return vantageErr
	}
	if sender == "" || sender == "panel" || sender == nodeID {
		return vantageNone
	}
	if len(udp) == 0 {
		return vantageErr
	}
	bad := false
	for _, c := range udp {
		if c.CheckedAt.Before(since) { // an older run, taken over from a caller that waited for it
			return vantageErr
		}
		bad = bad || c.Verdict != "ok"
	}
	for _, c := range tcp {
		if c.Verdict == "" || c.CheckedAt.Before(since) { // the probe did not run, or it is old
			return vantageErr
		}
		bad = bad || c.Verdict != tcpcheck.VerdictOurs
	}
	if bad {
		return vantageBad
	}
	return vantageOK
}

// nodeSelfGood is S without the job: every enabled inbound runs and the doctor's resolver check is OK, measured since the window
// began (an older agent has no fresh row: the answer is no and the gate acts as before).
func (s *Service) nodeSelfGood(ctx context.Context, nodeID string, inbounds []store.FleetInboundRow, since time.Time) bool {
	for _, in := range inbounds {
		if in.Enabled && in.State != "active" { // the panel's word for RUNNING (fleet.runState)
			return false
		}
	}
	rows, err := s.st.DoctorResults(ctx, nodeID)
	if err != nil {
		return false
	}
	for _, r := range rows {
		if r.CheckID == "resolver" {
			return r.Status == 1 && !r.Measured.Before(since)
		}
	}
	return false
}
