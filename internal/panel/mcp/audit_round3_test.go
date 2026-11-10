//go:build !js

package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

func TestDNSUpdateOnlyChecksChangedResolverLines(t *testing.T) {
	for _, address := range []string{
		"https://dns.de1.example/dns-query?profile=kids",
		"https://dns.de1.example/dns-query/family-filter-profile1",
	} {
		server := &adminv1.DnsServer{Kind: adminv1.DnsServerKind_DNS_SERVER_KIND_DOH, Address: address}
		before := &adminv1.DnsPreset{Servers: []*adminv1.DnsServer{server}}
		after := &adminv1.DnsPreset{Servers: []*adminv1.DnsServer{server}, BlockAds: true}
		if _, err := dnsMutationFacts("Default", "", before, after); err != nil {
			t.Errorf("updating another setting was blocked by unchanged resolver %q: %v", address, err)
		}
	}
	before := &adminv1.DnsPreset{Servers: []*adminv1.DnsServer{{Kind: adminv1.DnsServerKind_DNS_SERVER_KIND_DOH, Address: "https://dns.de1.example/dns-query"}}}
	after := &adminv1.DnsPreset{Servers: []*adminv1.DnsServer{{Kind: adminv1.DnsServerKind_DNS_SERVER_KIND_DOH, Address: "https://dns.de1.example/dns-query/" + strings.Repeat("A", 32)}}}
	if _, err := dnsMutationFacts("Default", "", before, after); err == nil {
		t.Fatal("an added resolver line that must be redacted was accepted")
	}
}

func TestDNSUpdateDiffsSplitRulesByContentAndBoundsBeforeBytes(t *testing.T) {
	plain := &adminv1.DnsServer{Kind: adminv1.DnsServerKind_DNS_SERVER_KIND_PLAIN, Address: "192.0.2.1"}
	rule := func(tag string, count int) *adminv1.DnsSplitRule {
		item := &adminv1.DnsSplitRule{Servers: []*adminv1.DnsServer{plain}}
		for i := range count {
			item.Suffixes = append(item.Suffixes, fmt.Sprintf("%s-%03d.example", tag, i))
		}
		return item
	}
	before := &adminv1.DnsPreset{Split: []*adminv1.DnsSplitRule{rule("aa", 200), rule("bb", 200), rule("cc", 200)}}
	after := &adminv1.DnsPreset{Split: []*adminv1.DnsSplitRule{rule("bb", 200), rule("cc", 200)}}
	facts, err := dnsMutationFacts("Default", "", before, after)
	if err != nil {
		t.Fatalf("removing the first split rule was refused: %v", err)
	}
	var splitSummary string
	var removed, added int
	for _, fact := range facts {
		if fact.Key == "split_rules" && strings.HasPrefix(fact.Value, "before -> after:") {
			splitSummary = fact.Value
		}
		if fact.Key == "split_rules before" && strings.HasPrefix(approvalChunkPayload(fact.Value), "removed: ") {
			removed++
		}
		if (fact.Key == "split_rules" || strings.HasPrefix(fact.Key, "split[")) && strings.HasPrefix(fact.Value, "added: ") {
			added++
		}
	}
	if !strings.Contains(splitSummary, "1 removed, 0 added, 2 unchanged") || removed != 1 || added != 0 {
		t.Fatalf("split rule diff was positional: summary=%q removed=%d added=%d facts=%+v", splitSummary, removed, added, facts)
	}

	largeBefore := &adminv1.DnsPreset{Split: []*adminv1.DnsSplitRule{rule("old", 1000)}}
	for i, suffix := range largeBefore.Split[0].Suffixes {
		largeBefore.Split[0].Suffixes[i] = strings.TrimSuffix(suffix, ".example") + "-" + strings.Repeat("x", 24) + ".example"
	}
	facts, err = dnsMutationFacts("Default", "", largeBefore, &adminv1.DnsPreset{BlockAds: true})
	if err != nil {
		t.Fatalf("large previous split value blocked a small change: %v", err)
	}
	encoded, err := marshal(facts)
	if err != nil || len(encoded) > 16<<10 {
		t.Fatalf("bounded DNS approval is %d bytes, err %v", len(encoded), err)
	}
	var markedMore bool
	for _, fact := range facts {
		if fact.Value == "1 more before" {
			markedMore = true
		}
	}
	if !markedMore {
		t.Fatalf("omitted old split content has no remainder marker: %+v", facts)
	}
}

func TestApprovalChunksDoNotRecheckAKeyAlphabetBoundary(t *testing.T) {
	prefix := strings.Repeat("word ", 160)
	run := strings.Repeat("ab", 31) + "a"
	value := prefix + run + " " + strings.Repeat("word ", 10)
	facts, err := approvalTextFacts("notes", value)
	if err != nil || len(facts) != 2 {
		t.Fatalf("legal long text was refused at a chunk boundary: facts=%d err=%v", len(facts), err)
	}
	var joined strings.Builder
	for _, fact := range facts {
		_, payload, ok := strings.Cut(fact.Value, ") ")
		if !ok {
			t.Fatalf("chunk has no prefix: %q", fact.Value)
		}
		joined.WriteString(payload)
	}
	if joined.String() != value {
		t.Fatal("approval chunks changed the value")
	}
}

func TestScrubJSONDoesNotEscapeHTMLAndKeepsCursorFields(t *testing.T) {
	pageToken := encodeTestUserPageToken("alpha")
	cursor := "1790000000~event_ref"
	value := strings.Repeat("<>&", 3000)
	input := `{"<>&":"` + value + `","next_page_token":"` + pageToken + `","next_cursor":"` + cursor + `"}`
	got := scrubJSON(input)
	if !json.Valid([]byte(got)) || !strings.Contains(got, `"<>&":"<>&`) || strings.Contains(got, `\u003c`) || len(got) > len(input)+20 {
		t.Fatalf("JSON output expanded or escaped HTML: len=%d input=%d preview=%q", len(got), len(input), got[:min(len(got), 80)])
	}
	if !strings.Contains(got, pageToken) || !strings.Contains(got, cursor) {
		t.Fatalf("valid pagination cursors were scrubbed: %s", got[len(got)-160:])
	}
	if scrubJSON(`{"next_page_token":"not-a-page-token"}`) != `{"next_page_token":"not-a-page-token"}` {
		t.Fatal("cursor-shaped field bypassed scrubbing without a valid page token")
	}
}

func TestScrubberRedactsKeyNamesByNormalizedContains(t *testing.T) {
	for _, name := range []string{"wg_private_key", "realityPrivateKey", "private.key", "private_keys", "my-secret key b64", "preshared.key", "PSK", "P_S_K", "password"} {
		if !secretKeyName(name) {
			t.Errorf("secret key name %q was not recognized", name)
		}
		got := scrubJSON(`{"` + name + `":"short-value"}`)
		if !strings.Contains(got, `"[key]"`) {
			t.Errorf("value under secret key name %q was not redacted: %s", name, got)
		}
	}
	for _, name := range []string{"public_key", "publicKey", "key_id", "keys", "key"} {
		if secretKeyName(name) {
			t.Errorf("public or generic key name %q was recognized as secret", name)
		}
		got := scrubJSON(`{"` + name + `":"short-value"}`)
		if !strings.Contains(got, `"short-value"`) {
			t.Errorf("ordinary value under key name %q was redacted: %s", name, got)
		}
	}
}

func TestLongLowEntropyRunDoesNotHideFollowingSecret(t *testing.T) {
	low := strings.Repeat("a", 40)
	secret := "Zx9Qw3Er7Ty1Ui5Op2As8Df4Gh6Jk0LmNbVcXz"
	got := scrub(low + " " + secret)
	want := low + " [token]"
	if got != want {
		t.Fatalf("scrub(%q) = %q, want %q", low+" "+secret, got, want)
	}
}

func TestApprovalJoinersAreBoundedAndCannotSplitCredentials(t *testing.T) {
	for _, value := range []string{"می\u200cخواهم", "check \U0001f469\u200d\U0001f4bb later", "©\u200d💻", "ab\u200dcd"} {
		facts, err := approvalTextFacts("notes", value)
		if err != nil || len(facts) != 1 || facts[0].Value != value {
			t.Errorf("valid joiner placement was refused: value=%q facts=%+v err=%v", value, facts, err)
		}
	}
	for _, value := range []string{"\u200dleading", "trailing\u200c", "a\u200c\u200d b", "a \u200db"} {
		if _, err := approvalTextFacts("notes", value); err == nil {
			t.Errorf("invalid joiner placement was accepted: %q", value)
		}
	}
	token := "tk1_abcdefgh\u200dijklmnopqrstuv"
	if _, err := approvalTextFacts("notes", token); err == nil {
		t.Fatal("a credential split by a joiner was accepted")
	}
	cleaned := clean("see tk1_abcdefgh\u200bijklmnopqrstuv", 100)
	if strings.Contains(cleaned, "tk1_") || !strings.Contains(cleaned, "[token]") {
		t.Fatalf("clean removed a format character after scrubbing: %q", cleaned)
	}
}

func TestUsersSearchPageTokenWorksThroughMCPAndAssistant(t *testing.T) {
	e := newTestEnv(t)
	e.w.users = map[string]*adminv1.GetUserResponse{
		"usr_page0": {User: &adminv1.User{Id: "usr_page0", Name: "alpha"}},
		"usr_page1": {User: &adminv1.User{Id: "usr_page1", Name: "bravo"}},
		"usr_page2": {User: &adminv1.User{Id: "usr_page2", Name: "charlie"}},
	}
	_, secret := e.token(ProfileReadonly)
	client := e.session(secret)
	first := decode[UsersV](t, mustOK(t, client, "users_search", map[string]any{"page_size": 1}))
	if len(first.Users) != 1 || first.Users[0].Name != "alpha" || !validCursor("next_page_token", first.NextPageToken) {
		t.Fatalf("first MCP page = %+v", first)
	}
	second := decode[UsersV](t, mustOK(t, client, "users_search", map[string]any{"page_size": 1, "page_token": first.NextPageToken}))
	if len(second.Users) != 1 || second.Users[0].Name != "bravo" {
		t.Fatalf("second MCP page = %+v", second)
	}
	if _, failed := callTool(t, client, "users_search", map[string]any{"page_token": "garbage"}); !failed {
		t.Fatal("MCP accepted a page token without the expected encoded prefix")
	}

	server, err := NewAssistant(Config{Plans: e.plans, Auth: e.auth, API: e.w.api(e.auth), Now: e.now})
	if err != nil {
		t.Fatal(err)
	}
	session, err := server.OpenAssistant(context.Background(), AssistantIdentity{TokenID: "tk_assistant_page", Profile: string(ProfileReadonly), AdminID: "adm_page", FollowUp: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	call := func(args string) UsersV {
		result, err := session.CallTool(context.Background(), "users_search", json.RawMessage(args))
		if err != nil || result.IsError {
			t.Fatalf("assistant users_search %s: %+v %v", args, result, err)
		}
		var view UsersV
		if err := json.Unmarshal([]byte(result.Text), &view); err != nil {
			t.Fatalf("assistant users_search result %q: %v", result.Text, err)
		}
		return view
	}
	assistantFirst := call(`{"page_size":1}`)
	if assistantFirst.NextPageToken == "" || !validCursor("next_page_token", assistantFirst.NextPageToken) {
		t.Fatalf("assistant page token = %q", assistantFirst.NextPageToken)
	}
	assistantSecond := call(fmt.Sprintf(`{"page_size":1,"page_token":%q}`, assistantFirst.NextPageToken))
	if len(assistantSecond.Users) != 1 || assistantSecond.Users[0].Name != "bravo" {
		t.Fatalf("second assistant page = %+v", assistantSecond)
	}
}

func TestFollowUpCannotCreateDangerousOperatorPlan(t *testing.T) {
	e := newTestEnv(t)
	server, err := NewAssistant(Config{Plans: e.plans, Auth: e.auth, API: e.w.api(e.auth), Now: e.now})
	if err != nil {
		t.Fatal(err)
	}
	session, err := server.OpenAssistant(context.Background(), AssistantIdentity{TokenID: "tk_followup_operator", Profile: string(ProfileOperator), AdminID: "adm_followup_operator", FollowUp: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	result, err := session.CallTool(context.Background(), "user_reset_traffic_plan", json.RawMessage(`{"user_ids":["usr_n0","usr_n1","usr_n2","usr_n3"]}`))
	if err != nil || !result.IsError || !strings.Contains(result.Text, "this plan needs owner approval") {
		t.Fatalf("dangerous follow-up plan result = %+v, err %v", result, err)
	}
	e.plans.mu.Lock()
	defer e.plans.mu.Unlock()
	if len(e.plans.byID) != 0 {
		t.Fatalf("follow-up stored %d dangerous plans", len(e.plans.byID))
	}
}

func encodeTestUserPageToken(name string) string {
	return base64.RawURLEncoding.EncodeToString([]byte("\x00mistgate-user-list-v1:a" + name))
}
