package agent

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/plugin"
)

type tcpClaimCall struct {
	kind   string
	id     string
	claims map[uint16]string
}

type tcpClaimProbe struct {
	*fakeEngine
	mu    sync.Mutex
	calls []tcpClaimCall
}

func newTCPClaimProbe() *tcpClaimProbe {
	return &tcpClaimProbe{fakeEngine: newFakeEngine("claims")}
}

func (e *tcpClaimProbe) factory() engine.Factory {
	return func(engine.Env) (engine.Engine, error) { return e, nil }
}

func (e *tcpClaimProbe) Apply(ctx context.Context, spec plugin.InboundSpec, creds []plugin.UserCred) (engine.ApplyReport, error) {
	e.record(tcpClaimCall{kind: "apply", id: spec.ID})
	return e.fakeEngine.Apply(ctx, spec, creds)
}

func (e *tcpClaimProbe) Remove(ctx context.Context, id string) error {
	e.record(tcpClaimCall{kind: "remove", id: id})
	return e.fakeEngine.Remove(ctx, id)
}

func (e *tcpClaimProbe) TCPClaims(_ context.Context, claimed map[uint16]string) {
	copy := make(map[uint16]string, len(claimed))
	for port, id := range claimed {
		copy[port] = id
	}
	e.record(tcpClaimCall{kind: "claims", claims: copy})
}

func (e *tcpClaimProbe) record(call tcpClaimCall) {
	e.mu.Lock()
	e.calls = append(e.calls, call)
	e.mu.Unlock()
}

func (e *tcpClaimProbe) resetCalls() {
	e.mu.Lock()
	e.calls = nil
	e.mu.Unlock()
}

func (e *tcpClaimProbe) recordedCalls() []tcpClaimCall {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]tcpClaimCall(nil), e.calls...)
}

func TestReconcileCallsTCPPortUserAroundRemovalAndApply(t *testing.T) {
	probe := newTCPClaimProbe()
	h := newHarness(t, harnessOpts{noStart: true, extra: map[string]engine.Factory{"claims": probe.factory()}})
	t.Cleanup(h.a.closeEngines)

	old := inb("inb_old", 0, 0, cred("crd_old"))
	old.Spec.Protocol = "claims"
	if got := h.a.applyDesired(context.Background(), fullState(1, old)); got == nil {
		t.Fatal("initial apply was ignored")
	}
	probe.resetCalls()

	tcp := inb("inb_a_tcp", 0, 0, cred("crd_tcp"))
	tcp.Spec.Protocol = "claims"
	tcp.Spec.Listen.Network = "tcp"
	tcp.Spec.Listen.Port = 443
	disabled := inb("inb_z_disabled", 0, 0, cred("crd_disabled"))
	disabled.Spec.Protocol = "claims"
	disabled.Spec.Enabled = false
	disabled.Spec.Listen.Network = "tcp"
	disabled.Spec.Listen.Port = 8443
	if got := h.a.applyDesired(context.Background(), fullState(2, tcp, disabled)); got == nil {
		t.Fatal("replacement apply was ignored")
	}

	calls := probe.recordedCalls()
	if len(calls) != 5 {
		t.Fatalf("calls = %+v, want removal, claims, two applies, claims", calls)
	}
	if calls[0].kind != "remove" || calls[0].id != "inb_old" || calls[1].kind != "claims" ||
		calls[2].kind != "apply" || calls[2].id != "inb_a_tcp" ||
		calls[3].kind != "apply" || calls[3].id != "inb_z_disabled" || calls[4].kind != "claims" {
		t.Fatalf("call order = %+v", calls)
	}
	wantClaims := map[uint16]string{443: "inb_a_tcp"}
	if !reflect.DeepEqual(calls[1].claims, wantClaims) || !reflect.DeepEqual(calls[4].claims, wantClaims) {
		t.Fatalf("claims = %+v and %+v, want enabled TCP listeners %v", calls[1].claims, calls[4].claims, wantClaims)
	}
	tcpCalls := h.host.tcpPorts()
	if len(tcpCalls) != 2 || !reflect.DeepEqual(tcpCalls[1], []uint16{443}) {
		t.Fatalf("TCP firewall ports = %+v, want only the running TCP listener", tcpCalls)
	}
}

func TestReconcileKeepsPreviousTCPClaimsUntilApplyCompletes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		newPort    uint32
		disabled   bool
		wantFirst  map[uint16]string
		wantSecond map[uint16]string
	}{
		{
			name:       "disabled",
			disabled:   true,
			wantFirst:  map[uint16]string{443: "inb_vless"},
			wantSecond: map[uint16]string{},
		},
		{
			name:       "moved",
			newPort:    8443,
			wantFirst:  map[uint16]string{443: "inb_vless", 8443: "inb_vless"},
			wantSecond: map[uint16]string{8443: "inb_vless"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probe := newTCPClaimProbe()
			h := newHarness(t, harnessOpts{noStart: true, extra: map[string]engine.Factory{"claims": probe.factory()}})
			t.Cleanup(h.a.closeEngines)

			old := inb("inb_vless", 0, 0, cred("old"))
			old.Spec.Protocol = "claims"
			old.Spec.Listen.Network = "tcp"
			old.Spec.Listen.Port = 443
			if got := h.a.applyDesired(context.Background(), fullState(1, old)); got == nil {
				t.Fatal("initial apply was ignored")
			}
			probe.resetCalls()

			next := inb("inb_vless", 0, 0, cred("new"))
			next.Spec.Protocol = "claims"
			next.Spec.Listen.Network = "tcp"
			next.Spec.Listen.Port = 443
			next.Spec.Enabled = !tc.disabled
			if tc.newPort != 0 {
				next.Spec.Listen.Port = tc.newPort
			}
			if got := h.a.applyDesired(context.Background(), fullState(2, next)); got == nil {
				t.Fatal("replacement apply was ignored")
			}

			calls := probe.recordedCalls()
			if len(calls) != 3 || calls[0].kind != "claims" || calls[1].kind != "apply" || calls[2].kind != "claims" {
				t.Fatalf("calls = %+v, want claims, apply, claims", calls)
			}
			if !reflect.DeepEqual(calls[0].claims, tc.wantFirst) || !reflect.DeepEqual(calls[2].claims, tc.wantSecond) {
				t.Fatalf("claims = %+v and %+v, want old/new union %v then next %v", calls[0].claims, calls[2].claims, tc.wantFirst, tc.wantSecond)
			}
		})
	}
}
