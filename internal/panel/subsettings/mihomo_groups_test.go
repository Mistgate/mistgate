package subsettings

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
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
		{"two blocks", rules, "grp_a", "mode: rule\nrules:\n  # still a comment\n  - DOMAIN,example.com,DIRECT\n  - DOMAIN,a.example,DIRECT\n  - MATCH,DIRECT"},
		{"shared block", rules, "grp_b", "mode: rule\nrules:\n  # still a comment\n  - DOMAIN,example.com,DIRECT\n  - MATCH,DIRECT"},
		{"unknown group", rules, "grp_c", "mode: rule\nrules:\n  - MATCH,DIRECT"},
		{"mixed line breaks", mixed, "grp_a", "mode: rule\r\nport: 1080\nrules: [MATCH]\n"},
		{"last line without LF", mixed, "grp_b", "mode: rule\r\nrules: [MATCH]\nmixed-port: 1081\r\n"},
		{"empty ID is common", mixed, "", "mode: rule\r\nrules: [MATCH]\n"},
		{"nested intersection", "mode: rule\n# @mistgate-if [\"grp_a\",\"grp_b\"]\r\nlog-level: info\n# @mistgate-if [\"grp_b\",\"grp_c\"]\nallow-lan: true\n# @mistgate-end\nipv6: false\n# @mistgate-end", "grp_b", "mode: rule\nlog-level: info\nallow-lan: true\nipv6: false\n"},
		{"matching child under excluded parent", "mode: rule\n# @mistgate-if [\"grp_a\"]\n# @mistgate-if [\"grp_b\"]\ninvalid: [\n# @mistgate-end\n# @mistgate-end", "grp_b", "mode: rule\n"},
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

func TestMihomoGroupLineBreaks(t *testing.T) {
	const marker = "# @mistgate-if [\"grp_a\"]\nallow-lan: true\n# @mistgate-end"
	for _, tc := range []struct{ name, text, want string }{
		{"CR", "mode: rule\r" + marker, "line 1:"},
		{"NEL", "mode: rule\u0085" + marker, "line 1:"},
		{"LS", "mode: rule\u2028" + marker, "line 1:"},
		{"PS", "mode: rule\u2029" + marker, "line 1:"},
		{"CR in JSON arguments", "mode: rule\r\n# @mistgate-if [\"grp_a\",\r \"grp_b\"]\n# @mistgate-end", "line 2:"},
		{"break after last marker", "mode: rule\n" + marker + "\n# comment\u2028", "line 5:"},
		{"trailing CR", "mode: rule\n" + marker + "\r", "line 4:"},
		{"unknown marker after break", "mode: rule\n# comment\u2029# @mistgate-unknown", "line 2:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveMihomoGroup(tc.text, "")
			if err == nil || got != "" || !strings.Contains(err.Error(), tc.want+" group blocks support only LF and CRLF") {
				t.Fatalf("resolution got %q, %v", got, err)
			}
			if err := CheckMihomoGroups(t.Context(), tc.text, []string{"grp_a"}, true); err == nil || !strings.Contains(err.Error(), tc.want+" group blocks support only LF and CRLF") {
				t.Fatalf("validation got %v", err)
			}
		})
	}
	const legacy = "mode: rule\r# inline # @mistgate-end\u0085literal: |\u2028  # @mistgate-if [\"grp_a\"]\u2029  no markers"
	if got, err := ResolveMihomoGroup(legacy, "grp_a"); err != nil || got != legacy || unsafe.StringData(got) != unsafe.StringData(legacy) {
		t.Fatalf("legacy text was changed or copied: %q, %v", got, err)
	}
}

func TestMihomoGroupPreservesScalarContent(t *testing.T) {
	for _, tc := range []struct{ style, separator string }{{"|+", "\n"}, {">+", " "}} {
		t.Run(tc.style, func(t *testing.T) {
			start := "mode: rule\r\nsample: " + tc.style + "\r\n  first\r\n"
			const selected = "  selected\r\n"
			const tail = "  last\r\n\r\n"
			const next = "next: keep"
			source := start + "# @mistgate-if [\"grp_a\"]\r\n" + selected + "# @mistgate-end\n" + tail +
				"# @mistgate-if [\"grp_inert\"]\nallow-lan: true\n# @mistgate-end\r\n" + next
			for _, group := range []string{"", "grp_a"} {
				included, value := "", "first"+tc.separator
				if group != "" {
					included, value = selected, value+"selected"+tc.separator
				}
				value += "last\n\n"
				resolved, err := ResolveMihomoGroup(source, group)
				if want := start + included + tail + next; err != nil || resolved != want {
					t.Fatalf("group %q: got %q, %v; want %q", group, resolved, err, want)
				}
				checkValue := func(text []byte) {
					t.Helper()
					var doc map[string]any
					if err := yaml.Unmarshal(text, &doc); err != nil || doc["sample"] != value {
						t.Fatalf("group %q: scalar got %q (%v), want %q", group, doc["sample"], err, value)
					}
				}
				checkValue([]byte(resolved))
				built, err := ApplyMihomoTemplate(resolved, trialServers(), MihomoOptions{})
				if err != nil {
					t.Fatal(err)
				}
				checkValue(built)
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
		{"unclosed outer block", "# @mistgate-if [\"grp_a\"]\r\n# @mistgate-if [\"grp_b\"]\n# @mistgate-end", "line 2: # @mistgate-if has no matching"},
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
				CheckMihomoGroups(t.Context(), text, nil, true),
				CheckMihomoGroups(t.Context(), text, []string{"grp_a"}, true),
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

func TestMihomoNestedValidationUsesEffectiveGroups(t *testing.T) {
	const text = "mode: rule\n# @mistgate-if [\"grp_a\"]\n# @mistgate-if [\"grp_b\"]\ninvalid: [\n# @mistgate-end\n# @mistgate-end\n"
	if err := CheckMihomoGroups(t.Context(), text, []string{"grp_a", "grp_b"}, true); err != nil {
		t.Fatal(err)
	}
	overlap := strings.Replace(text, `["grp_a"]`, `["grp_a","grp_b"]`, 1)
	if err := CheckMihomoGroups(t.Context(), overlap, []string{"grp_a", "grp_b"}, true); err == nil || !strings.Contains(err.Error(), `group "grp_b"`) {
		t.Fatalf("matching child must be validated for grp_b, got %v", err)
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
	if err := CheckMihomoGroups(t.Context(), text, []string{"grp_pc", "grp_mobile", "grp_router"}, true); err != nil {
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
				Tun     *struct {
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
		if err := CheckMihomoGroups(t.Context(), text, ids, true); err != nil {
			t.Errorf("unassigned invalid YAML must be inert: %v", err)
		}
	}
	if err := CheckMihomoGroups(t.Context(), text, []string{"grp_a", "grp_b"}, true); err == nil || !strings.Contains(err.Error(), `group "grp_b": line 6:`) {
		t.Errorf("expected group and duplicate on line 6: %v", err)
	}
	if err := CheckMihomoGroups(t.Context(), "# @mistgate-if [\"grp_a\"]\nmode: rule\n# @mistgate-end\n", []string{"grp_a"}, true); err == nil || !strings.Contains(err.Error(), "common: no YAML document") {
		t.Errorf("expected common variant refusal: %v", err)
	}
	const start = "mode: rule\n# @mistgate-if [\"grp_unassigned\"]\n# "
	const end = "\n# @mistgate-end\n"
	for _, tc := range []struct{ name, body, want string }{
		{"bytes", strings.Repeat("x", MaxMihomoTemplateBytes-len(start)-len(end)+1), "65537 bytes"},
		{"UTF-8", "\xff", "UTF-8"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := CheckMihomoGroups(t.Context(), start+tc.body+end, nil, true); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("must check the full source: %v", err)
			}
		})
	}
	const omitted = "# @mistgate-if [\"grp_inert\"]\r\nallow-lan: true\r\n# @mistgate-end\r\n"
	const duplicates = "mode: rule\nlog-level: info\n# @mistgate-if [\"grp_a\"]\nmode: direct\nlog-level: warning\n# @mistgate-end"
	err := CheckMihomoGroups(t.Context(), omitted+duplicates, []string{"grp_a"}, true)
	if err == nil || !strings.Contains(err.Error(), `group "grp_a": line 7: mapping key "mode" already defined at line 4`) ||
		!strings.Contains(err.Error(), `line 8: mapping key "log-level" already defined at line 5`) {
		t.Errorf("expected both duplicate and original source positions, got %v", err)
	}
	if err := CheckMihomoGroups(t.Context(), omitted+"mode: rule\nproxies: true", nil, true); err == nil || !strings.Contains(err.Error(), "common: line 5: `proxies` must be a list") {
		t.Errorf("expected a remapped common shape error, got %v", err)
	}
	const syntax = "mode: rule\nbad:\n\tvalue"
	var compactLine int
	if err := CheckMihomoTemplate(syntax, true); err == nil {
		t.Fatal("expected a YAML syntax error")
	} else if _, scanErr := fmt.Sscanf(err.Error(), "line %d:", &compactLine); scanErr != nil {
		t.Fatalf("expected a YAML syntax line number: %v", err)
	}
	if err := CheckMihomoGroups(t.Context(), omitted+syntax, nil, true); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("common: line %d:", compactLine+3)) {
		t.Errorf("expected a remapped YAML syntax error, got %v", err)
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
	if err := visitMihomoVariants(t.Context(), textWithTwin, ids, func(id, _ string, _ mihomoVariant) error {
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

func TestMihomoGroupValidationCanceled(t *testing.T) {
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	expired, stop := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
	defer stop()
	for _, ctx := range []context.Context{canceled, expired} {
		if err := CheckMihomoGroups(ctx, "# @mistgate-end", nil, true); !errors.Is(err, ctx.Err()) {
			t.Errorf("got %v, want %v before checking source", err, ctx.Err())
		}
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	const text = "mode: rule\n# @mistgate-if [\"grp_a\"]\nallow-lan: true\n# @mistgate-end"
	visits := 0
	err := visitMihomoVariants(ctx, text, []string{"grp_a"}, func(_, _ string, _ mihomoVariant) error {
		visits++
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) || visits != 1 {
		t.Errorf("expected cancellation after the common check, got %d visits, %v", visits, err)
	}
}

func TestMihomoGroupValidationWorkLimit(t *testing.T) {
	const common = "mode: rule\n"
	padding := "# " + strings.Repeat("x", 58<<10) + "\n"
	var ids []string
	var alternatives strings.Builder
	for i := range 24 {
		id := fmt.Sprintf("grp_%d", i)
		ids = append(ids, id)
		fmt.Fprintf(&alternatives, "# @mistgate-if [%q]\nmixed-port: %d\n# @mistgate-end\n", id, 1000+i)
	}

	large := common + padding + alternatives.String()
	visits, bytes := 0, 0
	err := visitMihomoVariants(t.Context(), large, ids, func(_, resolved string, _ mihomoVariant) error {
		visits++
		bytes += len(resolved)
		return nil
	})
	const extra = len("mixed-port: 1000\n")
	commonBytes := len(common) + len(padding)
	wantVisits := 1 + (maxMihomoVariantBytes-commonBytes)/(commonBytes+extra)
	if err == nil || !strings.Contains(err.Error(), "resolved YAML bytes") || visits != wantVisits ||
		bytes != commonBytes+(wantVisits-1)*(commonBytes+extra) || bytes > maxMihomoVariantBytes {
		t.Fatalf("invalid work cap: got %d visits, %d bytes, %v; want %d visits", visits, bytes, err, wantVisits)
	}
	if err := CheckMihomoGroups(t.Context(), large, ids, true); err == nil || !strings.Contains(err.Error(), "resolved YAML bytes") {
		t.Errorf("validation must enforce the variant work budget: %v", err)
	}
	small := common + "# @mistgate-if [\"grp_inert\"]\n" + padding + "# @mistgate-end\n" + alternatives.String()
	if err := CheckMihomoGroups(t.Context(), small, ids, true); err != nil {
		t.Errorf("excluded source must not count toward the variant work budget: %v", err)
	}
}
