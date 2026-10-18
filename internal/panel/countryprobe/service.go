// Package countryprobe is the third source of the country check ("is the node reachable from Russia"): one of the owner's own
// nodes inside the country, the probe node, tests with TCP SYNs of a limited TTL whether the connections to the other nodes of
// the fleet survive the national DPI box (the TSPU). The agent side is internal/tspuprobe (agent.proto "COUNTRY PROBE"); this
// package is the panel side: the settings and their admin API, the schedule, the batches and the storage of the answers.
//
// The probe node sits in a data centre, so what it reports is a datacentre view: its verdicts carry the confidence "dc_only"
// (countryreach.Combine) and Globalping on home networks outranks them. Only TCP is tested.
//
// It is an integration of its own and off by default. Off: no command is sent and the results are not shown. Disconnect: also
// deletes the stored rows and the settings. Only Russia has the references the method needs; with another country selected it
// does nothing. It is not built for the Cloudflare edition: a run takes minutes, the scheduled jobs of that edition have seconds.
package countryprobe

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/countryreach"
	"github.com/mistgate/mistgate/internal/panel/edition"
	"github.com/mistgate/mistgate/internal/panel/fleet"
	"github.com/mistgate/mistgate/internal/panel/store"
)

const (
	settingEnabled  = "tspu.enabled"      // "1" or "0"; absent = off
	settingNode     = "tspu.probe_node"   // the probe node's id
	settingInterval = "tspu.interval_min" // minutes
	settingLastRun  = "tspu.last_run"     // JSON of lastRun

	// Capability is what the probe node's agent lists when it can run the probe.
	Capability = "country-probe/1"

	DefaultIntervalMin = 120
	MinIntervalMin     = 30
	MaxIntervalMin     = 1440

	// batchPairs is the host:port pairs of one command. The agent takes 16; a batch with blocked pairs can take up to about two
	// minutes (each blocked pair is checked again alone), so 8 is the comfortable size.
	batchPairs = 8
	// maxPorts is the agent's limit per host.
	maxPorts = 4

	// busyRetry: a probe node that was busy is asked again after this, not after the whole interval.
	busyRetry = 5 * time.Minute
	testEvery = 60 * time.Second
	// runBudget bounds a whole run. ponytail: a fleet so big that its batches do not fit loses the batches at the end every time;
	// rotate the starting batch if a fleet ever gets that big.
	runBudget = 30 * time.Minute
)

// The status words of a run (TspuProbeRun.status).
const (
	StatusOK            = "ok"
	StatusControlFailed = "control_failed"
	StatusNoDPIHop      = "no_dpi_hop"
	StatusBusy          = "busy"
	StatusFailed        = "failed"
)

// The reasons of a no_dpi_hop run (TspuProbeRun.reason).
const (
	// ReasonRefsPass: a blocked reference answered, so blocked addresses get through on the probe node's path.
	ReasonRefsPass = "refs_pass"
	// ReasonNoHops: no router answered for any reference, so the route is hidden.
	ReasonNoHops = "no_hops"
)

// Fleet is the part of the fleet the probe uses.
type Fleet interface {
	CountryProbe(ctx context.Context, nodeID string, req fleet.CountryProbeRequest) (fleet.CountryProbeResult, error)
}

// Node is an active node as the probe sees it: whom it can probe from, and what to test.
type Node struct {
	ID, Name, Address, CountryCode string
	Caps                           []string // the capabilities its agent listed in its last Hello
	Online                         bool
	Ports                          []uint16 // the TCP ports of its enabled inbounds
}

func (n Node) declares() bool { return slices.Contains(n.Caps, Capability) }

// Config configures Service. Store, StepUp, Fleet and Nodes are required.
type Config struct {
	Store *store.Store
	// StepUp is auth.Service.RequireStepUp: changing the settings asks for it.
	StepUp func(context.Context) error
	Fleet  Fleet
	// Nodes lists the active nodes (health.Service.ProbeNodes).
	Nodes func(context.Context) ([]Node, error)
	// Country is the shared country of the country check (countryreach.Service.Country). Default RU.
	Country func(context.Context) string
	// OnChange is called after the settings changed, the source was disconnected or a run stored its rows, so that the alerts that
	// depend on it are brought up to date at once (health.Service.ReconcileCountry). May be nil.
	OnChange func(context.Context)
	// Edge is the Cloudflare edition: the source is not built for it, Get says so and nothing runs.
	Edge bool
	Log  *slog.Logger
	Now  func() time.Time
}

// Service is the integration.
type Service struct {
	cfg Config
	log *slog.Logger
	now func() time.Time

	runMu sync.Mutex // one run at a time

	mu       sync.Mutex
	gen      uint64 // bumped when the integration is switched off or disconnected; a run of an older one stores nothing
	inflight map[uint64]context.CancelFunc
	next     uint64
	lastTest time.Time
}

// New builds the service.
func New(cfg Config) (*Service, error) {
	if cfg.Store == nil || cfg.StepUp == nil || cfg.Fleet == nil || cfg.Nodes == nil {
		return nil, errors.New("countryprobe: Store, StepUp, Fleet and Nodes are required")
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Service{cfg: cfg, log: cfg.Log, now: cfg.Now, inflight: map[uint64]context.CancelFunc{}}, nil
}

// Handler is the admin TspuProbeService.
func (s *Service) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	return adminv1connect.NewTspuProbeServiceHandler(rpc{s}, append([]connect.HandlerOption{connect.WithReadMaxBytes(4 << 10)}, opts...)...)
}

// ---------------------------------------------------------------------------------------------------
// Settings

// lastRun is the status of the newest run, kept in a setting (JSON) so that it is the same on every process and survives a
// restart. At is unix seconds.
type lastRun struct {
	Status  string `json:"status"`
	At      int64  `json:"at"`
	Node    string `json:"node"`
	DPIHop  int    `json:"dpi_hop,omitempty"`
	Ref     string `json:"ref,omitempty"`
	Checked int    `json:"checked,omitempty"`
	Detail  string `json:"detail,omitempty"`
	// Reason is why a no_dpi_hop could not place the TSPU (RunRefsPass, RunNoHops); "" when unknown.
	Reason string `json:"reason,omitempty"`
}

type settings struct {
	enabled  bool
	node     string
	interval time.Duration
	last     lastRun
}

func (s *Service) load(ctx context.Context) (settings, error) {
	kv, err := s.cfg.Store.SettingValues(ctx, []string{settingEnabled, settingNode, settingInterval, settingLastRun})
	if err != nil {
		return settings{}, err
	}
	st := settings{enabled: kv[settingEnabled] == "1", node: kv[settingNode], interval: DefaultIntervalMin * time.Minute}
	if n, err := strconv.Atoi(kv[settingInterval]); err == nil && n >= MinIntervalMin && n <= MaxIntervalMin {
		st.interval = time.Duration(n) * time.Minute
	}
	if v := kv[settingLastRun]; v != "" {
		_ = json.Unmarshal([]byte(v), &st.last)
	}
	return st, nil
}

func (s *Service) country(ctx context.Context) string {
	if s.cfg.Country == nil {
		return countryreach.TSPUCountry
	}
	return s.cfg.Country(ctx)
}

// unavailable says why the source cannot work: "edition", "country", or "" when it can.
func (s *Service) unavailable(ctx context.Context) string {
	switch {
	case s.cfg.Edge:
		return "edition"
	case !strings.EqualFold(s.country(ctx), countryreach.TSPUCountry):
		return "country"
	}
	return ""
}

// State is what the health module needs: whether the results count, from which probe node, how old a row may be, and the newest
// run's status (nil when the source is off or has not run for this probe node).
type State struct {
	On      bool
	Vantage string
	MaxAge  time.Duration
	Run     *adminv1.TspuProbeRun
}

// State reads the settings; an off source (the default, or any trouble reading them) costs one read.
func (s *Service) State(ctx context.Context) State {
	if s.cfg.Edge { // not built for the edge: not even a read of the settings
		return State{}
	}
	st, err := s.load(ctx)
	if err != nil {
		s.log.Warn("countryprobe: read settings", "err", err)
		return State{}
	}
	if !st.enabled || st.node == "" || s.unavailable(ctx) != "" {
		return State{}
	}
	// a row stays good through two missed runs
	out := State{On: true, Vantage: st.node, MaxAge: 3 * st.interval}
	if st.last.At > 0 && st.last.Node == st.node {
		out.Run = runMsg(st.last)
	}
	return out
}

// ---------------------------------------------------------------------------------------------------
// The run

var errSwitchedOff = errors.New("countryprobe: switched off")

// generation is the version of the settings; a run takes it before it reads them.
func (s *Service) generation() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.gen
}

// track registers a run in flight so that switching the source off can cancel it. A run whose settings were read under an
// older generation (the source was switched off or changed meanwhile) is not admitted.
func (s *Service) track(ctx context.Context, gen uint64) (context.Context, func(), bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if gen != s.gen {
		return nil, nil, false
	}
	ctx, cancel := context.WithTimeout(ctx, runBudget)
	id := s.next
	s.next++
	s.inflight[id] = cancel
	return ctx, func() {
		s.mu.Lock()
		delete(s.inflight, id)
		s.mu.Unlock()
		cancel()
	}, true
}

// stop cancels every run in flight and keeps a run that started earlier from storing anything.
func (s *Service) stop() {
	s.mu.Lock()
	s.gen++
	for _, cancel := range s.inflight {
		cancel()
	}
	s.mu.Unlock()
}

// nodeResult is what the probe saw of one node.
type nodeResult struct {
	node    Node
	ports   []countryreach.TSPUPort
	verdict string // blocked | open | partial | unknown
}

// outcome is a finished run: its status and the nodes that got a verdict.
type outcome struct {
	run   lastRun
	nodes []nodeResult
}

// orderPorts puts 443 first (the port that matters), then the rest by number, at most the agent's four.
func orderPorts(ports []uint16) []uint16 {
	out := slices.Clone(ports)
	slices.SortFunc(out, func(a, b uint16) int {
		switch {
		case a == b:
			return 0
		case a == 443:
			return -1
		case b == 443:
			return 1
		}
		return int(a) - int(b)
	})
	out = slices.Compact(out)
	return out[:min(len(out), maxPorts)]
}

// targetsOf are the nodes to test: the online ones with a TCP port, never the probe node itself (nor a node on its address).
func targetsOf(probe Node, nodes []Node) []Node {
	var out []Node
	seen := map[string]bool{strings.ToLower(strings.TrimSpace(probe.Address)): true}
	for _, n := range nodes {
		addr := strings.ToLower(strings.TrimSpace(n.Address))
		if n.ID == probe.ID || !n.Online || addr == "" || seen[addr] {
			continue
		}
		if n.Ports = orderPorts(n.Ports); len(n.Ports) == 0 {
			continue
		}
		seen[addr] = true
		out = append(out, n)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// batches cut the targets into commands of at most batchPairs host:port pairs; the ports of a node stay together.
func batches(targets []Node) [][]Node {
	var out [][]Node
	var cur []Node
	pairs := 0
	for _, n := range targets {
		if pairs+len(n.Ports) > batchPairs && len(cur) > 0 {
			out, cur, pairs = append(out, cur), nil, 0
		}
		cur, pairs = append(cur, n), pairs+len(n.Ports)
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

func pairKey(n Node, port uint16) string { return n.Address + ":" + strconv.Itoa(int(port)) }

// refWalk is a run's walk through the blocked references: the one in use, and those that could not place the TSPU in this run
// with the agent's reason ("" when it gave none). A reference that failed is not asked again in the same run, so a run costs at
// most one failed command per reference, however many batches it has.
type refWalk struct {
	cur    int
	failed map[int]string
}

// ask sends one batch. A probe node whose agent cannot place the TSPU with a reference ("no_dpi_hop") is asked again with the
// next one that has not failed yet in this run, until one places it or all have failed.
func (s *Service) ask(ctx context.Context, probeID string, refs References, w *refWalk, batch []Node) (fleet.CountryProbeResult, error) {
	req := fleet.CountryProbeRequest{Control: refs.Control, ControlSNI: refs.ControlSNI}
	for _, n := range batch {
		req.Targets = append(req.Targets, fleet.CountryProbeTarget{Host: n.Address, Ports: n.Ports})
	}
	for {
		req.BlockedRef = refs.Blocked[w.cur]
		res, err := s.cfg.Fleet.CountryProbe(ctx, probeID, req)
		if err != nil || res.Code != "no_dpi_hop" {
			return res, err
		}
		w.failed[w.cur] = res.Reason
		next := -1
		for i := 1; i < len(refs.Blocked); i++ {
			if j := (w.cur + i) % len(refs.Blocked); !has(w.failed, j) {
				next = j
				break
			}
		}
		if next < 0 {
			return res, nil
		}
		w.cur = next
	}
}

func has(m map[int]string, k int) bool { _, ok := m[k]; return ok }

// reason folds why the references failed into the run's reason: ReasonRefsPass when any of them got an answer from its end
// (blocked addresses get through here), else ReasonNoHops when every one saw no router, else "" (not known, e.g. an older agent).
func (w *refWalk) reason() string {
	if len(w.failed) == 0 {
		return ""
	}
	all := true
	for _, r := range w.failed {
		switch r {
		case "ref_open", "ref_refused":
			return ReasonRefsPass
		case "no_hops":
		default:
			all = false
		}
	}
	if all {
		return ReasonNoHops
	}
	return ""
}

// transportDetail names why no answer came.
func transportDetail(err error) string {
	switch {
	case strings.Contains(err.Error(), "agent too old"):
		return "agent_too_old"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	}
	return "unreachable"
}

// execute runs the probe against the targets, batch after batch, and stores each batch's rows as its answer comes in. gen is
// the generation the run started under: once the source is switched off or disconnected, nothing more is stored.
func (s *Service) execute(ctx context.Context, gen uint64, st settings, probe Node, nodes []Node) (outcome, error) {
	refs, ok := ReferencesFor(s.country(ctx))
	if !ok {
		return outcome{}, errors.New("countryprobe: no references for the country")
	}
	out := outcome{run: lastRun{Node: probe.ID}}
	w := &refWalk{failed: map[int]string{}}
	if st.last.Node == probe.ID {
		if i := slices.Index(refs.Blocked, st.last.Ref); i >= 0 {
			w.cur = i // start with the reference that worked last time
		}
	}
	out.run.Status = StatusOK
	for _, batch := range batches(targetsOf(probe, nodes)) {
		res, err := s.ask(ctx, probe.ID, refs, w, batch)
		if ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return outcome{}, errSwitchedOff
		}
		if err != nil {
			s.log.Warn("countryprobe: the probe node did not answer", "node", probe.ID, "err", err)
			out.run.Status, out.run.Detail = StatusFailed, transportDetail(err)
			break
		}
		out.run.DPIHop, out.run.Ref = res.DPIHop, refs.Blocked[w.cur]
		switch res.Code {
		case "":
		case "control_failed":
			out.run.Status = StatusControlFailed
		case "no_dpi_hop":
			out.run.Status, out.run.Reason = StatusNoDPIHop, w.reason()
		case "busy":
			out.run.Status = StatusBusy
		default:
			s.log.Warn("countryprobe: the probe node refused the run", "node", probe.ID, "code", res.Code)
			out.run.Status, out.run.Detail = StatusFailed, "agent_error"
			if res.Code == "unsupported" {
				out.run.Detail = "agent_unsupported"
			}
		}
		if out.run.Status != StatusOK {
			break
		}
		rows, results := s.rowsOf(probe, batch, res)
		if err := s.store(ctx, gen, rows); err != nil {
			return outcome{}, err
		}
		out.nodes = append(out.nodes, results...)
	}
	out.run.Checked, out.run.At = len(out.nodes), s.now().Unix()
	if err := s.saveRun(ctx, gen, out.run); err != nil {
		return outcome{}, err
	}
	return out, nil
}

// rowsOf turns one answer into the rows to store: a verdict per node, folded from its ports. A port the agent gave no word for
// (its name did not resolve, the run's deadline passed) reads "timeout", which counts for nothing.
func (s *Service) rowsOf(probe Node, batch []Node, res fleet.CountryProbeResult) ([]store.TSPUCheck, []nodeResult) {
	now := s.now()
	rows := make([]store.TSPUCheck, 0, len(batch))
	results := make([]nodeResult, 0, len(batch))
	for _, n := range batch {
		ports := make([]countryreach.TSPUPort, len(n.Ports))
		for i, p := range n.Ports {
			v, ok := res.Verdicts[pairKey(n, p)]
			if !ok {
				v = "timeout"
			}
			ports[i] = countryreach.TSPUPort{Port: p, Verdict: v}
		}
		verdict := countryreach.FoldTSPU(ports)
		detail, _ := json.Marshal(ports)
		rows = append(rows, store.TSPUCheck{NodeID: n.ID, Vantage: probe.ID, At: now, Verdict: verdict, DPIHop: res.DPIHop, ControlOK: res.ControlOK, DetailJSON: string(detail)})
		results = append(results, nodeResult{node: n, ports: ports, verdict: verdict})
	}
	return rows, results
}

// store saves rows unless the source was switched off since the run started.
func (s *Service) store(ctx context.Context, gen uint64, rows []store.TSPUCheck) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if gen != s.gen {
		return errSwitchedOff
	}
	// a run that has just hit its budget still stores the answers it has
	return s.cfg.Store.InsertTSPUChecks(context.WithoutCancel(ctx), rows)
}

func (s *Service) saveRun(ctx context.Context, gen uint64, r lastRun) error {
	b, _ := json.Marshal(r)
	s.mu.Lock()
	defer s.mu.Unlock()
	if gen != s.gen {
		return errSwitchedOff
	}
	return s.cfg.Store.SetSettings(context.WithoutCancel(ctx), map[string]string{settingLastRun: string(b)})
}

// probeNode finds the chosen node among the active ones and says whether it can run the probe now.
func probeNode(nodes []Node, id string) (Node, bool) {
	for _, n := range nodes {
		if n.ID == id {
			return n, n.Online && n.declares()
		}
	}
	return Node{}, false
}

// runNow runs the probe: runMu must be held.
func (s *Service) runNow(ctx context.Context, gen uint64, st settings, probe Node, nodes []Node) (outcome, error) {
	ctx, done, admitted := s.track(ctx, gen)
	if !admitted {
		return outcome{}, errSwitchedOff
	}
	defer done()
	return s.execute(ctx, gen, st, probe, nodes)
}

// due says whether the next run is due: the newest row of this probe node is older than the interval (read from the store, so a
// restart does not fire at once), and so is its last run, which has no rows when it failed. A busy probe node is asked again
// after busyRetry.
func (s *Service) due(ctx context.Context, st settings) (bool, error) {
	last, err := s.cfg.Store.LastTSPUCheckAt(ctx, st.node)
	if err != nil {
		return false, err
	}
	next := last.Add(st.interval)
	if r := st.last; r.Node == st.node && r.At > 0 {
		at := time.Unix(r.At, 0)
		if r.Status == StatusBusy {
			next = at.Add(busyRetry)
		} else if t := at.Add(st.interval); t.After(next) {
			next = t
		}
	}
	return !s.now().Before(next), nil
}

// Tick runs the probe when it is due and reports whether it ran. The VPS loop calls it; it is safe to call often. Nothing
// happens (no command, no request of any kind) while the source is off, has no probe node, covers another country, or on the
// Cloudflare edition; the probe node being offline or too old skips the round without a trace.
func (s *Service) Tick(ctx context.Context) bool {
	if s.cfg.Edge {
		return false
	}
	gen := s.generation()
	st, err := s.load(ctx)
	if err != nil {
		s.log.Warn("countryprobe: read settings", "err", err)
		return false
	}
	if !st.enabled || st.node == "" || s.unavailable(ctx) != "" {
		return false
	}
	due, err := s.due(ctx, st)
	if err != nil {
		s.log.Warn("countryprobe: read results", "err", err)
		return false
	}
	if !due || !s.runMu.TryLock() {
		return false
	}
	defer s.runMu.Unlock()
	nodes, err := s.cfg.Nodes(ctx)
	if err != nil {
		s.log.Warn("countryprobe: read nodes", "err", err)
		return false
	}
	probe, fit := probeNode(nodes, st.node)
	if !fit {
		return false
	}
	if _, err := s.runNow(ctx, gen, st, probe, nodes); err != nil {
		if ctx.Err() == nil && !errors.Is(err, errSwitchedOff) {
			s.log.Warn("countryprobe: run failed", "err", err)
		}
		return false
	}
	s.changed(ctx)
	return true
}

func (s *Service) changed(ctx context.Context) {
	if s.cfg.OnChange != nil {
		s.cfg.OnChange(ctx)
	}
}

// ---------------------------------------------------------------------------------------------------
// The RPCs

type rpc struct{ s *Service }

func fail(code connect.Code, msg string) error { return connect.NewError(code, errors.New(msg)) }

var errInternal = fail(connect.CodeInternal, "internal error")

// owner is the signed-in owner; a token (API or MCP) is refused again here, whatever the policy table says.
func owner(ctx context.Context) (store.Admin, error) {
	a, ok := auth.AdminFrom(ctx)
	if !ok || strings.HasPrefix(a.ID, "token:") || strings.HasPrefix(a.ID, "mcp:") || auth.PrincipalFrom(ctx).Token || a.Role != store.RoleOwner {
		return a, fail(connect.CodePermissionDenied, "owner access required")
	}
	return a, nil
}

func runMsg(r lastRun) *adminv1.TspuProbeRun {
	return &adminv1.TspuProbeRun{Status: r.Status, AtUnix: r.At, NodeId: r.Node, DpiHop: uint32(max(r.DPIHop, 0)), BlockedRef: r.Ref,
		NodesChecked: uint32(max(r.Checked, 0)), Detail: r.Detail, Reason: r.Reason}
}

// current is the settings as an answer: the probe node picker lists every active node, those in the selected country first.
func (s *Service) current(ctx context.Context) (*adminv1.TspuProbeSettings, error) {
	st, err := s.load(ctx)
	if err != nil {
		s.log.Error("countryprobe: read settings", "err", err)
		return nil, errInternal
	}
	reason := s.unavailable(ctx)
	m := &adminv1.TspuProbeSettings{Enabled: st.enabled, Available: reason == "", UnavailableReason: reason, ProbeNodeId: st.node,
		IntervalMin: uint32(st.interval / time.Minute)}
	if s.cfg.Edge {
		return m, nil
	}
	nodes, err := s.cfg.Nodes(ctx)
	if err != nil {
		s.log.Error("countryprobe: read nodes", "err", err)
		return nil, errInternal
	}
	country := s.country(ctx)
	for _, n := range nodes {
		m.Nodes = append(m.Nodes, &adminv1.TspuProbeNode{Id: n.ID, Name: n.Name, CountryCode: n.CountryCode,
			InCountry: strings.EqualFold(n.CountryCode, country), Declares: n.declares(), Online: n.Online})
	}
	sort.SliceStable(m.Nodes, func(i, j int) bool {
		a, b := m.Nodes[i], m.Nodes[j]
		if a.InCountry != b.InCountry {
			return a.InCountry
		}
		return a.Name < b.Name
	})
	if st.enabled && st.last.At > 0 {
		m.LastRun = runMsg(st.last)
	}
	return m, nil
}

func (r rpc) GetTspuProbe(ctx context.Context, _ *connect.Request[adminv1.GetTspuProbeRequest]) (*connect.Response[adminv1.GetTspuProbeResponse], error) {
	if _, err := owner(ctx); err != nil {
		return nil, err
	}
	cur, err := r.s.current(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.GetTspuProbeResponse{Settings: cur}), nil
}

func (r rpc) UpdateTspuProbe(ctx context.Context, req *connect.Request[adminv1.UpdateTspuProbeRequest]) (*connect.Response[adminv1.UpdateTspuProbeResponse], error) {
	admin, err := owner(ctx)
	if err != nil {
		return nil, err
	}
	s := r.s
	if err := edition.Check(s.cfg.Edge, edition.TSPUProbe); err != nil {
		return nil, err
	}
	if err := s.cfg.StepUp(ctx); err != nil {
		return nil, err
	}
	st, err := s.load(ctx)
	if err != nil {
		return nil, errInternal
	}
	kv := map[string]string{}
	changed := map[string]any{}
	stopRun := false
	if e := req.Msg.Enabled; e != nil {
		kv[settingEnabled], changed["enabled"] = "0", *e
		if *e {
			kv[settingEnabled] = "1"
		}
		stopRun = !*e
	}
	if n := req.Msg.IntervalMin; n != nil {
		if *n < MinIntervalMin || *n > MaxIntervalMin {
			return nil, fail(connect.CodeInvalidArgument, "interval_min: "+strconv.Itoa(MinIntervalMin)+".."+strconv.Itoa(MaxIntervalMin))
		}
		kv[settingInterval], changed["interval_min"] = strconv.Itoa(int(*n)), *n
	}
	if id := req.Msg.ProbeNodeId; id != nil {
		if *id != "" {
			nodes, err := s.cfg.Nodes(ctx)
			if err != nil {
				s.log.Error("countryprobe: read nodes", "err", err)
				return nil, errInternal
			}
			if err := checkProbeNode(nodes, *id); err != nil {
				return nil, err
			}
		}
		kv[settingNode], changed["probe_node"] = *id, *id
		stopRun = stopRun || *id != st.node
	}
	if len(kv) > 0 {
		if stopRun {
			s.stop() // a run for the old choice must not store anything
		}
		if err := s.cfg.Store.SetSettings(ctx, kv); err != nil {
			s.log.Error("countryprobe: save settings", "err", err)
			return nil, errInternal
		}
		s.changed(ctx)
	}
	s.audit(ctx, admin, "tspu_probe_update", changed)
	cur, err := s.current(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.UpdateTspuProbeResponse{Settings: cur}), nil
}

// checkProbeNode refuses a node that cannot be the probe node: unknown, without the capability, or offline.
func checkProbeNode(nodes []Node, id string) error {
	for _, n := range nodes {
		if n.ID != id {
			continue
		}
		switch {
		case !n.declares():
			return fail(connect.CodeInvalidArgument, "probe_node_not_capable")
		case !n.Online:
			return fail(connect.CodeInvalidArgument, "probe_node_offline")
		}
		return nil
	}
	return fail(connect.CodeInvalidArgument, "probe_node_unknown")
}

func (r rpc) DisconnectTspuProbe(ctx context.Context, _ *connect.Request[adminv1.DisconnectTspuProbeRequest]) (*connect.Response[adminv1.DisconnectTspuProbeResponse], error) {
	admin, err := owner(ctx)
	if err != nil {
		return nil, err
	}
	s := r.s
	if err := s.cfg.StepUp(ctx); err != nil {
		return nil, err
	}
	s.stop() // first: nothing new may be stored while the settings go
	if err := s.cfg.Store.DeleteSettings(ctx, settingEnabled, settingNode, settingInterval, settingLastRun); err != nil {
		s.log.Error("countryprobe: disconnect", "err", err)
		return nil, errInternal
	}
	if err := s.cfg.Store.DeleteTSPUChecks(ctx); err != nil {
		s.log.Error("countryprobe: forget the results", "err", err)
		return nil, errInternal
	}
	s.changed(ctx)
	s.audit(ctx, admin, "tspu_probe_disconnect", nil)
	cur, err := s.current(ctx)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&adminv1.DisconnectTspuProbeResponse{Settings: cur}), nil
}

// TestTspuProbe runs the probe now against the fleet, whatever the schedule says, and stores the rows like a scheduled run.
func (r rpc) TestTspuProbe(ctx context.Context, _ *connect.Request[adminv1.TestTspuProbeRequest]) (*connect.Response[adminv1.TestTspuProbeResponse], error) {
	if _, err := owner(ctx); err != nil {
		return nil, err
	}
	s := r.s
	if err := edition.Check(s.cfg.Edge, edition.TSPUProbe); err != nil {
		return nil, err
	}
	gen := s.generation()
	st, err := s.load(ctx)
	if err != nil {
		return nil, errInternal
	}
	switch {
	case !st.enabled:
		return nil, fail(connect.CodeFailedPrecondition, "tspu_off")
	case s.unavailable(ctx) != "":
		return nil, fail(connect.CodeFailedPrecondition, "tspu_unavailable_for_country")
	case st.node == "":
		return nil, fail(connect.CodeFailedPrecondition, "tspu_no_probe_node")
	}
	nodes, err := s.cfg.Nodes(ctx)
	if err != nil {
		s.log.Error("countryprobe: read nodes", "err", err)
		return nil, errInternal
	}
	probe, fit := probeNode(nodes, st.node)
	if !fit {
		return nil, fail(connect.CodeFailedPrecondition, "tspu_probe_node_unfit")
	}
	s.mu.Lock()
	wait := s.lastTest.Add(testEvery).Sub(s.now())
	if wait <= 0 {
		s.lastTest = s.now()
	}
	s.mu.Unlock()
	if wait > 0 {
		return nil, fail(connect.CodeResourceExhausted, "tested a moment ago: try again in "+strconv.Itoa(int(wait.Seconds())+1)+" s")
	}
	if !s.runMu.TryLock() {
		return nil, fail(connect.CodeFailedPrecondition, "tspu_run_in_progress")
	}
	defer s.runMu.Unlock()
	// A run takes minutes: it outlives the owner closing the tab (the rows and the run status are stored either way), and the
	// HTTP server lifts its write deadline for this procedure (httpserver.longUnary).
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), runBudget)
	defer cancel()
	res, err := s.runNow(rctx, gen, st, probe, nodes)
	switch {
	case errors.Is(err, errSwitchedOff):
		return nil, fail(connect.CodeFailedPrecondition, "tspu_off")
	case err != nil:
		s.log.Error("countryprobe: test", "err", err)
		return nil, errInternal
	}
	s.changed(ctx)
	resp := &adminv1.TestTspuProbeResponse{Run: runMsg(res.run)}
	for _, n := range res.nodes {
		m := &adminv1.TspuNodeVerdict{NodeId: n.node.ID, Name: n.node.Name, Verdict: n.verdict}
		for _, p := range n.ports {
			m.Ports = append(m.Ports, &adminv1.TspuPort{Port: uint32(p.Port), Verdict: p.Verdict})
		}
		resp.Nodes = append(resp.Nodes, m)
	}
	return connect.NewResponse(resp), nil
}

// audit records who did what; params is marshalled as a JSON object.
func (s *Service) audit(ctx context.Context, admin store.Admin, action string, params map[string]any) {
	p := ""
	if len(params) > 0 {
		b, _ := json.Marshal(params)
		p = string(b)
	}
	if err := s.cfg.Store.Audit(ctx, s.now(), store.AuditEntry{Actor: admin.ID, Action: action, Params: p, Result: "ok"}); err != nil {
		s.log.Warn("countryprobe: audit", "action", action, "err", err)
	}
}
