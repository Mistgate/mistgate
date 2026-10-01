package dns

import (
	"strings"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// Preset changes are in the audit log by name; the default preset only when it really changes (the subscription
// settings page saves it with every edit), and no row holds a server list.
func TestPresetChangesAreAudited(t *testing.T) {
	e := newEnv(t)
	p, err := e.create("Home", plain("10.0.0.53"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.UpdateDnsPreset(e.ctx, connect.NewRequest(&adminv1.UpdateDnsPresetRequest{Id: p.Id, Name: "Home DNS", Servers: []*adminv1.DnsServer{plain("10.0.0.54")}})); err != nil {
		t.Fatal(err)
	}
	if err := e.s.SetDefaultPresetID(e.ctx, p.Id); err != nil {
		t.Fatal(err)
	}
	if err := e.s.SetDefaultPresetID(e.ctx, p.Id); err != nil { // saved again, unchanged
		t.Fatal(err)
	}
	if err := e.s.SetDefaultPresetID(e.ctx, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.DeleteDnsPreset(e.ctx, connect.NewRequest(&adminv1.DeleteDnsPresetRequest{Id: p.Id})); err != nil {
		t.Fatal(err)
	}
	rows, err := e.st.ListAudit(e.ctx, "", 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for i := len(rows) - 1; i >= 0; i-- {
		r := rows[i]
		got = append(got, r.Action)
		if strings.Contains(r.Params, "10.0.0.5") {
			t.Errorf("%s carries a server: %s", r.Action, r.Params)
		}
	}
	if strings.Join(got, ",") != "preset_create,preset_update,preset_default,preset_default,preset_delete" {
		t.Fatalf("audit rows: %v", got)
	}
	// newest first: the delete, back to the built-in default, the default set, the update
	if !strings.Contains(rows[0].Params, `"name":"Home DNS"`) || !strings.Contains(rows[1].Params, `"preset":""`) ||
		!strings.Contains(rows[2].Params, `"name":"Home DNS"`) || !strings.Contains(rows[3].Params, `"name":"Home DNS"`) {
		t.Errorf("params: %+v", rows)
	}
}
