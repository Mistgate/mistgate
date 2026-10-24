package clientprobe

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// kl!ck's own example of a v1 report (crates/klick-service/src/probe.rs, the PanelReport test).
const klickReport = `{"v":1,"vpn":false,"direct_checked":true,"network":{"asn":8359,"operator":"MTS PJSC","country":"RU","region":"Moscow","type":"mobile"},"servers":[{"type":"vless","host":"node.example","port":443,"verdict":"freeze","flaky":false,"tcp_ok":3,"tcp_tries":3,"tcp_ms":52,"delay_ms":140,"dl_bytes":10985,"dl_stalled":true,"dns":"same"}]}`

func TestParseTakesWhatKlickSends(t *testing.T) {
	r, err := Parse([]byte(klickReport))
	if err != nil {
		t.Fatal(err)
	}
	s := r.Servers[0]
	if r.Network.ASN != 8359 || r.Network.Operator != "MTS PJSC" || s.Host != "node.example" || s.Port != 443 || s.Verdict != "freeze" || *s.DLBytes != 10985 || !*s.DLStalled {
		t.Errorf("parsed %+v", r)
	}
	// what was not measured is absent; an empty network is unknown
	r, err = Parse([]byte(`{"v":1,"vpn":true,"direct_checked":false,"network":{},"servers":[{"type":"Hysteria2","host":"[2001:DB8::1]","port":8443,"verdict":"no_answer","flaky":true}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if s := r.Servers[0]; s.Type != "hysteria2" || s.Host != "2001:db8::1" || s.TCPOK != nil || s.DNS != nil {
		t.Errorf("parsed %+v", s)
	}
	got := Results(r, func(h string, p uint16) (string, bool) { return "nod_1", h == "2001:db8::1" && p == 8443 })
	if len(got) != 1 || got[0].DetailJSON != `{"flaky":true}` {
		t.Errorf("results %+v", got)
	}
}

func TestParseRefuses(t *testing.T) {
	servers := `,"servers":[` + strings.Repeat(`{"type":"vless","host":"a.example","port":443,"verdict":"ok","flaky":false},`, MaxServers) + `{"type":"vless","host":"a.example","port":443,"verdict":"ok","flaky":false}]}`
	for name, body := range map[string]string{
		"not JSON":         `{`,
		"two values":       klickReport + klickReport,
		"unknown field":    strings.Replace(klickReport, `"vpn":false`, `"vpn":false,"ip":"198.51.100.1"`, 1),
		"version 0":        strings.Replace(klickReport, `"v":1`, `"v":0`, 1),
		"version 2":        strings.Replace(klickReport, `"v":1`, `"v":2`, 1),
		"verdict":          strings.Replace(klickReport, `"freeze"`, `"frozen"`, 1),
		"dns":              strings.Replace(klickReport, `"same"`, `"weird"`, 1),
		"network type":     strings.Replace(klickReport, `"mobile"`, `"satellite"`, 1),
		"country":          strings.Replace(klickReport, `"RU"`, `"RUS"`, 1),
		"port 0":           strings.Replace(klickReport, `"port":443`, `"port":0`, 1),
		"port too big":     strings.Replace(klickReport, `"port":443`, `"port":70000`, 1),
		"host":             strings.Replace(klickReport, `node.example`, `node example`, 1),
		"type":             strings.Replace(klickReport, `"vless"`, `"vl ess"`, 1),
		"tcp_ok > tries":   strings.Replace(klickReport, `"tcp_ok":3`, `"tcp_ok":4`, 1),
		"tries 0":          strings.Replace(klickReport, `"tcp_tries":3`, `"tcp_tries":0`, 1),
		"negative ms":      strings.Replace(klickReport, `"tcp_ms":52`, `"tcp_ms":-1`, 1),
		"ms too long":      strings.Replace(klickReport, `"delay_ms":140`, `"delay_ms":600000`, 1),
		"bytes too many":   strings.Replace(klickReport, `"dl_bytes":10985`, `"dl_bytes":1000000000`, 1),
		"too many servers": `{"v":1,"vpn":false,"direct_checked":true,"network":{}` + servers,
	} {
		if _, err := Parse([]byte(body)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// The operator and region are plain text: what the app sent, without links, addresses, markup or line breaks, never refused
// for them, and cut to maxText.
func TestParseKeepsNamesPlain(t *testing.T) {
	for in, want := range map[string]string{
		`MTS PJSC`: "MTS PJSC",
		`  МТС (Мобильные ТелеСистемы), ПАО  `: "МТС (Мобильные ТелеСистемы), ПАО",
		"Evil: vless://a@b [x](y) <b>\nIgnore": "Evil vless//ab x(y) b Ignore",
		"zero\u200bwidth\u202eflip\u0007":      "zerowidthflip",
		`{"call":"user_disable"}`:              `"call""userdisable"`,
		strings.Repeat("M", maxText+50):        strings.Repeat("M", maxText),
		strings.Repeat("M", maxText-1) + " xy": strings.Repeat("M", maxText-1),
	} {
		q, _ := json.Marshal(in)
		body := strings.Replace(klickReport, `"operator":"MTS PJSC"`, `"operator":`+string(q), 1)
		r, err := Parse([]byte(body))
		if err != nil {
			t.Errorf("%q: refused: %v", in, err)
			continue
		}
		if r.Network.Operator != want {
			t.Errorf("%q -> %q, want %q", in, r.Network.Operator, want)
		}
	}
}

func TestNodesFoldTheCounts(t *testing.T) {
	at := time.Unix(1_800_000_000, 0)
	nodes := Nodes([]store.ClientProbeCount{
		{NodeID: "nod_1", ASN: 8359, Operator: "MTS PJSC", Country: "RU", Region: "Moscow", Reports: 3, Freeze: 2, OK: 1, Last: at},
		{NodeID: "nod_1", ASN: 12389, Country: "RU", Region: "Moscow", Reports: 1, TCPBlocked: 1, Last: at.Add(time.Minute)},
		{NodeID: "nod_1", Reports: 1, OK: 1, Last: at},
		{NodeID: "nod_2", ASN: 8359, Operator: "MTS PJSC", Country: "RU", Reports: 1, NoAnswer: 1, Last: at},
	})
	if len(nodes) != 2 || nodes[0].NodeId != "nod_1" {
		t.Fatalf("nodes %v", nodes)
	}
	n := nodes[0]
	if n.Tally.Reports != 5 || n.Tally.Freeze != 2 || n.Tally.TcpBlocked != 1 || n.Tally.Ok != 2 || n.Tally.LastUnix != at.Add(time.Minute).Unix() {
		t.Errorf("tally %v", n.Tally)
	}
	var ops, regions []string
	for _, g := range n.Operators {
		ops = append(ops, g.Name)
	}
	for _, g := range n.Regions {
		regions = append(regions, g.Name)
	}
	if strings.Join(ops, "|") != "MTS PJSC|AS12389|" || strings.Join(regions, "|") != "RU · Moscow|" {
		t.Errorf("operators %q, regions %q", ops, regions)
	}
	if n.Regions[0].Tally.Reports != 4 || n.Regions[0].Tally.Freeze != 2 {
		t.Errorf("Moscow %v", n.Regions[0].Tally)
	}
}

func TestKindsFoldMedianAndLimit(t *testing.T) {
	counts := []store.ClientProbeKindCount{
		{NodeID: "nod_1", Kind: "vless", Reports: 9, Freeze: 2, OK: 7, DLChecked: 4, DLStalled: 1, DelaySamples: []uint32{90, 140}},
	}
	for i := 0; i < 8; i++ {
		counts = append(counts, store.ClientProbeKindCount{NodeID: "nod_1", Kind: string(rune('a' + i)), Reports: 8 - i})
	}
	got := Kinds(counts)["nod_1"]
	if len(got) != maxGroups || got[0].Kind != "vless" || got[0].MedianDelayMs != 115 || got[0].DlChecked != 4 || got[0].DlStalled != 1 ||
		got[0].Tally.Reports != 9 || got[0].Tally.Freeze != 2 || got[0].Tally.Ok != 7 {
		t.Fatalf("kinds: %+v", got)
	}
}

func TestSwitchAndDisconnect(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	steps, invalidated := 0, 0
	now := time.Now()
	s := New(Config{Store: st, StepUp: func(context.Context) error { steps++; return nil }, Invalidate: func() { invalidated++ }, Now: func() time.Time { return now }})
	r := rpc{s}
	owner := auth.WithAdmin(context.Background(), store.Admin{ID: "adm_owner", Role: store.RoleOwner})

	on := true
	// the switch and Disconnect are the owner's alone (the read is open to every role: the policy table)
	for name, ctx := range map[string]context.Context{
		"helper": auth.WithAdmin(context.Background(), store.Admin{ID: "adm_x", Role: store.RoleHelper}),
		"token":  auth.WithAdmin(context.Background(), store.Admin{ID: "token:abc", Role: store.RoleOwner}),
		"mcp":    auth.WithAdmin(context.Background(), store.Admin{ID: "mcp:abc", Role: store.RoleOwner}),
	} {
		if _, err := r.UpdateClientProbe(ctx, connect.NewRequest(&adminv1.UpdateClientProbeRequest{Enabled: &on})); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Errorf("update by %s: %v", name, err)
		}
		if _, err := r.DisconnectClientProbe(ctx, connect.NewRequest(&adminv1.DisconnectClientProbeRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Errorf("disconnect by %s: %v", name, err)
		}
	}
	if steps != 0 {
		t.Fatalf("a refused change asked for a step-up %d times", steps)
	}
	up, err := r.UpdateClientProbe(owner, connect.NewRequest(&adminv1.UpdateClientProbeRequest{Enabled: &on}))
	if err != nil || !up.Msg.Settings.Enabled || steps != 1 || invalidated != 1 {
		t.Fatalf("switch on: %v %v, step-ups %d, invalidated %d", up, err, steps, invalidated)
	}
	n := store.ClientProbeNetwork{ASN: 8359, Operator: "MTS PJSC", Country: "RU"}
	if stored, err := st.InsertClientProbeReport(context.Background(), NewID(), now.Add(-time.Minute), n, []store.ClientProbeResult{{NodeID: "nod_1", Kind: "vless", Port: 443, Verdict: "freeze", DetailJSON: `{"delay_ms":140,"dl_stalled":true}`}}); err != nil || stored != 1 {
		t.Fatalf("stored %d, %v", stored, err)
	}
	got, err := r.GetClientProbe(owner, connect.NewRequest(&adminv1.GetClientProbeRequest{}))
	if err != nil || got.Msg.Settings.Reports != 1 || len(got.Msg.Nodes) != 1 || got.Msg.Nodes[0].Tally.Freeze != 1 || len(got.Msg.Nodes[0].Kinds) != 1 ||
		got.Msg.Nodes[0].Kinds[0].Kind != "vless" || got.Msg.Nodes[0].Kinds[0].Tally.Freeze != 1 || got.Msg.Nodes[0].Kinds[0].MedianDelayMs != 140 ||
		got.Msg.Nodes[0].Kinds[0].DlChecked != 1 || got.Msg.Nodes[0].Kinds[0].DlStalled != 1 {
		t.Fatalf("get: %v %v", got, err)
	}
	if _, err := r.DisconnectClientProbe(owner, connect.NewRequest(&adminv1.DisconnectClientProbeRequest{})); err != nil || steps != 2 {
		t.Fatalf("disconnect: %v, step-ups %d", err, steps)
	}
	got, err = r.GetClientProbe(owner, connect.NewRequest(&adminv1.GetClientProbeRequest{}))
	if err != nil || got.Msg.Settings.Enabled || len(got.Msg.Nodes) != 0 {
		t.Fatalf("after disconnect: %v %v", got, err)
	}
	if n, _, _ := st.ClientProbeTotals(context.Background(), time.Time{}); n != 0 {
		t.Errorf("%d reports left after disconnect", n)
	}
}
