package globalping

import (
	"context"
	"errors"
	"math"
	"net/http"
	"time"

	"github.com/mistgate/mistgate/internal/panel/health"
)

// The Globalping implementation of health.CountrySource. One round, as the API's own client guidelines describe it:
//
//  1. a control measurement (TCP 443 to a site that is never blocked in the country) from home and mobile networks
//     (tag eyeball-network) of the country, limit = probes. Its id names the probes of the round.
//  2. if fewer than two of those probes reached the control, once more from any network of the country.
//  3. every other measurement passes that id as "locations", so every node is seen by the same probes: an ICMP ping of the
//     node's address and a TCP ping of each of its ports.
//  4. all of them are polled together: a short wait, then GET by id, until none is in progress or the round's time is up.
//
// No measurement ever targets anything but the control site, the nodes' addresses given by the caller and their names.

const (
	pingPackets  = 3
	defaultPoll  = 500 * time.Millisecond // the guideline: wait 500 ms after a response, then ask again
	maxRateWait  = 10 * time.Second       // a 429 on a read: wait at most this and ask again
	defaultReset = 5 * time.Minute        // a 429 on a create that does not say when
	minEyeball   = 2
)

var (
	errSwitchedOff = health.ErrCountryStopped
	errNoEyeball   = errors.New("not enough home or mobile probes")
)

type source struct {
	svc   *Service
	c     *client
	limit int
	poll  time.Duration
	gen   uint64
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
	id, ctl, err := s.control(ctx, r, true)
	if err == nil && countValid(ctl) < minEyeball {
		err = errNoEyeball
	}
	eyeball := !errors.Is(err, errNoEyeball)
	if !eyeball {
		id, ctl, err = s.control(ctx, r, false)
	}
	if err != nil {
		return health.CountryObserved{}, err
	}
	obs := health.CountryObserved{Eyeball: eyeball}
	probes := make([]health.ObservedProbe, len(ctl.Results))
	for i, it := range ctl.Results {
		_, ok, _ := pingResult(it.Result)
		probes[i] = health.ObservedProbe{Network: it.Probe.Network, ASN: it.Probe.ASN, City: it.Probe.City,
			Online: it.Result.Status != "offline", ControlOK: ok, Nodes: map[string]health.ObservedNode{}}
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
			case "tcp":
				answered, ok, ms := pingResult(it.Result)
				n.Answered = answered || n.Answered
				n.TCP = append(n.TCP, health.ObservedPort{Port: j.port, OK: ok, MS: ms})
			}
			probes[i].Nodes[j.key] = n
		}
	}
	return obs, nil
}

// control runs the control measurement and waits for it. eyeball restricts the probes to home and mobile networks;
// errNoEyeball says there were too few of them to start with.
func (s *source) control(ctx context.Context, r health.CountryRound, eyeball bool) (string, measurement, error) {
	loc := location{Country: r.Country, Limit: r.Probes}
	if eyeball {
		loc.Tags = []string{"eyeball-network"}
	}
	c, err := s.create(ctx, measurementRequest{Type: "ping", Target: r.ControlHost, Locations: []location{loc},
		MeasurementOptions: pingOptions{Protocol: "TCP", Port: r.ControlPort, Packets: pingPackets}})
	var ae *apiError
	switch {
	case eyeball && errors.As(err, &ae) && ae.Status == http.StatusUnprocessableEntity:
		return "", measurement{}, errNoEyeball // no_probes_found
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
