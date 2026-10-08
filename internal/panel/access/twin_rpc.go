package access

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/protocols/awg"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

// The same server with and without WARP (docs: two profiles of one protocol on different ports, one of them with
// egress "warp"; the client lists both entries). TwinProfile makes the second one in a single step.

// twinSuffix marks the WARP twin in its name; the default subscription template includes profile names, so "files" and
// "files · WARP" read apart in the client.
const twinSuffix = " · WARP"

// twinPreferred are the UDP ports a Hysteria2 twin tries first: another "web-looking" port, since 443 stays with the
// first profile. AmneziaWG takes a random one in 10000-60000, like a new profile does.
var twinPreferred = []uint16{8443, 4443, 2053, 2083, 2087, 2096}

// twinName is the twin's name: the profile's name with the WARP mark added (egress "warp") or taken off, and a number
// after the stem when that name is taken (the 64-byte limit of cleanName holds).
func twinName(name, egress string, taken map[string]bool) string {
	base, mark := strings.TrimSuffix(name, twinSuffix), ""
	if egress == "warp" {
		mark = twinSuffix
	}
	for n := 1; ; n++ {
		stem := base
		if n > 1 {
			stem = fmt.Sprintf("%s %d", base, n)
		}
		for len(stem)+len(mark) > 64 {
			_, size := utf8.DecodeLastRuneInString(stem)
			stem = stem[:len(stem)-size]
		}
		if c := stem + mark; !taken[c] {
			return c
		}
	}
}

// egressOf reads the exit out of a settings document ("direct" when it names none).
func egressOf(settings json.RawMessage) string {
	var v struct {
		Egress string `json:"egress"`
	}
	_ = json.Unmarshal(settings, &v)
	if v.Egress == "warp" {
		return "warp"
	}
	return "direct"
}

// twinSettings is the settings document the twin is created from: the profile's own with the exit, the port and no
// hopping, secrets masked (so that CreateProfile makes new ones), and, for AmneziaWG, neither client network (a free one is
// chosen) nor the signature seed (the per-device chains of the twin must not repeat the profile's). hopDropped: the
// profile had a hop range.
func twinSettings(id string, merged json.RawMessage, secretPtrs []string, egress string, port uint16) (doc json.RawMessage, hopDropped bool, err error) {
	masked, err := protocols.MaskSecrets(merged, secretPtrs)
	if err != nil {
		return nil, false, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(masked, &m); err != nil {
		return nil, false, err
	}
	m["egress"], _ = json.Marshal(egress)
	m["port"], _ = json.Marshal(port)
	if raw, ok := m["hop"]; ok {
		from, to := hopOf(raw)
		hopDropped = from != 0 || to != 0
		m["hop"] = json.RawMessage(`{"from":0,"to":0}`)
	}
	if id == awg.ID {
		delete(m, "subnet4")
		delete(m, "subnet6")
		var ob map[string]json.RawMessage
		if json.Unmarshal(m["obfuscation"], &ob) == nil {
			delete(ob, "signature_seed")
			m["obfuscation"], _ = json.Marshal(ob)
		}
	}
	doc, err = json.Marshal(m)
	return doc, hopDropped, err
}

func hopOf(raw json.RawMessage) (from, to int) {
	var h struct {
		From int `json:"from"`
		To   int `json:"to"`
	}
	_ = json.Unmarshal(raw, &h)
	return h.From, h.To
}

// freePorts returns up to n accepted candidates in the protocol's preferred order, then up to 64 random ports.
func freePorts(protocol string, n int, ok func(uint16) bool) []uint16 {
	if n <= 0 {
		return nil
	}
	var cands []uint16
	if protocol != awg.ID {
		cands = append(cands, twinPreferred...)
	}
	for range 64 {
		p, err := awg.RandomPort()
		if err != nil {
			break // no randomness: the preferred ports are all there is to offer
		}
		cands = append(cands, uint16(p))
	}
	seen := make(map[uint16]bool, len(cands))
	var out []uint16
	for _, p := range cands {
		if p != 0 && !seen[p] && ok(p) {
			out = append(out, p)
			seen[p] = true
			if len(out) == n {
				break
			}
		}
	}
	return out
}

type twinPortNodeChecks struct {
	checks map[uint16]store.PortCheck
	reason string
}

// twinPort chooses a port that is free on every node and has no bad delivery result on any of them.
func (s *Service) twinPort(ctx context.Context, protocol string, nodes []store.AccessNode, want uint32) (uint16, []*adminv1.PortCheck, error) {
	if want > 65535 {
		return 0, nil, invalid("port must be 1-65535")
	}
	listens := make([][]nodeListen, len(nodes))
	nodeIDs := make([]string, len(nodes))
	for i, n := range nodes {
		nodeIDs[i] = n.ID
		var err error
		if listens[i], _, err = s.nodeInbounds(ctx, n.ID, ""); err != nil {
			return 0, nil, err
		}
	}
	cache, err := s.readPortChecks(ctx, nodeIDs...)
	if err != nil {
		return 0, nil, s.internal("read UDP port checks", err)
	}
	now := s.now()
	// takenOn says why a port is taken on one of the nodes ("" = free on all of them)
	takenOn := func(port uint16) string {
		for i, n := range nodes {
			if c := clashOf(plugin.Listen{Network: "udp", Port: port}, listens[i]); c != nil {
				return fmt.Sprintf("UDP port %d is already used by profile %q on node %s", port, c.profile, n.Name)
			}
		}
		return ""
	}
	if want != 0 {
		if why := takenOn(uint16(want)); why != "" {
			return 0, nil, connect.NewError(connect.CodeAlreadyExists, errors.New(why))
		}
		for _, n := range nodes {
			if bad, ok := cachedPortLossy(cache[n.ID], uint16(want), now); ok {
				free := s.twinFreePort(protocol, nodes, listens, cache, now)
				return 0, nil, portLossyRefusal(ctx, s, n.Name, bad, free)
			}
		}
		messages, err := s.twinPortMessages(ctx, nodes, uint16(want), nil, cache, nil)
		return uint16(want), messages, err
	}

	candidates := freePorts(protocol, len(twinPreferred)+64, func(p uint16) bool { return takenOn(p) == "" })
	var skipped []store.PortCheck
	var clean []uint16
	for _, candidate := range candidates {
		bad := false
		for _, n := range nodes {
			if row, ok := cachedPortLossy(cache[n.ID], candidate, now); ok {
				skipped = append(skipped, row)
				bad = true
			}
		}
		if !bad {
			clean = append(clean, candidate)
		}
	}
	slices.SortStableFunc(clean, func(a, b uint16) int {
		fresh := func(port uint16) int {
			count := 0
			for _, n := range nodes {
				if check, ok := cache[n.ID][port]; ok && check.Fresh(now) {
					count++
				}
			}
			return count
		}
		fa, fb := fresh(a), fresh(b)
		if fa > fb {
			return -1
		}
		if fa < fb {
			return 1
		}
		return 0
	})
	if len(clean) > 8 {
		clean = clean[:8]
	}

	for start := 0; start < len(clean); start += 4 {
		end := min(start+4, len(clean))
		batch := clean[start:end]
		results := s.checkTwinBatch(ctx, nodes, batch, cache)
		for _, candidate := range batch {
			bad := false
			for i := range nodes {
				row, ok := results[i].checks[candidate]
				if ok && results[i].reason != "same_host" && badPortVerdict(row.Verdict) {
					skipped = append(skipped, row)
					bad = true
				}
			}
			if bad {
				continue
			}
			messages, err := s.twinPortMessages(ctx, nodes, candidate, results, cache, skipped)
			return candidate, messages, err
		}
	}
	return 0, nil, coded(connect.CodeFailedPrecondition, "no_clean_port")
}

func (s *Service) twinFreePort(protocol string, nodes []store.AccessNode, listens [][]nodeListen, cache portCheckCache, now time.Time) uint16 {
	takenOn := func(port uint16) bool {
		for i := range nodes {
			if clashOf(plugin.Listen{Network: "udp", Port: port}, listens[i]) != nil {
				return true
			}
			if check, ok := cachedPortLossy(cache[nodes[i].ID], port, now); ok {
				_ = check
				return true
			}
		}
		return false
	}
	ports := freePorts(protocol, len(twinPreferred)+64, func(port uint16) bool { return !takenOn(port) })
	best, bestFresh := uint16(0), -1
	for _, port := range ports {
		fresh := 0
		for _, node := range nodes {
			if check, ok := cache[node.ID][port]; ok && check.Fresh(now) {
				fresh++
			}
		}
		if fresh > bestFresh {
			best, bestFresh = port, fresh
		}
	}
	return best
}

func (s *Service) checkTwinBatch(ctx context.Context, nodes []store.AccessNode, ports []uint16,
	cache portCheckCache) []twinPortNodeChecks {
	results := make([]twinPortNodeChecks, len(nodes))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for i, node := range nodes {
		allFresh := len(ports) > 0
		for _, port := range ports {
			check, ok := cache[node.ID][port]
			if !ok || !check.Fresh(s.now()) || check.Bad(s.now()) {
				allFresh = false
				break
			}
		}
		if allFresh {
			results[i].checks = make(map[uint16]store.PortCheck, len(ports))
			for _, port := range ports {
				results[i].checks[port] = cache[node.ID][port]
			}
			continue
		}
		if s.cfg.CheckPorts == nil {
			results[i].reason = "inconclusive"
			continue
		}
		wg.Add(1)
		go func(i int, node store.AccessNode) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				results[i].reason = "failed"
				return
			}
			defer func() { <-sem }()
			runCtx, cancel := context.WithTimeout(ctx, portCheckTimeout)
			rows, sender, reason := s.cfg.CheckPorts(runCtx, node.ID, ports)
			cancel()
			results[i].checks = make(map[uint16]store.PortCheck, len(rows))
			results[i].reason = reason
			for _, row := range rows {
				if row.NodeID == "" {
					row.NodeID = node.ID
				}
				if row.Sender == "" {
					row.Sender = sender
				}
				results[i].checks[row.Port] = row
			}
		}(i, node)
	}
	wg.Wait()
	return results
}

func (s *Service) twinPortMessages(ctx context.Context, nodes []store.AccessNode, chosen uint16,
	results []twinPortNodeChecks, cache portCheckCache, skipped []store.PortCheck) ([]*adminv1.PortCheck, error) {
	out := make([]*adminv1.PortCheck, 0, len(nodes)+len(skipped))
	for i, node := range nodes {
		row, ok := store.PortCheck{}, false
		reason := ""
		if i < len(results) {
			row, ok = results[i].checks[chosen]
			reason = results[i].reason
		}
		if !ok {
			row, ok = cache[node.ID][chosen]
		}
		if !ok {
			row = store.PortCheck{NodeID: node.ID, Port: chosen}
			if reason == "" {
				reason = "inconclusive"
			}
		}
		if row.NodeID == "" {
			row.NodeID = node.ID
		}
		if row.Port == 0 {
			row.Port = chosen
		}
		if reason == "same_host" {
			row.Verdict = ""
		}
		out = append(out, s.portCheckMessage(ctx, row, reason))
	}
	seen := make(map[string]bool)
	for _, row := range skipped {
		key := row.NodeID + "/" + strconv.Itoa(int(row.Port))
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, s.portCheckMessage(ctx, row, ""))
	}
	return out, nil
}

func (s *Service) TwinProfile(ctx context.Context, req *connect.Request[adminv1.TwinProfileRequest]) (*connect.Response[adminv1.TwinProfileResponse], error) {
	m := req.Msg
	a := s.st.Access()
	p, err := a.Profile(ctx, m.ProfileId)
	if errors.Is(err, store.ErrNotFound) {
		return nil, notFound("profile")
	} else if err != nil {
		return nil, s.internal("get profile", err)
	}
	proto, err := s.protocol(p.Protocol)
	if err != nil {
		return nil, s.internal("profile protocol", err)
	}
	merged, err := s.mergedSettings(p)
	if err != nil {
		return nil, s.internal("open profile secrets", err)
	}
	def, err := proto.DefaultSettings()
	if err != nil {
		return nil, s.internal("default settings", err)
	}
	var keys map[string]json.RawMessage
	_ = json.Unmarshal(def, &keys)
	if _, ok := keys["egress"]; !ok {
		return nil, failed("%s has no exit setting", proto.DisplayName())
	}
	cur, to := egressOf(merged), m.Egress
	switch to {
	case "":
		to = map[string]string{"direct": "warp", "warp": "direct"}[cur]
	case "direct", "warp":
	default:
		return nil, invalid("egress must be \"direct\" or \"warp\"")
	}
	if to == cur {
		return nil, invalid("the profile already exits %s", to)
	}

	// where the profile is: the nodes of its enabled inbounds (a switched-off one is on purpose), its groups
	full, err := a.InboundsFull(ctx, "")
	if err != nil {
		return nil, s.internal("inbounds", err)
	}
	var nodes []store.AccessNode
	sni := map[string]string{}
	resp := &adminv1.TwinProfileResponse{Egress: to}
	for _, f := range full {
		if f.Profile.ID == p.ID && f.Inbound.Enabled && !slices.Contains(m.SkipNodeIds, f.Node.ID) {
			nodes = append(nodes, f.Node)
			sni[f.Node.ID] = f.Inbound.TLSServerNameOverride
			resp.NodeIds = append(resp.NodeIds, f.Node.ID)
		}
	}
	groups, err := a.Groups(ctx)
	if err != nil {
		return nil, s.internal("list groups", err)
	}
	groups = slices.DeleteFunc(groups, func(g store.AccessGroup) bool { return !slices.Contains(g.ProfileIDs, p.ID) })
	for _, g := range groups {
		resp.GroupIds = append(resp.GroupIds, g.ID)
	}
	all, err := a.Profiles(ctx)
	if err != nil {
		return nil, s.internal("list profiles", err)
	}
	taken := make(map[string]bool, len(all))
	for _, o := range all {
		taken[o.Name] = true
	}

	port, portChecks, err := s.twinPort(ctx, p.Protocol, nodes, m.Port)
	if err != nil {
		return nil, err
	}
	input, hopDropped, err := twinSettings(p.Protocol, merged, s.secretPtrs[p.Protocol], to, port)
	if err != nil {
		return nil, s.internal("twin settings", err)
	}
	resp.Name, resp.Port, resp.HopDropped, resp.PortChecks = twinName(p.Name, to, taken), uint32(port), hopDropped, portChecks
	if _, errs, err := s.resolveSettings(proto, string(input), nil, false); err != nil {
		return nil, err
	} else if len(errs) > 0 {
		return nil, fieldErrors(errs)
	}
	if m.DryRun {
		return connect.NewResponse(resp), nil
	}

	if resp.Profile, err = s.makeTwin(ctx, p, resp.Name, input, nodes, sni, groups); err != nil {
		return nil, err
	}
	return connect.NewResponse(resp), nil
}

// makeTwin creates the profile, puts it on the nodes and into the groups, through the public calls (so that every check
// and every event of those calls happens). A failing step takes back what was made: deleting the profile also takes it
// out of the groups.
func (s *Service) makeTwin(ctx context.Context, src store.AccessProfile, name string, settings json.RawMessage, nodes []store.AccessNode, sni map[string]string, groups []store.AccessGroup) (*adminv1.ProfileSummary, error) {
	created, err := s.CreateProfile(ctx, connect.NewRequest(&adminv1.CreateProfileRequest{Protocol: src.Protocol, Name: name, SettingsJson: string(settings)}))
	if err != nil {
		return nil, err
	}
	id := created.Msg.Profile.Id
	var inbounds []string
	undo := func(cause error) error {
		bg := context.WithoutCancel(ctx)
		for _, in := range inbounds {
			if _, err := s.DeleteInbound(bg, connect.NewRequest(&adminv1.DeleteInboundRequest{InboundId: in})); err != nil {
				s.log.Warn("access: twin rollback: inbound not removed", "inbound", in, "err", err)
			}
		}
		if _, err := s.DeleteProfile(bg, connect.NewRequest(&adminv1.DeleteProfileRequest{ProfileId: id})); err != nil {
			s.log.Warn("access: twin rollback: profile not removed", "profile", id, "err", err)
		}
		return cause
	}
	for _, n := range nodes {
		r, err := s.CreateInbound(ctx, connect.NewRequest(&adminv1.CreateInboundRequest{ProfileId: id, NodeId: n.ID, TlsServerNameOverride: sni[n.ID]}))
		if err != nil {
			return nil, undo(err)
		}
		inbounds = append(inbounds, r.Msg.Inbound.Id)
	}
	for _, g := range groups {
		// The set is read before the loop, so an edit of the same group in the meantime is overwritten
		ids := append(slices.Clone(g.ProfileIDs), id)
		if _, err := s.UpdateGroup(ctx, connect.NewRequest(&adminv1.UpdateGroupRequest{GroupId: g.ID, ProfileIds: &adminv1.ProfileIds{Values: ids}})); err != nil {
			return nil, undo(err)
		}
	}
	p, err := s.st.Access().Profile(ctx, id)
	if err != nil {
		return nil, s.internal("get twin", err)
	}
	in, err := s.st.Access().InboundsOfProfile(ctx, id)
	if err != nil {
		return nil, s.internal("twin inbounds", err)
	}
	return s.profileSummary(ctx, p, in)
}
