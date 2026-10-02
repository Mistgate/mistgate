package health

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// rpc is the admin HealthService (health.proto).
type rpc struct{ s *Service }

func badRequest(msg string) error {
	return connect.NewError(connect.CodeInvalidArgument, errors.New(msg))
}

func notFound(what string) error {
	return connect.NewError(connect.CodeNotFound, fmt.Errorf("%s not found", what))
}

func precondition(msg string) error {
	return connect.NewError(connect.CodeFailedPrecondition, errors.New(msg))
}

// ---------------------------------------------------------------------------------------------------
// Alerts

const (
	defaultHistory = 7 * 24 * time.Hour
	maxHistory     = 30 * 24 * time.Hour
	historyLimit   = 200
	maxMute        = 7 * 24 * time.Hour
)

func (s *Service) nodeNames(ctx context.Context) (map[string]string, error) {
	nodes, err := s.st.Nodes(ctx, true)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(nodes))
	for _, n := range nodes {
		out[n.ID] = n.Name
	}
	return out, nil
}

// alertMsg converts a stored alert. fixable(a) tells whether the doctor still offers its fix on a node that can apply it.
func alertMsg(a store.HealthAlert, names map[string]string, fixable func(store.HealthAlert) string) *adminv1.Alert {
	m := &adminv1.Alert{
		Id: a.ID, Severity: adminv1.AlertSeverity(a.Severity), Kind: kindProto[a.Kind], NodeId: a.NodeID, NodeName: names[a.NodeID],
		Subject: a.Subject, TitleKey: a.TitleKey, Params: a.Params, WhyKey: a.WhyKey,
		FirstSeenUnix: a.FirstSeen.Unix(), LastSeenUnix: a.LastSeen.Unix(), ResolvedAtUnix: fleetUnix(a.ResolvedAt),
		Resolution: a.Resolution, MutedUntilUnix: fleetUnix(a.MutedUntil),
	}
	active := a.ResolvedAt.IsZero()
	if active {
		if fix := fixable(a); fix != "" {
			m.Actions = append(m.Actions, "apply_fix:"+fix)
		}
		m.Actions = append(m.Actions, alertActions(a)...)
	}
	if a.NodeID != "" {
		m.Actions = append(m.Actions, "open_node")
	}
	if active {
		if a.Kind == kDoctorWarn {
			m.Actions = append(m.Actions, "accept")
		}
		m.Actions = append(m.Actions, "mute")
	}
	return m
}

// alertActions are the buttons that act on what an alert says, by its kind and diagnosis: restart a profile that does
// not answer, go where a port or a certificate is changed, open the WARP card.
func alertActions(a store.HealthAlert) []string {
	if a.NodeID == "" {
		return nil
	}
	variant := a.WhyKey[strings.LastIndex(a.WhyKey, ".")+1:]
	switch a.Kind {
	case kCheckFailed:
		switch variant {
		case "timeout", "auth", "refused":
			return []string{"restart_inbound"}
		case "udp_blocked", "tls":
			return []string{"open_profiles"}
		case "warp_path":
			return []string{"open_warp"}
		}
	case kNoTraffic:
		switch variant {
		case "auth", "refused":
			return []string{"restart_inbounds"}
		case "udp_blocked", "tls":
			return []string{"open_profiles"}
		}
	case kCertExpiry:
		if a.Subject != "cert" { // a profile's certificate, not the agent's own
			return []string{"open_profiles"}
		}
	case kDoctorWarn, kDoctorFail:
		switch a.Params["check"] {
		case "warp_path":
			return []string{"open_warp"}
		case "port_conflicts":
			return []string{"open_profiles"}
		}
	}
	return nil
}

// withOnline adds params.online to an alert that offers a restart: the open sessions that the restart drops now.
func withOnline(m *adminv1.Alert, online map[string]int, inbounds func(nodeID string) []string) {
	var ids []string
	switch {
	case slices.Contains(m.Actions, "restart_inbound"):
		ids = []string{m.Params["inbound"]}
	case slices.Contains(m.Actions, "restart_inbounds"):
		ids = inbounds(m.NodeId)
	default:
		return
	}
	n := 0
	for _, id := range ids {
		n += online[id]
	}
	p := maps.Clone(m.Params)
	if p == nil {
		p = map[string]string{}
	}
	p["online"] = strconv.Itoa(n)
	m.Params = p
}

// fixOffers maps (node, check) to the fix the stored doctor row offers now, for nodes that can apply it.
func (s *Service) fixOffers(ctx context.Context) (func(store.HealthAlert) string, error) {
	rows, err := s.st.DoctorResults(ctx, "")
	if err != nil {
		return nil, err
	}
	offers := map[[2]string]string{}
	for _, r := range rows {
		if r.FixID != "" {
			offers[[2]string{r.NodeID, r.CheckID}] = r.FixID
		}
	}
	return func(a store.HealthAlert) string {
		check := a.Params["check"]
		if a.NodeID == "" || check == "" {
			return ""
		}
		fix := offers[[2]string{a.NodeID, check}]
		if fix == "" {
			return ""
		}
		if up, caps, _ := s.fl.Live(a.NodeID); !up || !slices.Contains(caps, capDoctor) {
			return ""
		}
		return fix
	}, nil
}

func (r rpc) ListAlerts(ctx context.Context, req *connect.Request[adminv1.ListAlertsRequest]) (*connect.Response[adminv1.ListAlertsResponse], error) {
	s := r.s
	now := s.now()
	window := min(time.Duration(req.Msg.HistoryWindowS)*time.Second, maxHistory)
	if window == 0 {
		window = defaultHistory
	}
	active, err := s.st.ActiveAlerts(ctx)
	if err != nil {
		return nil, s.internal("list alerts", err)
	}
	hist, err := s.st.AlertHistory(ctx, now.Add(-window), req.Msg.NodeId, historyLimit)
	if err != nil {
		return nil, s.internal("alert history", err)
	}
	names, err := s.nodeNames(ctx)
	if err != nil {
		return nil, s.internal("node names", err)
	}
	fixable, err := s.fixOffers(ctx)
	if err != nil {
		return nil, s.internal("doctor rows", err)
	}
	sort.SliceStable(active, func(i, j int) bool { // critical first, then newest first_seen (the query is newest first)
		return active[i].Severity == sevCritical && active[j].Severity != sevCritical
	})
	sn, err := s.snapshot(ctx)
	if err != nil {
		return nil, s.internal("inbounds", err)
	}
	online := s.fl.OnlineByInbound()
	inbounds := func(nodeID string) []string {
		var ids []string
		for _, t := range sn.byNode[nodeID] {
			ids = append(ids, t.in.ID)
		}
		return ids
	}
	resp := &adminv1.ListAlertsResponse{NowUnix: now.Unix()}
	for _, a := range active {
		if req.Msg.NodeId == "" || a.NodeID == req.Msg.NodeId {
			m := alertMsg(a, names, fixable)
			withOnline(m, online, inbounds)
			resp.Active = append(resp.Active, m)
		}
	}
	for _, a := range hist {
		resp.History = append(resp.History, alertMsg(a, names, fixable))
	}
	return connect.NewResponse(resp), nil
}

func (r rpc) MuteAlert(ctx context.Context, req *connect.Request[adminv1.MuteAlertRequest]) (*connect.Response[adminv1.MuteAlertResponse], error) {
	s := r.s
	d := time.Duration(req.Msg.DurationS) * time.Second
	if d > maxMute {
		return nil, badRequest("duration_s must be at most 604800 (7 days)")
	}
	var until time.Time
	if d > 0 {
		until = s.now().Add(d)
	}
	a, err := s.st.MuteAlert(ctx, req.Msg.AlertId, until)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, notFound("alert")
	case errors.Is(err, store.ErrConflict):
		return nil, precondition("the alert is already resolved")
	case err != nil:
		return nil, s.internal("mute alert", err)
	}
	s.audit(ctx, "health.mute_alert", map[string]string{"alert_id": a.ID, "kind": a.Kind, "node_id": a.NodeID, "duration_s": fmt.Sprint(req.Msg.DurationS)})
	names, _ := s.nodeNames(ctx)
	fixable, err := s.fixOffers(ctx)
	if err != nil {
		return nil, s.internal("doctor rows", err)
	}
	return connect.NewResponse(&adminv1.MuteAlertResponse{Alert: alertMsg(a, names, fixable)}), nil
}

// ---------------------------------------------------------------------------------------------------
// Synthetic checks

const buckets = 48 // half-hour buckets = 24 h

func resultMsg(r Result) *adminv1.CheckResult {
	return &adminv1.CheckResult{Status: r.Status, AtUnix: r.At.Unix(), LatencyMs: r.LatencyMS, ExitIp: r.ExitIP, ExitCountry: r.ExitCountry,
		ErrorCode: r.ErrorCode, ErrorDetail: r.ErrorDetail}
}

func (r rpc) GetChecks(ctx context.Context, req *connect.Request[adminv1.GetChecksRequest]) (*connect.Response[adminv1.GetChecksResponse], error) {
	s := r.s
	now := s.now()
	sn, err := s.snapshot(ctx)
	if err != nil {
		return nil, s.internal("checks", err)
	}
	if id := req.Msg.NodeId; id != "" && len(sn.byNode[id]) == 0 && !slices.ContainsFunc(sn.nodes, func(n store.NodeRow) bool { return n.ID == id }) {
		return nil, notFound("node")
	}
	cur := now.Unix() - now.Unix()%1800
	first := cur - (buckets-1)*1800
	samples, err := s.st.SamplesSince(ctx, time.Unix(first, 0))
	if err != nil {
		return nil, s.internal("check samples", err)
	}
	type acc struct {
		ok, failed int
		lat        []uint32
	}
	hist := map[string][buckets]acc{}
	for _, x := range samples {
		i := int((x.At.Unix() - first) / 1800)
		if i < 0 || i >= buckets {
			continue
		}
		h := hist[x.InboundID]
		if x.Status == int(adminv1.CheckStatus_CHECK_STATUS_FAILED) {
			h[i].failed++
		} else {
			h[i].ok++
			if x.LatencyMS > 0 {
				h[i].lat = append(h[i].lat, x.LatencyMS)
			}
		}
		hist[x.InboundID] = h
	}

	resp := &adminv1.GetChecksResponse{NowUnix: now.Unix(), IntervalS: uint32(s.interval(ctx) / time.Second)}
	colIdx := map[string]int{}
	var cols []*adminv1.CheckColumn
	for _, n := range sn.nodes {
		if req.Msg.NodeId != "" && n.ID != req.Msg.NodeId {
			continue
		}
		for _, t := range sn.byNode[n.ID] {
			if _, ok := colIdx[t.in.ProfileID]; !ok {
				colIdx[t.in.ProfileID] = len(cols)
				cols = append(cols, &adminv1.CheckColumn{ProfileId: t.in.ProfileID, ProfileName: t.in.ProfileName, Protocol: t.in.Protocol})
			}
		}
	}
	sort.SliceStable(cols, func(i, j int) bool {
		return strings.ToLower(cols[i].ProfileName) < strings.ToLower(cols[j].ProfileName)
	})
	for i, c := range cols {
		colIdx[c.ProfileId] = i
	}
	resp.Columns = cols
	for _, n := range sn.nodes {
		if req.Msg.NodeId != "" && n.ID != req.Msg.NodeId {
			continue
		}
		row := &adminv1.CheckRow{NodeId: n.ID, NodeName: n.Name, CountryCode: n.CountryCode, NodeStatus: s.fl.NodeStatus(ctx, n),
			Cells: make([]*adminv1.CheckCell, len(cols))}
		for i := range row.Cells {
			row.Cells[i] = &adminv1.CheckCell{}
		}
		for _, t := range sn.byNode[n.ID] {
			cell := &adminv1.CheckCell{Deployed: true, InboundId: t.in.ID, History: make([]*adminv1.CheckBucket, buckets)}
			c := s.cellOf(ctx, t.in.ID)
			cell.FailStreak = uint32(c.streak)
			if why := s.skipReason(t); why != "" {
				cell.Last = &adminv1.CheckResult{Status: adminv1.CheckStatus_CHECK_STATUS_SKIPPED, AtUnix: now.Unix(), ErrorCode: why}
			} else if c.last != nil {
				cell.Last = resultMsg(*c.last)
			}
			h := hist[t.in.ID]
			for i := range buckets {
				b := &adminv1.CheckBucket{StartUnix: first + int64(i)*1800, Ok: uint32(h[i].ok), Failed: uint32(h[i].failed)}
				if l := h[i].lat; len(l) > 0 {
					slices.Sort(l)
					b.LatencyMs = l[len(l)/2]
				}
				cell.History[i] = b
			}
			row.Cells[colIdx[t.in.ProfileID]] = cell
		}
		resp.Rows = append(resp.Rows, row)
	}
	return connect.NewResponse(resp), nil
}

func (r rpc) RunChecksNow(ctx context.Context, req *connect.Request[adminv1.RunChecksNowRequest]) (*connect.Response[adminv1.RunChecksNowResponse], error) {
	s := r.s
	if id := req.Msg.NodeId; id != "" {
		if _, err := s.st.Node(ctx, id); errors.Is(err, store.ErrNotFound) {
			return nil, notFound("node")
		} else if err != nil {
			return nil, s.internal("run checks", err)
		}
	}
	scheduled, skipped, retry, err := s.RunChecksNow(ctx, req.Msg.NodeId)
	if err != nil {
		return nil, s.internal("run checks", err)
	}
	if scheduled == 0 && retry > 0 {
		return nil, connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("checks ran a moment ago: try again in %d s", int(retry.Seconds())))
	}
	return connect.NewResponse(&adminv1.RunChecksNowResponse{Scheduled: uint32(scheduled), Skipped: uint32(skipped)}), nil
}

// ---------------------------------------------------------------------------------------------------
// Doctor

func (r rpc) GetDoctor(ctx context.Context, req *connect.Request[adminv1.GetDoctorRequest]) (*connect.Response[adminv1.GetDoctorResponse], error) {
	s := r.s
	nodes, err := s.doctorViews(ctx, req.Msg.NodeId)
	if err != nil {
		return nil, s.internal("get doctor", err)
	}
	if req.Msg.NodeId != "" && len(nodes) == 0 {
		return nil, notFound("node")
	}
	return connect.NewResponse(&adminv1.GetDoctorResponse{NowUnix: s.now().Unix(), Nodes: nodes}), nil
}

const runDoctorWait = 30 * time.Second

func (r rpc) RunDoctor(ctx context.Context, req *connect.Request[adminv1.RunDoctorRequest]) (*connect.Response[adminv1.RunDoctorResponse], error) {
	s := r.s
	if id := req.Msg.NodeId; id != "" {
		n, err := s.st.Node(ctx, id)
		if errors.Is(err, store.ErrNotFound) || (err == nil && n.State == "retired") {
			return nil, notFound("node")
		} else if err != nil {
			return nil, s.internal("run doctor", err)
		}
		rep, err := s.fl.RunDoctor(ctx, id, nil, runDoctorWait)
		if err != nil {
			return nil, err
		}
		if rep.Error != "" {
			return nil, precondition("the node cannot run the doctor now: " + store.Clip(rep.Error, 64))
		}
	} else {
		nodes, err := s.st.Nodes(ctx, false)
		if err != nil {
			return nil, s.internal("run doctor", err)
		}
		var wg sync.WaitGroup
		for _, n := range nodes {
			if up, caps, _ := s.fl.Live(n.ID); !up || !slices.Contains(caps, capDoctor) {
				continue // offline and old nodes keep their last report; that is not an error
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := s.fl.RunDoctor(ctx, n.ID, nil, runDoctorWait); err != nil {
					s.log.Info("health: run doctor", "node", n.ID, "err", err)
				}
			}()
		}
		wg.Wait()
	}
	views, err := s.doctorViews(ctx, req.Msg.NodeId)
	if err != nil {
		return nil, s.internal("run doctor", err)
	}
	return connect.NewResponse(&adminv1.RunDoctorResponse{NowUnix: s.now().Unix(), Nodes: views}), nil
}

// ---------------------------------------------------------------------------------------------------
// ApplyFix: plan, then confirm

const (
	planTTL   = 10 * time.Minute
	recheckBy = 10 * time.Second
)

// fixPlan is what the admin was shown in step 1; a plan id is valid for exactly this.
type fixPlan struct {
	node, fix string
	params    map[string]string
	expires   time.Time
}

func (r rpc) ApplyFix(ctx context.Context, req *connect.Request[adminv1.ApplyFixRequest]) (*connect.Response[adminv1.ApplyFixResponse], error) {
	s := r.s
	m := req.Msg
	fix := knownFix(m.FixId)
	if fix == "" {
		return nil, badRequest("unknown fix")
	}
	n, err := s.st.Node(ctx, m.NodeId)
	if errors.Is(err, store.ErrNotFound) || (err == nil && n.State == "retired") {
		return nil, notFound("node")
	} else if err != nil {
		return nil, s.internal("apply fix", err)
	}
	params, err := s.fixParams(ctx, n, fix, m.Params)
	if err != nil {
		return nil, err
	}
	if m.DryRun {
		return r.plan(ctx, n, fix, params)
	}
	return r.apply(ctx, n, fix, params, m.PlanId)
}

// fixParams checks the arguments of a fix: only restart_inbound takes one, inbound_id, and it must be an inbound of this node.
func (s *Service) fixParams(ctx context.Context, n store.NodeRow, fix string, in map[string]string) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range in {
		if fix != "restart_inbound" || k != "inbound_id" {
			return nil, badRequest("unexpected parameter " + store.Clip(k, 32))
		}
		if v != "" {
			out[k] = v
		}
	}
	if id := out["inbound_id"]; id != "" {
		rows, err := s.st.FleetInbounds(ctx, n.ID, false)
		if err != nil {
			return nil, s.internal("apply fix", err)
		}
		if !slices.ContainsFunc(rows, func(r store.FleetInboundRow) bool { return r.ID == id }) {
			return nil, badRequest("inbound_id is not an inbound of this node")
		}
	}
	return out, nil
}

// plan is step 1: the node says what would happen, nothing changes.
func (r rpc) plan(ctx context.Context, n store.NodeRow, fix string, params map[string]string) (*connect.Response[adminv1.ApplyFixResponse], error) {
	s := r.s
	cr, err := s.fl.ApplyFix(ctx, n.ID, fix, true, params)
	if err != nil {
		return nil, err
	}
	if !cr.Ok {
		return nil, precondition("the node cannot do this fix: " + store.Clip(cr.Error, 120))
	}
	now := s.now()
	id := store.NewID("pln_")
	s.fixMu.Lock()
	for k, p := range s.plans {
		if !now.Before(p.expires) {
			delete(s.plans, k)
		}
	}
	s.plans[id] = fixPlan{node: n.ID, fix: fix, params: params, expires: now.Add(planTTL)}
	s.fixMu.Unlock()
	detail := store.Clip(cr.Detail, 256)
	audit := map[string]string{"node_id": n.ID, "fix_id": fix, "detail": detail}
	maps.Copy(audit, params)
	s.audit(ctx, "health.fix_plan", audit)
	planParams := cr.Params
	if fix == "restart_inbound" {
		planParams = s.restartFacts(ctx, n.ID, params["inbound_id"], cr.Params)
	}
	return connect.NewResponse(&adminv1.ApplyFixResponse{
		Plan:   &adminv1.FixPlan{FixId: fix, TitleKey: "health.fix." + fix + ".plan", Params: planParams, Detail: detail, Disruptive: fix == "restart_inbound" || fix == "reconnect_warp"},
		PlanId: id, PlanExpiresUnix: now.Add(planTTL).Unix(),
	}), nil
}

// restartFacts are the node's plan params of a restart with what the dialog says by name: "profiles", the names of
// the inbounds the node will restart (its "inbounds", else the one asked for), and "online", their open sessions now.
func (s *Service) restartFacts(ctx context.Context, nodeID, asked string, from map[string]string) map[string]string {
	out := maps.Clone(from)
	if out == nil {
		out = map[string]string{}
	}
	ids := strings.Split(from["inbounds"], ",")
	if from["inbounds"] == "" {
		ids = []string{asked}
	}
	sn, err := s.snapshot(ctx)
	if err != nil {
		return out
	}
	online := s.fl.OnlineByInbound()
	var names []string
	n := 0
	for _, id := range ids {
		if t := sn.targets[strings.TrimSpace(id)]; t != nil && t.node.ID == nodeID {
			names = append(names, t.in.ProfileName)
			n += online[t.in.ID]
		}
	}
	if len(names) > 0 {
		out["profiles"] = strings.Join(names, ", ")
		out["online"] = strconv.Itoa(n)
	}
	return out
}

// ---------------------------------------------------------------------------------------------------
// "This is normal for this node"

// doctorRow loads a node that is not retired and the stored result of one of its checks.
func (s *Service) doctorRow(ctx context.Context, nodeID, checkID string) (store.NodeRow, store.DoctorRow, error) {
	n, err := s.st.Node(ctx, nodeID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && n.State == "retired") {
		return n, store.DoctorRow{}, notFound("node")
	} else if err != nil {
		return n, store.DoctorRow{}, s.internal("doctor item", err)
	}
	rows, err := s.st.DoctorResults(ctx, n.ID)
	if err != nil {
		return n, store.DoctorRow{}, s.internal("doctor item", err)
	}
	for _, r := range rows {
		if r.CheckID == checkID {
			return n, r, nil
		}
	}
	return n, store.DoctorRow{}, notFound("doctor item")
}

// nodeView is the doctor state of one node after a change, with its alerts already brought up to date.
func (s *Service) nodeView(ctx context.Context, nodeID string) (*adminv1.NodeDoctor, error) {
	s.evaluate(ctx)
	views, err := s.doctorViews(ctx, nodeID)
	if err != nil || len(views) != 1 {
		return nil, s.internal("doctor view", err)
	}
	return views[0], nil
}

func (r rpc) AcceptDoctorItem(ctx context.Context, req *connect.Request[adminv1.AcceptDoctorItemRequest]) (*connect.Response[adminv1.AcceptDoctorItemResponse], error) {
	s := r.s
	n, row, err := s.doctorRow(ctx, req.Msg.NodeId, req.Msg.CheckId)
	if err != nil {
		return nil, err
	}
	// a certificate running out is never normal; a FAIL is not accepted either
	if row.Status != int(agentv1.DoctorStatus_DOCTOR_STATUS_WARN) || row.CheckID == "cert_expiry" {
		return nil, precondition("only a warning can be accepted")
	}
	// what is accepted is the fact the code names; without one (an older agent) it would cover any later warning
	if row.DetailCode == "" {
		return nil, precondition("this warning has no detail code (an older agent): update the agent to accept it")
	}
	switch ok, err := s.st.AcceptDoctor(ctx, store.DoctorAccept{NodeID: n.ID, CheckID: row.CheckID, DetailCode: row.DetailCode, By: s.cfg.Actor(ctx), At: s.now()}); {
	case err != nil:
		return nil, s.internal("accept doctor item", err)
	case !ok: // a newer report changed the result since it was read here
		return nil, precondition("the check's result changed; look again")
	}
	s.audit(ctx, "health.accept_doctor", map[string]string{"node_id": n.ID, "node": n.Name, "check": row.CheckID, "detail_code": row.DetailCode})
	v, err := s.nodeView(ctx, n.ID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.AcceptDoctorItemResponse{Doctor: v}), nil
}

func (r rpc) UnacceptDoctorItem(ctx context.Context, req *connect.Request[adminv1.UnacceptDoctorItemRequest]) (*connect.Response[adminv1.UnacceptDoctorItemResponse], error) {
	s := r.s
	n, err := s.st.Node(ctx, req.Msg.NodeId)
	if errors.Is(err, store.ErrNotFound) || (err == nil && n.State == "retired") {
		return nil, notFound("node")
	} else if err != nil {
		return nil, s.internal("unaccept doctor item", err)
	}
	removed, err := s.st.UnacceptDoctor(ctx, n.ID, req.Msg.CheckId)
	if err != nil {
		return nil, s.internal("unaccept doctor item", err)
	}
	if removed {
		s.audit(ctx, "health.unaccept_doctor", map[string]string{"node_id": n.ID, "node": n.Name, "check": req.Msg.CheckId})
	}
	v, err := s.nodeView(ctx, n.ID)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.UnacceptDoctorItemResponse{Doctor: v}), nil
}

// apply is step 2: it needs the plan id of step 1 for this node, fix and arguments, once and within ten minutes.
func (r rpc) apply(ctx context.Context, n store.NodeRow, fix string, params map[string]string, planID string) (*connect.Response[adminv1.ApplyFixResponse], error) {
	s := r.s
	now := s.now()
	s.fixMu.Lock()
	p, ok := s.plans[planID]
	if ok && !now.Before(p.expires) {
		delete(s.plans, planID)
		ok = false
	}
	if !ok || p.node != n.ID || p.fix != fix || !maps.Equal(p.params, params) {
		s.fixMu.Unlock()
		return nil, precondition("no fresh plan")
	}
	if s.fixBusy[n.ID] {
		s.fixMu.Unlock()
		return nil, precondition("another fix is running on this node")
	}
	delete(s.plans, planID) // single use
	s.fixBusy[n.ID] = true
	s.fixMu.Unlock()
	defer func() {
		s.fixMu.Lock()
		delete(s.fixBusy, n.ID)
		s.fixMu.Unlock()
	}()

	report := s.nextReport(n.ID) // the agent re-checks after a fix: subscribe before it can happen
	cr, err := s.fl.ApplyFix(ctx, n.ID, fix, false, params)
	if err != nil {
		return nil, err
	}
	audit := map[string]string{"node_id": n.ID, "fix_id": fix, "ok": fmt.Sprint(cr.Ok), "error": store.Clip(cr.Error, 120), "affected": fmt.Sprint(cr.Affected)}
	maps.Copy(audit, params)
	s.audit(ctx, "health.apply_fix", audit)

	resp := &adminv1.ApplyFixResponse{Applied: cr.Ok, Error: store.Clip(cr.Error, 120), Affected: cr.Affected, ResultParams: cr.Params}
	if cr.Ok {
		s.fixMu.Lock()
		s.recentFix[n.ID+"/"+fix] = s.now()
		s.fixMu.Unlock()
		select { // the re-check report, if it comes soon; else the old state is returned
		case <-report:
		case <-time.After(recheckBy):
		case <-ctx.Done():
		}
		s.evaluate(ctx) // alerts that the fix cleared resolve now, as "fix_applied"
		views, err := s.doctorViews(ctx, n.ID)
		if err != nil {
			return nil, s.internal("apply fix", err)
		}
		if len(views) == 1 {
			resp.Doctor = views[0]
		}
	}
	return connect.NewResponse(resp), nil
}
