package access

import (
	"reflect"
	"strings"
	"testing"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func warningByCode(warnings []*adminv1.StatusReason, code string) *adminv1.StatusReason {
	for _, warning := range warnings {
		if warning.Code == code {
			return warning
		}
	}
	return nil
}

func TestUpdateInboundDirectInSubscriptionsValidateOnlyAndPreservesDesiredState(t *testing.T) {
	f := newRelayFix(t)
	f.sql("UPDATE inbound SET state = 'active' WHERE id = ?", f.server.Id)
	before := must(f.s.Desired(f.ctx, "nod_de1"))
	f.resetNotify()
	auditBefore := must(f.st.ListAuditQuery(f.ctx, store.AuditQuery{Actions: []string{"inbound_update"}, Limit: 50}))

	checked := must(f.s.UpdateInbound(f.ctx, req(&adminv1.UpdateInboundRequest{
		InboundId: f.server.Id, DirectInSubscriptions: new(false), ValidateOnly: true,
	}))).Msg
	warning := warningByCode(checked.Warnings, "inbound_no_way_in")
	if checked.Inbound.DirectInSubscriptions || warning == nil ||
		warning.Params["profile"] != "files" || warning.Params["node"] != "de1" || warning.Params["reason"] != "no_usable_relay" {
		t.Fatalf("validate-only result: inbound=%+v warnings=%+v", checked.Inbound, checked.Warnings)
	}
	in := must(f.st.Access().Inbound(f.ctx, f.server.Id))
	if !in.DirectInSubscriptions || in.State != "active" || f.notify.n.Load() != 0 {
		t.Fatalf("validate-only wrote inbound %+v or notified %d", in, f.notify.n.Load())
	}
	auditAfter := must(f.st.ListAuditQuery(f.ctx, store.AuditQuery{Actions: []string{"inbound_update"}, Limit: 50}))
	if len(auditAfter) != len(auditBefore) {
		t.Fatalf("validate-only wrote an audit event: before=%d after=%d", len(auditBefore), len(auditAfter))
	}

	updated := must(f.s.UpdateInbound(f.ctx, req(&adminv1.UpdateInboundRequest{
		InboundId: f.server.Id, DirectInSubscriptions: new(false),
	}))).Msg
	in = must(f.st.Access().Inbound(f.ctx, f.server.Id))
	after := must(f.s.Desired(f.ctx, "nod_de1"))
	rows := f.audited("inbound_update")
	if updated.Inbound.DirectInSubscriptions || in.DirectInSubscriptions || in.State != "active" || in.SpecVersion != 1 ||
		!reflect.DeepEqual(before, after) || f.notify.n.Load() != 0 || len(rows) != len(auditBefore)+1 ||
		!strings.Contains(rows[0].Params, "\"direct_in_subscriptions\":false") {
		t.Fatalf("direct-only update changed desired state or missed audit: inbound=%+v notify=%d desiredBefore=%+v desiredAfter=%+v audit=%+v",
			in, f.notify.n.Load(), before, after, rows)
	}
}

func TestUpdateInboundDirectInSubscriptionsNoWayInWarningCases(t *testing.T) {
	f := newTwinFix(t)
	turnOff := func(inboundID string) *adminv1.UpdateInboundResponse {
		t.Helper()
		return must(f.s.UpdateInbound(f.ctx, req(&adminv1.UpdateInboundRequest{
			InboundId: inboundID, DirectInSubscriptions: new(false), ValidateOnly: true,
		}))).Msg
	}
	warning := func(response *adminv1.UpdateInboundResponse) *adminv1.StatusReason {
		t.Helper()
		return warningByCode(response.Warnings, "inbound_no_way_in")
	}

	if got := warning(turnOff(f.server.Id)); got == nil {
		t.Fatal("no route warning missing when IPv6 and relays are absent")
	}
	r := must(f.create(f.server.Id, "nod_fi1", 8443))
	f.switchOn(r.Id)
	if got := warning(turnOff(f.server.Id)); got != nil {
		t.Errorf("usable relay warned: %+v", got)
	}

	f.sql("UPDATE relay SET enabled = 0 WHERE id = ?", r.Id)
	f.entry("nod_de1", entryAddr)
	if got := warning(turnOff(f.server.Id)); got != nil {
		t.Errorf("IPv6 entry warned: %+v", got)
	}

	cdn := f.vlessWith("cdn", "{\"security\":\"tls\",\"transport\":\"ws\",\"tls_mode\":\"acme_domain\",\"sni\":\"origin.example.com\",\"cdn\":{\"host\":\"cdn.example.org\",\"port\":443},\"ws\":{\"path\":\"/ws\",\"host\":\"\"}}")
	cdnIn := f.inbound(cdn.Id, "nod_nl1")
	if got := warning(turnOff(cdnIn.Id)); got != nil {
		t.Errorf("CDN route warned: %+v", got)
	}

	awg := must(f.s.CreateProfile(f.ctx, req(&adminv1.CreateProfileRequest{Protocol: "awg", Name: "awg"}))).Msg.Profile
	awgIn := f.inbound(awg.Id, "nod_nl1")
	got := warning(turnOff(awgIn.Id))
	if got == nil || got.Params["reason"] != "awg" || got.Params["profile"] != "awg" || got.Params["node"] != "nl1" {
		t.Errorf("AmneziaWG warning = %+v", got)
	}
}

func TestInboundProtoExposesDirectAndCDNFields(t *testing.T) {
	f := newEnv(t)
	f.node("nod_de1", "de1", "de1.example.com", "active")
	p := must(f.s.CreateProfile(f.ctx, req(&adminv1.CreateProfileRequest{
		Protocol: "vless", Name: "cdn",
		SettingsJson: "{\"security\":\"tls\",\"transport\":\"ws\",\"tls_mode\":\"acme_domain\",\"sni\":\"origin.example.com\",\"cdn\":{\"host\":\"cdn.example.org\",\"port\":443},\"ws\":{\"path\":\"/ws\",\"host\":\"\"}}",
	}))).Msg.Profile
	in := f.inbound(p.Id, "nod_de1")
	if !in.DirectInSubscriptions || !in.Cdn {
		t.Fatalf("inbound fields: %+v", in)
	}
}
