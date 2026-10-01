package auth

import (
	"slices"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/store"
)

// The owner's card gets the codes the panel stored with the plan; a plan from before codes still shows its text.
func TestApprovalCarriesCodes(t *testing.T) {
	p := store.MCPPlan{
		ID: "pln_1", Tool: "node_fix", Status: store.PlanFailed, CreatedAt: time.Unix(100, 0), ExpiresAt: time.Unix(700, 0),
		FactsJSON: `[{"key":"node","value":"de1","untrusted":true},` +
			`{"key":"fix","value":"restart_inbound","code":"restart_inbound","params":{"profile":"hy2 · 443","port":"443"},"untrusted_params":["profile"]},` +
			`{"key":"duration","value":"3600 s","code":"seconds","params":{"n":"3600"}}]`,
		Danger: `["fleet"]`, Error: "failed_precondition: no trusted bundle", OutcomeCode: "no_trusted_bundle", OutcomeParams: `{"detail":"x"}`,
	}
	a := toProtoApproval(p)
	if len(a.Facts) != 3 || !a.Facts[0].Untrusted || a.Facts[0].Code != "" {
		t.Fatalf("facts: %+v", a.Facts)
	}
	if f := a.Facts[1]; f.Code != "restart_inbound" || f.Params["profile"] != "hy2 · 443" || !slices.Equal(f.UntrustedParams, []string{"profile"}) || f.Untrusted {
		t.Errorf("fix fact: %+v", f)
	}
	if f := a.Facts[2]; f.Code != "seconds" || f.Params["n"] != "3600" {
		t.Errorf("duration fact: %+v", f)
	}
	if a.OutcomeCode != "no_trusted_bundle" || a.OutcomeParams["detail"] != "x" || a.Error == "" {
		t.Errorf("outcome: %q %v", a.OutcomeCode, a.OutcomeParams)
	}
	// older plans: no code, the text stays
	p.OutcomeCode, p.OutcomeParams, p.FactsJSON = "", "{}", `[{"key":"effect","value":"they can connect again"}]`
	if a := toProtoApproval(p); a.OutcomeCode != "" || len(a.OutcomeParams) != 0 || a.Facts[0].Code != "" || a.Facts[0].Value != "they can connect again" {
		t.Errorf("an old plan: %+v", a)
	}
}
