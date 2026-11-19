//go:build !js

package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	probev1 "github.com/mistgate/mistgate/gen/mistgate/probe/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

// The neighbour scan of a node (ProbeService.NodeSniScan, the MCP tool node_neighbor_sites; agent.proto "NEIGHBOR SITES"): the node
// walks the /24 of its own address on TCP 443 and says which sites there can serve as a REALITY target. It is outgoing traffic to
// addresses that are not the owner's, so it starts only on the owner's action, at most once in sniScanPause per node, one at a time.
//
// The panel does not connect to anything the node found, resolves none of the names and sends them nowhere: it parses the answer
// strictly (what a node says is the text of strangers' certificates), drops its own addresses and names, keeps the rest and shows
// it as text. The audit row carries a count and an error word, never a name.
//
// The code is not in the Cloudflare edition (the ProbeService is not mounted there).

const (
	sniScanPause = 30 * time.Minute
	sniScanWait  = 60 * time.Second // the agent's own budget is 50 s
	sniMaxList   = 20               // candidates kept, as the agent sends at most
	sniPerIP     = 2                // names kept per address, as the agent looks at
	sniMaxJSON   = 64 << 10         // the largest "candidates" value taken from an agent
	sniMaxCount  = 254              // the most addresses a /24 scan can have looked at
)

type sniCandidateJSON struct {
	IP         string `json:"ip"`
	Name       string `json:"name"`
	DNS        string `json:"dns"`
	H2         bool   `json:"h2,omitempty"`
	SingleSite bool   `json:"single_site,omitempty"`
	HTTP       uint32 `json:"http,omitempty"`
	Ms         uint32 `json:"ms,omitempty"`
	Days       uint32 `json:"days,omitempty"`
}

// sniScanJSON is what a scan produced after the parsing and the filter; it is also the stored JSON.
type sniScanJSON struct {
	Range      string             `json:"range"`
	Addresses  uint32             `json:"addresses"`
	Open       uint32             `json:"open"`
	TLS13      uint32             `json:"tls13"`
	MLKEM      uint32             `json:"mlkem"`
	Valid      uint32             `json:"valid"`
	Partial    bool               `json:"partial,omitempty"`
	OwnLeftOut uint32             `json:"own_left_out,omitempty"`
	Candidates []sniCandidateJSON `json:"candidates"`
}

// NodeSniScan reads the last neighbour scan of a node (run = false) or starts a new one.
func (s probeService) NodeSniScan(ctx context.Context, req *connect.Request[probev1.NodeSniScanRequest]) (*connect.Response[probev1.NodeSniScanResponse], error) {
	f := s.f
	node, err := f.probeNode(ctx, req.Msg.NodeId)
	if err != nil {
		return nil, err
	}
	out := &probev1.NodeSniScanResponse{NodeName: node.Name}
	last, lastAt, err := f.lastSniScan(ctx, node.ID)
	if err != nil {
		return nil, internalErr(f.log.Error, "read neighbour scan", err)
	}
	if last != nil {
		fillSniScan(out, last, lastAt)
	}
	if !req.Msg.Run {
		return connect.NewResponse(out), nil
	}

	// refusals that cost nothing: no limit is used up and nothing is audited
	liveRows, err := f.Live(ctx)
	if err != nil {
		return nil, internalErr(f.log.Error, "read live nodes for neighbour scan", err)
	}
	nodeLive, code := f.probeLive(liveRowsByID(liveRows), node.ID, capSniScan)
	if code != "" {
		out.Error = code
		return connect.NewResponse(out), nil
	}
	if !f.takeSniScan(node.ID) {
		out.Error = "busy"
		return connect.NewResponse(out), nil
	}
	defer f.dropSniScan(node.ID)
	if wait := f.sniScanWait(lastAt, node.ID); wait > 0 {
		out.Error, out.RetryAfterS = "too_soon", uint32((wait+time.Second-1)/time.Second)
		return connect.NewResponse(out), nil
	}
	release, err := f.startProbe(ctx, node.ID, node.ID)
	if err != nil {
		return nil, err
	}
	defer release()

	res, code := f.runSniScan(ctx, node, nodeLive)
	if code == "" {
		if b, err := json.Marshal(res); err == nil {
			at := f.now().UTC()
			if err := f.st.PutNodeSniScan(ctx, node.ID, at, string(b)); err != nil {
				f.log.Warn("store neighbour scan", "code", "store_failed", "err", err)
			}
			last, lastAt = res, at
		}
	}
	out.Error = code
	if last != nil {
		fillSniScan(out, last, lastAt)
	}
	found := 0
	if code == "" {
		found = len(res.Candidates)
	}
	f.audit(context.WithoutCancel(ctx), "node.sni_scan", map[string]string{"node": node.Name, "found": strconv.Itoa(found), "error": code})
	return connect.NewResponse(out), nil
}

// lastSniScan is the node's last finished scan; nil when there is none or the stored JSON cannot be read.
func (f *Fleet) lastSniScan(ctx context.Context, nodeID string) (*sniScanJSON, time.Time, error) {
	row, ok, err := f.st.NodeSniScan(ctx, nodeID)
	if err != nil || !ok {
		return nil, time.Time{}, err
	}
	var last sniScanJSON
	if json.Unmarshal([]byte(row.ResultJSON), &last) != nil {
		return nil, row.At, nil // the time still holds the pause
	}
	return &last, row.At, nil
}

func fillSniScan(out *probev1.NodeSniScanResponse, r *sniScanJSON, at time.Time) {
	out.Candidates = nil
	out.AtUnix, out.Range = at.Unix(), r.Range
	out.Addresses, out.Open, out.Tls13, out.Mlkem, out.Valid = r.Addresses, r.Open, r.TLS13, r.MLKEM, r.Valid
	out.Partial, out.OwnLeftOut = r.Partial, r.OwnLeftOut
	for _, c := range r.Candidates {
		out.Candidates = append(out.Candidates, &probev1.SniCandidate{Ip: c.IP, Name: c.Name, Dns: c.DNS, H2: c.H2,
			SingleSite: c.SingleSite, Http: c.HTTP, Ms: c.Ms, Days: c.Days})
	}
}

// sniScanWait is how long until the node may be scanned again: the pause counts from the last finished scan (kept in the database,
// so it holds across a restart) or from the last start that reached the agent, whichever is later.
func (f *Fleet) sniScanWait(storedAt time.Time, nodeID string) time.Duration {
	p := &f.probes
	p.mu.Lock()
	started := p.sniStart[nodeID]
	p.mu.Unlock()
	if storedAt.After(started) {
		started = storedAt
	}
	if started.IsZero() {
		return 0
	}
	return sniScanPause - f.now().Sub(started)
}

// takeSniScan reserves the node for a scan; false when one runs already.
func (f *Fleet) takeSniScan(nodeID string) bool {
	p := &f.probes
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sniBusy == nil {
		p.sniBusy = map[string]struct{}{}
	}
	if p.sniStart == nil {
		p.sniStart = map[string]time.Time{}
	}
	if _, running := p.sniBusy[nodeID]; running {
		return false
	}
	p.sniBusy[nodeID] = struct{}{}
	return true
}

func (f *Fleet) dropSniScan(nodeID string) {
	p := &f.probes
	p.mu.Lock()
	delete(p.sniBusy, nodeID)
	p.mu.Unlock()
}

// runSniScan sends the command and reads the answer; the code is a short error word, empty when the scan ran.
func (f *Fleet) runSniScan(ctx context.Context, node store.NodeRow, live store.NodeLiveRow) (*sniScanJSON, string) {
	addr, ok := f.sniNodeAddr(ctx, node)
	if !ok {
		return nil, "no_ipv4"
	}
	if !addr.IsGlobalUnicast() || addr.IsPrivate() {
		return nil, "not_my_address" // a private address is a NAT: the agent would refuse it too
	}
	prefix := netip.PrefixFrom(addr, 24).Masked()

	p := &f.probes
	p.mu.Lock()
	p.sniStart[node.ID] = f.now() // the pause counts from here, whatever the scan ends in
	p.mu.Unlock()

	// a closed tab does not end the scan: the node walks on, and its answer is kept for the next look
	result, err := f.commandWithLive(context.WithoutCancel(ctx), node.ID, sniScanWait, func(reqID string) *agentv1.ConnectResponse {
		return &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_SniScan{SniScan: &agentv1.SniScan{RequestId: reqID, Address: addr.String()}}}
	}, &live)
	switch {
	case errors.Is(err, errNodeOffline):
		return nil, "node_offline"
	case err != nil:
		return nil, "failed"
	case result == nil || !result.Ok:
		return nil, sniScanError(result)
	}
	res, ok := parseSniScan(result.Params, prefix)
	if !ok {
		return nil, "failed"
	}
	own, err := f.ownSniNames(ctx)
	if err != nil {
		f.log.Warn("list own names for neighbour scan", "code", "store_failed", "err", err)
		return nil, "failed"
	}
	own.filter(res)
	return res, ""
}

// sniNodeAddr is the node's public IPv4 as the panel knows it: its address, or for a name what the panel resolves it to.
func (f *Fleet) sniNodeAddr(ctx context.Context, node store.NodeRow) (netip.Addr, bool) {
	if a, err := netip.ParseAddr(strings.Trim(node.Address, "[]")); err == nil {
		a = a.Unmap()
		return a, a.Is4()
	}
	if resolver, ok := f.hooks().(networkNodeIPv4); ok {
		if a, ok := resolver.NodeIPv4(ctx, node); ok {
			a = a.Unmap()
			return a, a.Is4()
		}
	}
	return netip.Addr{}, false
}

// sniScanError words the agent's refusal. Anything but the three it is meant to say is a plain failure.
func sniScanError(result *agentv1.CommandResult) string {
	if result == nil {
		return "failed"
	}
	switch code := strings.SplitN(result.Error, ":", 2)[0]; code {
	case "busy", "not_my_address", "no_roots":
		return code
	}
	return "failed"
}

// ---------------------------------------------------------------------------------------------------------------------
// the strict reading of an agent's answer

// sniHost is a plain host name: lower-case labels of letters, digits and hyphens, at least two, the last one starting with a letter.
var sniHost = regexp.MustCompile(`^(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

func validSniHost(s string) bool { return len(s) <= 253 && sniHost.MatchString(s) }

func sniCount(params map[string]string, key string) uint32 {
	n, err := strconv.ParseUint(params[key], 10, 32)
	if err != nil {
		return 0
	}
	return uint32(min(n, sniMaxCount))
}

// parseSniScan reads the params of a SniScan answer: counts held in their bounds and in order (each is a subset of the one before),
// and at most sniMaxList candidates whose address lies in the scanned /24 and whose name is a plain host name, every other field
// held to its vocabulary or range; whatever else the JSON holds is dropped. ok is false for an answer that is not JSON at all.
func parseSniScan(params map[string]string, prefix netip.Prefix) (*sniScanJSON, bool) {
	raw := params["candidates"]
	if len(raw) > sniMaxJSON {
		return nil, false
	}
	var got []struct {
		IP         string `json:"ip"`
		Name       string `json:"name"`
		DNS        string `json:"dns"`
		H2         bool   `json:"h2"`
		SingleSite bool   `json:"single_site"`
		HTTP       int    `json:"http"`
		Ms         int    `json:"ms"`
		Days       int    `json:"days"`
	}
	if raw != "" && json.Unmarshal([]byte(raw), &got) != nil {
		return nil, false
	}
	res := &sniScanJSON{Range: prefix.String(), Partial: params["partial"] == "1", Candidates: []sniCandidateJSON{}}
	res.Addresses = sniCount(params, "addresses")
	res.Open = min(sniCount(params, "open"), res.Addresses)
	res.TLS13 = min(sniCount(params, "tls13"), res.Open)
	res.MLKEM = min(sniCount(params, "mlkem"), res.TLS13)
	res.Valid = min(sniCount(params, "valid"), res.MLKEM)

	seen := map[[2]string]bool{}
	perIP := map[netip.Addr]int{}
	for _, c := range got {
		ip, err := netip.ParseAddr(c.IP)
		if err != nil || !ip.Is4() || !prefix.Contains(ip) {
			continue
		}
		if !validSniHost(c.Name) || seen[[2]string{ip.String(), c.Name}] || perIP[ip] >= sniPerIP {
			continue
		}
		switch c.DNS {
		case "here", "subnet", "elsewhere", "none":
		default:
			continue
		}
		if len(res.Candidates) == sniMaxList {
			break
		}
		seen[[2]string{ip.String(), c.Name}] = true
		perIP[ip]++
		cand := sniCandidateJSON{IP: ip.String(), Name: c.Name, DNS: c.DNS, H2: c.H2, SingleSite: c.SingleSite}
		if c.HTTP >= 100 && c.HTTP <= 599 {
			cand.HTTP = uint32(c.HTTP)
		}
		if c.Ms > 0 {
			cand.Ms = uint32(min(c.Ms, 60_000))
		}
		if c.Days > 0 {
			cand.Days = uint32(min(c.Days, 36_500))
		}
		res.Candidates = append(res.Candidates, cand)
	}
	return res, true
}

// ---------------------------------------------------------------------------------------------------------------------
// the panel's own addresses and names

// ownSni is what a neighbour must not be: an address or a name of one of the panel's nodes, of their inbounds or of the panel itself.
type ownSni struct {
	ips   map[netip.Addr]bool
	names map[string]bool
}

func (o *ownSni) add(host string) {
	host = strings.ToLower(strings.TrimSuffix(strings.Trim(strings.TrimSpace(host), "[]"), "."))
	if host == "" {
		return
	}
	if a, err := netip.ParseAddr(host); err == nil {
		o.ips[a.Unmap()] = true
		return
	}
	o.names[host] = true
}

// addURL adds the host of a URL or of a host:port.
func (o *ownSni) addURL(raw string) {
	if raw == "" {
		return
	}
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		o.add(u.Hostname())
		return
	}
	if host, _, err := net.SplitHostPort(raw); err == nil {
		o.add(host)
		return
	}
	o.add(raw)
}

func (f *Fleet) ownSniNames(ctx context.Context) (*ownSni, error) {
	own := &ownSni{ips: map[netip.Addr]bool{}, names: map[string]bool{}}
	nodes, err := f.st.Nodes(ctx, false)
	if err != nil {
		return nil, err
	}
	for _, n := range nodes {
		own.add(n.Address)
		if a, ok := f.sniNodeAddr(ctx, n); ok {
			own.ips[a] = true
		}
		specs, _, err := f.tcpSpecsOf(ctx, n)
		if err != nil {
			return nil, err
		}
		for _, s := range specs {
			own.add(s.Spec.TLS.ServerName)
			v, ok := plugin.VLESSOf(s.Spec)
			if !ok {
				continue
			}
			for _, name := range v.ServerNames {
				own.add(name)
			}
			own.add(v.TargetHost())
		}
	}
	own.addURL(f.cfg.PublicURL)
	own.addURL(f.cfg.PanelAddr)
	own.addURL(f.cfg.LinkURL)
	return own, nil
}

// filter drops the candidates that are the panel's own and counts them.
func (o *ownSni) filter(r *sniScanJSON) {
	kept := r.Candidates[:0]
	for _, c := range r.Candidates {
		if ip, err := netip.ParseAddr(c.IP); err == nil && o.ips[ip] || o.names[c.Name] {
			r.OwnLeftOut++
			continue
		}
		kept = append(kept, c)
	}
	r.Candidates = kept
}
