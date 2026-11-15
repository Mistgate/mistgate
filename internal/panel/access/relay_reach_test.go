package access

import (
	"cmp"
	"encoding/json"
	"slices"
	"testing"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func TestRequestedRelayStages(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  *adminv1.CheckRelayRequest
		want []string
	}{
		{name: "default", req: &adminv1.CheckRelayRequest{}},
		{name: "reverse reach", req: &adminv1.CheckRelayRequest{ReverseReach: true},
			want: []string{store.RelayStageLeg, store.RelayStagePath, store.RelayStageReverseReach}},
		{name: "speed proof implies reverse reach", req: &adminv1.CheckRelayRequest{ReverseSpeed: true},
			want: []string{store.RelayStageLeg, store.RelayStagePath, store.RelayStageReverseReach, store.RelayStageReverseSpeed}},
	} {
		if got := requestedRelayStages(tc.req); !slices.Equal(got, tc.want) {
			t.Errorf("%s: stages = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestExitReachHint(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	link := func(exit string, stage store.RelayStage) store.RelayLink {
		var l store.RelayLink
		l.ExitNodeID = exit
		b, err := json.Marshal(store.RelayCheck{At: now.Unix(), ReverseReach: stage})
		if err != nil {
			t.Fatal(err)
		}
		l.CheckJSON = string(b)
		return l
	}
	stage := func(status, code string, age time.Duration) store.RelayStage {
		return store.RelayStage{Status: status, Code: code, At: now.Add(-age).Unix()}
	}
	for _, tc := range []struct {
		name   string
		links  []store.RelayLink
		want   string
		source string
		port   uint16 // the entry's endpoint now; 0 = 443
	}{
		{name: "no relay of this pair", links: []store.RelayLink{link("nod_other", stage("ok", "", time.Minute))}, want: "no_data"},
		{name: "never ran", links: []store.RelayLink{link("nod_de1", store.RelayStage{})}, want: "no_data"},
		{name: "passed", links: []store.RelayLink{link("nod_de1", stage("ok", "", time.Hour))}, want: "reach", source: "exit_probe"},
		{name: "no answer", links: []store.RelayLink{link("nod_de1", stage("fail", "timeout", time.Hour))}, want: "blocked", source: "exit_probe"},
		{name: "answered by something else", links: []store.RelayLink{link("nod_de1", stage("fail", "other", time.Hour))}, want: "partial", source: "exit_probe"},
		{name: "refused is no finding about reach", links: []store.RelayLink{link("nod_de1", stage("skipped", "entry_site_unavailable", time.Hour))}, want: "no_data"},
		{name: "agent too old", links: []store.RelayLink{link("nod_de1", stage("skipped", "unsupported", time.Hour))}, want: "no_data"},
		{name: "the exit's agent offline says nothing", links: []store.RelayLink{link("nod_de1", stage("fail", "exit_offline", time.Hour))}, want: "no_data"},
		{name: "a probe of another endpoint of the entry says nothing", links: []store.RelayLink{link("nod_de1", store.RelayStage{Status: "ok", Port: 8443, At: now.Add(-time.Hour).Unix()})}, want: "no_data"},
		{name: "a probe of the endpoint the entry has now counts", links: []store.RelayLink{link("nod_de1", store.RelayStage{Status: "ok", Port: 8443, At: now.Add(-time.Hour).Unix()})}, port: 8443, want: "reach", source: "exit_probe"},
		{name: "too old", links: []store.RelayLink{link("nod_de1", stage("ok", "", 25*time.Hour))}, want: "no_data"},
		{name: "the newest of two relays wins", links: []store.RelayLink{link("nod_de1", stage("ok", "", 3*time.Hour)),
			link("nod_de1", stage("fail", "timeout", time.Hour))}, want: "blocked", source: "exit_probe"},
	} {
		got := exitReachHint(tc.links, "nod_de1", cmp.Or(tc.port, 443), now)
		if got.Verdict != tc.want || got.Source != tc.source || (tc.want != "no_data") != (got.AtUnix != 0) {
			t.Errorf("%s: hint = %+v, want %s from %q", tc.name, got, tc.want, tc.source)
		}
	}
}
