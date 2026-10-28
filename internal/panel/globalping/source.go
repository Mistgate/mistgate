package globalping

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"net/http"
	"slices"
	"time"

	"github.com/mistgate/mistgate/internal/panel/health"
)

// The Globalping implementation of health.CountrySource. When a round asks for named networks, it reads GET /v1/probes
// and pins up to probes-1 home or mobile probes by ASN and city in the control request. A generic country location uses
// the remaining budget; if listing fails, the control request uses one generic country location. If fewer than two
// home or mobile probes reach the control, it retries across any network in the country. The control measurement's id
// selects the same probes for each node's ICMP test and TCP tests of its ports. All measurements are polled together
// until they finish or the round's deadline expires. Requests target only the control site and caller-provided nodes.
//
// An IPv6 round (Family 6) is a round of its own. Its control is a hostname measured with ipVersion 6, so only probes that have
// IPv6 take part and a probe that cannot open the control site over IPv6 is not counted; the control of an IPv4 round must not be
// reused (a measurement id repeats its probes without looking at their IPv6, and a probe without IPv6 would read as blocked).
// The nodes are IPv6 literals, which take no ipVersion, and use the id of the IPv6 control. The probe list is not read: it does not
// say which probes have IPv6, so the operators are not pinned and the control takes generic eyeball probes, then any network.

const (
	pingPackets  = 3
	defaultPoll  = 500 * time.Millisecond // the guideline: wait 500 ms after a response, then ask again
	maxRateWait  = 10 * time.Second       // a 429 on a read: wait at most this and ask again
	defaultReset = 5 * time.Minute        // a 429 on a create that does not say when
	probesWait   = 10 * time.Second
	minEyeball   = 2
)

var (
	errSwitchedOff = health.ErrCountryStopped
	errNoEyeball   = errors.New("not enough home or mobile probes")
)

const familyV6 = 6 // CountryRound.Family of an IPv6 round

type source struct {
	svc          *Service
	c            *client
	limit        int
	poll         time.Duration
	gen          uint64
	probeTimeout time.Duration // test override; zero uses probesWait
}

func (s *source) Name() string     { return "globalping" }
func (s *source) HourlyLimit() int { return s.limit }

func (s *source) Measure(ctx context.Context, r health.CountryRound) (health.CountryObserved, error) {
	tctx, done, err := s.svc.track(ctx, s.gen)
	if err != nil {
		return health.CountryObserved{}, err
	}
	defer done()
	obs, err := s.measure(tctx, r)
	if err != nil && tctx.Err() != nil && ctx.Err() == nil { // not the caller's deadline: the owner switched it off
		err = errSwitchedOff
	}
	return obs, err
}

func (s *source) measure(ctx context.Context, r health.CountryRound) (health.CountryObserved, error) {
	plan := s.controlPlan(ctx, r)
	obs := health.CountryObserved{NoProbe: plan.noProbe, Asked: plan.asked}
	id, ctl, err := s.control(ctx, r, true, plan)
	if err == nil && countValid(ctl) < minEyeball {
		err = errNoEyeball
	}
	eyeball := !errors.Is(err, errNoEyeball)
	if !eyeball {
		id, ctl, err = s.control(ctx, r, false, plan)
	}
	if err != nil {
		return obs, err
	}
	obs.Eyeball = eyeball
	probes := make([]health.ObservedProbe, len(ctl.Results))
	for i, it := range ctl.Results {
		_, ok, _ := pingResult(it.Result)
		probes[i] = health.ObservedProbe{Network: it.Probe.Network, ASN: it.Probe.ASN, City: it.Probe.City,
			Eyeball: slices.Contains(it.Probe.Tags, "eyeball-network"), Online: it.Result.Status != "offline", ControlOK: ok,
			Nodes: map[string]health.ObservedNode{}}
	}
	obs.Probes = probes
	if countValid(ctl) < minEyeball { // nothing to compare with: do not spend the node measurements
		return obs, nil
	}

	type job struct {
		key, kind string // kind: icmp | tcp
		port      uint16
	}
	jobs := map[string]job{}
	var ids []string
	expected := make(map[string]int, len(r.Targets))
	completed := make([]map[string]int, len(probes))
	for i := range completed {
		completed[i] = map[string]int{}
	}
	add := func(req measurementRequest, j job) error {
		c, err := s.create(ctx, req)
		if err != nil {
			return err
		}
		jobs[c.ID] = j
		ids = append(ids, c.ID)
		return nil
	}
	for _, t := range r.Targets {
		expected[t.Key] = 1 + len(t.Ports)
		if err := add(measurementRequest{Type: "ping", Target: t.IP, Locations: id, MeasurementOptions: pingOptions{Packets: pingPackets}}, job{key: t.Key, kind: "icmp"}); err != nil {
			return obs, err
		}
		for _, port := range t.Ports {
			if err := add(measurementRequest{Type: "ping", Target: t.IP, Locations: id,
				MeasurementOptions: pingOptions{Protocol: "TCP", Port: port, Packets: pingPackets}}, job{key: t.Key, kind: "tcp", port: port}); err != nil {
				return obs, err
			}
		}
	}
	done2, err := s.pollAll(ctx, ids)
	if errors.Is(err, errSwitchedOff) || errors.Is(err, context.Canceled) {
		return obs, errSwitchedOff
	}
	// A deadline in the middle keeps what finished; the rest stays unanswered and the verdict says unknown.
	for _, id := range ids { // in the order they were made: the ports of a node stay in the order of the round
		m, ok := done2[id]
		if !ok {
			continue
		}
		j := jobs[id]
		for i, it := range m.Results {
			if i >= len(probes) {
				break
			}
			n := probes[i].Nodes[j.key]
			switch j.kind {
			case "icmp":
				answered, ok, ms := pingResult(it.Result)
				n.Answered, n.ICMPOK, n.ICMPMs = answered, ok, ms
				if answered {
					completed[i][j.key]++
				}
			case "tcp":
				answered, ok, ms := pingResult(it.Result)
				n.Answered = answered || n.Answered
				n.TCP = append(n.TCP, health.ObservedPort{Port: j.port, OK: ok, MS: ms})
				if answered {
					completed[i][j.key]++
				}
			}
			probes[i].Nodes[j.key] = n
		}
	}
	for i := range probes {
		for key, node := range probes[i].Nodes {
			node.Complete = expected[key] > 0 && completed[i][key] == expected[key]
			probes[i].Nodes[key] = node
		}
	}
	return obs, nil
}

type controlPlan struct {
	picks   []location
	noProbe []string
	asked   []string
	planned bool
}

func (s *source) controlPlan(ctx context.Context, r health.CountryRound) controlPlan {
	if len(r.Want) == 0 || r.Family == familyV6 {
		return controlPlan{}
	}
	timeout := s.probeTimeout
	if timeout <= 0 {
		timeout = probesWait
	}
	probesCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	probes, err := s.c.probes(probesCtx)
	if err != nil {
		s.svc.log.Warn("globalping: probe list", "err", err)
		return controlPlan{} // listing probes is best-effort; the generic country location remains the fallback
	}

	byASN := make(map[uint32][]listedProbe)
	for _, probe := range probes {
		if probe.Location.Country == r.Country && slices.Contains(probe.Tags, "eyeball-network") {
			byASN[probe.Location.ASN] = append(byASN[probe.Location.ASN], probe)
		}
	}

	plan := controlPlan{planned: true}
	used := map[string]bool{}
	maxPicks := max(r.Probes-1, 0)
	for _, want := range r.Want {
		var matches []listedProbe
		for _, asn := range want.ASNs {
			for _, probe := range byASN[asn] {
				if want.City == "" || probe.Location.City == want.City {
					matches = append(matches, probe)
				}
			}
		}
		if len(matches) == 0 {
			if want.City == "" && want.Name != "" {
				plan.noProbe = append(plan.noProbe, want.Name)
			}
			continue
		}
		if len(plan.picks) >= maxPicks || used[want.Name] {
			continue
		}
		probe := matches[rand.IntN(len(matches))]
		plan.picks = append(plan.picks, location{Country: r.Country, ASN: probe.Location.ASN, City: probe.Location.City,
			Tags: []string{"eyeball-network"}, Limit: 1})
		if want.Name != "" {
			plan.asked = append(plan.asked, want.Name)
		}
		used[want.Name] = true
	}
	return plan
}

// control runs the control measurement and waits for it. eyeball restricts the probes to home and mobile networks;
// errNoEyeball says there were too few of them to start with.
func (s *source) control(ctx context.Context, r health.CountryRound, eyeball bool, plan controlPlan) (string, measurement, error) {
	var locations []location
	if plan.planned {
		locations = slices.Clone(plan.picks)
		generic := location{Country: r.Country, Limit: r.Probes - len(plan.picks)}
		if eyeball {
			generic.Tags = []string{"eyeball-network"}
		}
		locations = append(locations, generic)
	} else {
		loc := location{Country: r.Country, Limit: r.Probes}
		if eyeball {
			loc.Tags = []string{"eyeball-network"}
		}
		locations = []location{loc}
	}
	opts := pingOptions{Protocol: "TCP", Port: r.ControlPort, Packets: pingPackets}
	if r.Family == familyV6 {
		opts.IPVersion = 6
	}
	c, err := s.create(ctx, measurementRequest{Type: "ping", Target: r.ControlHost, Locations: locations, MeasurementOptions: opts})
	var ae *apiError
	switch {
	case eyeball && errors.As(err, &ae) && ae.Status == http.StatusUnprocessableEntity:
		return "", measurement{}, errNoEyeball // no_probes_found
	case r.Family == familyV6 && errors.As(err, &ae) && ae.Status == http.StatusUnprocessableEntity:
		return "", measurement{}, health.ErrCountryNoProbes // no_probes_found on any network: none of the country's probes has IPv6
	case err != nil:
		return "", measurement{}, err
	case eyeball && c.ProbesCount < minEyeball:
		return "", measurement{}, errNoEyeball
	}
	done, err := s.pollAll(ctx, []string{c.ID})
	m, ok := done[c.ID]
	if !ok {
		if err == nil {
			err = errors.New("globalping: the control measurement did not finish")
		}
		return "", measurement{}, err
	}
	return c.ID, m, nil
}

// create posts a measurement; a 429 becomes the caller's signal to wait.
func (s *source) create(ctx context.Context, r measurementRequest) (created, error) {
	if err := ctx.Err(); err != nil {
		return created{}, err
	}
	c, err := s.c.create(ctx, r)
	var ae *apiError
	if errors.As(err, &ae) && ae.Status == http.StatusTooManyRequests {
		reset := ae.Reset
		if reset <= 0 {
			reset = defaultReset
		}
		return created{}, &health.CountryLimitError{Reset: reset}
	}
	return c, err
}

// pollAll waits for the measurements to leave "in-progress" and returns the finished ones. A context that ends first
// returns what finished with the context's error.
func (s *source) pollAll(ctx context.Context, ids []string) (map[string]measurement, error) {
	done := make(map[string]measurement, len(ids))
	pending := ids
	for len(pending) > 0 {
		if err := sleep(ctx, s.poll); err != nil {
			return done, err
		}
		var next []string
		for _, id := range pending {
			m, err := s.c.get(ctx, id)
			var ae *apiError
			switch {
			case errors.As(err, &ae) && ae.Status == http.StatusTooManyRequests:
				if err := sleep(ctx, min(max(ae.Reset, s.poll), maxRateWait)); err != nil {
					return done, err
				}
				next = append(next, id)
				continue
			case err != nil:
				return done, err
			case m.Status == "in-progress":
				next = append(next, id)
				continue
			}
			done[id] = m
		}
		pending = next
	}
	return done, nil
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// pingResult reads a ping test: answered (the probe produced a verdict about the target), ok (at least one packet came back)
// and the average round trip. A failed test whose cause is the target counts as an answer of "no"; one that failed in the
// probe or the API says nothing.
func pingResult(r testResult) (answered, ok bool, ms uint32) {
	switch r.Status {
	case "finished":
		ok = r.Stats.Rcv > 0
		if ok && r.Stats.Avg != nil {
			ms = uint32(math.Round(*r.Stats.Avg))
		}
		return true, ok, ms
	case "failed":
		return r.FailureSource == "target", false, 0
	}
	return false, false, 0
}

func countValid(m measurement) int {
	n := 0
	for _, it := range m.Results {
		if _, ok, _ := pingResult(it.Result); ok {
			n++
		}
	}
	return n
}
