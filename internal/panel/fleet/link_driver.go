package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"google.golang.org/protobuf/proto"
)

// The edge edition's adapter of the session core: the stateless counterpart of runSession. The NodeLink Durable Object
// (edge/worker/src/nodelink.ts) holds the socket and the state string; every event it sees is one Link call.

// LinkKind is the kind of a NodeLink event (the LinkEvent kinds of edge/worker/src/panellink.ts).
type LinkKind string

const (
	LinkOpen    LinkKind = "open"
	LinkFrame   LinkKind = "frame"
	LinkAlarm   LinkKind = "alarm"
	LinkDesired LinkKind = "desired"
	LinkRequest LinkKind = "request"
	LinkClosed  LinkKind = "closed"
)

// linkMaxSteps bounds the core steps of one call. A real call needs at most 3 (Hello, desired, prepared).
const linkMaxSteps = 4

// linkMaxReason is the longest WebSocket close reason (RFC 6455: 123 bytes after the 2-byte code).
const linkMaxReason = 123

// LinkIn is one NodeLink event, filled from the object's name, storage and clock.
type LinkIn struct {
	NodeID       string    // ctx.id.name
	State        string    // the previous LinkOut.State; "" before the node's first session
	Kind         LinkKind  //
	At           time.Time // the object's Date.now()
	Generation   uint64    // open: the generation the accepted LinkAuth stored = OwnerGeneration = node_live.session
	CertSerial   string    // open: from LinkAccept
	CertNotAfter time.Time // open
	Frame        []byte    // frame: one ConnectRequest; request: one ConnectResponse
	RequestID    string    // request
	DeadlineAt   time.Time // request: the caller's absolute deadline
}

// LinkOut is what the object does with the result of one event: write State, send Frames, resolve Replies, set the alarm,
// then close.
type LinkOut struct {
	State   string      // written by the object before it sends Frames
	Frames  [][]byte    // ConnectResponse, in order
	Close   *LinkClose  // after Frames; the object then runs the closed step
	AlarmAt time.Time   // zero = no session alarm
	Replies []LinkReply // Frame nil = refused, expired or gone
	Forget  bool        // the node is retired: delete all storage and the alarm after this block
}

// LinkClose is a WebSocket close the session asks for.
type LinkClose struct {
	Code   uint16
	Reason string // at most 123 bytes
}

// LinkReply answers an admin request: the agent's whole ConnectRequest frame, or nil when the request was refused,
// expired or gone.
type LinkReply struct {
	RequestID string
	Frame     []byte
}

// Link runs one NodeLink event through the session core. It returns an error (the object then closes the socket with
// 1011 and writes nothing) when the state does not decode, Generation is 0 on open, any other event arrives without a
// live state, the core fails, or one call needs more than linkMaxSteps core steps: folding errors into LinkOut would
// let the object save a half-applied state.
func (f *Fleet) Link(ctx context.Context, in LinkIn) (LinkOut, error) {
	if in.NodeID == "" || in.At.IsZero() {
		return LinkOut{}, errors.New("link event needs a node id and a time")
	}
	var state SessionState
	if in.State != "" {
		reason := ""
		if err := json.Unmarshal([]byte(in.State), &state); err != nil {
			reason = "undecodable"
		} else if state.NodeID != in.NodeID {
			reason = "another node's"
		}
		if reason != "" {
			// A new LinkAuth must be able to repair an object whose stored state is corrupt (a panel rollback that changed
			// a field type, a bad write): open starts clean, every other event keeps the error.
			if in.Kind != LinkOpen {
				return LinkOut{}, fmt.Errorf("link state is %s", reason)
			}
			f.log.Warn("link state dropped on open", "node", in.NodeID, "reason", reason)
			state = SessionState{}
		}
	}
	c := &linkCall{f: f, ctx: ctx, core: NewSessionCore(f), at: in.At}
	switch in.Kind {
	case LinkOpen:
		// Only Poison carries over: everything else describes the socket that just ended.
		if in.Generation == 0 {
			return LinkOut{}, errors.New("link open needs a generation")
		}
		c.state = SessionState{Version: sessionStateVersion, NodeID: in.NodeID, OwnerGeneration: in.Generation,
			PeerCertSerial: in.CertSerial, PeerCertNotAfter: in.CertNotAfter, Poison: state.Poison}
		if err := c.step(SessionEvent{Kind: EventOpen}); err != nil {
			return LinkOut{}, err
		}
		return c.finish()
	case LinkClosed:
		// A socket can close before it ever opened a session, and the object closes a session once per socket.
		if in.State == "" || state.Disconnected {
			return LinkOut{State: in.State}, nil
		}
	default:
		if in.State == "" || state.Disconnected {
			return LinkOut{}, fmt.Errorf("link %s event without a live session", in.Kind)
		}
	}
	c.state = state
	// A step that does nothing must keep the alarm the object holds: no alarm means "delete it".
	c.out.AlarmAt = alarmTime(nextSessionAlarm(c.state, in.At))
	var err error
	switch in.Kind {
	case LinkFrame:
		err = c.frame(in.Frame)
	case LinkAlarm:
		err = c.step(SessionEvent{Kind: EventAlarm})
	case LinkDesired:
		// The VPS pokes only sessions registered after Hello: state must not reach the agent before HelloAck.
		if c.state.InstanceID != "" {
			err = c.step(SessionEvent{Kind: EventDesiredChanged})
		}
	case LinkRequest:
		// A request that is already past the caller's deadline is never sent: the caller has been told "timeout".
		var frame agentv1.ConnectResponse
		if (!in.DeadlineAt.IsZero() && !in.At.Before(in.DeadlineAt)) || proto.Unmarshal(in.Frame, &frame) != nil {
			c.out.Replies = append(c.out.Replies, LinkReply{RequestID: in.RequestID})
			break
		}
		err = c.step(SessionEvent{Kind: EventAdminCommand, Request: &AdminRequest{RequestID: in.RequestID, Deadline: in.DeadlineAt, Frame: &frame}})
	case LinkClosed:
		forget := !c.state.RetireAt.IsZero() // the closed step clears it; the store committed the retirement before the frame
		if err = c.step(SessionEvent{Kind: EventDisconnected}); err == nil {
			c.out.Forget = forget
		}
	default:
		err = fmt.Errorf("unknown link event kind %q", in.Kind)
	}
	if err != nil {
		return LinkOut{}, err
	}
	return c.finish()
}

// linkCall is the state of one Link call.
type linkCall struct {
	f     *Fleet
	ctx   context.Context
	core  *SessionCore
	at    time.Time
	state SessionState
	out   LinkOut
	steps int
}

// frame handles one frame of the agent: the first must be Hello, and an accepted Hello is followed by the first desired
// state, after HelloAck (as runSession does on the VPS).
func (c *linkCall) frame(raw []byte) error {
	var msg agentv1.ConnectRequest
	if err := proto.Unmarshal(raw, &msg); err != nil {
		c.closeWith(CloseInvalidArgument, "invalid ConnectRequest frame")
		return nil
	}
	if c.state.InstanceID != "" {
		return c.step(SessionEvent{Kind: EventAgentFrame, Frame: &msg})
	}
	if err := c.step(SessionEvent{Kind: EventHello, Frame: &msg}); err != nil || c.out.Close != nil {
		return err
	}
	return c.step(SessionEvent{Kind: EventDesiredChanged})
}

// step runs one core step and performs its effects, including the desired-state preparation the core asks for (no event
// can arrive in the middle of a call, so the read runs inline and Preparing is never stored). A transition that closes
// keeps none of its frames or effects and ends the call, as on the VPS.
func (c *linkCall) step(event SessionEvent) error {
	if c.steps == linkMaxSteps {
		return fmt.Errorf("link call needs more than %d core steps", linkMaxSteps)
	}
	c.steps++
	event.At = c.at
	tr, err := c.core.Step(c.ctx, &c.state, event)
	if err != nil {
		return err
	}
	c.out.AlarmAt = alarmTime(tr.NextAlarm)
	if tr.Close != nil {
		c.closeWith(tr.Close.Class, tr.Close.Reason)
		return nil
	}
	for _, frame := range tr.Frames {
		raw, err := proto.Marshal(frame)
		if err != nil {
			return err
		}
		c.out.Frames = append(c.out.Frames, raw)
	}
	prepare := false
	for _, effect := range tr.Effects {
		switch effect.Kind {
		case EffectPrepareDesired:
			prepare = true
		case EffectReply:
			reply := LinkReply{RequestID: effect.RequestID}
			if effect.Reply != nil {
				if reply.Frame, err = proto.Marshal(effect.Reply); err != nil {
					return err
				}
			}
			c.out.Replies = append(c.out.Replies, reply)
		case EffectLogChunk:
			// Log streaming is Unimplemented on the edge: there is no subscriber to deliver to.
		default:
			c.f.runSharedEffect(c.ctx, c.state.NodeID, effect)
		}
	}
	if !prepare {
		return nil
	}
	prepared := c.f.preparedEvent(c.ctx, c.state.NodeID)
	if prepared.Err != nil && c.ctx.Err() == nil {
		c.f.log.Warn("prepare desired state", "node", c.state.NodeID, "err", prepared.Err)
	}
	return c.step(prepared)
}

// closeWith asks the object to close the socket. The agent ignores codes; the object only needs the three kinds.
func (c *linkCall) closeWith(class CloseClass, reason string) {
	code := uint16(1008)
	switch class {
	case CloseConflict:
		code = 4000
	case CloseInternal:
		code = 1011
	}
	c.out.Close = &LinkClose{Code: code, Reason: store.Clip(reason, linkMaxReason)}
}

// finish encodes the state, which is also what a closing call returns: Poison must reach the next session. A closed
// session never stores a half-finished preparation.
func (c *linkCall) finish() (LinkOut, error) {
	// A call that outlived its context writes nothing (the object gave up on it too); a lost state ends in a full resend.
	if err := c.ctx.Err(); err != nil {
		return LinkOut{}, err
	}
	if c.out.Close != nil {
		c.state.Preparing, c.state.PrepareDirty = false, false
	}
	raw, err := json.Marshal(c.state)
	if err != nil {
		return LinkOut{}, err
	}
	c.out.State = string(raw)
	return c.out, nil
}

func alarmTime(next *time.Time) time.Time {
	if next == nil {
		return time.Time{}
	}
	return *next
}
