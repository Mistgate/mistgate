package ownprobe

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"connectrpc.com/connect"

	ownprobev1 "github.com/mistgate/mistgate/gen/mistgate/ownprobe/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/countryreach"
	"github.com/mistgate/mistgate/internal/panel/store"
)

const (
	apiPrefix = "/aaaaaaaaaaaaaaaaaaaaaaaa/"
	apiURL    = "https://panel.example.test"
)

var singleQuoted = regexp.MustCompile(`'(?:[^']|'\\'')*'`)

// The four commands a person can paste: the router's and the server's, to install and to update. The same download, the same hash
// check, the same timeouts; only what runs the script differs.
func TestCommandBuilders(t *testing.T) {
	hash := ScriptHash()
	token := "mgp_" + strings.Repeat("a", 52)
	const base = "https://panel.example.test/aaaaaaaaaaaaaaaaaaaaaaaa"
	got := map[string]func() (string, error){
		"router install": func() (string, error) { return BuildInstallCommand(apiURL, apiPrefix, token, hash) },
		"server install": func() (string, error) { return BuildServerInstallCommand(apiURL, apiPrefix, token, hash) },
		"router update":  func() (string, error) { return BuildUpdateCommand(apiURL, apiPrefix, hash) },
		"server update":  func() (string, error) { return BuildServerUpdateCommand(apiURL, apiPrefix, hash) },
	}
	want := map[string]string{
		"router install": "opkg update && opkg install curl ca-bundle coreutils-sha256sum && cd /tmp && curl -fsS --connect-timeout 15 -m 120 -o mgp.sh '" + base + "/probe.sh'" +
			" && echo '" + hash + "  mgp.sh' | sha256sum -c - && sh mgp.sh install '" + base + "' '" + token + "'",
		"server install": `f=$(mktemp) && curl -fsS --connect-timeout 15 -m 120 -o "$f" '` + base + `/probe.sh'` +
			` && echo "` + hash + `  $f" | sha256sum -c - && sudo sh "$f" install '` + base + "' '" + token + "'",
		"router update": "cd /tmp && curl -fsS --connect-timeout 15 -m 120 -o mgp.sh '" + base + "/probe.sh'" +
			" && echo '" + hash + "  mgp.sh' | sha256sum -c - && sh mgp.sh upgrade",
		"server update": `f=$(mktemp) && curl -fsS --connect-timeout 15 -m 120 -o "$f" '` + base + `/probe.sh'` +
			` && echo "` + hash + `  $f" | sha256sum -c - && sudo sh "$f" upgrade`,
	}
	for name, build := range got {
		command, err := build()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if command != want[name] {
			t.Errorf("%s:\n got %s\nwant %s", name, command, want[name])
		}
		isInstall := strings.Contains(name, "install")
		if strings.Contains(command, token) != isInstall || (strings.Contains(command, "opkg") != (name == "router install")) || strings.Contains(command, "sudo") != strings.HasPrefix(name, "server") {
			t.Errorf("%s has the wrong parts: %s", name, command)
		}
		if strings.Contains(command, "sha256sum -c -") == false || !strings.Contains(command, "--connect-timeout 15 -m 120") || strings.Contains(command, " -L") {
			t.Errorf("%s does not check the hash or bound the download: %s", name, command)
		}
	}
}

// Whatever the settings hold, a command has the skeleton it has with good ones and nothing of an input outside single quotes; an
// input that cannot be quoted safely is refused. The server's lines have one double-quoted word of their own, "$f", the same in all.
func TestCommandsHoldHostileInputInQuotes(t *testing.T) {
	hash := ScriptHash()
	token := "mgp_" + strings.Repeat("a", 52)
	builders := map[string]func(url, prefix, hash string) (string, error){
		"router install": func(u, p, h string) (string, error) { return BuildInstallCommand(u, p, token, h) },
		"server install": func(u, p, h string) (string, error) { return BuildServerInstallCommand(u, p, token, h) },
		"router update":  BuildUpdateCommand,
		"server update":  BuildServerUpdateCommand,
	}
	hostileURLs := []string{
		"https://panel.example.test/'; touch /tmp/x; '", "https://panel.example.test/$(touch x)", "https://panel.example.test/`id`",
		"https://panel.example.test/a b", "https://panel.example.test/\n touch x", "https://panel.example.test/\"; id; \"", "https://user:pw@panel.example.test",
		"https://panel.example.test?x=1", "https://panel.example.test#f", "http://panel.example.test", "panel.example.test", "https://",
		"https://pa'nel.example.test", "https://panel.example.test/a'b/", "https://panel.example.test/a\x00b", "https://panel.example.test/${IFS}x",
	}
	badPrefixes := []string{"/aaaaaaaaaaaaaaaaaaaa;touch/", "/aaaa/", "aaaaaaaaaaaaaaaaaaaaaaaaaa", "/AAAAAAAAAAAAAAAAAAAAAAAA/", "/aaaaaaaaaaaaaaaaaaaa$(x)/", "/aaaaaaaaaaaaaaaaaaaa'''a/", ""}
	badHashes := []string{strings.Repeat("a", 60) + ";id;", "abc", strings.ToUpper(hash), hash + "a", "", strings.Repeat("a", 63) + "'", strings.Repeat("a", 62) + "$f"}
	for name, build := range builders {
		good, err := build(apiURL, apiPrefix, hash)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		skeleton := singleQuoted.ReplaceAllString(good, "Q")
		for _, u := range hostileURLs {
			got, err := build(u, apiPrefix, hash)
			if err != nil {
				continue
			}
			if strings.ContainsAny(got, "\n\r\x00") || singleQuoted.ReplaceAllString(got, "Q") != skeleton {
				t.Errorf("%s: public URL %q gave a command that escapes its quotes: %s", name, u, got)
			}
		}
		for _, p := range badPrefixes {
			if _, err := build(apiURL, p, hash); err == nil {
				t.Errorf("%s: prefix %q accepted", name, p)
			}
		}
		for _, h := range badHashes {
			if _, err := build(apiURL, apiPrefix, h); err == nil {
				t.Errorf("%s: hash %q accepted", name, h)
			}
		}
	}
	// an install without a good token is no install, and the update has none to leak
	for _, bad := range []string{"", "mgp_aaaa", "mgp_" + strings.Repeat("a", 51) + "'", "mgp_" + strings.Repeat("A", 52), strings.Repeat("a", 56)} {
		if _, err := BuildInstallCommand(apiURL, apiPrefix, bad, hash); err == nil {
			t.Errorf("router install with token %q accepted", bad)
		}
		if _, err := BuildServerInstallCommand(apiURL, apiPrefix, bad, hash); err == nil {
			t.Errorf("server install with token %q accepted", bad)
		}
	}
}

// What the panel serves and what the commands check are one file: the hash in every command is the sha256 of the bytes of
// /probe.sh, and Get names the version the probes report.
func TestCommandsCheckTheServedFileAndGetNamesItsVersion(t *testing.T) {
	f := probeFixture(t)
	served := get(f.PublicHandler(""), "/probe.sh", homeIP)
	sum := sha256.Sum256(served.Body.Bytes())
	served256 := hex.EncodeToString(sum[:])
	ctx := auth.WithAdmin(context.Background(), store.Admin{ID: "adm_owner", Role: store.RoleOwner})
	r := rpc{f.Service}

	update, err := r.UpdateCommand(ctx, connect.NewRequest(&ownprobev1.OwnProbeServiceUpdateCommandRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	created, err := r.Create(ctx, connect.NewRequest(&ownprobev1.OwnProbeServiceCreateRequest{Name: "office", City: "Springfield", Kind: "home"}))
	if err != nil {
		t.Fatal(err)
	}
	for name, command := range map[string]string{"update": update.Msg.Command, "update (server)": update.Msg.ServerCommand,
		"install": created.Msg.Command, "install (server)": created.Msg.ServerCommand} {
		if !strings.Contains(command, served256+"  ") || !strings.Contains(command, "/aaaaaaaaaaaaaaaaaaaaaaaa/probe.sh") {
			t.Errorf("%s does not check the served file (%s): %s", name, served256, command)
		}
	}
	if !strings.Contains(created.Msg.Command, created.Msg.Token) || !strings.Contains(created.Msg.ServerCommand, created.Msg.Token) || created.Msg.ScriptSha256 != served256 {
		t.Errorf("Create = %+v", created.Msg)
	}
	if strings.Contains(created.Msg.Command, "sudo") || !strings.Contains(created.Msg.Command, "opkg") || strings.Contains(created.Msg.ServerCommand, "opkg") || !strings.Contains(created.Msg.ServerCommand, "sudo sh") {
		t.Errorf("the two install commands are not for a router and for a server: %s | %s", created.Msg.Command, created.Msg.ServerCommand)
	}
	for _, c := range []string{update.Msg.Command, update.Msg.ServerCommand} {
		if strings.Contains(c, "mgp_") || strings.Contains(c, "install") || !strings.HasSuffix(c, " upgrade") {
			t.Errorf("an update command carries a token or does not upgrade: %s", c)
		}
	}
	// the version a probe is compared with is what Get says, the first 12 characters of that hash
	got, err := r.Get(ctx, connect.NewRequest(&ownprobev1.OwnProbeServiceGetRequest{}))
	if err != nil || got.Msg.ScriptVersion != served256[:12] || update.Msg.ScriptVersion != served256[:12] || ScriptVersion() != served256[:12] {
		t.Fatalf("Get script version = %q, %v; want %q", got.Msg.GetScriptVersion(), err, served256[:12])
	}
}

// The update command is the owner's, like every call of the service, and needs a public HTTPS address to be built.
func TestUpdateCommandIsOwnersAndNeedsAnAddress(t *testing.T) {
	f := probeFixture(t)
	r := rpc{f.Service}
	for name, c := range map[string]context.Context{
		"no admin": context.Background(), "helper": auth.WithAdmin(context.Background(), store.Admin{ID: "adm_x", Role: store.RoleHelper}),
		"token": auth.WithAdmin(context.Background(), store.Admin{ID: "token:abc", Role: store.RoleOwner}),
		"mcp":   auth.WithAdmin(context.Background(), store.Admin{ID: "mcp:abc", Role: store.RoleOwner}),
	} {
		if _, err := r.UpdateCommand(c, connect.NewRequest(&ownprobev1.OwnProbeServiceUpdateCommandRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Errorf("UpdateCommand as %s: %v", name, err)
		}
	}
	for _, publicURL := range []string{"", "http://panel.example.test", "https://"} {
		g := probeFixture(t, func(c *Config) { c.PublicURL = publicURL })
		ctx := auth.WithAdmin(context.Background(), store.Admin{ID: "adm_owner", Role: store.RoleOwner})
		if _, err := (rpc{g.Service}).UpdateCommand(ctx, connect.NewRequest(&ownprobev1.OwnProbeServiceUpdateCommandRequest{})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
			t.Errorf("UpdateCommand with public URL %q: %v", publicURL, err)
		}
	}
}

// A point names its probe in a field of its own, its operator and city stay in the network, and the port the reason comes from
// is the port of the target that gave it.
func TestPointCarriesTheProbeNameAndThePortOfItsReason(t *testing.T) {
	probe := store.OwnProbe{ID: "prb_de1", Name: "office", City: "Springfield", Kind: "home", ASN: 64512, Operator: "ExampleNet"}
	at := func(port uint16, kind string) Target {
		return Target{NodeID: "nod_de1", IP: "203.0.113.12", Port: port, Kind: kind, SNI: "target.example.test"}
	}
	ok := SyncResult{TCP: "ok", TLS: "ok", Fetch: "full"}
	judge := func(reference string, items ...targetResult) countryreach.Vantage {
		return judgeProbe(probe, items, reference, probeFixture(t).now)
	}

	dark := judge("blocked", targetResult{at(8443, "tls"), SyncResult{TCP: "ok", TLS: "reset", Fetch: "-"}})
	if dark.State != "dark" || dark.Why != "tls_reset" || dark.Port != 8443 || dark.ProbeName != "office" || dark.Network != "ExampleNet · Springfield" || dark.Kind != "home" {
		t.Errorf("a reset TLS = %+v", dark)
	}
	// the first dark target is the one named
	two := judge("blocked", targetResult{at(443, "tcp"), SyncResult{TCP: "timeout", TLS: "-", Fetch: "-"}}, targetResult{at(8443, "tls"), SyncResult{TCP: "ok", TLS: "reset", Fetch: "-"}})
	if two.Why != "tcp_timeout" || two.Port != 443 {
		t.Errorf("two dark targets = %+v", two)
	}
	// another port that reaches wins over the dark one: no reason, no port
	reach := judge("blocked", targetResult{at(443, "tls"), SyncResult{TCP: "timeout", TLS: "-", Fetch: "-"}}, targetResult{at(8443, "tls"), ok})
	if reach.State != "reach" || reach.Why != "" || reach.Port != 0 {
		t.Errorf("one port reaches = %+v", reach)
	}
	// a point that reaches but proves nothing, or says nothing about a target, names no port
	if v := judge("open", targetResult{at(443, "tls"), ok}); v.State != "uncertain" || v.Why != "no_filter" || v.Port != 0 {
		t.Errorf("an open reference = %+v", v)
	}
	if v := judge("blocked", targetResult{at(2053, "tls"), SyncResult{TCP: "refused", TLS: "-", Fetch: "-"}}); v.State != "uncertain" || v.Why != "tcp_reset" || v.Port != 2053 {
		t.Errorf("a refused port = %+v", v)
	}
	if v := makeVantage(probe, "uncertain", "no_control", false, false, probeFixture(t).now); v.Port != 0 || v.ProbeName != "office" || strings.Contains(v.Network, "probe") {
		t.Errorf("a point without a control = %+v", v)
	}
	// the name of an unnamed-network probe and the alert text: no word of any language in the network, the name in quotes
	bare := store.OwnProbe{ID: "prb_x", Name: "kitchen", ASN: 64496}
	if v := makeVantage(bare, "reach", "", true, true, probeFixture(t).now); v.Network != "AS64496" || v.ProbeName != "kitchen" {
		t.Errorf("a probe without operator or city = %+v", v)
	}
}

// What the probe's last finished round said of the reference address reaches the owner with the probe, before any point, and
// stays through the owner's "check now".
func TestProbeCarriesTheReferenceOfItsLastRound(t *testing.T) {
	f := probeFixture(t)
	f.enable(t)
	p, secret := f.probe(t, "prb_de1", "de1")
	ctx := auth.WithAdmin(context.Background(), store.Admin{ID: "adm_owner", Role: store.RoleOwner})
	r := rpc{f.Service}
	reference := func() string {
		t.Helper()
		got, err := r.Get(ctx, connect.NewRequest(&ownprobev1.OwnProbeServiceGetRequest{}))
		if err != nil || len(got.Msg.Probes) != 1 {
			t.Fatalf("Get = %+v, %v", got, err)
		}
		return got.Msg.Probes[0].Reference
	}
	if got := reference(); got != "" {
		t.Fatalf("a probe that has not finished a round says %q", got)
	}
	for _, c := range []struct{ wire, want string }{{"blocked", "filtered"}, {"open", "open"}, {"fail", "unavailable"}, {"blocked", "filtered"}} {
		f.setJob(t, p.ID, testJobID, defaultTargets)
		if w := post(f.PublicHandler(""), "/sync", homeIP, "Bearer "+secret, report(testJobID, "ok", c.wire, rowTLSOnly)); w.Code != http.StatusOK {
			t.Fatalf("sync = %d", w.Code)
		}
		if got := reference(); got != c.want {
			t.Fatalf("reference %q is shown as %q, want %q", c.wire, got, c.want)
		}
	}
	// "check now" and the switch-off do not make the probe forget it; a revocation and a new network do
	if _, err := r.CheckNow(ctx, connect.NewRequest(&ownprobev1.OwnProbeServiceCheckNowRequest{Id: p.ID})); err != nil {
		t.Fatal(err)
	}
	if got := reference(); got != "filtered" {
		t.Fatalf("after check now: %q", got)
	}
	if _, err := r.Disconnect(ctx, connect.NewRequest(&ownprobev1.OwnProbeServiceDisconnectRequest{})); err != nil {
		t.Fatal(err)
	}
	if got := reference(); got != "filtered" {
		t.Fatalf("after the switch-off: %q", got)
	}
	f.enable(t)
	if w := post(f.PublicHandler(""), "/sync", otherIP, "Bearer "+secret, report(noJobID, "ok", "blocked")); w.Code != http.StatusOK {
		t.Fatalf("sync from another network = %d", w.Code)
	}
	if got := reference(); got != "" {
		t.Fatalf("a probe on a network that nobody confirmed still shows %q", got)
	}
}
