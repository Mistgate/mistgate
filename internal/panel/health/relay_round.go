package health

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

const (
	relayTargetPrefix      = "relay:"
	relayAutoCheckInterval = 30 * time.Minute
)

func relayTargetID(id string) string { return relayTargetPrefix + id }

func scheduledTarget(sn *snapshot, id string) *target {
	if t := sn.targets[id]; t != nil {
		return t
	}
	return sn.relayTargets[id]
}

func (s *Service) scheduledSkipReason(t *target, live map[string]store.NodeLiveRow) string {
	if t.relay == nil {
		return s.skipReason(t, live[t.node.ID])
	}
	if t.relay.entry.State != "active" || !live[t.relay.entry.ID].Connected {
		return skipNodeOffline
	}
	return s.skipReason(t, live[t.node.ID])
}

func relayTargetKey(t *target) string {
	if t == nil || t.relay == nil {
		return ""
	}
	link := t.relay.link
	key := strings.Join([]string{link.ID, link.ViaNodeID, t.relay.entry.Address, t.node.Address, t.in.ID, t.in.ProfileID, t.in.Protocol,
		strconv.FormatUint(uint64(t.in.ProfileVersion), 10), strconv.FormatUint(t.in.SpecVersion, 10), t.in.TLSNameOverride,
		link.Network, strconv.Itoa(int(link.Port)), strconv.Itoa(int(t.relay.exitPort))}, "\x00")
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// roundRelay probes the exit inbound through the entry's forwarded port. Its result lives in check_json.monitor and never
// enters the exit inbound's client-eye history or failure streak.
func (s *Service) roundRelay(ctx context.Context, id string) (Result, bool) {
	s.invalidateSnapshot()
	sn, err := s.snapshot(ctx)
	if err != nil {
		return Result{}, false
	}
	t := sn.relayTargets[id]
	if t == nil || t.relay == nil {
		return Result{}, false
	}
	if reason, ok := s.relaySkip(ctx, t); !ok || reason != "" {
		return Result{}, false
	}

	res, skipped := s.attempt(ctx, t)
	if skipped {
		return Result{}, false
	}
	if res.Status != adminv1.CheckStatus_CHECK_STATUS_OK {
		select {
		case <-ctx.Done():
			return Result{}, false
		case <-time.After(s.cfg.RetryDelay):
		}
		if reason, ok := s.relaySkip(ctx, t); !ok || reason != "" {
			return Result{}, false
		}
		if again, skipped := s.attempt(ctx, t); !skipped {
			res = again
		}
	}
	if res.Status != adminv1.CheckStatus_CHECK_STATUS_OK {
		res.Status = adminv1.CheckStatus_CHECK_STATUS_FAILED
		if res.ErrorCode == "" {
			res.ErrorCode = "timeout"
		}
	}

	link := t.relay.link
	previous, _ := link.Check()
	key := relayTargetKey(t)
	now := s.now()
	monitor := store.RelayMonitorCheck{AtUnix: now.Unix(), OK: res.Status == adminv1.CheckStatus_CHECK_STATUS_OK,
		ResponseMS: res.ResponseMS, TargetKey: key}
	if !monitor.OK {
		monitor.Code = res.ErrorCode
		monitor.Stage = relayStageForCode(res.ErrorCode)
		monitor.FailStreak = 1
		if previous.Monitor != nil && !previous.Monitor.OK && previous.Monitor.TargetKey == key && previous.Monitor.AtUnix > 0 &&
			now.Unix() >= previous.Monitor.AtUnix && now.Sub(time.Unix(previous.Monitor.AtUnix, 0)) <= 2*s.interval(ctx)+time.Minute {
			monitor.FailStreak = previous.Monitor.FailStreak + 1
		}
		if previous.Monitor != nil {
			monitor.AutoCheckAt = previous.Monitor.AutoCheckAt
			monitor.AutoCheckTargetKey = previous.Monitor.AutoCheckTargetKey
		}
	}

	if !monitor.OK {
		s.diagnoseRelay(ctx, link.ID, key, &monitor)
	}
	if err := s.st.SetRelayMonitorCheck(ctx, link.ID, monitor); err != nil && err != store.ErrNotFound {
		s.log.Warn("health: store relay monitor", "relay", link.ID, "err", err)
	}
	return res, true
}

func (s *Service) relaySkip(ctx context.Context, t *target) (string, bool) {
	exitLive, err := s.liveNode(ctx, t.node.ID)
	if err != nil {
		return "", false
	}
	if reason := s.skipReason(t, exitLive); reason != "" {
		return reason, true
	}
	entryLive, err := s.liveNode(ctx, t.relay.entry.ID)
	if err != nil {
		return "", false
	}
	if t.relay.entry.State != "active" || !entryLive.Connected {
		return skipNodeOffline, true
	}
	return "", true
}

func (s *Service) diagnoseRelay(ctx context.Context, id, targetKey string, monitor *store.RelayMonitorCheck) {
	now := s.now()
	due := monitor.AutoCheckTargetKey != targetKey || monitor.AutoCheckAt == 0 ||
		(now.Unix() >= monitor.AutoCheckAt && now.Sub(time.Unix(monitor.AutoCheckAt, 0)) >= relayAutoCheckInterval)
	if !due {
		return
	}
	monitor.AutoCheckAt, monitor.AutoCheckTargetKey = now.Unix(), targetKey
	if err := s.st.SetRelayMonitorCheck(ctx, id, *monitor); err != nil {
		s.log.Warn("health: reserve relay diagnosis", "relay", id, "err", err)
		return
	}
	checkCtx, cancel := context.WithTimeout(ctx, roundBudget)
	defer cancel()
	check, err := s.fl.CheckRelay(checkCtx, id)
	if err != nil {
		s.log.Warn("health: diagnose relay", "relay", id, "err", err)
		return
	}
	switch {
	case check.Leg.Status == "fail":
		monitor.Stage, monitor.Code = "leg", check.Leg.Code
	case check.Path.Status == "fail":
		monitor.Stage, monitor.Code = "entry", check.Path.Code
	case check.Verified() && monitor.Stage != "freeze":
		monitor.Stage = "path"
	}
}

func relayStageForCode(code string) string {
	if code == "freeze" {
		return "freeze"
	}
	return "path"
}

func (s *Service) addRelayDownConds(ctx context.Context, now time.Time, sn *snapshot, links []store.RelayLink, active []store.HealthAlert, d *derived) {
	activeKeys := make(map[key]bool, len(active))
	for _, alert := range active {
		activeKeys[key{alert.Kind, alert.NodeID, alert.Subject}] = true
	}
	add := func(c cond) { d.conds[c.key] = c }
	staleAfter := 2*s.interval(ctx) + time.Minute
	for _, link := range links {
		k := key{kRelayDown, link.ViaNodeID, link.ID}
		entryDown := key{kNodeDown, link.ViaNodeID, ""}
		exitDown := key{kNodeDown, link.ExitNodeID, ""}
		exitCheck := key{kCheckFailed, link.ExitNodeID, link.InboundID}
		exitSilent := key{kNoTraffic, link.ExitNodeID, ""}
		if activeKeys[entryDown] || activeKeys[exitDown] || activeKeys[exitCheck] || activeKeys[exitSilent] ||
			d.conds[entryDown].kind != "" || d.conds[exitDown].kind != "" || d.conds[exitCheck].kind != "" || d.conds[exitSilent].kind != "" {
			d.superseded[k] = true
			continue
		}
		if d.holdNode[link.ViaNodeID] || d.holdNode[link.ExitNodeID] {
			d.holdKeys[k] = true
			continue
		}
		t := sn.relayTargets[relayTargetID(link.ID)]
		if t == nil {
			continue
		}
		check, ok := link.Check()
		if !ok || check.Monitor == nil {
			d.holdKeys[k] = true
			continue
		}
		monitor := check.Monitor
		if monitor.AtUnix <= 0 || now.Unix() < monitor.AtUnix || now.Sub(time.Unix(monitor.AtUnix, 0)) > staleAfter ||
			monitor.TargetKey == "" || monitor.TargetKey != relayTargetKey(t) {
			d.holdKeys[k] = true
			continue
		}
		if monitor.OK {
			continue
		}
		if monitor.FailStreak < failStreakMin {
			d.holdKeys[k] = true
			continue
		}
		stage := monitor.Stage
		if stage != "leg" && stage != "entry" && stage != "path" && stage != "freeze" {
			stage = relayStageForCode(monitor.Code)
		}
		add(cond{key: k, severity: sevWarning, why: "health.alert.relay_down.why." + stage, params: map[string]string{
			"relay": link.ViaNodeName, "exit": link.ExitNodeName, "exit_profile": link.ProfileName,
			"port": strconv.Itoa(int(link.Port)), "stage": stage,
		}})
	}
}
