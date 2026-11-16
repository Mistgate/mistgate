package fleet

import (
	"encoding/json"
	"testing"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func TestHelloInfoCleansNodeReportedDisplayText(t *testing.T) {
	hello := &agentv1.Hello{
		AgentVersion: "v1\u202e.2",
		Facts: &agentv1.HostFacts{
			Hostname: "de\u202e1", Os: "Debian\u2066", Kernel: "6.1\u200b.0", Arch: "amd64", Virt: "kvm",
		},
		Engines:    []*agentv1.EngineInfo{{Protocol: "awg\u200f", Version: "1.2.3"}},
		LastUpdate: &agentv1.LastUpdate{Outcome: agentv1.UpdateOutcome_UPDATE_OUTCOME_FAILED, FromVersion: "v1\u202e.0", Reason: "bad\u2066signature"},
	}

	info := helloInfo(hello)
	if info.AgentVersion != "v1.2" || info.Facts.Hostname != "de1" || info.Facts.OS != "Debian" || info.Facts.Kernel != "6.1.0" || info.Facts.Engines[0].Protocol != "awg" {
		t.Fatalf("node display text kept format controls: %+v", info)
	}
	var update store.LastUpdateRow
	if err := json.Unmarshal([]byte(info.LastUpdateJSON), &update); err != nil {
		t.Fatal(err)
	}
	if update.FromVersion != "v1.0" || update.Reason != "badsignature" {
		t.Fatalf("node update display text kept format controls: %+v", update)
	}
}
