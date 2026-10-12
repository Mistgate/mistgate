package access

import (
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/plugin"
)

var updateSubGolden = flag.Bool("update-sub-golden", false, "rewrite testdata/sub_without_vless.golden")

// Registering the VLESS plugin must change nothing for a person whose group has no VLESS profile: the golden file was
// written by the panel before the plugin existed (main 30be712) and its bytes are what a subscription gives now.
// Only the two random values of a credential are masked: the device's Hysteria2 secret.
func TestSubscriptionWithoutVLESSProfileIsByteIdenticalToMain(t *testing.T) {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	e.node("nod_nl1", "nl1", "nl1.example.com", "active")
	const settings = `{"obfs":{"type":"salamander","password":"golden-obfs-password"}}`
	hy2 := e.profile("hy2 443", settings)
	hy2b := e.profile("hy2 8443", `{"port":8443,"obfs":{"type":"none"}}`)
	e.inbound(hy2.Id, "nod_de1")
	e.inbound(hy2.Id, "nod_nl1")
	e.inbound(hy2b.Id, "nod_nl1")
	group := e.group("default", hy2.Id, hy2b.Id)
	user := e.user("alice", group, nil).User
	token := e.tokenOf(user.Id)

	secret := regexp.MustCompile(`(hysteria2://)[^@]+@|(?m)^(\s+password: )"[^"]*"`)
	var got strings.Builder
	for _, format := range []plugin.ClientFormat{plugin.FormatURIList, plugin.FormatMihomo} {
		view := must(e.s.SubscriptionWith(e.ctx, token, SubOptions{Format: format, NoTouch: true, NoPageData: true}))
		got.WriteString("# " + string(format) + "\n")
		for i, line := range view.Lines {
			srv := view.Servers[i]
			got.WriteString("## " + srv.Node + " | " + srv.Profile + " | " + srv.Protocol + " | " + srv.Exit + "\n")
			got.WriteString(secret.ReplaceAllStringFunc(line, func(m string) string {
				if strings.HasPrefix(m, "hysteria2://") {
					return "hysteria2://<secret>@"
				}
				return strings.SplitN(m, `"`, 2)[0] + `"<secret>"`
			}))
			got.WriteString("\n")
		}
	}

	path := filepath.Join("testdata", "sub_without_vless.golden")
	if *updateSubGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got.String()), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.String() != string(want) {
		t.Errorf("subscription bytes changed for a user without a VLESS profile:\n--- got\n%s\n--- want\n%s", got.String(), want)
	}
	// The device holds a VLESS credential now (one row per device, issued on the fetch), but nothing renders it.
	if n := e.count(`SELECT count(*) FROM device_credential WHERE protocol = 'vless'`); n != 1 {
		t.Errorf("vless credential rows = %d, want 1", n)
	}
}
