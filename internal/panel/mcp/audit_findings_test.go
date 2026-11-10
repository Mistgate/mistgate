//go:build !js

package mcp

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/dns"
	"github.com/mistgate/mistgate/internal/panel/tasks/spec"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func auditResultText(t *testing.T, result *sdk.CallToolResult) string {
	t.Helper()
	if len(result.Content) != 1 {
		t.Fatalf("result content count = %d", len(result.Content))
	}
	content, ok := result.Content[0].(*sdk.TextContent)
	if !ok {
		t.Fatalf("result content type = %T", result.Content[0])
	}
	return content.Text
}

func approvalChunkPayload(value string) string {
	_, payload, ok := strings.Cut(value, ") ")
	if !ok {
		return value
	}
	return payload
}

func TestResultScrubsStringLeavesWithoutBreakingJSON(t *testing.T) {
	value := struct {
		Inbounds []inboundV `json:"inbounds"`
	}{Inbounds: []inboundV{{ID: "inb_1", Profile: "p", Protocol: "awg", Port: 443, State: "failed",
		LastError: clean("apply: wireguard: invalid private key", 200)}}}
	result, _, err := result(value)
	if err != nil {
		t.Fatal(err)
	}
	text := auditResultText(t, result)
	if !json.Valid([]byte(text)) {
		t.Fatalf("scrubbed result is invalid JSON: %s", text)
	}
	condition := spec.Condition{Path: "inbounds[].state", Op: spec.OpEq, Value: "failed"}
	matches, err := condition.Match([]byte(text), 5)
	if err != nil || len(matches) != 1 {
		t.Fatalf("condition matches=%v error=%v for %s", matches, err, text)
	}
	if !strings.Contains(text, "invalid") || !strings.Contains(text, `"state":"failed"`) {
		t.Fatalf("expected the key error and failed state to remain readable, got %s", text)
	}
}

func TestResultScrubberKeepsEntriesBetweenPEMLikeNames(t *testing.T) {
	value := struct {
		Groups []groupV `json:"groups"`
	}{Groups: []groupV{
		{ID: "grp_a", Name: "-----BEGIN PRIVATE KEY-----"},
		{ID: "grp_hidden", Name: "victims"},
		{ID: "grp_z", Name: "-----END PRIVATE KEY-----"},
	}}
	result, _, err := result(value)
	if err != nil {
		t.Fatal(err)
	}
	text := auditResultText(t, result)
	if !json.Valid([]byte(text)) || !strings.Contains(text, "grp_hidden") || !strings.Contains(text, "victims") {
		t.Fatalf("scrubber lost JSON entries: %s", text)
	}
}

func TestPlanResultKeepsLiteralConfirmMarkerInFact(t *testing.T) {
	token := newConfirm()
	out := &PlanOut{PlanID: "pln_x", Summary: "s", ConfirmToken: token, ExpiresInS: 600,
		Facts: []Fact{{Key: "user", Value: nm("bob @@confirm@@"), Untrusted: true}}}
	result, _, err := planResult(out)
	if err != nil {
		t.Fatal(err)
	}
	var got PlanOut
	if err := json.Unmarshal([]byte(auditResultText(t, result)), &got); err != nil {
		t.Fatal(err)
	}
	if got.ConfirmToken != token || got.Facts[0].Value != "bob @@confirm@@" {
		t.Fatalf("confirm token or literal fact changed: token=%q fact=%q", got.ConfirmToken, got.Facts[0].Value)
	}
}

func TestDNSApprovalFactsShowResolverAndSplitChanges(t *testing.T) {
	old := &adminv1.DnsPreset{
		Servers: []*adminv1.DnsServer{{Kind: adminv1.DnsServerKind_DNS_SERVER_KIND_DOH, Address: "https://dns.de1.example/query", ProviderVariant: "de1/family"}},
		Split:   []*adminv1.DnsSplitRule{{Suffixes: []string{"a.example"}, Servers: []*adminv1.DnsServer{{Kind: adminv1.DnsServerKind_DNS_SERVER_KIND_PLAIN, Address: "192.0.2.10"}}}},
	}
	updated := &adminv1.DnsPreset{
		Servers: []*adminv1.DnsServer{{Kind: adminv1.DnsServerKind_DNS_SERVER_KIND_DOH, Address: "https://dns.nl1.example/query", ProviderVariant: "nl1/basic"}},
		Split:   []*adminv1.DnsSplitRule{{Geo: []string{"nl"}, Servers: []*adminv1.DnsServer{{Kind: adminv1.DnsServerKind_DNS_SERVER_KIND_DOT, Address: "dns.nl1.example:853", ProviderVariant: "nl1/private"}}}},
	}
	facts, err := dnsMutationFacts("Default", "", old, updated)
	if err != nil {
		t.Fatal(err)
	}
	joined := ""
	for _, fact := range facts {
		joined += fact.Key + ": " + fact.Value + "\n"
	}
	for _, want := range []string{"removed", "added", "unchanged", "https://dns.de1.example/query", "https://dns.nl1.example/query", "de1/family", "nl1/basic", "suffixes=[\"a.example\"]", "geo=[\"nl\"]", "192.0.2.10"} {
		if !strings.Contains(joined, want) {
			t.Errorf("approval facts do not show %q:\n%s", want, joined)
		}
	}
	created, err := dnsMutationFacts("Default", "", nil, old)
	if err != nil {
		t.Fatal(err)
	}
	var resolverCount, ruleCount int
	for _, fact := range created {
		if fact.Key == "resolver" {
			resolverCount++
			if !fact.Untrusted {
				t.Errorf("created resolver fact is not untrusted: %+v", fact)
			}
		}
		if strings.HasPrefix(fact.Key, "split[") {
			ruleCount++
			if !fact.Untrusted {
				t.Errorf("created split rule fact is not untrusted: %+v", fact)
			}
		}
	}
	if resolverCount != 2 || ruleCount != 1 {
		t.Fatalf("create facts have %d resolvers and %d split rules: %+v", resolverCount, ruleCount, created)
	}
	for _, fact := range facts {
		if (fact.Key == "resolver" || strings.HasPrefix(fact.Key, "split[") ||
			((fact.Key == "resolvers" || fact.Key == "split_rules") && (strings.HasPrefix(fact.Value, "added: ") || strings.HasPrefix(fact.Value, "removed: ")))) && !fact.Untrusted {
			t.Errorf("untrusted DNS fact is not marked: %+v", fact)
		}
	}
}

func TestApprovalTextFactsSplitLongChangesWholeAndRefuseRedactedContent(t *testing.T) {
	before := strings.Repeat("a ", 499) + "a"
	after := strings.Repeat("after ", 341) + "ok"
	facts, err := approvalTextChangeFacts("announcement", before, after)
	if err != nil {
		t.Fatal(err)
	}
	var gotBefore, gotAfter strings.Builder
	for _, fact := range facts {
		switch fact.Key {
		case "announcement before":
			gotBefore.WriteString(approvalChunkPayload(fact.Value))
		case "announcement after":
			gotAfter.WriteString(approvalChunkPayload(fact.Value))
		}
	}
	if gotBefore.String() != before || gotAfter.String() != after {
		t.Fatalf("change facts lost text: before %d/%d, after %d/%d", gotBefore.Len(), len(before), gotAfter.Len(), len(after))
	}
	if _, err := approvalTextFacts("prompt", "open VLESS user 3f2a9c1e-7b4d-4e8a-9f10-2b6c8d0e1a57"); err == nil || !strings.Contains(err.Error(), "cannot be shown whole") {
		t.Fatalf("unshowable prompt was accepted: %v", err)
	}
}

func TestApprovalTextAllowsJoinersAndScrubsOnlyTheBeforeValue(t *testing.T) {
	for name, text := range map[string]string{
		"Persian ZWNJ": "\u0645\u06cc\u200c\u062e\u0648\u0627\u0647\u0645",
		"emoji ZWJ":    "check \U0001f469\u200d\U0001f4bb daily",
	} {
		facts, err := approvalTextFacts("prompt", text)
		if err != nil || len(facts) != 1 || facts[0].Value != text {
			t.Errorf("%s text was not preserved: facts=%+v err=%v", name, facts, err)
		}
	}
	for name, text := range map[string]string{
		"bidi control": "abc\u202edef",
		"soft hyphen":  "co\u00adoperate",
	} {
		if _, err := approvalTextFacts("prompt", text); err == nil {
			t.Errorf("new %s text was accepted", name)
		}
	}

	before := strings.Repeat("ordinary notes ", 80) + "https://de1.example/client?ticket=12345\u202e"
	facts, err := approvalTextChangeFacts("notes", before, "moved to nl1")
	if err != nil {
		t.Fatalf("an unshowable old value blocked a safe new value: %v", err)
	}
	var old strings.Builder
	chunks := 0
	for _, fact := range facts {
		if fact.Key == "notes before" {
			old.WriteString(approvalChunkPayload(fact.Value))
			chunks++
		}
	}
	if chunks < 2 || strings.Contains(old.String(), "ticket=12345") || strings.ContainsRune(old.String(), '\u202e') || !strings.Contains(old.String(), "[redacted]") {
		t.Fatalf("old text was not scrubbed and chunked: chunks=%d value=%q", chunks, old.String())
	}
	if _, err := approvalTextChangeFacts("support_url", "https://de1.example/support?lang=ru", "https://nl1.example/help"); err != nil {
		t.Fatalf("a query in the old support URL blocked its replacement: %v", err)
	}
	if _, err := approvalTextChangeFacts("support_url", "old value", "https://nl1.example/help?ticket=12345"); err == nil {
		t.Fatal("an unsafe new support URL was accepted")
	}
}

func TestChangedIDFactsListEveryAddedAndRemovedID(t *testing.T) {
	var oldIDs, newIDs []string
	for i := range 50 {
		oldIDs = append(oldIDs, "node_"+strconv.Itoa(i))
		newIDs = append(newIDs, "node_"+strconv.Itoa(i+1))
	}
	facts := changedIDFacts("disabled_node_ids", oldIDs, newIDs)
	var removed, added int
	for _, fact := range facts {
		if strings.HasPrefix(fact.Value, "removed: ") {
			removed++
			if !fact.Untrusted {
				t.Errorf("removed ID is not marked untrusted: %+v", fact)
			}
		}
		if strings.HasPrefix(fact.Value, "added: ") {
			added++
			if !fact.Untrusted {
				t.Errorf("added ID is not marked untrusted: %+v", fact)
			}
		}
	}
	if removed != 1 || added != 1 {
		t.Fatalf("50-item ID diff only showed %d removed and %d added IDs", removed, added)
	}
}

func TestDNSApprovalRefusesListsItCannotShowWhole(t *testing.T) {
	tooManyResolvers := &adminv1.DnsPreset{}
	for i := 0; i <= dnsApprovalMaxResolvers; i++ {
		tooManyResolvers.Servers = append(tooManyResolvers.Servers, &adminv1.DnsServer{Kind: adminv1.DnsServerKind_DNS_SERVER_KIND_PLAIN, Address: "dns.de1.example"})
	}
	if _, err := dnsMutationFacts("Default", "", nil, tooManyResolvers); err == nil || !strings.Contains(err.Error(), "at most 16") {
		t.Fatalf("too many resolvers error = %v", err)
	}
	tooManyRules := &adminv1.DnsPreset{}
	for i := 0; i <= dnsApprovalMaxRules; i++ {
		tooManyRules.Split = append(tooManyRules.Split, &adminv1.DnsSplitRule{Suffixes: []string{"a.example"}})
	}
	if _, err := dnsMutationFacts("Default", "", nil, tooManyRules); err == nil || !strings.Contains(err.Error(), "at most 32") {
		t.Fatalf("too many split rules error = %v", err)
	}
}

func TestDNSApprovalCountsResolverListsSeparatelyAndBoundsBeforeLines(t *testing.T) {
	plain := func(address string) *adminv1.DnsServer {
		return &adminv1.DnsServer{Kind: adminv1.DnsServerKind_DNS_SERVER_KIND_PLAIN, Address: address}
	}
	withSplit := &adminv1.DnsPreset{}
	for i := 1; i <= dns.MaxServers; i++ {
		withSplit.Servers = append(withSplit.Servers, plain("192.0.2."+strconv.Itoa(i)))
	}
	withSplit.Split = []*adminv1.DnsSplitRule{
		{Suffixes: []string{"a.example"}},
		{Suffixes: []string{"b.example"}},
	}
	for rule := range withSplit.Split {
		for i := 1; i <= dns.MaxServers; i++ {
			withSplit.Split[rule].Servers = append(withSplit.Split[rule].Servers, plain("198.51.100."+strconv.Itoa(i)))
		}
	}
	if _, err := dnsMutationFacts("Default", "", nil, withSplit); err != nil {
		t.Fatalf("main and split resolver counts were combined: %v", err)
	}

	before := &adminv1.DnsPreset{}
	for i := 1; i <= dns.MaxServers+1; i++ {
		before.Servers = append(before.Servers, plain("192.0.2."+strconv.Itoa(i)))
	}
	after := &adminv1.DnsPreset{Servers: []*adminv1.DnsServer{before.Servers[0]}}
	facts, err := dnsMutationFacts("Default", "", before, after)
	if err != nil {
		t.Fatalf("shrinking an existing 17-resolver preset was refused: %v", err)
	}
	var shownBefore int
	var hasRemainder bool
	for _, fact := range facts {
		if fact.Key == "resolvers before" {
			shownBefore++
		}
		if strings.HasSuffix(fact.Value, " more before") {
			hasRemainder = true
		}
	}
	if shownBefore != dnsApprovalBeforeLineLimit || !hasRemainder {
		t.Fatalf("before resolver lines were not capped with a remainder: shown=%d facts=%+v", shownBefore, facts)
	}
}

func TestDNSApprovalChunksLongSplitRulesAndNamesOverflowRule(t *testing.T) {
	plain := &adminv1.DnsServer{Kind: adminv1.DnsServerKind_DNS_SERVER_KIND_PLAIN, Address: "192.0.2.1"}
	suffixes := make([]string, 60)
	for i := range suffixes {
		suffixes[i] = "service-" + strconv.Itoa(i) + "-" + strings.Repeat("x", 24) + ".example"
	}
	facts, err := dnsMutationFacts("Default", "", nil, &adminv1.DnsPreset{
		Split: []*adminv1.DnsSplitRule{{Suffixes: suffixes, Servers: []*adminv1.DnsServer{plain}}},
	})
	if err != nil {
		t.Fatalf("long split rule was refused instead of chunked: %v", err)
	}
	chunks := 0
	for _, fact := range facts {
		if fact.Key == "split[1]" {
			chunks++
		}
	}
	if chunks < 2 {
		t.Fatalf("long split rule was not split into approval facts: %+v", facts)
	}

	tooManySplitResolvers := &adminv1.DnsPreset{Split: []*adminv1.DnsSplitRule{{Suffixes: []string{"a.example"}}}}
	for range dnsApprovalMaxResolvers + 1 {
		tooManySplitResolvers.Split[0].Servers = append(tooManySplitResolvers.Split[0].Servers, plain)
	}
	if _, err := dnsMutationFacts("Default", "", nil, tooManySplitResolvers); err == nil || !strings.Contains(err.Error(), "split[1]") {
		t.Fatalf("split resolver overflow did not name its rule: %v", err)
	}
}

func TestTaskApprovalFactsShowPromptAndStepArgumentsWhole(t *testing.T) {
	prompt := strings.Repeat("Check the fleet and report. ", 80) + "END"
	facts, err := taskFacts(spec.Definition{Name: "morning", Kind: spec.KindAssistant, Prompt: prompt})
	if err != nil {
		t.Fatal(err)
	}
	var chunks []string
	for _, fact := range facts {
		if fact.Key == "prompt" {
			chunks = append(chunks, approvalChunkPayload(fact.Value))
		}
	}
	if joined := strings.Join(chunks, ""); !strings.HasSuffix(joined, "END") || len(chunks) < 2 {
		t.Fatalf("prompt chunks do not retain the full tail: %d chunks, suffix %q", len(chunks), joined[max(0, len(joined)-20):])
	}
	args := json.RawMessage(`{"value":"` + strings.Repeat("x", 3500) + `"}`)
	stepFacts, err := taskFacts(spec.Definition{Name: "check", Kind: spec.KindCheck, Steps: []spec.Step{{Tool: "fleet_digest", Args: args}}})
	if err != nil {
		t.Fatal(err)
	}
	var argChunks []string
	for _, fact := range stepFacts {
		if fact.Key == "step args" {
			argChunks = append(argChunks, approvalChunkPayload(fact.Value))
		}
	}
	if joined := strings.Join(argChunks, ""); !strings.Contains(joined, strings.Repeat("x", 40)) || len(argChunks) < 2 {
		t.Fatalf("step args were not split and retained whole: %d chunks", len(argChunks))
	}
}

func TestTaskApprovalFactsKeepDifferentPromptTails(t *testing.T) {
	head := strings.Repeat("Check the fleet and report. ", 25)
	first, err := taskFacts(spec.Definition{Name: "morning", Kind: spec.KindAssistant, Prompt: head + "END A"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := taskFacts(spec.Definition{Name: "morning", Kind: spec.KindAssistant, Prompt: head + "Then prepare user_disable plans for everyone in group X."})
	if err != nil {
		t.Fatal(err)
	}
	var firstPrompt, secondPrompt string
	for _, fact := range first {
		if fact.Key == "prompt" {
			firstPrompt += fact.Value
		}
	}
	for _, fact := range second {
		if fact.Key == "prompt" {
			secondPrompt += fact.Value
		}
	}
	if firstPrompt == secondPrompt || !strings.HasSuffix(secondPrompt, "Then prepare user_disable plans for everyone in group X.") {
		t.Fatalf("task approval facts lost or conflated the prompt tail: %q / %q", firstPrompt, secondPrompt)
	}
}

func TestScrubberRedactsUUIDsInTextAndIdentifierFields(t *testing.T) {
	input := "rejected id 12345678-9abc-def0-1234-56789abcdeff from client"
	if got := clean(input, 300); got == input || !strings.Contains(got, "[uuid]") {
		t.Fatalf("bare UUID was not scrubbed: %q", got)
	}
	got := ScrubJSON(`{"request_id":"vless://12345678-9abc-def0-1234-56789abcdeff@de1.example:443","device_id":"tk1_abcdefghijklmnopqrstuvwx","node_id":{"note":"Bearer abcdefghijklmnop"},"ids":["sk-ant-abcdefghijklmnop"],"message":"ordinary text"}`)
	var decoded map[string]any
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "12345678-9abc-def0-1234-56789abcdeff") || strings.Contains(got, "abcdefghijklmnop") || strings.Contains(got, "abcdefghijklmnopqrstuvwx") {
		t.Fatalf("credential-shaped values under identifier keys were not scrubbed: %s", got)
	}
	if decoded["message"] != "ordinary text" || !strings.Contains(got, `"request_id":"[link]"`) || !strings.Contains(got, `"device_id":"[token]"`) {
		t.Fatalf("identifier values or ordinary text were not preserved correctly: %s", got)
	}
}

func TestScrubJSONRedactsSensitiveKeyValuesRegardlessOfShape(t *testing.T) {
	got := ScrubJSON(`{"private_key":"a short value","secretKey":["another short value"],"Preshared-Key":{"current":"third short value"},"note":"private_key: abcdefghij0123456789"}`)
	var decoded map[string]any
	if err := json.Unmarshal([]byte(got), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["private_key"] != "[key]" {
		t.Fatalf("private_key value = %#v", decoded["private_key"])
	}
	secret, ok := decoded["secretKey"].([]any)
	if !ok || len(secret) != 1 || secret[0] != "[key]" {
		t.Fatalf("secretKey array = %#v", decoded["secretKey"])
	}
	preshared, ok := decoded["Preshared-Key"].(map[string]any)
	if !ok || preshared["current"] != "[key]" {
		t.Fatalf("Preshared-Key object = %#v", decoded["Preshared-Key"])
	}
	if strings.Contains(got, "abcdefghij0123456789") || strings.Contains(got, "another short value") {
		t.Fatalf("a secret-shaped string survived under a sensitive key: %s", got)
	}
}

func TestScrubJSONRetainsFieldOrderAndNumberText(t *testing.T) {
	value := json.RawMessage(`{"zeta":"first","truncated":false,"alpha":[1],"max_uint":18446744073709551615,"decimal":1.0,"exponent":1e21}`)
	result, _, err := result(value)
	if err != nil {
		t.Fatal(err)
	}
	text := auditResultText(t, result)
	if !json.Valid([]byte(text)) {
		t.Fatalf("result is invalid JSON: %s", text)
	}
	positions := []int{strings.Index(text, `"zeta"`), strings.Index(text, `"truncated"`), strings.Index(text, `"alpha"`)}
	if positions[0] < 0 || positions[0] >= positions[1] || positions[1] >= positions[2] {
		t.Fatalf("result field order changed: %s", text)
	}
	for _, number := range []string{"18446744073709551615", "1.0", "1e21"} {
		if !strings.Contains(text, number) {
			t.Errorf("JSON number spelling %q changed: %s", number, text)
		}
	}
}

func TestScrubTextIsIdempotent(t *testing.T) {
	for _, input := range []string{
		"GET https://panel.example/sub/abcdefghijklmnopqrstuvwxyz012345?x=1 failed",
		"Authorization: Bearer abcdefghijklmnop",
		"private key abcdefghijklmnopqrstuv",
		"id 12345678-9abc-def0-1234-56789abcdeff",
		"vless://x@de1.example:443 and [Interface]",
	} {
		first := ScrubText(input)
		if second := ScrubText(first); second != first {
			t.Errorf("scrubbing was not stable: %q -> %q -> %q", input, first, second)
		}
	}
}

func TestCheckNameExplainsUUIDShapedNames(t *testing.T) {
	err := checkName("12345678-9abc-def0-1234-56789abcdeff")
	if err == nil || !strings.Contains(err.Error(), "credential id") || strings.Contains(err.Error(), "1 to 64 characters") {
		t.Fatalf("UUID-shaped name error = %v", err)
	}
}
