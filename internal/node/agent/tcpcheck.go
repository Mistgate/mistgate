package agent

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/tcpcheck"
)

// maxTCPProbes is how many TcpProbe commands run at once.
const maxTCPProbes = 4

// httpMarker is the part of the node's certificate source (internal/node/certs) that arms the one-shot HTTP-01 mark.
type httpMarker interface {
	Mark(token, answer []byte, until time.Time) bool
	HTTP01State() string
}

// tcpProbeState is the agent's bookkeeping for TcpProbe: how many run now, and the test seam for the prober.
type tcpProbeState struct {
	mu    sync.Mutex
	n     int
	probe func(context.Context, string, []tcpcheck.Target) (string, map[uint16]string, error)
}

// httpMark handles one HttpMark command: arm the node's HTTP-01 listener for one request (agent.proto "TCP DELIVERY CHECK").
func (a *Agent) httpMark(s *session, r *pb.HttpMark) {
	if len(r.Token) != tcpcheck.MarkSize || len(r.Answer) != tcpcheck.MarkSize || r.HoldS < 1 || r.HoldS > 60 {
		s.send(cmdResult(&pb.CommandResult{RequestId: r.RequestId, Error: "bad_params"}))
		return
	}
	if a.measuring.Load() {
		s.send(cmdResult(&pb.CommandResult{RequestId: r.RequestId, Error: "busy"}))
		return
	}
	state := "off"
	marker, ok := a.cfg.Certs.(httpMarker)
	if ok {
		state = marker.HTTP01State()
	}
	if state == "serving" && !marker.Mark(r.Token, r.Answer, time.Now().Add(time.Duration(r.HoldS)*time.Second)) {
		s.send(cmdResult(&pb.CommandResult{RequestId: r.RequestId, Error: "busy"}))
		return
	}
	s.send(cmdResult(&pb.CommandResult{RequestId: r.RequestId, Ok: true, Params: map[string]string{"http01": state}}))
}

// tcpProbe handles one TcpProbe command: test the targets on another node and answer with a verdict per port.
func (a *Agent) tcpProbe(s *session, r *pb.TcpProbe) {
	targets, err := tcpTargets(r.Targets)
	if err == nil {
		err = tcpcheck.Validate(r.Host, targets)
	}
	if err != nil {
		s.send(cmdResult(&pb.CommandResult{RequestId: r.RequestId, Error: "bad_params"}))
		return
	}
	if a.measuring.Load() {
		s.send(cmdResult(&pb.CommandResult{RequestId: r.RequestId, Error: "busy"}))
		return
	}
	a.tcp.mu.Lock()
	if a.measuring.Load() || a.tcp.n >= maxTCPProbes {
		a.tcp.mu.Unlock()
		s.send(cmdResult(&pb.CommandResult{RequestId: r.RequestId, Error: "busy"}))
		return
	}
	a.tcp.n++
	probe := a.tcp.probe
	a.tcp.mu.Unlock()
	defer func() {
		a.tcp.mu.Lock()
		a.tcp.n--
		a.tcp.mu.Unlock()
	}()

	if probe == nil {
		probe = tcpcheck.Probe
	}
	family, verdicts, err := probe(s.ctx, r.Host, targets)
	if s.ctx.Err() != nil {
		return
	}
	if err != nil {
		s.send(cmdResult(&pb.CommandResult{RequestId: r.RequestId, Error: tcpProbeError(err)}))
		return
	}
	params := make(map[string]string, len(verdicts)+1)
	params["family"] = family
	for port, verdict := range verdicts {
		params["t"+strconv.Itoa(int(port))] = verdict
	}
	s.send(cmdResult(&pb.CommandResult{RequestId: r.RequestId, Ok: true, Params: params}))
}

func tcpTargets(raw []*pb.TcpTarget) ([]tcpcheck.Target, error) {
	out := make([]tcpcheck.Target, 0, len(raw))
	for _, t := range raw {
		if t == nil || t.Port == 0 || t.Port > 65535 {
			return nil, tcpcheck.ErrBadParams
		}
		out = append(out, tcpcheck.Target{Port: uint16(t.Port), Kind: t.Kind, SNI: t.Sni, PinSHA256: t.PinSha256,
			Token: t.Token, Answer: t.Answer})
	}
	return out, nil
}

func tcpProbeError(err error) string {
	switch {
	case errors.Is(err, tcpcheck.ErrBadParams):
		return "bad_params"
	case errors.Is(err, tcpcheck.ErrUnsupportedHost):
		return "unsupported_host"
	case errors.Is(err, tcpcheck.ErrNoRoute):
		return "no_route"
	default:
		return "failed: " + shortUDPError(err)
	}
}
