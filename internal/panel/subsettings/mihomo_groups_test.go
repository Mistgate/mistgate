package subsettings

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"unsafe"

	"gopkg.in/yaml.v3"
)

func TestResolveMihomoGroup(t *testing.T) {
	const legacy = "# a comment about # @mistgate-if [\"grp_a\"]\n" +
		"# @mistgate is ordinary\nliteral: |\r\n  # @mistgate-unknown\r\n  # @mistgate-end"
	const rules = "mode: rule\nrules:\n" +
		"# @mistgate-if [\"grp_a\",\"grp_b\"]\n  # still a comment\n  - DOMAIN,example.com,DIRECT\n# @mistgate-end\n" +
		"# @mistgate-if [\"grp_a\"]\n  - DOMAIN,a.example,DIRECT\n# @mistgate-end\n  - MATCH,DIRECT"
	const mixed = "mode: rule\r\n# @mistgate-if\t [ \"grp_a\" ] \t\r\nport: 1080\n# @mistgate-end\t\r\nrules: [MATCH]\n# @mistgate-if [\"grp_b\"]\r\nmixed-port: 1081\r\n# @mistgate-end"
	for _, tc := range []struct {
		name, source, group, want string
	}{
		{"legacy unchanged", legacy, "grp_a", legacy},
		{"two blocks", rules, "grp_a", "mode: rule\nrules:\n\n  # still a comment\n  - DOMAIN,example.com,DIRECT\n\n\n  - DOMAIN,a.example,DIRECT\n\n  - MATCH,DIRECT"},
		{"shared block", rules, "grp_b", "mode: rule\nrules:\n\n  # still a comment\n  - DOMAIN,example.com,DIRECT\n\n\n\n\n  - MATCH,DIRECT"},
		{"unknown group", rules, "grp_c", "mode: rule\nrules:\n" + strings.Repeat("\n", 7) + "  - MATCH,DIRECT"},
		{"mixed line breaks", mixed, "grp_a", "mode: rule\r\n\r\nport: 1080\n\r\nrules: [MATCH]\n\r\n\r\n"},
		{"last line without LF", mixed, "grp_b", "mode: rule\r\n\r\n\n\r\nrules: [MATCH]\n\r\nmixed-port: 1081\r\n"},
		{"empty ID is common", mixed, "", "mode: rule\r\n\r\n\n\r\nrules: [MATCH]\n\r\n\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveMihomoGroup(tc.source, tc.group)
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
			if tc.source == tc.want && unsafe.StringData(got) != unsafe.StringData(tc.source) {
				t.Error("legacy text was copied")
			}
		})
	}
}

func TestMihomoGroupDirectiveErrors(t *testing.T) {
	ids := make([]string, maxHeaderGroups+1)
	for i := range ids {
		ids[i] = fmt.Sprintf("grp_%d", i)
	}
	many, err := json.Marshal(ids)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, source, want string
	}{
		{"missing end", "# @mistgate-if [\"grp_a\"]\nmode: rule\n", "line 2: # @mistgate-if has no matching"},
		{"stray end", "# @mistgate-end", "line 2: # @mistgate-end has no matching"},
		{"nested", "# @mistgate-if [\"grp_a\"]\r\n# @mistgate-if [\"grp_b\"]\n# @mistgate-end", "line 3: nested"},
		{"unknown", "# @mistgate-maybe", "line 2: unknown"},
		{"end tail", "# @mistgate-if [\"grp_a\"]\n# @mistgate-end # no tail", "line 3: # @mistgate-end does not take arguments"},
		{"if tail", "# @mistgate-if [\"grp_a\"] []\n# @mistgate-end", "line 2: # @mistgate-if requires"},
		{"bad JSON", "# @mistgate-if ['grp_a']\n# @mistgate-end", "line 2: # @mistgate-if requires"},
		{"empty", "# @mistgate-if []\n# @mistgate-end", "line 2: # @mistgate-if requires"},
		{"null", "# @mistgate-if null\n# @mistgate-end", "line 2: # @mistgate-if requires"},
		{"null member", "# @mistgate-if [null]\n# @mistgate-end", "line 2: # @mistgate-if has a repeated or malformed"},
		{"repeated", "# @mistgate-if [\"grp_a\",\"grp_\\u0061\"]\n# @mistgate-end", "line 2: # @mistgate-if has a repeated or malformed"},
		{"bad ID", "# @mistgate-if [\"grp a\"]\n# @mistgate-end", "line 2: # @mistgate-if has a repeated or malformed"},
		{"too many", "# @mistgate-if " + string(many) + "\n# @mistgate-end", "line 2: # @mistgate-if accepts at most 100"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text := "mode: rule\r\n" + tc.source
			for _, err := range []error{
				CheckMihomoGroups(text, nil, true),
				CheckMihomoGroups(text, []string{"grp_a"}, true),
			} {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("validation got %v; want %q", err, tc.want)
				}
			}
			if resolved, err := ResolveMihomoGroup(text, "grp_x"); err == nil || !strings.Contains(err.Error(), tc.want) || resolved != "" {
				t.Fatalf("resolver got %q, %v; want no text and %q", resolved, err, tc.want)
			}
		})
	}
}

func TestMihomoGroupVariants(t *testing.T) {
	const text = `mode: rule
# @mistgate-if ["grp_pc","grp_mobile"]
find-process-mode: strict
# @mistgate-end
# @mistgate-if ["grp_router"]
find-process-mode: off
# @mistgate-end
# @mistgate-if ["grp_mobile"]
tun:
  enable: true
  exclude-package: [com.example.mobile]
# @mistgate-end
# @mistgate-if ["grp_pc","grp_router"]
tun:
  enable: true
# @mistgate-end
dns:
  fake-ip-filter:
# @mistgate-if ["grp_mobile"]
    - "*"
# @mistgate-end
    - '*.example.com'
proxy-groups:
  - name: Proxy
    type: select
    proxies:
      - DIRECT
# @mistgate-if ["grp_pc"]
      # keep this comment
      - PC
  - name: PC
    type: select
    use: [example]
# @mistgate-end
# @mistgate-if ["grp_pc"]
proxy-providers:
  example: {type: file, path: ./pc.yaml}
# @mistgate-end
rules:
# @mistgate-if ["grp_pc"]
  - DOMAIN,pc.example,PC
# @mistgate-end
  - MATCH,Proxy
`
	if err := CheckMihomoGroups(text, []string{"grp_pc", "grp_mobile", "grp_router"}, true); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		id, process     string
		tun, mobile, pc bool
	}{
		{"", "", false, false, false},
		{"grp_pc", "strict", true, false, true},
		{"grp_mobile", "strict", true, true, false},
		{"grp_router", "off", true, false, false},
	} {
		t.Run(tc.id, func(t *testing.T) {
			resolved, err := ResolveMihomoGroup(text, tc.id)
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Process string `yaml:"find-process-mode"`
				Tun *struct {
					Enable  bool
					Exclude []string `yaml:"exclude-package"`
				}
				DNS struct {
					Filter []string `yaml:"fake-ip-filter"`
				}
				Groups    []tplGroup     `yaml:"proxy-groups"`
				Providers map[string]any `yaml:"proxy-providers"`
				Rules     []string
			}
			if err := yaml.Unmarshal([]byte(resolved), &got); err != nil {
				t.Fatal(err)
			}
			if got.Process != tc.process || (got.Tun != nil) != tc.tun || got.Tun != nil && (!got.Tun.Enable || (len(got.Tun.Exclude) != 0) != tc.mobile) {
				t.Errorf("unexpected process mode or TUN for %q: %q, %+v", tc.id, got.Process, got.Tun)
			}
			filter := []string{"*.example.com"}
			members, rules := []string{"DIRECT"}, []string{"MATCH,Proxy"}
			groupCount := 1
			if tc.mobile {
				filter = append([]string{"*"}, filter...)
			}
			if tc.pc {
				groupCount++
				members = append(members, "PC")
				rules = append([]string{"DOMAIN,pc.example,PC"}, rules...)
			}
			if !slices.Equal(got.DNS.Filter, filter) || !slices.Equal(got.Rules, rules) || len(got.Groups) != groupCount || !slices.Equal(got.Groups[0].Proxies, members) {
				t.Errorf("unexpected DNS, proxy groups or rules: %+v", got)
			}
			if (got.Providers["example"] != nil) != tc.pc || (len(got.Groups) == 2 && !slices.Equal(got.Groups[1].Use, []string{"example"})) || strings.Contains(resolved, "# keep this comment") != tc.pc || strings.Contains(resolved, "# @mistgate-") {
				t.Errorf("unexpected provider, comment or directive:\n%s", resolved)
			}
		})
	}
}

func TestCheckMihomoGroupsReportsVariantAndSourceLine(t *testing.T) {
	const text = "mode: rule\n# @mistgate-if [\"grp_a\"]\nport: 1080\n# @mistgate-end\n" +
		"# @mistgate-if [\"grp_b\"]\nmode: direct\n# @mistgate-end\n"
	for _, ids := range [][]string{nil, {"grp_a"}, {"grp_a", "grp_recreated"}} {
		if err := CheckMihomoGroups(text, ids, true); err != nil {
			t.Errorf("unassigned invalid YAML must be inert: %v", err)
		}
	}
	if err := CheckMihomoGroups(text, []string{"grp_a", "grp_b"}, true); err == nil || !strings.Contains(err.Error(), `group "grp_b": line 6:`) {
		t.Errorf("expected group and duplicate on line 6: %v", err)
	}
	if err := CheckMihomoGroups("# @mistgate-if [\"grp_a\"]\nmode: rule\n# @mistgate-end\n", []string{"grp_a"}, true); err == nil || !strings.Contains(err.Error(), "common: no YAML document") {
		t.Errorf("expected common variant refusal: %v", err)
	}
	const start = "mode: rule\n# @mistgate-if [\"grp_unassigned\"]\n# "
	const end = "\n# @mistgate-end\n"
	for _, tc := range []struct{ name, body, want string }{
		{"bytes", strings.Repeat("x", MaxMihomoTemplateBytes-len(start)-len(end)+1), "65537 bytes"},
		{"UTF-8", "\xff", "UTF-8"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := CheckMihomoGroups(start+tc.body+end, nil, true); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("must check the full source: %v", err)
			}
		})
	}
}

func TestMihomoGroupWarningsAndVariantDeduplication(t *testing.T) {
	const text = "mode: rule\nrules:\n  - DOMAIN,common.example,Unknown\n" +
		"# @mistgate-if [\"grp_a\",\"grp_b\"]\n  - DOMAIN,shared.example,SharedUnknown\n# @mistgate-end\n" +
		"# @mistgate-if [\"grp_b\"]\n  - DOMAIN,mobile.example,MobileUnknown\n# @mistgate-end\n" +
		"# @mistgate-if [\"grp_inert\"]\n  - DOMAIN,inert.example,InertUnknown\n# @mistgate-end\n" +
		"  - MATCH,DIRECT\n# @mistgate-if [\"grp_a\",\"grp_c\"]\ndns: {enable: true}\n# @mistgate-end\n"
	// A fourth assigned group enables the same blocks as grp_a; groups without blocks share common.
	textWithTwin := strings.ReplaceAll(text, `"grp_a",`, `"grp_a","grp_twin",`)
	ids := []string{"grp_empty", "grp_a", "grp_b", "grp_c", "grp_twin"}
	var visited []string
	if err := visitMihomoVariants(textWithTwin, ids, func(id, _ string) error {
		visited = append(visited, id)
		return nil
	}); err != nil || !slices.Equal(visited, []string{"", "grp_a", "grp_b", "grp_c"}) {
		t.Fatalf("distinct variants: %v, %v", visited, err)
	}
	want := []MihomoWarning{
		{Code: WarnNoServerGroup},
		{Code: WarnUnknownRuleTarget, Arg: "Unknown"},
		{Code: WarnOwnDNS},
		{Code: WarnUnknownRuleTarget, Arg: "SharedUnknown"},
		{Code: WarnUnknownRuleTarget, Arg: "MobileUnknown"},
	}
	if got := MihomoGroupWarnings(textWithTwin, ids, true); !slices.Equal(got, want) {
		t.Errorf("warnings %v; want %v", got, want)
	}
	if got := MihomoGroupWarnings(text+"# @mistgate-end\n", ids, true); len(got) != 0 {
		t.Errorf("warning from malformed source: %v", got)
	}
}
