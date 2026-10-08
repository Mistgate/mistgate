package agent

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/hostctl"
	"github.com/mistgate/mistgate/internal/udpcheck"
)

type udpCountTimer interface {
	Stop() bool
}

type udpCountArm struct {
	tag   [8]byte
	timer udpCountTimer
}

// udpCount handles one arm or stop command. The panel's stream reader calls it in a worker goroutine.
func (a *Agent) udpCount(s *session, r *pb.UdpCount) {
	if len(r.Tag) != 8 {
		s.send(cmdResult(&pb.CommandResult{RequestId: r.RequestId, Error: "bad_params"}))
		return
	}
	var tag [8]byte
	copy(tag[:], r.Tag)
	counter, ok := a.host.(hostctl.UDPCounter)
	if !ok {
		s.send(cmdResult(&pb.CommandResult{RequestId: r.RequestId, Error: "unsupported_host"}))
		return
	}

	if r.Stop {
		a.stopUDPCount(s, r.RequestId, tag, counter)
		return
	}
	ports, err := udpPorts(r.Ports)
	if err != nil || r.HoldS < 1 || r.HoldS > 60 {
		s.send(cmdResult(&pb.CommandResult{RequestId: r.RequestId, Error: "bad_params"}))
		return
	}
	if a.measuring.Load() {
		s.send(cmdResult(&pb.CommandResult{RequestId: r.RequestId, Error: "busy"}))
		return
	}

	a.udpMu.Lock()
	if a.measuring.Load() || a.udpArmed != nil {
		a.udpMu.Unlock()
		s.send(cmdResult(&pb.CommandResult{RequestId: r.RequestId, Error: "busy"}))
		return
	}
	if err := counter.CountUDP(tag, ports); err != nil {
		a.udpMu.Unlock()
		s.send(cmdResult(&pb.CommandResult{RequestId: r.RequestId, Error: hostCommandError(err)}))
		return
	}
	arm := &udpCountArm{tag: tag}
	after := a.udpAfterFunc
	if after == nil {
		after = func(d time.Duration, f func()) udpCountTimer { return time.AfterFunc(d, f) }
	}
	arm.timer = after(time.Duration(r.HoldS)*time.Second, func() { a.expireUDPCount(arm) })
	a.udpArmed = arm
	a.udpMu.Unlock()
	s.send(cmdResult(&pb.CommandResult{RequestId: r.RequestId, Ok: true}))
}

func (a *Agent) stopUDPCount(s *session, requestID string, tag [8]byte, counter hostctl.UDPCounter) {
	a.udpMu.Lock()
	arm := a.udpArmed
	if arm == nil || arm.tag != tag {
		a.udpMu.Unlock()
		s.send(cmdResult(&pb.CommandResult{RequestId: requestID, Error: "not_armed"}))
		return
	}
	counts, err := counter.TakeUDPCount()
	if err != nil {
		a.udpMu.Unlock()
		s.send(cmdResult(&pb.CommandResult{RequestId: requestID, Error: hostCommandError(err)}))
		return
	}
	if arm.timer != nil {
		arm.timer.Stop()
	}
	a.udpArmed = nil
	a.udpMu.Unlock()

	params := make(map[string]string, len(counts)*2)
	for port, count := range counts {
		params["p"+strconv.Itoa(int(port))] = strconv.FormatUint(count.Packets, 10)
		params["b"+strconv.Itoa(int(port))] = strconv.FormatUint(count.Bytes, 10)
	}
	s.send(cmdResult(&pb.CommandResult{RequestId: requestID, Ok: true, Params: params}))
}

func (a *Agent) expireUDPCount(arm *udpCountArm) {
	a.udpMu.Lock()
	defer a.udpMu.Unlock()
	if a.udpArmed != arm {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var err error
	if cleaner, ok := a.host.(hostctl.UDPCountCleaner); ok {
		err = cleaner.CleanupUDPCount(ctx)
	} else {
		err = hostctl.CleanupUDPCount(ctx)
	}
	if err != nil {
		a.log.Warn("UDP check hold cleanup failed", "err", err)
	}
	a.udpArmed = nil
}

func (a *Agent) udpSend(s *session, r *pb.UdpSend) {
	ports, err := udpPorts(r.Ports)
	if err != nil || udpcheck.Validate(r.Host, ports, int(r.Count), int(r.Pps), int(r.Size)) != nil || len(r.Tag) != 8 {
		s.send(cmdResult(&pb.CommandResult{RequestId: r.RequestId, Error: "bad_params"}))
		return
	}
	if _, ok := a.host.(hostctl.UDPCounter); !ok {
		s.send(cmdResult(&pb.CommandResult{RequestId: r.RequestId, Error: "unsupported_host"}))
		return
	}
	if a.measuring.Load() {
		s.send(cmdResult(&pb.CommandResult{RequestId: r.RequestId, Error: "busy"}))
		return
	}
	a.udpMu.Lock()
	if a.measuring.Load() || a.udpSends >= 4 {
		a.udpMu.Unlock()
		s.send(cmdResult(&pb.CommandResult{RequestId: r.RequestId, Error: "busy"}))
		return
	}
	a.udpSends++
	a.udpMu.Unlock()
	defer func() {
		a.udpMu.Lock()
		a.udpSends--
		a.udpMu.Unlock()
	}()

	var tag [8]byte
	copy(tag[:], r.Tag)
	send := a.sendUDP
	if send == nil {
		send = udpcheck.Send
	}
	family, sent, err := send(s.ctx, r.Host, ports, tag, int(r.Count), int(r.Pps), int(r.Size))
	if s.ctx.Err() != nil {
		return
	}
	if err != nil {
		s.send(cmdResult(&pb.CommandResult{RequestId: r.RequestId, Error: udpSendError(err)}))
		return
	}
	s.send(cmdResult(&pb.CommandResult{RequestId: r.RequestId, Ok: true, Params: map[string]string{
		"family": family,
		"sent":   strconv.Itoa(sent),
	}}))
}

func udpPorts(raw []uint32) ([]uint16, error) {
	if len(raw) == 0 || len(raw) > udpcheck.MaxPorts {
		return nil, udpcheck.ErrBadParams
	}
	ports := make([]uint16, len(raw))
	seen := make(map[uint16]struct{}, len(raw))
	for i, port := range raw {
		if port == 0 || port > 65535 {
			return nil, udpcheck.ErrBadParams
		}
		p := uint16(port)
		if _, ok := seen[p]; ok {
			return nil, udpcheck.ErrBadParams
		}
		seen[p] = struct{}{}
		ports[i] = p
	}
	return ports, nil
}

func hostCommandError(err error) string {
	if errors.Is(err, hostctl.ErrUnsupported) {
		return "unsupported_host"
	}
	return "failed: " + shortUDPError(err)
}

func udpSendError(err error) string {
	switch {
	case errors.Is(err, udpcheck.ErrBadParams):
		return "bad_params"
	case errors.Is(err, udpcheck.ErrUnsupportedHost):
		return "unsupported_host"
	case errors.Is(err, udpcheck.ErrNoRoute):
		return "no_route"
	default:
		return "failed: " + shortUDPError(err)
	}
}

func shortUDPError(err error) string {
	s := strings.Join(strings.Fields(err.Error()), " ")
	if s == "" {
		return "unknown error"
	}
	return clipText(s, 100)
}
