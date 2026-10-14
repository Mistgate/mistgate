package access

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/plugin"
)

// fmtRow is what a profile shows for the four subscription formats, in the order link list, Mihomo, Xray JSON, sing-box:
// "" = carried in full, "!code" = not carried for that reason, "~code" = carried with that loss.
type fmtRow [4]string

var allFormats = []adminv1.SubFormat{
	adminv1.SubFormat_SUB_FORMAT_BASE64_URIS, adminv1.SubFormat_SUB_FORMAT_MIHOMO_YAML,
	adminv1.SubFormat_SUB_FORMAT_XRAY_JSON, adminv1.SubFormat_SUB_FORMAT_SINGBOX_JSON,
}

func rowOf(t *testing.T, fs []*adminv1.ProfileFormat) fmtRow {
	t.Helper()
	if len(fs) != len(allFormats) {
		t.Fatalf("formats %v, want one per format", fs)
	}
	var r fmtRow
	for i, f := range fs {
		if f.Format != allFormats[i] {
			t.Fatalf("format %d is %v, want %v", i, f.Format, allFormats[i])
		}
		switch {
		case !f.Ok:
			r[i] = "!" + f.Note
		case f.Note != "":
			r[i] = "~" + f.Note
		}
	}
	return r
}

// The compatibility matrix of the Xray JSON / sing-box plan, profile kind by profile kind. The preview of the draft, the
// profile list and a real subscription (a node with the profile deployed, a user, each format) must all agree.
var formatMatrix = []struct {
	name, protocol, settings string
	want                     fmtRow
}{
	{"hy2 salamander, ACME", "hysteria2", `{}`, fmtRow{}},
	{"hy2 no obfs", "hysteria2", `{"obfs":{"type":"none"}}`, fmtRow{}},
	{"hy2 self-signed", "hysteria2", `{"tls_mode":"self_signed"}`, fmtRow{"", "", "", "!self_signed"}},
	{"hy2 gecko", "hysteria2", `{"obfs":{"type":"gecko"}}`, fmtRow{"!gecko", "", "!gecko", "!gecko"}},
	{"hy2 hop range", "hysteria2", `{"port":10000,"hop":{"from":20000,"to":30000}}`, fmtRow{"~hop_single_port", "", "~hop_single_port", ""}},
	{"hy2 WARP twin", "hysteria2", `{"egress":"warp"}`, fmtRow{}},
	{"vless REALITY XHTTP", "vless", `{"reality":{"target":"www.example.org:443"}}`, fmtRow{"", "", "", "!xhttp"}},
	{"vless REALITY TCP", "vless", `{"transport":"tcp","reality":{"target":"www.example.org:443"}}`, fmtRow{}},
	{"vless REALITY own site", "vless", `{}`, fmtRow{"", "", "", "!xhttp"}},
	{"vless TLS XHTTP", "vless", `{"security":"tls","transport":"xhttp","tls_mode":"acme_domain","sni":"origin.example.com"}`, fmtRow{"", "", "", "!xhttp"}},
	{"vless TLS XHTTP via CDN", "vless", `{"security":"tls","transport":"xhttp","tls_mode":"acme_domain","sni":"origin.example.com","cdn":{"host":"cdn.example.org","port":443}}`, fmtRow{"", "", "", "!xhttp"}},
	{"vless TLS WebSocket", "vless", `{"security":"tls","transport":"ws","tls_mode":"acme_domain","sni":"origin.example.com","ws":{"path":"/ws","host":""}}`, fmtRow{}},
	{"vless TLS WebSocket via CDN", "vless", `{"security":"tls","transport":"ws","tls_mode":"acme_domain","sni":"origin.example.com","cdn":{"host":"cdn.example.org","port":443},"ws":{"path":"/ws","host":""}}`, fmtRow{}},
	{"vless WARP twin", "vless", `{"egress":"warp","reality":{"target":"www.example.org:443"}}`, fmtRow{"", "", "", "!xhttp"}},
	{"AmneziaWG", "awg", `{}`, fmtRow{"!awg", "", "!awg", "!awg"}},
}

func TestProfileFormatsFollowTheMatrix(t *testing.T) {
	for _, c := range formatMatrix {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			prev := must(e.s.PreviewProfile(e.ctx, req(&adminv1.PreviewProfileRequest{Protocol: c.protocol, SettingsJson: c.settings}))).Msg
			if len(prev.Errors) != 0 {
				t.Fatalf("the draft is refused: %v", prev.Errors)
			}
			if got := rowOf(t, prev.Formats); got != c.want {
				t.Errorf("PreviewProfile formats = %q, want %q", got, c.want)
			}
			p := must(e.s.CreateProfile(e.ctx, req(&adminv1.CreateProfileRequest{Protocol: c.protocol, Name: "p", SettingsJson: c.settings}))).Msg.Profile
			if got := rowOf(t, p.Formats); got != c.want {
				t.Errorf("CreateProfile formats = %q, want %q", got, c.want)
			}
			list := must(e.s.ListProfiles(e.ctx, req(&adminv1.ListProfilesRequest{}))).Msg.Profiles
			if len(list) != 1 {
				t.Fatalf("%d profiles listed", len(list))
			}
			if got := rowOf(t, list[0].Formats); got != c.want {
				t.Errorf("ListProfiles formats = %q, want %q", got, c.want)
			}
			if got := rowOf(t, must(e.s.GetProfile(e.ctx, req(&adminv1.GetProfileRequest{ProfileId: p.Id}))).Msg.Profile.Formats); got != c.want {
				t.Errorf("GetProfile formats = %q, want %q", got, c.want)
			}
		})
	}
}

// The trial render is the subscription's render: for every profile of the matrix deployed on a real node (a self-signed one
// after its node reported the pin), the formats say exactly which of the four subscriptions carry a server of it.
func TestProfileFormatsAgreeWithTheRealSubscription(t *testing.T) {
	for _, c := range formatMatrix {
		if c.protocol == "awg" {
			continue // AmneziaWG is a key, not a server line: its row is checked in the matrix above and by the awg plugin
		}
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.node("nod_de1", "de1", "de1.example.com", "active")
			p := must(e.s.CreateProfile(e.ctx, req(&adminv1.CreateProfileRequest{Protocol: c.protocol, Name: "p", SettingsJson: c.settings}))).Msg.Profile
			in := e.inbound(p.Id, "nod_de1")
			if strings.Contains(c.settings, "self_signed") {
				e.sql(`UPDATE inbound SET cert_pin_sha256 = ? WHERE id = ?`, strings.Repeat("cd", 32), in.Id)
			}
			u := e.user("alice", e.group("g", p.Id), nil).User
			token := e.tokenOf(u.Id)
			for i, f := range []plugin.ClientFormat{plugin.FormatURIList, plugin.FormatMihomo, plugin.FormatXrayJSON, plugin.FormatSingbox} {
				v := must(e.s.SubscriptionWith(e.ctx, token, SubOptions{Format: f, NoTouch: true}))
				if got, want := len(v.Servers) == 1, p.Formats[i].Ok; got != want {
					t.Errorf("%s: the subscription carries the server = %v, the formats say %v (%q)", f, got, want, p.Formats[i].Note)
				}
			}
		})
	}
}

func TestPreviewProfileWithErrorsHasNoFormats(t *testing.T) {
	e := newEnv(t)
	for _, proto := range []string{"hysteria2", "vless", "awg"} {
		r := must(e.s.PreviewProfile(e.ctx, req(&adminv1.PreviewProfileRequest{Protocol: proto, SettingsJson: `{"port":0}`}))).Msg
		if len(r.Errors) == 0 || len(r.Formats) != 0 {
			t.Errorf("%s: errors %v, formats %v; want errors and no formats", proto, r.Errors, r.Formats)
		}
	}
}

// A format answer is a flag and a code: nothing of the secrets the trial render holds can reach it.
func TestProfileFormatsCarryNoSecret(t *testing.T) {
	e := newEnv(t)
	const pw = "obfs-secret-0123456789abcdef"
	hy := `{"obfs":{"type":"salamander","password":"` + pw + `"},"hop":{"from":20000,"to":30000}}`
	p := e.profile("p", hy)
	v := e.vlessProfile("v")
	list := must(e.s.ListProfiles(e.ctx, req(&adminv1.ListProfilesRequest{}))).Msg.Profiles
	prev := must(e.s.PreviewProfile(e.ctx, req(&adminv1.PreviewProfileRequest{Protocol: "hysteria2", SettingsJson: hy}))).Msg
	known := map[string]bool{"": true, plugin.NoteXHTTP: true, plugin.NoteSelfSigned: true, plugin.NoteGecko: true, plugin.NoteAWG: true, plugin.NoteHopSinglePort: true, "unsupported": true}
	all := append([]*adminv1.ProfileFormat{}, prev.Formats...)
	for _, s := range append(list, p, v) {
		all = append(all, s.Formats...)
	}
	if len(all) != 5*4 {
		t.Fatalf("%d format rows, want 20", len(all))
	}
	for _, f := range all {
		if !known[f.Note] {
			t.Errorf("note %q is not a code", f.Note)
		}
		raw, err := protojson.Marshal(f)
		if err != nil || strings.Contains(string(raw), pw) || strings.Contains(string(raw), "••") {
			t.Errorf("format %s leaks: %v", raw, err)
		}
	}
}

// ListProfiles fills the formats of every profile. With 50 profiles (the mix of the matrix, AmneziaWG included) it must stay
// cheap: the log line is the number the step report quotes (run with -v).
func TestListProfilesCostWith50Profiles(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 50; i++ {
		c := formatMatrix[i%len(formatMatrix)]
		must(e.s.CreateProfile(e.ctx, req(&adminv1.CreateProfileRequest{Protocol: c.protocol, Name: fmt.Sprintf("p%02d", i), SettingsJson: c.settings})))
	}
	list := must(e.s.ListProfiles(e.ctx, req(&adminv1.ListProfilesRequest{}))).Msg.Profiles
	type loaded struct {
		proto  protocols.Protocol
		merged json.RawMessage
	}
	var all []loaded
	for _, sum := range list {
		p := must(e.st.Access().Profile(e.ctx, sum.Id))
		proto, _ := e.s.reg.Get(p.Protocol)
		all = append(all, loaded{proto, must(e.s.mergedSettings(p))})
	}
	const rounds = 20
	var whole, formats time.Duration
	for r := 0; r < rounds; r++ {
		start := time.Now()
		if got := must(e.s.ListProfiles(e.ctx, req(&adminv1.ListProfilesRequest{}))).Msg.Profiles; len(got) != 50 {
			t.Fatalf("%d profiles", len(got))
		}
		whole += time.Since(start)
		start = time.Now()
		for _, l := range all {
			if got := e.s.profileFormats(l.proto, l.merged); len(got) != 4 {
				t.Fatalf("%d formats", len(got))
			}
		}
		formats += time.Since(start)
	}
	t.Logf("ListProfiles with 50 profiles: %v per call; of which the trial renders: %v per call", whole/rounds, formats/rounds)
}

// The profiles of a user (user_get, and through it the MCP views) carry the same formats as the profile list.
func TestUserProfilesCarryFormats(t *testing.T) {
	e := newEnv(t)
	gecko := e.profile("gecko", `{"obfs":{"type":"gecko"}}`)
	web := e.vlessProfile("web")
	u := e.user("alice", e.group("g", gecko.Id, web.Id), nil).User
	got := must(e.s.GetUser(e.ctx, req(&adminv1.GetUserRequest{UserId: u.Id}))).Msg.Profiles
	want := map[string]fmtRow{"gecko": {"!gecko", "", "!gecko", "!gecko"}, "web": {"", "", "", "!xhttp"}}
	if len(got) != 2 {
		t.Fatalf("%d profiles", len(got))
	}
	for _, p := range got {
		if r := rowOf(t, p.Formats); r != want[p.Name] {
			t.Errorf("%s: formats %q, want %q", p.Name, r, want[p.Name])
		}
	}
}
