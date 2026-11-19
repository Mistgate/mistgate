package access

import (
	"fmt"
	"net/url"
	"strings"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The three calls that put an inbound on a node (CreateInbound, UpdateInbound, UpdateProfile) admit it by the same rules:
// a refusal for the port, the warnings, and the loss check of a UDP port. This table is those rules as the calls show them,
// for a UDP profile on a node with no neighbour: one row per call, kind of change, check or real run, state of the port's
// cache and AllowLossyPort.

type admitOutcome struct {
	refusal string // the code word with the values worth pinning, "" when the call went through
	warns   string // the warning codes with their port
	checks  int    // CheckPorts calls
	audited int    // "the admin chose a lossy port" rows
	written bool   // the call changed something in the store
}

func (o admitOutcome) String() string {
	return fmt.Sprintf("refusal=%s warns=%s checks=%d audited=%d written=%v", o.refusal, o.warns, o.checks, o.audited, o.written)
}

func refusalOf(err error) string {
	if err == nil {
		return ""
	}
	msg := connect.CodeOf(err).String() + ": "
	head, query, _ := strings.Cut(strings.TrimPrefix(err.Error(), msg), ": ")
	got, _ := url.ParseQuery(query)
	out := head
	for _, k := range []string{"port", "free"} {
		if v := got.Get(k); v != "" {
			out += " " + k + "=" + v
		}
	}
	return out
}

func warnsOf(ws []*adminv1.StatusReason) string {
	var out []string
	for _, w := range ws {
		item := w.Code
		if p := w.Params["port"]; p != "" {
			item += ":" + p
		}
		out = append(out, item)
	}
	return strings.Join(out, ",")
}

func (e *env) auditedLossy() int {
	n := 0
	for _, row := range must(e.st.ListAudit(e.ctx, "", 0, 200)) {
		if row.Action == "port_lossy_override" {
			n++
		}
	}
	return n
}

// admitCase runs one call. rpc: create | inbound | profile. move: port (the port changes to 8443), same (the change keeps the
// port), on (an inbound that was off is switched on). cache: clean (no row; a real check finds the port fine), cached (a
// broken row is stored for the port the inbound ends on, a real check finds it broken too), live (no row; a real check
// finds it broken).
func admitCase(t *testing.T, rpc, move, cache string, check, allow bool) admitOutcome {
	t.Helper()
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	p := e.profile("main", `{"tls_mode":"self_signed"}`)
	var inboundID string
	if rpc != "create" {
		inboundID = e.inbound(p.Id, "nod_de1").Id
	}
	if move == "on" {
		e.sql(`UPDATE inbound SET enabled = 0 WHERE id = ?`, inboundID)
	}
	target := uint16(443)
	if move == "port" {
		target = 8443
	}
	if cache == "cached" {
		savePortResult(t, e, portResult(e, "nod_de1", target, "broken", 300, 100))
	}
	checker := usePortChecker(e, func(node string, port uint16) (store.PortCheck, bool) {
		if port == target && cache != "clean" {
			return portResult(e, node, port, "broken", 300, 100), true
		}
		return portResult(e, node, port, "ok", 300, 300), true
	}, "")
	stored := func() string {
		return fmt.Sprintf("%d %d %d %d %d", e.count(`SELECT count(*) FROM inbound`), e.count(`SELECT COALESCE(sum(spec_version), 0) FROM inbound`),
			e.count(`SELECT COALESCE(sum(enabled), 0) FROM inbound`), e.count(`SELECT COALESCE(sum(COALESCE(port_override, 0)), 0) FROM inbound`),
			e.count(`SELECT COALESCE(sum(version), 0) FROM profile`))
	}
	before := stored()

	var warnings []*adminv1.StatusReason
	var err error
	switch rpc {
	case "create":
		var r *connect.Response[adminv1.CreateInboundResponse]
		if r, err = e.s.CreateInbound(e.ctx, req(&adminv1.CreateInboundRequest{
			ProfileId: p.Id, NodeId: "nod_de1", PortOverride: 8443, ValidateOnly: check, AllowLossyPort: allow,
		})); err == nil {
			warnings = r.Msg.Warnings
		}
	case "inbound":
		m := &adminv1.UpdateInboundRequest{InboundId: inboundID, ValidateOnly: check, AllowLossyPort: allow}
		switch move {
		case "port":
			m.PortOverride = new(uint32(8443))
		case "same":
			m.TlsServerNameOverride = new("vpn.example.com")
		case "on":
			m.Enabled = new(true)
		}
		var r *connect.Response[adminv1.UpdateInboundResponse]
		if r, err = e.s.UpdateInbound(e.ctx, req(m)); err == nil {
			warnings = r.Msg.Warnings
		}
	case "profile":
		settings := `{"tls_mode":"self_signed","down_mbps":50}`
		if move == "port" {
			settings = `{"tls_mode":"self_signed","port":8443}`
		}
		var r *connect.Response[adminv1.UpdateProfileResponse]
		if r, err = e.s.UpdateProfile(e.ctx, req(&adminv1.UpdateProfileRequest{
			ProfileId: p.Id, ExpectedVersion: p.Version, SettingsJson: &settings, DryRun: check, AllowLossyPort: allow,
		})); err == nil {
			warnings = r.Msg.Profile.Warnings
		}
	}
	calls, _ := checker.snapshot()
	return admitOutcome{refusal: refusalOf(err), warns: warnsOf(warnings), checks: len(calls), audited: e.auditedLossy(), written: stored() != before}
}

func TestInboundAdmissionTable(t *testing.T) {
	type key struct {
		rpc, move, cache string
		check, allow     bool
	}
	name := func(k key) string {
		return fmt.Sprintf("%s/%s/%s/check=%v/allow=%v", k.rpc, k.move, k.cache, k.check, k.allow)
	}
	var cases []key
	for _, rm := range [][2]string{{"create", "port"}, {"inbound", "port"}, {"inbound", "same"}, {"inbound", "on"}, {"profile", "port"}, {"profile", "same"}} {
		for _, cache := range []string{"clean", "cached", "live"} {
			for _, check := range []bool{true, false} {
				for _, allow := range []bool{false, true} {
					cases = append(cases, key{rm[0], rm[1], cache, check, allow})
				}
			}
		}
	}
	for _, k := range cases {
		t.Run(name(k), func(t *testing.T) {
			got := admitCase(t, k.rpc, k.move, k.cache, k.check, k.allow).String()
			if want, ok := wantAdmission[name(k)]; !ok {
				t.Errorf("no expectation for %s: %s", name(k), got)
			} else if got != want {
				t.Errorf("%s:\n got  %s\n want %s", name(k), got, want)
			}
		})
	}
}

// wantAdmission is what the calls answered before the admission was one function, as admitCase reports it.
var wantAdmission = map[string]string{
	"create/port/cached/check=false/allow=false":  "refusal=port_lossy port=8443 free=443 warns= checks=1 audited=0 written=false",
	"create/port/cached/check=false/allow=true":   "refusal= warns=port_lossy:8443 checks=1 audited=1 written=true",
	"create/port/cached/check=true/allow=false":   "refusal=port_lossy port=8443 free=4443 warns= checks=0 audited=0 written=false",
	"create/port/cached/check=true/allow=true":    "refusal= warns=port_lossy:8443 checks=0 audited=0 written=false",
	"create/port/clean/check=false/allow=false":   "refusal= warns= checks=1 audited=0 written=true",
	"create/port/clean/check=false/allow=true":    "refusal= warns= checks=1 audited=0 written=true",
	"create/port/clean/check=true/allow=false":    "refusal= warns= checks=0 audited=0 written=false",
	"create/port/clean/check=true/allow=true":     "refusal= warns= checks=0 audited=0 written=false",
	"create/port/live/check=false/allow=false":    "refusal=port_lossy port=8443 free=443 warns= checks=1 audited=0 written=false",
	"create/port/live/check=false/allow=true":     "refusal= warns=port_lossy:8443 checks=1 audited=1 written=true",
	"create/port/live/check=true/allow=false":     "refusal= warns= checks=0 audited=0 written=false",
	"create/port/live/check=true/allow=true":      "refusal= warns= checks=0 audited=0 written=false",
	"inbound/on/cached/check=false/allow=false":   "refusal=port_lossy port=443 free=8443 warns= checks=1 audited=0 written=false",
	"inbound/on/cached/check=false/allow=true":    "refusal= warns=port_lossy:443 checks=1 audited=1 written=true",
	"inbound/on/cached/check=true/allow=false":    "refusal=port_lossy port=443 free=8443 warns= checks=0 audited=0 written=false",
	"inbound/on/cached/check=true/allow=true":     "refusal= warns=port_lossy:443 checks=0 audited=0 written=false",
	"inbound/on/clean/check=false/allow=false":    "refusal= warns= checks=1 audited=0 written=true",
	"inbound/on/clean/check=false/allow=true":     "refusal= warns= checks=1 audited=0 written=true",
	"inbound/on/clean/check=true/allow=false":     "refusal= warns= checks=0 audited=0 written=false",
	"inbound/on/clean/check=true/allow=true":      "refusal= warns= checks=0 audited=0 written=false",
	"inbound/on/live/check=false/allow=false":     "refusal=port_lossy port=443 free=8443 warns= checks=1 audited=0 written=false",
	"inbound/on/live/check=false/allow=true":      "refusal= warns=port_lossy:443 checks=1 audited=1 written=true",
	"inbound/on/live/check=true/allow=false":      "refusal= warns= checks=0 audited=0 written=false",
	"inbound/on/live/check=true/allow=true":       "refusal= warns= checks=0 audited=0 written=false",
	"inbound/port/cached/check=false/allow=false": "refusal=port_lossy port=8443 free=443 warns= checks=1 audited=0 written=false",
	"inbound/port/cached/check=false/allow=true":  "refusal= warns=port_lossy:8443 checks=1 audited=1 written=true",
	"inbound/port/cached/check=true/allow=false":  "refusal=port_lossy port=8443 free=4443 warns= checks=0 audited=0 written=false",
	"inbound/port/cached/check=true/allow=true":   "refusal= warns=port_lossy:8443 checks=0 audited=0 written=false",
	"inbound/port/clean/check=false/allow=false":  "refusal= warns= checks=1 audited=0 written=true",
	"inbound/port/clean/check=false/allow=true":   "refusal= warns= checks=1 audited=0 written=true",
	"inbound/port/clean/check=true/allow=false":   "refusal= warns= checks=0 audited=0 written=false",
	"inbound/port/clean/check=true/allow=true":    "refusal= warns= checks=0 audited=0 written=false",
	"inbound/port/live/check=false/allow=false":   "refusal=port_lossy port=8443 free=443 warns= checks=1 audited=0 written=false",
	"inbound/port/live/check=false/allow=true":    "refusal= warns=port_lossy:8443 checks=1 audited=1 written=true",
	"inbound/port/live/check=true/allow=false":    "refusal= warns= checks=0 audited=0 written=false",
	"inbound/port/live/check=true/allow=true":     "refusal= warns= checks=0 audited=0 written=false",
	"inbound/same/cached/check=false/allow=false": "refusal= warns=port_lossy:443 checks=0 audited=0 written=true",
	"inbound/same/cached/check=false/allow=true":  "refusal= warns=port_lossy:443 checks=0 audited=0 written=true",
	"inbound/same/cached/check=true/allow=false":  "refusal= warns=port_lossy:443 checks=0 audited=0 written=false",
	"inbound/same/cached/check=true/allow=true":   "refusal= warns=port_lossy:443 checks=0 audited=0 written=false",
	"inbound/same/clean/check=false/allow=false":  "refusal= warns= checks=0 audited=0 written=true",
	"inbound/same/clean/check=false/allow=true":   "refusal= warns= checks=0 audited=0 written=true",
	"inbound/same/clean/check=true/allow=false":   "refusal= warns= checks=0 audited=0 written=false",
	"inbound/same/clean/check=true/allow=true":    "refusal= warns= checks=0 audited=0 written=false",
	"inbound/same/live/check=false/allow=false":   "refusal= warns= checks=0 audited=0 written=true",
	"inbound/same/live/check=false/allow=true":    "refusal= warns= checks=0 audited=0 written=true",
	"inbound/same/live/check=true/allow=false":    "refusal= warns= checks=0 audited=0 written=false",
	"inbound/same/live/check=true/allow=true":     "refusal= warns= checks=0 audited=0 written=false",
	"profile/port/cached/check=false/allow=false": "refusal=port_lossy port=8443 free=443 warns= checks=1 audited=0 written=false",
	"profile/port/cached/check=false/allow=true":  "refusal= warns=port_lossy:8443 checks=1 audited=1 written=true",
	"profile/port/cached/check=true/allow=false":  "refusal=port_lossy port=8443 free=4443 warns= checks=0 audited=0 written=false",
	"profile/port/cached/check=true/allow=true":   "refusal= warns=port_lossy:8443 checks=0 audited=0 written=false",
	"profile/port/clean/check=false/allow=false":  "refusal= warns= checks=1 audited=0 written=true",
	"profile/port/clean/check=false/allow=true":   "refusal= warns= checks=1 audited=0 written=true",
	"profile/port/clean/check=true/allow=false":   "refusal= warns= checks=0 audited=0 written=false",
	"profile/port/clean/check=true/allow=true":    "refusal= warns= checks=0 audited=0 written=false",
	"profile/port/live/check=false/allow=false":   "refusal=port_lossy port=8443 free=443 warns= checks=1 audited=0 written=false",
	"profile/port/live/check=false/allow=true":    "refusal= warns=port_lossy:8443 checks=1 audited=1 written=true",
	"profile/port/live/check=true/allow=false":    "refusal= warns= checks=0 audited=0 written=false",
	"profile/port/live/check=true/allow=true":     "refusal= warns= checks=0 audited=0 written=false",
	"profile/same/cached/check=false/allow=false": "refusal= warns=port_lossy:443 checks=0 audited=0 written=true",
	"profile/same/cached/check=false/allow=true":  "refusal= warns=port_lossy:443 checks=0 audited=0 written=true",
	"profile/same/cached/check=true/allow=false":  "refusal= warns=port_lossy:443 checks=0 audited=0 written=false",
	"profile/same/cached/check=true/allow=true":   "refusal= warns=port_lossy:443 checks=0 audited=0 written=false",
	"profile/same/clean/check=false/allow=false":  "refusal= warns= checks=0 audited=0 written=true",
	"profile/same/clean/check=false/allow=true":   "refusal= warns= checks=0 audited=0 written=true",
	"profile/same/clean/check=true/allow=false":   "refusal= warns= checks=0 audited=0 written=false",
	"profile/same/clean/check=true/allow=true":    "refusal= warns= checks=0 audited=0 written=false",
	"profile/same/live/check=false/allow=false":   "refusal= warns= checks=0 audited=0 written=true",
	"profile/same/live/check=false/allow=true":    "refusal= warns= checks=0 audited=0 written=true",
	"profile/same/live/check=true/allow=false":    "refusal= warns= checks=0 audited=0 written=false",
	"profile/same/live/check=true/allow=true":     "refusal= warns= checks=0 audited=0 written=false",
}

// UpdateProfile tells what the same change through the inbound would: a profile that stands where another one of the
// node holds says so before the write, as the check of the dialog does.
func TestUpdateProfileWarnsAsTheInboundDoes(t *testing.T) {
	update := func(e *env, p *adminv1.ProfileSummary, settings string, dryRun bool) []*adminv1.StatusReason {
		t.Helper()
		r := must(e.s.UpdateProfile(e.ctx, req(&adminv1.UpdateProfileRequest{ProfileId: p.Id, ExpectedVersion: p.Version, SettingsJson: &settings, DryRun: dryRun})))
		return r.Msg.Profile.Warnings
	}
	for _, dryRun := range []bool{true, false} {
		t.Run(fmt.Sprintf("a change of Hysteria2 whose site VLESS has paused dry_run=%v", dryRun), func(t *testing.T) {
			e := newEnv(t)
			e.node("nod_de1", "de1", "de1.example.com", "active")
			vless := e.vlessProfile("vless")
			hy2 := e.profile("hy2", `{"tls_mode":"self_signed"}`)
			e.inbound(vless.Id, "nod_de1")
			e.inbound(hy2.Id, "nod_de1")
			got := update(e, hy2, `{"tls_mode":"self_signed","down_mbps":50}`, dryRun)
			if len(got) != 1 || got[0].Code != "hy2_decoy_paused" || got[0].Params["port"] != "443" || got[0].Params["hy2"] != "hy2" || got[0].Params["vless"] != "vless" {
				t.Fatalf("warnings = %+v", got)
			}
		})
		t.Run(fmt.Sprintf("VLESS moves onto the site port of Hysteria2 dry_run=%v", dryRun), func(t *testing.T) {
			e := newEnv(t)
			e.node("nod_de1", "de1", "de1.example.com", "active")
			vless := must(e.s.CreateProfile(e.ctx, req(&adminv1.CreateProfileRequest{
				Protocol: "vless", Name: "vless", SettingsJson: `{"port":8443,"reality":{"target":"www.example.org:443"}}`,
			}))).Msg.Profile
			hy2 := e.profile("hy2", `{"tls_mode":"self_signed"}`) // the site of the default is on 443
			e.inbound(vless.Id, "nod_de1")
			e.inbound(hy2.Id, "nod_de1")
			got := update(e, vless, `{"port":443,"reality":{"target":"www.example.org:443"}}`, dryRun)
			if len(got) != 1 || got[0].Code != "hy2_decoy_paused" || got[0].Params["port"] != "443" {
				t.Fatalf("warnings = %+v", got)
			}
		})
		t.Run(fmt.Sprintf("a WARP exit on a node without an account dry_run=%v", dryRun), func(t *testing.T) {
			e := newEnv(t)
			e.node("nod_de1", "de1", "de1.example.com", "active")
			p := e.profile("main", `{"tls_mode":"self_signed","port":8443}`)
			e.inbound(p.Id, "nod_de1")
			got := update(e, p, `{"tls_mode":"self_signed","port":8443,"egress":"warp"}`, dryRun)
			if len(got) != 1 || got[0].Code != "warp_missing" || got[0].Params["state"] != "none" {
				t.Fatalf("warnings = %+v", got)
			}
		})
	}
}
