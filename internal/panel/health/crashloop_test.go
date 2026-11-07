package health

import (
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/store"
)

// Three crash restarts of an agent in 30 minutes are one warning; restarts that were an update, a boot or a clean stop do not
// count, and the alert resolves by itself once the crashes leave the window.
func TestAgentCrashLoopOpensAndResolves(t *testing.T) {
	e := newEnv(t)
	e.node("de1", "hetzner", true)
	e.node("nl1", "provider-b", true)
	started := func(node string, ago time.Duration, params map[string]string) {
		t.Helper()
		if err := e.st.InsertEvent(e.ctx, store.EventRow{Time: e.clock.Now().Add(-ago), Severity: 1, Code: "agent_started", Source: "agent", NodeID: node, Params: params}); err != nil {
			t.Fatal(err)
		}
	}
	crash := map[string]string{"prev_exit": "unclean", "crash": "1", "reason": "restart"}
	started("de1", 25*time.Minute, crash)
	started("de1", 12*time.Minute, crash)
	started("de1", 20*time.Minute, map[string]string{"prev_exit": "clean", "reason": "restart"})
	started("de1", 3*time.Minute, map[string]string{"prev_exit": "unclean", "reason": "boot"}) // power loss: not a crash
	started("nl1", 5*time.Minute, crash)
	e.evaluate()
	want(t, e.active()) // two crashes on de1, one on nl1

	started("de1", 2*time.Minute, crash)
	e.evaluate()
	got := e.active()
	want(t, got, "agent_crash_loop/de1/agent")
	a := got["agent_crash_loop/de1/agent"]
	if a.Severity != sevWarning || a.WhyKey != "health.alert.agent_crash_loop.why" || a.TitleKey != "health.alert.agent_crash_loop.title" ||
		a.Params["count"] != "3" || a.Params["window_min"] != "30" {
		t.Errorf("alert %+v", a)
	}

	e.clock.Advance(11 * time.Minute) // the first crash leaves the window: two are left
	e.evaluate()
	want(t, e.active())
}
