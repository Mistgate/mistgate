package subs

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/access"
	"github.com/mistgate/mistgate/internal/panel/dns"
	"github.com/mistgate/mistgate/internal/panel/instance"
)

// The body of sync, assembled from the plugins' own Mistgate settings (one source of truth for the settings text). The
// golden document is stored indented; the handler writes it compact.

const (
	hy2Settings = "../protocols/hysteria2/testdata/mistgate_hop.json"
	awgSettings = "../protocols/awg/testdata/mistgate_awg31.json"
	awgV4       = "../protocols/awg/testdata/mistgate_awg31_v4only.json"
	vlessTCP    = "../protocols/vless/testdata/mistgate_tcp_reality.json"
)

func intp(n int) *int { return &n }

// syncView is a person with two servers (edge-fra-a: Hysteria2 direct, over IPv6 and through a relay, and AmneziaWG; edge-ams-b: Hysteria2 through
// WARP, VLESS and AmneziaWG), two Telegram proxy links and a quota. The node names are invented to be looked for in the output.
func syncView(t *testing.T) access.SubView {
	t.Helper()
	hy2, awgV6, awgNoV6, vless := fixture(t, hy2Settings), fixture(t, awgSettings), fixture(t, awgV4), fixture(t, vlessTCP)
	return access.SubView{
		UserID: "usr_1", UserName: "alice", Status: access.StatusActive, AppTelegram: true,
		Telegram: []access.SubServer{
			{URI: "tg://proxy?server=de2.example.com&port=8443&secret=eeabcdef", NodeID: "nod_1", PathKey: "direct", Node: "edge-fra-a", CountryCode: "DE", Location: "Frankfurt"},
			{URI: "tg://proxy?server=2001:db8::5&port=8443&secret=eeabcdef", NodeID: "nod_1", PathKey: "ipv6", Node: "edge-fra-a", CountryCode: "DE", Location: "Frankfurt", Mark: access.MarkIPv6},
			{URI: "tg://proxy?server=fi1.example.com&port=8443&secret=eeabcdef", NodeID: "nod_1", PathKey: "relay:nod_9", Node: "edge-fra-a", CountryCode: "DE", Location: "Frankfurt", Mark: access.ViaMark("fi")},
			{URI: "tg://proxy?server=se1.example.com&port=8443&secret=eeabcdef", NodeID: "nod_1", PathKey: "relay:nod_8", Node: "edge-fra-a", CountryCode: "DE", Location: "Frankfurt", Mark: access.ViaMark("")}, // a relay with no country
		},
		DevicesUsed: 3, DeviceLimit: 5, Up: 1 << 20, Down: 3 << 20, Total: 10 << 30, Expires: time.Unix(1_800_000_000, 0), QuotaReset: "month", NextReset: time.Unix(1_790_000_000, 0),
		Servers: []access.SubServer{
			{URI: hy2, Protocol: "hysteria2", ProfileID: "prf_h", Profile: "Hysteria2", NodeID: "nod_1", InboundID: "inb_1", PathKey: "direct", Node: "edge-fra-a", CountryCode: "DE", Location: "Frankfurt", Exit: "direct", IPv6Egress: true, LoadPercent: intp(12)},
			{URI: hy2, Protocol: "hysteria2", ProfileID: "prf_h", Profile: "Hysteria2", NodeID: "nod_1", InboundID: "inb_1", PathKey: "ipv6", Node: "edge-fra-a", CountryCode: "DE", Location: "Frankfurt", Exit: "direct", IPv6Egress: true, LoadPercent: intp(12), Mark: access.MarkIPv6},
			{URI: hy2, Protocol: "hysteria2", ProfileID: "prf_h", Profile: "Hysteria2", NodeID: "nod_1", InboundID: "inb_1", PathKey: "relay:nod_9", Node: "edge-fra-a", CountryCode: "DE", Location: "Frankfurt", Exit: "direct", IPv6Egress: true, LoadPercent: intp(55), Mark: access.ViaMark(" fi ")},
			{URI: awgV6, Protocol: "awg", ProfileID: "prf_a", Profile: "AWG 2.0", NodeID: "nod_1", InboundID: "inb_2", PathKey: "direct", Node: "edge-fra-a", CountryCode: "DE", Location: "Frankfurt", Exit: "direct", IPv6Egress: true, LoadPercent: intp(12)},
			{URI: hy2, Protocol: "hysteria2", ProfileID: "prf_h", Profile: "Hysteria2 WARP", NodeID: "nod_2", InboundID: "inb_3", PathKey: "direct", Node: "edge-ams-b", CountryCode: "nl", Location: "Amsterdam", Exit: "warp", IPv6Egress: true, LoadPercent: intp(90), PublicLabel: " Fast "},
			{URI: vless, Protocol: "vless", ProfileID: "prf_v", Profile: "VLESS Reality", NodeID: "nod_2", InboundID: "inb_5", PathKey: "direct", Node: "edge-ams-b", CountryCode: "nl", Location: "Amsterdam", Exit: "direct", LoadPercent: intp(90)},
			{URI: vless, Protocol: "vless", ProfileID: "prf_v", Profile: "VLESS Reality", NodeID: "nod_2", InboundID: "inb_5", PathKey: "relay:nod_9", Node: "edge-ams-b", CountryCode: "nl", Location: "Amsterdam", Exit: "direct", LoadPercent: intp(90)}, // a relay with no country
			{URI: awgNoV6, Protocol: "awg", ProfileID: "prf_a", Profile: "AWG 2.0", NodeID: "nod_2", InboundID: "inb_4", PathKey: "direct", Node: "edge-ams-b", CountryCode: "nl", Location: "Amsterdam", Exit: "direct"},
		},
	}
}

func syncInput(t *testing.T) clientSyncInput {
	t.Helper()
	return clientSyncInput{
		DeviceID: "dev_laptop_1", View: syncView(t),
		Brand:    instance.Settings{BrandHead: "Mist", BrandTail: "gate", Accent: "#7c6cff", LogoSVG: `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 8 8"><circle cx="4" cy="4" r="3"/></svg>`},
		Settings: &adminv1.SubscriptionSettings{SupportUrl: "https://support.example.com", Announcement: "Maintenance on Friday."},
		ProbeOn:  true, DNS: ruSplit(),
	}
}

func marshalSync(t *testing.T, in clientSyncInput) []byte {
	t.Helper()
	b, err := json.Marshal(buildClientSync(in))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestClientSyncBodyGolden(t *testing.T) {
	body := marshalSync(t, syncInput(t))
	goldenJSON(t, "client_sync_active.json", body)
	for _, name := range []string{"edge-fra-a", "edge-ams-b", "alice", "usr_1", "nod_1", "nod_2", "inb_1", "prf_h", "k3xq8"} {
		if strings.Contains(string(body), name) {
			t.Errorf("the body names %q", name)
		}
	}

	// The same input, byte for byte, twice: nothing in the body varies by itself.
	if again := marshalSync(t, syncInput(t)); string(again) != string(body) {
		t.Error("two renders of one input differ")
	}

	// A person without access: the status and the plan, no servers and no DNS, and the rest as it was.
	for _, status := range []string{access.StatusDisabled, access.StatusExpired, access.StatusLimited} {
		in := syncInput(t)
		in.View.Status = status
		var doc struct {
			Status  string
			Servers []any
			DNS     any
			Plan    map[string]any
			Brand   map[string]any
			Notice  map[string]any
		}
		if err := json.Unmarshal(marshalSync(t, in), &doc); err != nil {
			t.Fatal(err)
		}
		if doc.Status != status || doc.Servers == nil || len(doc.Servers) != 0 || doc.DNS != nil || doc.Plan["quota_bytes"] != float64(10<<30) || doc.Brand == nil || doc.Notice == nil {
			t.Errorf("%s: %+v", status, doc)
		}
	}
	in := syncInput(t)
	in.View.Status = access.StatusLimited
	goldenJSON(t, "client_sync_limited.json", marshalSync(t, in))
}

// The plan's reset: "none" means no reset time; a period is kept with its time whether or not there is a quota, because it zeroes
// the used counter the person sees (the page shows the same).
// A limit of 0 (no limit) would be told as 0, as quota_bytes is; a negative number is never told.
func TestClientSyncDevicesLimitZeroIsNone(t *testing.T) {
	for _, c := range []struct{ limit, want int }{{0, 0}, {-1, 0}, {7, 7}} {
		in := syncInput(t)
		in.View.DeviceLimit = c.limit
		if got := buildClientSync(in).Devices; got.Limit != c.want || got.Used != 3 {
			t.Errorf("limit %d: %+v, want %d", c.limit, got, c.want)
		}
	}
}

func TestClientSyncPlanReset(t *testing.T) {
	next := time.Unix(1_790_000_000, 0)
	for _, c := range []struct {
		name      string
		total     uint64
		reset     string
		next      time.Time
		wantReset string
		wantNext  int64
	}{
		{"quota and a month", 10 << 30, "month", next, "month", 1_790_000_000},
		{"no quota and a month", 0, "month", next, "month", 1_790_000_000},
		{"quota and none", 10 << 30, access.ResetNone, time.Time{}, "none", 0},
		{"no quota and none", 0, access.ResetNone, time.Time{}, "none", 0},
		{"no quota and an empty period", 0, "", next, "none", 0},
	} {
		in := syncInput(t)
		in.View.Total, in.View.QuotaReset, in.View.NextReset = c.total, c.reset, c.next
		var doc struct {
			Plan struct {
				QuotaBytes    uint64 `json:"quota_bytes"`
				QuotaReset    string `json:"quota_reset"`
				NextResetUnix int64  `json:"next_reset_unix"`
			}
		}
		if err := json.Unmarshal(marshalSync(t, in), &doc); err != nil {
			t.Fatal(err)
		}
		if doc.Plan.QuotaBytes != c.total || doc.Plan.QuotaReset != c.wantReset || doc.Plan.NextResetUnix != c.wantNext {
			t.Errorf("%s: plan %+v, want reset %q next %d", c.name, doc.Plan, c.wantReset, c.wantNext)
		}
	}
}

// The structure of the answer: what is there when, and in what shape.
func TestClientSyncBodyShape(t *testing.T) {
	var doc struct {
		V          int    `json:"v"`
		DeviceID   string `json:"device_id"`
		Status     string
		SyncEveryS int `json:"sync_every_s"`
		Brand      struct {
			Parts []string
		}
		SupportURL string `json:"support_url"`
		Notice     struct{ ID, Text string }
		Plan       map[string]any
		Devices    map[string]any
		Probe      map[string]any
		Debug      map[string]any
		DNS        struct {
			Transport string
			Servers   []map[string]any
			Split     []struct{ Suffixes, Geo []string }
		}
		Servers []struct {
			ID, Country, City, Label, Load string
			Paths                          []struct {
				ID, Protocol, Label, Via string
				ViaCountry               string `json:"via_country"`
				IPv6                     bool
				IPv6Egress               bool `json:"ipv6_egress"`
				Settings                 map[string]any
			}
		}
		Telegram []struct {
			ServerID   string `json:"server_id"`
			Country    string
			City       string
			Via        string
			ViaCountry string `json:"via_country"`
			IPv6       bool
			Link       string
		}
	}
	if err := json.Unmarshal(marshalSync(t, syncInput(t)), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.V != 1 || doc.DeviceID != "dev_laptop_1" || doc.Status != "active" || doc.SyncEveryS != 3600 || len(doc.Brand.Parts) != 2 ||
		doc.SupportURL != "https://support.example.com" || doc.Notice.Text != "Maintenance on Friday." || len(doc.Notice.ID) != 64 ||
		doc.Probe["intake"] != true || doc.Probe["v"] != float64(1) || doc.Debug["intake"] != false {
		t.Fatalf("header fields: %+v", doc)
	}
	if doc.Devices["used"] != float64(3) || doc.Devices["limit"] != float64(5) || len(doc.Devices) != 2 {
		t.Errorf("devices: %v", doc.Devices)
	}
	if doc.Plan["expires_unix"] != float64(1_800_000_000) || doc.Plan["used_bytes"] != float64(4<<20) || doc.Plan["quota_reset"] != "month" || doc.Plan["next_reset_unix"] != float64(1_790_000_000) {
		t.Errorf("plan: %v", doc.Plan)
	}
	if len(doc.Servers) != 2 || doc.Servers[0].Country != "DE" || doc.Servers[0].City != "Frankfurt" || doc.Servers[0].Load != "low" ||
		doc.Servers[1].Country != "NL" || doc.Servers[1].Load != "high" {
		t.Fatalf("servers: %+v", doc.Servers)
	}
	var got []string
	ids := map[string]bool{}
	for _, s := range doc.Servers {
		for _, p := range s.Paths {
			got = append(got, p.Protocol+"/"+p.Via+"/"+map[bool]string{true: "v6", false: "v4"}[p.IPv6])
			if ids[p.ID] || len(p.Settings) == 0 {
				t.Errorf("path id %q repeated or empty settings", p.ID)
			}
			ids[p.ID] = true
		}
	}
	var labels, viaCountries []string
	for _, s := range doc.Servers {
		for _, p := range s.Paths {
			labels = append(labels, p.Label)
			viaCountries = append(viaCountries, p.ViaCountry)
		}
	}
	if got, want := strings.Join(labels, "|"), "Hysteria2|Hysteria2|Hysteria2|AWG 2.0|Hysteria2 WARP|VLESS Reality|VLESS Reality|AWG 2.0"; got != want {
		t.Errorf("labels: %q, want %q", got, want)
	}
	if got, want := strings.Join(viaCountries, "|"), "||FI|||||"; got != want { // FI on the relay with a country; none on the one without
		t.Errorf("via_country: %q, want %q", got, want)
	}
	if want := "hysteria2/direct/v4 hysteria2/direct/v6 hysteria2/relay/v4 awg/direct/v4 hysteria2/warp/v4 vless/direct/v4 vless/relay/v4 awg/direct/v4"; strings.Join(got, " ") != want {
		t.Errorf("paths: %v, want %s", got, want)
	}
	if doc.Servers[0].ID == doc.Servers[1].ID || !strings.HasPrefix(doc.Servers[0].ID, "srv_") || !strings.HasPrefix(doc.Servers[0].Paths[0].ID, "pth_") {
		t.Errorf("ids: %q %q", doc.Servers[0].ID, doc.Servers[0].Paths[0].ID)
	}
	if doc.Servers[0].Label != "" || doc.Servers[1].Label != "Fast" { // trimmed; none where the owner set none
		t.Errorf("server labels: %q %q", doc.Servers[0].Label, doc.Servers[1].Label)
	}
	if len(doc.Telegram) != 4 || doc.Telegram[0].ServerID != doc.Servers[0].ID || doc.Telegram[0].Country != "DE" || doc.Telegram[0].City != "Frankfurt" ||
		doc.Telegram[0].Link != "tg://proxy?server=de2.example.com&port=8443&secret=eeabcdef" || !strings.Contains(doc.Telegram[2].Link, "fi1.example.com") {
		t.Errorf("telegram: %+v", doc.Telegram)
	}
	var egress []string
	for _, srv := range doc.Servers {
		for _, p := range srv.Paths {
			egress = append(egress, strconv.FormatBool(p.IPv6Egress))
		}
	}
	if got, want := strings.Join(egress, " "), "true true true true true false false false"; got != want { // node 1 and the WARP path have it
		t.Errorf("ipv6_egress: %q, want %q", got, want)
	}
	var kinds []string
	for _, l := range doc.Telegram {
		kinds = append(kinds, l.Via+"/"+l.ViaCountry+"/"+map[bool]string{true: "v6", false: "v4"}[l.IPv6])
	}
	if got, want := strings.Join(kinds, " "), "direct//v4 direct//v6 relay/FI/v4 relay//v4"; got != want {
		t.Errorf("telegram twins: %q, want %q", got, want)
	}
	if doc.DNS.Transport != "plain" || len(doc.DNS.Servers) < 2 || len(doc.DNS.Split) != 1 || doc.DNS.Split[0].Geo == nil {
		t.Errorf("dns: %+v", doc.DNS)
	}

	// Nothing optional is there when it is not set: no support URL, no notice, no DNS; a server without a load sample has no load.
	in := syncInput(t)
	in.Settings, in.DNS, in.View.Servers, in.View.Telegram = &adminv1.SubscriptionSettings{}, nil, in.View.Servers[7:], nil
	in.View.Servers[0].Profile = ""
	in.Brand.BrandTail = ""
	var bare map[string]any
	if err := json.Unmarshal(marshalSync(t, in), &bare); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"support_url", "notice", "dns", "asn", "telegram"} {
		if _, ok := bare[k]; ok {
			t.Errorf("%q is present when unset", k)
		}
	}
	if brand := bare["brand"].(map[string]any); len(brand["parts"].([]any)) != 1 {
		t.Errorf("brand: %v", brand)
	}
	srv := bare["servers"].([]any)[0].(map[string]any)
	if srv["load"] != nil {
		t.Errorf("a server without a sample has a load: %v", srv)
	}
	if path := srv["paths"].([]any)[0].(map[string]any); path["label"] != nil || path["via_country"] != nil {
		t.Errorf("a path with no profile name or relay country has a label or via_country: %v", path)
	}
}

// The ETag is of this body: the notice id follows the text, a protocol setting or a status moves the bytes, the same input does not.
func TestClientSyncBodyChangesOnlyWithData(t *testing.T) {
	base := string(marshalSync(t, syncInput(t)))
	changed := map[string]func(*clientSyncInput){
		"notice":        func(in *clientSyncInput) { in.Settings.Announcement = "Maintenance on Saturday." },
		"status":        func(in *clientSyncInput) { in.View.Status = access.StatusDisabled },
		"used":          func(in *clientSyncInput) { in.View.Down++ },
		"devices used":  func(in *clientSyncInput) { in.View.DevicesUsed++ },
		"devices limit": func(in *clientSyncInput) { in.View.DeviceLimit = 6 },
		"server": func(in *clientSyncInput) {
			in.View.Servers = append([]access.SubServer(nil), in.View.Servers...)
			in.View.Servers[0].URI = strings.Replace(in.View.Servers[0].URI, "20000-30000", "20001-30000", 1)
		},
		"dns": func(in *clientSyncInput) {
			in.DNS = &dns.Preset{Name: "std", Transport: dns.KindPlain, Servers: []dns.Server{{Variant: "cloudflare/standard"}}}
		},
		"server label": func(in *clientSyncInput) {
			in.View.Servers = append([]access.SubServer(nil), in.View.Servers...)
			in.View.Servers[4].PublicLabel = "Fast 2"
		},
		"ipv6 egress": func(in *clientSyncInput) {
			in.View.Servers = append([]access.SubServer(nil), in.View.Servers...)
			in.View.Servers[0].IPv6Egress = false
		},
		"label": func(in *clientSyncInput) {
			in.View.Servers = append([]access.SubServer(nil), in.View.Servers...)
			in.View.Servers[0].Profile = "Hysteria2 fast"
		},
		"via_country": func(in *clientSyncInput) {
			in.View.Servers = append([]access.SubServer(nil), in.View.Servers...)
			in.View.Servers[2].Mark = access.ViaMark("se")
		},
		"telegram": func(in *clientSyncInput) {
			in.View.Telegram = append([]access.SubServer(nil), in.View.Telegram...)
			in.View.Telegram[0].URI += "0"
		},
		"telegram off": func(in *clientSyncInput) { in.View.AppTelegram = false },
		"telegram twin": func(in *clientSyncInput) {
			in.View.Telegram = append([]access.SubServer(nil), in.View.Telegram...)
			in.View.Telegram[2].Mark = access.ViaMark("se")
		},
		"telegram ipv6": func(in *clientSyncInput) {
			in.View.Telegram = append([]access.SubServer(nil), in.View.Telegram...)
			in.View.Telegram[1].PathKey = "direct"
		},
		"probe": func(in *clientSyncInput) { in.ProbeOn = false },
		"brand": func(in *clientSyncInput) { in.Brand.Accent = "#112233" },
	}
	for name, mut := range changed {
		in := syncInput(t)
		mut(&in)
		if string(marshalSync(t, in)) == base {
			t.Errorf("a change of %s did not change the body", name)
		}
	}
	a, b := syncInput(t), syncInput(t)
	a.Settings.Announcement, b.Settings.Announcement = "one", "two"
	var na, nb struct{ Notice struct{ ID string } }
	if err := json.Unmarshal(marshalSync(t, a), &na); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(marshalSync(t, b), &nb); err != nil {
		t.Fatal(err)
	}
	if na.Notice.ID == "" || na.Notice.ID == nb.Notice.ID {
		t.Errorf("notice ids %q %q", na.Notice.ID, nb.Notice.ID)
	}
}

// The Telegram links are for an active person whose Telegram app is on and whose Mistgate app is not switched off; a link with no
// text is no link, and without one the field is not there at all.
func TestClientSyncTelegramOnlyForWhomItIsFor(t *testing.T) {
	has := func(in clientSyncInput) bool {
		var doc map[string]any
		if err := json.Unmarshal(marshalSync(t, in), &doc); err != nil {
			t.Fatal(err)
		}
		_, ok := doc["telegram"]
		return ok
	}
	if !has(syncInput(t)) {
		t.Fatal("an active person with Telegram has no links")
	}
	for name, mut := range map[string]func(*clientSyncInput){
		"disabled":         func(in *clientSyncInput) { in.View.Status = access.StatusDisabled },
		"expired":          func(in *clientSyncInput) { in.View.Status = access.StatusExpired },
		"limited":          func(in *clientSyncInput) { in.View.Status = access.StatusLimited },
		"app disabled":     func(in *clientSyncInput) { in.View.AppMistgateOff = true },
		"telegram off":     func(in *clientSyncInput) { in.View.AppTelegram = false },
		"no links":         func(in *clientSyncInput) { in.View.Telegram = nil },
		"links with no tg": func(in *clientSyncInput) { in.View.Telegram = []access.SubServer{{NodeID: "nod_1"}} },
	} {
		in := syncInput(t)
		mut(&in)
		if has(in) {
			t.Errorf("%s: the answer carries telegram", name)
		}
	}
}
