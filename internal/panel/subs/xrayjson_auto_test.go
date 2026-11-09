package subs

import (
	"bytes"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func xrayConfigs(t *testing.T, b []byte) []xrayConfig {
	t.Helper()
	var configs []xrayConfig
	if err := json.Unmarshal(b, &configs); err != nil {
		t.Fatalf("decode Xray configs: %v\n%s", err, b)
	}
	return configs
}

func xrayConfigBytes(t *testing.T, b []byte) []json.RawMessage {
	t.Helper()
	var configs []json.RawMessage
	if err := json.Unmarshal(b, &configs); err != nil {
		t.Fatalf("decode Xray config array: %v\n%s", err, b)
	}
	return configs
}

func outboundTags(t *testing.T, outbounds []json.RawMessage) []string {
	t.Helper()
	tags := make([]string, 0, len(outbounds))
	for _, outbound := range outbounds {
		var item struct {
			Tag string `json:"tag"`
		}
		if err := json.Unmarshal(outbound, &item); err != nil {
			t.Fatalf("decode Xray outbound tag: %v", err)
		}
		tags = append(tags, item.Tag)
	}
	return tags
}

func xrayTagValues(value any) []string {
	var tags []string
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			if key == "tag" {
				if tag, ok := child.(string); ok {
					tags = append(tags, tag)
				}
			}
			tags = append(tags, xrayTagValues(child)...)
		}
	case []any:
		for _, child := range value {
			tags = append(tags, xrayTagValues(child)...)
		}
	}
	return tags
}

func TestXrayJSONAutoSelect(t *testing.T) {
	lines := fixtures(t, xrayFragments)
	names := []string{"Auto", "nl1", "fi1"}
	preset := ruSplit()
	preset.BlockAds = true
	legacy, err := (xrayProfile{lines: lines, names: names, preset: preset}).build()
	if err != nil {
		t.Fatal(err)
	}
	autoBytes, err := (xrayProfile{lines: lines, names: names, preset: preset, autoSelect: true}).build()
	if err != nil {
		t.Fatal(err)
	}
	legacyRaw := xrayConfigBytes(t, legacy)
	autoRaw := xrayConfigBytes(t, autoBytes)
	if len(autoRaw) != len(lines)+1 || len(legacyRaw) != len(lines) {
		t.Fatalf("auto configs = %d and legacy configs = %d for %d servers", len(autoRaw), len(legacyRaw), len(lines))
	}
	for i := range legacyRaw {
		if !bytes.Equal(autoRaw[i+1], legacyRaw[i]) {
			t.Errorf("per-server config %d changed when Auto was added", i)
		}
	}

	configs := xrayConfigs(t, autoBytes)
	auto := configs[0]
	legacyConfig := xrayConfigs(t, legacy)[0]
	if want := uniqueName("Auto", map[string]bool{"Auto": true}); auto.Remarks != want || slices.Contains(names, auto.Remarks) {
		t.Errorf("Auto remarks %q collide with a server name", auto.Remarks)
	}
	autoTags := outboundTags(t, auto.Outbounds)
	if len(autoTags) < len(lines) {
		t.Fatalf("Auto has %d outbounds for %d servers", len(autoTags), len(lines))
	}
	if got, want := autoTags[:len(lines)], []string{"proxy-1", "proxy-2", "proxy-3"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Auto proxy tags = %v, want %v", got, want)
	}
	if got, want := autoTags[len(lines):], []string{"direct", "dns-out", "block"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Auto static outbound tags = %v, want %v", got, want)
	}
	for i, tag := range autoTags {
		if matchesPrefix := strings.HasPrefix(tag, xrayAutoProxyPrefix); matchesPrefix != (i < len(lines)) {
			t.Errorf("outbound tag %q at index %d has selector-prefix match %v", tag, i, matchesPrefix)
		}
	}
	var autoTree any
	if err := json.Unmarshal(autoRaw[0], &autoTree); err != nil {
		t.Fatal(err)
	}
	allPrefixTags := make([]string, 0, len(lines))
	for _, tag := range xrayTagValues(autoTree) {
		if strings.HasPrefix(tag, xrayAutoProxyPrefix) {
			allPrefixTags = append(allPrefixTags, tag)
		}
	}
	slices.Sort(allPrefixTags)
	if want := []string{"proxy-1", "proxy-2", "proxy-3"}; !reflect.DeepEqual(allPrefixTags, want) {
		t.Errorf("config tags with selector prefix = %v, want only the proxy outbounds %v", allPrefixTags, want)
	}
	if auto.Routing.DomainStrategy != "AsIs" || len(auto.Routing.Balancers) != 1 {
		t.Fatalf("Auto routing = %#v", auto.Routing)
	}
	balancer := auto.Routing.Balancers[0]
	if balancer.Tag != xrayAutoBalancerTag || !reflect.DeepEqual(balancer.Selector, []string{xrayAutoProxyPrefix}) ||
		balancer.Strategy.Type != "leastPing" || balancer.FallbackTag != "proxy-1" {
		t.Errorf("Auto balancer = %#v", balancer)
	}
	if auto.Observatory == nil || !reflect.DeepEqual(auto.Observatory.SubjectSelector, []string{xrayAutoProxyPrefix}) ||
		auto.Observatory.ProbeURL != xrayAutoProbeURL || auto.Observatory.ProbeInterval != "5m" || !auto.Observatory.EnableConcurrency {
		t.Errorf("Auto observatory = %#v", auto.Observatory)
	}
	if !reflect.DeepEqual(auto.Inbounds, legacyConfig.Inbounds) || !reflect.DeepEqual(auto.Log, legacyConfig.Log) || !reflect.DeepEqual(auto.DNS, legacyConfig.DNS) {
		t.Error("Auto did not reuse the per-server inbound, log and DNS sections")
	}
	if len(auto.Routing.Rules) != len(legacyConfig.Routing.Rules) {
		t.Fatalf("Auto has %d rules, legacy has %d", len(auto.Routing.Rules), len(legacyConfig.Routing.Rules))
	}
	balancerRules := 0
	for i, rule := range auto.Routing.Rules {
		want := legacyConfig.Routing.Rules[i]
		if want.OutboundTag == xrayProxyTag {
			want.OutboundTag = ""
			want.BalancerTag = xrayAutoBalancerTag
		}
		if !reflect.DeepEqual(rule, want) {
			t.Errorf("Auto rule %d = %#v, want %#v", i, rule, want)
		}
		if rule.OutboundTag == xrayProxyTag {
			t.Errorf("Auto rule %d still has outboundTag %q", i, rule.OutboundTag)
		}
		if rule.BalancerTag == xrayAutoBalancerTag {
			balancerRules++
		}
	}
	if balancerRules == 0 {
		t.Error("Auto has no rule that selects its balancer")
	}
	if !reflect.DeepEqual(auto.Outbounds[len(lines):], configs[1].Outbounds[1:]) {
		t.Error("Auto static outbounds differ from the per-server static outbounds")
	}
}

func TestXrayJSONAutoSelectNeedsTwoServersAndSetting(t *testing.T) {
	lines := fixtures(t, xrayFragments[:2])
	for _, tc := range []struct {
		name       string
		lines      []string
		autoSelect bool
		want       int
	}{
		{name: "one server", lines: lines[:1], autoSelect: true, want: 1},
		{name: "setting off", lines: lines, autoSelect: false, want: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := (xrayProfile{lines: tc.lines, autoSelect: tc.autoSelect}).build()
			if err != nil {
				t.Fatal(err)
			}
			configs := xrayConfigs(t, b)
			if len(configs) != tc.want {
				t.Fatalf("got %d configs, want %d", len(configs), tc.want)
			}
			for _, c := range configs {
				if c.Observatory != nil || len(c.Routing.Balancers) != 0 {
					t.Errorf("unexpected Auto fields in %q", c.Remarks)
				}
			}
		})
	}
}

func TestXrayJSONAutoSelectLeavesPlaceholdersUnchanged(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    xrayProfile
	}{
		{name: "note", p: xrayProfile{note: "Access paused"}},
		{name: "no rendered servers", p: xrayProfile{lines: []string{"not json"}, none: "No servers"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want, err := tc.p.build()
			if err != nil {
				t.Fatal(err)
			}
			tc.p.autoSelect = true
			got, err := tc.p.build()
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("placeholder changed with Auto enabled:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

func TestXrayJSONAutoOutboundTagsAreSequentialAfterSkippedLines(t *testing.T) {
	lines := fixtures(t, xrayFragments[:2])
	b, err := (xrayProfile{lines: []string{"not json", lines[0], "[]", lines[1]}, names: []string{"bad", "de1", "bad", "nl1"}, autoSelect: true}).build()
	if err != nil {
		t.Fatal(err)
	}
	configs := xrayConfigs(t, b)
	if len(configs) != 3 {
		t.Fatalf("got %d configs, want Auto and two rendered servers", len(configs))
	}
	if got, want := outboundTags(t, configs[0].Outbounds), []string{"proxy-1", "proxy-2", "direct"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Auto tags after skipped lines = %v, want %v", got, want)
	}
	if configs[1].Remarks != "de1" || configs[2].Remarks != "nl1" {
		t.Errorf("per-server remarks = %q, %q", configs[1].Remarks, configs[2].Remarks)
	}
}
