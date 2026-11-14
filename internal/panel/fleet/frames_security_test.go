package fleet

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

func TestAgentFrameLimitClosesPersistentlyFloodingSession(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("de1")
	c, _, _ := connectFull(a, "frame-security")
	frame := &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_Event{Event: &agentv1.Event{Code: "frame_probe"}}}
	for i := 0; i < agentFrameBurst+agentFrameDropsToClose+16; i++ {
		if err := c.st.Send(frame); err != nil {
			break
		}
	}
	if err := c.ended(); code(err) != connect.CodeResourceExhausted {
		t.Fatalf("flooding session ended with %v, want resource exhausted", err)
	}
}

// A reliable frame over the rate is never skipped (the ack of a later one would cover it): the session ends and the agent
// replays from the last ack. The frames carry a code only the panel may write, so each costs the panel almost nothing and
// the test does not depend on how fast the database is.
func TestAgentFrameLimitNeverSkipsAReliableFrame(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("de1")
	c, _, _ := connectFull(a, "frame-reliable")
	for seq := uint64(1); seq <= agentFrameBurst+8; seq++ {
		frame := &agentv1.ConnectRequest{Seq: seq, Message: &agentv1.ConnectRequest_Event{Event: &agentv1.Event{Code: "node_down", TimeUnix: time.Now().Unix()}}}
		if err := c.st.Send(frame); err != nil {
			break
		}
	}
	if err := c.ended(); code(err) != connect.CodeResourceExhausted {
		t.Fatalf("session ended with %v, want resource exhausted", err)
	}
	if got := e.count(`SELECT last_seq FROM node WHERE id = ?`, a.nodeID); got > agentFrameBurst {
		t.Fatalf("last_seq = %d: a frame beyond the burst was processed", got)
	}
}
