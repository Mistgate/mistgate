package fleet

import (
	"testing"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func TestPrepareFailureAlertRequiresRequestedRunningTransition(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("de1")
	panelNow := time.Now().UTC().Truncate(time.Second)
	e.f.now = func() time.Time { return panelNow }
	type failure struct {
		node, code string
		at         int64
	}
	var got []failure
	e.f.cfg.OnAwgPrepareFailed = func(node, code string, at int64) { got = append(got, failure{node, code, at}) }
	c, _, _ := connectFull(a, "awg-security")
	sendFailure := func(seq uint64) {
		t.Helper()
		c.send(seq, &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_Event{Event: &agentv1.Event{
			Code: evPrepFailed, TimeUnix: 1, Params: map[string]string{"code": "apt_lock", "reason": "package manager busy"},
		}}})
		c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetAck() != nil && m.GetAck().UpToSeq >= seq })
	}
	sendFailure(1) // forged without a panel request
	if len(got) != 0 {
		t.Fatalf("forged failure before a prepare request notified owner: %+v", got)
	}
	if err := e.st.SetAwgPrepare(e.ctx, a.nodeID, store.AwgPrepareRow{State: store.AwgPrepareRunning, Since: panelNow.Unix() - 5, Want: true}); err != nil {
		t.Fatal(err)
	}
	sendFailure(2)
	sendFailure(3) // the state already is failed
	if len(got) != 1 || got[0] != (failure{node: a.nodeID, code: "apt_lock", at: panelNow.Unix()}) {
		t.Fatalf("callbacks = %+v, want one panel-timestamped running-to-failed transition", got)
	}
}
