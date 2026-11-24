//go:build !js

package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
)

func hashText(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:16])
}

type subsSnapshot struct {
	settings *adminv1.SubscriptionSettings
	hash     string
	shared   uint32
	awgKeys  uint32
}

func (c *call) subscriptionSnapshot() (*subsSnapshot, error) {
	r, err := c.cl.Subs.GetSubscriptionSettings(c.ctx, connect.NewRequest(&adminv1.GetSubscriptionSettingsRequest{}))
	if err != nil {
		return nil, apiError(err)
	}
	s := r.Msg.GetSettings()
	if s == nil {
		return nil, errors.New("the panel sent no subscription settings")
	}
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(s)
	if err != nil {
		return nil, errors.New("internal error")
	}
	h := hashText(string(b))
	return &subsSnapshot{settings: s, hash: h, shared: r.Msg.GetSharedLinkUsers(), awgKeys: r.Msg.GetLinkAwgKeys()}, nil
}

func (c *call) saveSubscriptionSettings(hash string, next *adminv1.SubscriptionSettings) error {
	cur, err := c.subscriptionSnapshot()
	if err != nil {
		return err
	}
	if cur.hash != hash {
		return changedSincePlan()
	}
	if err := checked(next); err != nil {
		return err
	}
	if _, err := c.cl.Subs.UpdateSubscriptionSettings(c.ctx, connect.NewRequest(&adminv1.UpdateSubscriptionSettingsRequest{Settings: next})); err != nil {
		return apiError(err)
	}
	return nil
}

var settingsProcs = procs(adminv1connect.SubscriptionServiceGetSubscriptionSettingsProcedure, adminv1connect.SubscriptionServiceUpdateSubscriptionSettingsProcedure)

type serveRuleArg struct {
	UAContains string `json:"ua_contains" jsonschema:"case-insensitive substring of the User-Agent, e.g. happ or mihomo"`
	Format     string `json:"format" jsonschema:"base64_uris, user_page, decoy, mihomo_yaml, xray_json or singbox_json"`
}

type subsRulesArgs struct {
	ReasonField
	Rules        []serveRuleArg `json:"rules" jsonschema:"complete ordered User-Agent rules; first match wins"`
	SettingsHash string         `json:"settings_hash,omitempty" jsonschema:"-"`
}

var subFormats = map[string]adminv1.SubFormat{
	"base64_uris":  adminv1.SubFormat_SUB_FORMAT_BASE64_URIS,
	"user_page":    adminv1.SubFormat_SUB_FORMAT_USER_PAGE,
	"decoy":        adminv1.SubFormat_SUB_FORMAT_DECOY,
	"mihomo_yaml":  adminv1.SubFormat_SUB_FORMAT_MIHOMO_YAML,
	"xray_json":    adminv1.SubFormat_SUB_FORMAT_XRAY_JSON,
	"singbox_json": adminv1.SubFormat_SUB_FORMAT_SINGBOX_JSON,
}

func makeServeRules(in []serveRuleArg) ([]*adminv1.ServeRule, error) {
	if len(in) > 32 {
		return nil, errors.New("rules: at most 32 ordered entries")
	}
	out := make([]*adminv1.ServeRule, 0, len(in))
	for i, v := range in {
		ua := strings.TrimSpace(v.UAContains)
		if ua == "" || len(ua) > 200 {
			return nil, fmt.Errorf("rules[%d].ua_contains must be 1-200 characters", i)
		}
		if err := visible("ua_contains", ua); err != nil {
			return nil, err
		}
		if err := rejectSecretShaped("ua_contains", ua); err != nil {
			return nil, err
		}
		f, ok := subFormats[strings.ToLower(strings.TrimSpace(v.Format))]
		if !ok {
			return nil, fmt.Errorf("rules[%d].format must be base64_uris, user_page, decoy, mihomo_yaml, xray_json or singbox_json", i)
		}
		out = append(out, &adminv1.ServeRule{UaContains: ua, Format: f})
	}
	return out, nil
}

var subsRulesSet = changeSpec[subsRulesArgs]{
	name: "subscription_rules_set", min: ProfileOperator, danger: true, procs: settingsProcs,
	desc: "Replace the ordered User-Agent serving rules for subscriptions. The first matching substring wins; rules with no match use the panel fallback. This changes what users receive, so it waits for the owner.",
	plan: func(c *call, a subsRulesArgs) (*planned, error) {
		cur, err := c.subscriptionSnapshot()
		if err != nil {
			return nil, err
		}
		rules, err := makeServeRules(a.Rules)
		if err != nil {
			return nil, err
		}
		next := proto.Clone(cur.settings).(*adminv1.SubscriptionSettings)
		next.Rules = rules
		if err := checked(next); err != nil {
			return nil, err
		}
		if proto.Equal(cur.settings, next) {
			return nil, errors.New("nothing to change: the rules already look like this")
		}
		facts := []Fact{codedFact("rules", fmt.Sprintf("replace the ordered rules with %d entries", len(rules)), "rule_count", "n", strconv.Itoa(len(rules)))}
		for i, rule := range rules {
			facts = append(facts, Fact{Key: fmt.Sprintf("rule_%d", i+1), Value: strconv.Quote(rule.GetUaContains()) + " -> " + subFormatWord(rule.GetFormat()), Untrusted: true})
		}
		a.Rules = make([]serveRuleArg, len(rules))
		for i, r := range rules {
			a.Rules[i] = serveRuleArg{UAContains: r.GetUaContains(), Format: subFormatWord(r.GetFormat())}
		}
		a.SettingsHash = cur.hash
		return &planned{Summary: "Replace the ordered User-Agent rules used to choose subscription formats. The owner must approve this shared change.", Facts: facts, Danger: []string{dangerPage}, Params: a}, nil
	},
	apply: func(c *call, a subsRulesArgs, _ Plan) (done, error) {
		rules, err := makeServeRules(a.Rules)
		if err != nil {
			return done{}, failure("plan_unreadable", "the stored rules are unreadable: make a new plan")
		}
		cur, err := c.subscriptionSnapshot()
		if err != nil {
			return done{}, err
		}
		if cur.hash != a.SettingsHash {
			return done{}, changedSincePlan()
		}
		next := proto.Clone(cur.settings).(*adminv1.SubscriptionSettings)
		next.Rules = rules
		if err := c.saveSubscriptionSettings(a.SettingsHash, next); err != nil {
			return done{}, err
		}
		return doneWith("subscription_rules_updated", "Subscription serving rules updated."), nil
	},
}

func subFormatWord(f adminv1.SubFormat) string {
	for w, value := range subFormats {
		if value == f {
			return w
		}
	}
	return "base64_uris"
}

type subsTextsArgs struct {
	ReasonField
	Title               *string `json:"title,omitempty" jsonschema:"shared subscription title; empty uses the brand"`
	Announcement        *string `json:"announcement,omitempty" jsonschema:"text shown in apps and on the page; empty hides it"`
	SupportURL          *string `json:"support_url,omitempty" jsonschema:"support link; empty removes it"`
	UpdateIntervalHours *uint32 `json:"update_interval_hours,omitempty" jsonschema:"hours between app refreshes; 0 uses the default 12"`
	ServerNameTemplate  *string `json:"server_name_template,omitempty" jsonschema:"server name template using {flag}, {country}, {node}, and {profile}; empty uses the default"`
	// ResponseHeaders replaces the whole list of the owner's own response headers of a successful subscription answer; [] removes them.
	ResponseHeaders *[]responseHeaderArg `json:"response_headers,omitempty" jsonschema:"the complete ordered list of the owner's own response headers (for example the flclashx-* headers FlClashX reads), each with the formats and groups it goes to; [] removes them all. The names of the panel's own and the web server's headers are refused; at most 20 entries and 4096 bytes in all as sent. When two entries of one name apply to an answer, the first wins. Refusals and the user page carry none"`
	SettingsHash    string               `json:"settings_hash,omitempty" jsonschema:"-"`
}

var subsTextsSet = changeSpec[subsTextsArgs]{
	name: "subscription_texts_set", min: ProfileOperator, danger: true, procs: slices.Concat(settingsProcs, procs(adminv1connect.GroupServiceListGroupsProcedure)),
	desc: "Change the subscription title, announcement, support link, refresh interval, server-name template or the owner's own response headers shared by users. Give only fields to change. Text is shown in the owner's approval card; this always waits for approval.",
	plan: func(c *call, a subsTextsArgs) (*planned, error) {
		if a.Title == nil && a.Announcement == nil && a.SupportURL == nil && a.UpdateIntervalHours == nil && a.ServerNameTemplate == nil && a.ResponseHeaders == nil {
			return nil, errors.New("give at least one subscription text field to change")
		}
		cur, err := c.subscriptionSnapshot()
		if err != nil {
			return nil, err
		}
		next := proto.Clone(cur.settings).(*adminv1.SubscriptionSettings)
		var facts []Fact
		for _, f := range []struct {
			key string
			in  *string
			dst *string
		}{{"title", a.Title, &next.Title}, {"announcement", a.Announcement, &next.Announcement}, {"support_url", a.SupportURL, &next.SupportUrl}, {"server_name_template", a.ServerNameTemplate, &next.ServerNameTemplate}} {
			if f.in == nil {
				continue
			}
			if err := visible(f.key, *f.in); err != nil {
				return nil, err
			}
			if err := rejectSecretShaped(f.key, *f.in); err != nil {
				return nil, err
			}
			if *f.dst != *f.in {
				changeFacts, err := approvalTextChangeFacts(f.key, *f.dst, *f.in)
				if err != nil {
					return nil, err
				}
				facts = append(facts, changeFacts...)
				*f.dst = *f.in
			}
		}
		if a.UpdateIntervalHours != nil && next.GetUpdateIntervalHours() != *a.UpdateIntervalHours {
			facts = append(facts, changeFact("update_interval_hours", strconv.FormatUint(uint64(next.GetUpdateIntervalHours()), 10)+" -> "+strconv.FormatUint(uint64(*a.UpdateIntervalHours), 10), strconv.FormatUint(uint64(next.GetUpdateIntervalHours()), 10), strconv.FormatUint(uint64(*a.UpdateIntervalHours), 10)))
			next.UpdateIntervalHours = *a.UpdateIntervalHours
		}
		if a.ResponseHeaders != nil {
			groups, err := c.groupNames()
			if err != nil {
				return nil, err
			}
			list, err := makeResponseHeaders(*a.ResponseHeaders, groups)
			if err != nil {
				return nil, err
			}
			next.ResponseHeaders = list
			if err := checked(next); err != nil { // normalized: the facts and the stored plan show what is saved
				return nil, err
			}
			if !slices.EqualFunc(cur.settings.ResponseHeaders, next.ResponseHeaders, func(x, y *adminv1.ResponseHeader) bool { return proto.Equal(x, y) }) {
				facts = append(facts, responseHeaderFacts(next.ResponseHeaders, groups)...)
			}
			args := responseHeaderArgs(next.ResponseHeaders)
			a.ResponseHeaders = &args
		}
		if len(facts) == 0 {
			return nil, errors.New("nothing to change: the settings already look like this")
		}
		if err := checked(next); err != nil {
			return nil, err
		}
		a.SettingsHash = cur.hash
		return &planned{Summary: "Change the shared subscription title, texts, own response headers or display settings. The owner must approve this change.", Facts: facts, Danger: []string{dangerPage}, Params: a}, nil
	},
	apply: func(c *call, a subsTextsArgs, _ Plan) (done, error) {
		cur, err := c.subscriptionSnapshot()
		if err != nil {
			return done{}, err
		}
		if cur.hash != a.SettingsHash {
			return done{}, changedSincePlan()
		}
		next := proto.Clone(cur.settings).(*adminv1.SubscriptionSettings)
		if a.Title != nil {
			next.Title = *a.Title
		}
		if a.Announcement != nil {
			next.Announcement = *a.Announcement
		}
		if a.SupportURL != nil {
			next.SupportUrl = *a.SupportURL
		}
		if a.UpdateIntervalHours != nil {
			next.UpdateIntervalHours = *a.UpdateIntervalHours
		}
		if a.ServerNameTemplate != nil {
			next.ServerNameTemplate = *a.ServerNameTemplate
		}
		if a.ResponseHeaders != nil {
			groups, err := c.groupNames()
			if err != nil {
				return done{}, err
			}
			list, err := makeResponseHeaders(*a.ResponseHeaders, groups)
			if err != nil {
				return done{}, failure("plan_unreadable", "the stored headers are unreadable: make a new plan")
			}
			next.ResponseHeaders = list
		}
		if err := c.saveSubscriptionSettings(a.SettingsHash, next); err != nil {
			return done{}, err
		}
		return doneWith("subscription_texts_updated", "Subscription text settings updated."), nil
	},
}

type responseHeaderArg struct {
	Name    string   `json:"name" jsonschema:"an HTTP header name, e.g. flclashx-servicename; the panel's own and the web server's headers are refused (Profile-*, Subscription-*, X-Hwid-*, Mistgate-*, Content-*, Cache-Control, Set-Cookie and the like: the full list is reserved_header_names of subscription_settings_get)"`
	Value   string   `json:"value" jsonschema:"1 to 1000 bytes, no line breaks or control characters; ASCII unless base64 is true"`
	Base64  bool     `json:"base64,omitempty" jsonschema:"send the value as base64: and its base64 (text with other than ASCII characters needs it)"`
	Formats []string `json:"formats,omitempty" jsonschema:"base64_uris, mihomo_yaml, xray_json or singbox_json; empty = every format"`
	Groups  []string `json:"groups,omitempty" jsonschema:"ids of the groups whose people get it; empty = every group"`
}

var headerFormatWords = map[string]adminv1.SubFormat{
	"base64_uris": adminv1.SubFormat_SUB_FORMAT_BASE64_URIS, "mihomo_yaml": adminv1.SubFormat_SUB_FORMAT_MIHOMO_YAML,
	"xray_json": adminv1.SubFormat_SUB_FORMAT_XRAY_JSON, "singbox_json": adminv1.SubFormat_SUB_FORMAT_SINGBOX_JSON,
}

// makeResponseHeaders turns the agent's list into the settings' own; groupName is the name of each group id the panel has.
func makeResponseHeaders(in []responseHeaderArg, groupName map[string]string) ([]*adminv1.ResponseHeader, error) {
	if len(in) > subsettings.MaxResponseHeaders {
		return nil, fmt.Errorf("headers: at most %d entries", subsettings.MaxResponseHeaders)
	}
	out := make([]*adminv1.ResponseHeader, 0, len(in))
	for i, h := range in {
		w := fmt.Sprintf("headers[%d]", i)
		name, value := strings.TrimSpace(h.Name), strings.TrimSpace(h.Value)
		for _, f := range []struct{ what, s string }{{w + ".name", name}, {w + ".value", value}} {
			if err := visible(f.what, f.s); err != nil {
				return nil, err
			}
			if err := rejectSecretShaped(f.what, f.s); err != nil {
				return nil, err
			}
		}
		e := &adminv1.ResponseHeader{Name: name, Value: value, Base64: h.Base64}
		for _, word := range h.Formats {
			f, ok := headerFormatWords[strings.ToLower(strings.TrimSpace(word))]
			if !ok {
				return nil, fmt.Errorf("%s.formats: base64_uris, mihomo_yaml, xray_json or singbox_json", w)
			}
			e.Formats = append(e.Formats, f)
		}
		if len(h.Groups) > 0 {
			ids, err := uniqueIDs(w+".groups", h.Groups, false)
			if err != nil {
				return nil, err
			}
			for _, id := range ids {
				if _, ok := groupName[id]; !ok {
					return nil, fmt.Errorf("%s.groups: no group with id %s (group ids are in groups_list)", w, clean(id, 60))
				}
			}
			e.OnlyGroups, e.GroupIds = true, ids
		}
		out = append(out, e)
	}
	return out, nil
}

// groupNames is the name of every group by id.
func (c *call) groupNames() (map[string]string, error) {
	r, err := c.cl.Group.ListGroups(c.ctx, connect.NewRequest(&adminv1.ListGroupsRequest{}))
	if err != nil {
		return nil, apiError(err)
	}
	out := map[string]string{}
	for _, g := range r.Msg.GetGroups() {
		out[g.GetId()] = g.GetName()
	}
	return out, nil
}

func headerWords(formats []adminv1.SubFormat) []string {
	out := make([]string, 0, len(formats))
	for _, f := range formats {
		out = append(out, subFormatWord(f))
	}
	return out
}

// responseHeaderFacts shows the owner what the list will be: one fact per entry, in order. The text is the agent's, so untrusted.
func responseHeaderFacts(list []*adminv1.ResponseHeader, groups map[string]string) []Fact {
	facts := []Fact{codedFact("response_headers", fmt.Sprintf("replace the own response headers with %d entries", len(list)), "response_header_count", "n", strconv.Itoa(len(list)))}
	for i, e := range list {
		scope := "all formats"
		if len(e.Formats) > 0 {
			scope = strings.Join(headerWords(e.Formats), ", ")
		}
		who := "all groups"
		if e.OnlyGroups {
			var names []string
			for _, id := range e.GroupIds {
				names = append(names, clean(groups[id], 60))
			}
			who = "groups: " + strings.Join(names, ", ")
		}
		enc := ""
		if e.Base64 {
			enc = " [base64]"
		}
		facts = append(facts, Fact{Key: fmt.Sprintf("header_%d", i+1), Value: strconv.Quote(e.Name) + ": " + strconv.Quote(e.Value) + enc + " (" + scope + "; " + who + ")", Untrusted: true})
	}
	return facts
}

// responseHeaderArgs is the normalized list as the plan stores it for the apply.
func responseHeaderArgs(list []*adminv1.ResponseHeader) []responseHeaderArg {
	out := make([]responseHeaderArg, 0, len(list))
	for _, e := range list {
		arg := responseHeaderArg{Name: e.Name, Value: e.Value, Base64: e.Base64}
		if len(e.Formats) > 0 {
			arg.Formats = headerWords(e.Formats)
		}
		if e.OnlyGroups {
			arg.Groups = e.GroupIds
		}
		out = append(out, arg)
	}
	return out
}

type subsLinkLimitsArgs struct {
	ReasonField
	Mode                *string `json:"mode,omitempty" jsonschema:"track counts only; enforce refuses new devices over the limit; strict also refuses apps without a device id"`
	DeviceIdleAfterDays *uint32 `json:"device_idle_after_days,omitempty" jsonschema:"idle after this many days without a connection; 0 disables it; otherwise 7 to 365"`
	LinkAWG             *bool   `json:"link_awg,omitempty" jsonschema:"include AmneziaWG servers in subscription link apps"`
	LinkAutoSelect      *bool   `json:"link_auto_select,omitempty" jsonschema:"put automatic server selection (Auto) first in the Mihomo, sing-box and Xray JSON subscription documents"`
	LinkAutoInterval    *uint32 `json:"link_auto_interval,omitempty" jsonschema:"seconds between the server checks of the Auto group: 60, 120, 300 or 600"`
	SettingsHash        string  `json:"settings_hash,omitempty" jsonschema:"-"`
}

var linkModes = map[string]adminv1.LinkDeviceLimit{
	"track":   adminv1.LinkDeviceLimit_LINK_DEVICE_LIMIT_TRACK,
	"enforce": adminv1.LinkDeviceLimit_LINK_DEVICE_LIMIT_ENFORCE,
	"strict":  adminv1.LinkDeviceLimit_LINK_DEVICE_LIMIT_STRICT,
}

func linkModeWord(s *adminv1.SubscriptionSettings) string {
	mode := s.GetLinkDeviceLimit()
	if mode == adminv1.LinkDeviceLimit_LINK_DEVICE_LIMIT_UNSPECIFIED {
		return "track"
	}
	for word, value := range linkModes {
		if mode == value {
			return word
		}
	}
	return "track"
}

var subsLinkLimitsSet = changeSpec[subsLinkLimitsArgs]{
	name: "subscription_link_limits_set", min: ProfileOperator, danger: true, procs: settingsProcs,
	desc: "Set device-limit handling and the idle period for subscription link apps, whether link apps include AmneziaWG entries, whether Mihomo and sing-box profiles include automatic server selection, and how often the Auto group checks the servers (60, 120, 300 or 600 seconds). The idle period is 30 days by default, 0 disables it, and otherwise accepts 7 to 365 days. Strict mode refuses shared no-device-id users; turning link_awg off drops the link keys of the link apps on the next fetch (not those of the Mistgate app). Always waits for owner approval.",
	plan: func(c *call, a subsLinkLimitsArgs) (*planned, error) {
		if a.Mode == nil && a.DeviceIdleAfterDays == nil && a.LinkAWG == nil && a.LinkAutoSelect == nil && a.LinkAutoInterval == nil {
			return nil, errors.New("give mode, device_idle_after_days, link_awg, link_auto_select, link_auto_interval, or a combination")
		}
		cur, err := c.subscriptionSnapshot()
		if err != nil {
			return nil, err
		}
		next := proto.Clone(cur.settings).(*adminv1.SubscriptionSettings)
		var facts []Fact
		if a.Mode != nil {
			mode := strings.ToLower(strings.TrimSpace(*a.Mode))
			value, ok := linkModes[mode]
			if !ok {
				return nil, errors.New("mode must be track, enforce or strict")
			}
			a.Mode = &mode
			before := linkModeWord(cur.settings)
			if before != mode {
				next.LinkDeviceLimit = &value
				facts = append(facts, changeFact("mode", before+" -> "+mode, before, mode))
			}
			if mode == "strict" && before != "strict" && cur.shared > 0 {
				n := strconv.FormatUint(uint64(cur.shared), 10)
				facts = append(facts, codedFact("shared_link_users", plural(int(cur.shared), "shared user will be refused", "shared users will be refused"), "shared_link_users", "n", n))
			}
		}
		if a.DeviceIdleAfterDays != nil && cur.settings.GetDeviceIdleAfterDays() != *a.DeviceIdleAfterDays {
			before := strconv.FormatUint(uint64(cur.settings.GetDeviceIdleAfterDays()), 10)
			after := strconv.FormatUint(uint64(*a.DeviceIdleAfterDays), 10)
			next.DeviceIdleAfterDays = a.DeviceIdleAfterDays
			facts = append(facts, changeFact("device_idle_after_days", before+" -> "+after, before, after))
		}
		if a.LinkAWG != nil {
			before := subsettings.LinkAWG(cur.settings)
			if before != *a.LinkAWG {
				next.LinkAwg = a.LinkAWG
				facts = append(facts, changeFact("link_awg", strconv.FormatBool(before)+" -> "+strconv.FormatBool(*a.LinkAWG), strconv.FormatBool(before), strconv.FormatBool(*a.LinkAWG)))
				if !*a.LinkAWG && cur.awgKeys > 0 {
					facts = append(facts, codedFact("link_awg_keys", plural(int(cur.awgKeys), "link key will be dropped on the next fetch", "link keys will be dropped on the next fetch"), "link_awg_keys", "n", strconv.FormatUint(uint64(cur.awgKeys), 10)))
				}
			}
		}
		if a.LinkAutoSelect != nil {
			before := subsettings.LinkAutoSelect(cur.settings)
			if before != *a.LinkAutoSelect {
				next.LinkAutoSelect = a.LinkAutoSelect
				facts = append(facts, changeFact("link_auto_select", strconv.FormatBool(before)+" -> "+strconv.FormatBool(*a.LinkAutoSelect), strconv.FormatBool(before), strconv.FormatBool(*a.LinkAutoSelect)))
			}
		}
		if a.LinkAutoInterval != nil {
			before := strconv.Itoa(subsettings.LinkAutoInterval(cur.settings))
			after := strconv.FormatUint(uint64(*a.LinkAutoInterval), 10)
			if before != after {
				next.LinkAutoInterval = a.LinkAutoInterval
				facts = append(facts, changeFact("link_auto_interval", before+" -> "+after, before, after))
			}
		}
		if len(facts) == 0 {
			return nil, errors.New("nothing to change: the settings already look like this")
		}
		if err := checked(next); err != nil {
			return nil, err
		}
		a.SettingsHash = cur.hash
		return &planned{Summary: "Change link-app device-limit handling, AmneziaWG links, or automatic server selection. The owner must approve this shared change.", Facts: facts, Danger: []string{dangerPage}, Params: a}, nil
	},
	apply: func(c *call, a subsLinkLimitsArgs, _ Plan) (done, error) {
		cur, err := c.subscriptionSnapshot()
		if err != nil {
			return done{}, err
		}
		if cur.hash != a.SettingsHash {
			return done{}, changedSincePlan()
		}
		next := proto.Clone(cur.settings).(*adminv1.SubscriptionSettings)
		if a.Mode != nil {
			mode := linkModes[*a.Mode]
			next.LinkDeviceLimit = &mode
		}
		if a.DeviceIdleAfterDays != nil {
			next.DeviceIdleAfterDays = a.DeviceIdleAfterDays
		}
		if a.LinkAWG != nil {
			next.LinkAwg = a.LinkAWG
		}
		if a.LinkAutoSelect != nil {
			next.LinkAutoSelect = a.LinkAutoSelect
		}
		if a.LinkAutoInterval != nil {
			next.LinkAutoInterval = a.LinkAutoInterval
		}
		if err := c.saveSubscriptionSettings(a.SettingsHash, next); err != nil {
			return done{}, err
		}
		return doneWith("subscription_link_limits_updated", "Subscription link settings updated."), nil
	},
}

type subsPageArgs struct {
	ReasonField
	ShowAnnouncement  *bool   `json:"show_announcement,omitempty"`
	ShowSupport       *bool   `json:"show_support,omitempty"`
	ShowQR            *bool   `json:"show_qr,omitempty"`
	DeviceSelfService *bool   `json:"device_self_service,omitempty"`
	PagePassword      *bool   `json:"page_password,omitempty"`
	DNSChoice         *bool   `json:"dns_choice,omitempty"`
	RUDirectKey       *bool   `json:"ru_direct_key,omitempty"`
	HappPing          *string `json:"happ_ping,omitempty" jsonschema:"off or tcp"`
	HappPingOnOpen    *bool   `json:"happ_ping_on_open,omitempty"`
	SettingsHash      string  `json:"settings_hash,omitempty" jsonschema:"-"`
}

var subsPageSet = changeSpec[subsPageArgs]{
	name: "subscription_page_set", min: ProfileOperator, danger: true, procs: settingsProcs,
	desc: "Set options users see on the public subscription page (announcement, support, QR, device self-service, page password, DNS choices and the AmneziaWG key “Russian services direct”) and Happ ping behavior. Every user sees the page change; always waits for owner approval.",
	plan: func(c *call, a subsPageArgs) (*planned, error) {
		if a.ShowAnnouncement == nil && a.ShowSupport == nil && a.ShowQR == nil && a.DeviceSelfService == nil && a.PagePassword == nil && a.DNSChoice == nil && a.RUDirectKey == nil && a.HappPing == nil && a.HappPingOnOpen == nil {
			return nil, errors.New("give at least one page or Happ setting to change")
		}
		cur, err := c.subscriptionSnapshot()
		if err != nil {
			return nil, err
		}
		next := proto.Clone(cur.settings).(*adminv1.SubscriptionSettings)
		page := next.UserPage
		if page == nil {
			page = &adminv1.UserPageOptions{}
			next.UserPage = page
		}
		before := proto.Clone(cur.settings).(*adminv1.SubscriptionSettings)
		for _, f := range []struct {
			in  *bool
			dst *bool
		}{{a.ShowAnnouncement, &page.ShowAnnouncement}, {a.ShowSupport, &page.ShowSupport}, {a.ShowQR, &page.ShowQr}} {
			if f.in != nil {
				*f.dst = *f.in
			}
		}
		if a.DeviceSelfService != nil {
			page.AllowDeviceSelfService = a.DeviceSelfService
		}
		if a.PagePassword != nil {
			page.RequirePagePassword = a.PagePassword
		}
		if a.DNSChoice != nil {
			page.AllowDnsChoice = a.DNSChoice
		}
		if a.RUDirectKey != nil {
			page.AllowRuDirectKey = a.RUDirectKey
		}
		if a.HappPing != nil {
			switch strings.ToLower(strings.TrimSpace(*a.HappPing)) {
			case "off":
				next.HappPing = adminv1.HappPing_HAPP_PING_UNSPECIFIED
				*a.HappPing = "off"
			case "tcp":
				next.HappPing = adminv1.HappPing_HAPP_PING_TCP
				*a.HappPing = "tcp"
			default:
				return nil, errors.New("happ_ping must be off or tcp")
			}
		}
		if a.HappPingOnOpen != nil {
			next.HappPingOnOpen = *a.HappPingOnOpen
		}
		if err := checked(next); err != nil {
			return nil, err
		}
		if proto.Equal(before, next) {
			return nil, errors.New("nothing to change: the settings already look like this")
		}
		facts := []Fact{}
		for _, f := range []struct {
			key      string
			from, to bool
			changed  bool
		}{{"show_announcement", before.GetUserPage().GetShowAnnouncement(), page.GetShowAnnouncement(), a.ShowAnnouncement != nil}, {"show_support", before.GetUserPage().GetShowSupport(), page.GetShowSupport(), a.ShowSupport != nil}, {"show_qr", before.GetUserPage().GetShowQr(), page.GetShowQr(), a.ShowQR != nil}, {"device_self_service", subsettings.SelfService(before), subsettings.SelfService(next), a.DeviceSelfService != nil}, {"page_password", subsettings.PagePassword(before), subsettings.PagePassword(next), a.PagePassword != nil}, {"dns_choice", subsettings.DNSChoice(before), subsettings.DNSChoice(next), a.DNSChoice != nil}, {"ru_direct_key", subsettings.RUDirect(before), subsettings.RUDirect(next), a.RUDirectKey != nil}} {
			if f.changed && f.from != f.to {
				from, to := strconv.FormatBool(f.from), strconv.FormatBool(f.to)
				facts = append(facts, changeFact(f.key, from+" -> "+to, from, to))
			}
		}
		if a.HappPing != nil && before.GetHappPing() != next.GetHappPing() {
			facts = append(facts, changeFact("happ_ping", happPingWord(before.GetHappPing())+" -> "+happPingWord(next.GetHappPing()), happPingWord(before.GetHappPing()), happPingWord(next.GetHappPing())))
		}
		if a.HappPingOnOpen != nil && before.GetHappPingOnOpen() != next.GetHappPingOnOpen() {
			from, to := strconv.FormatBool(before.GetHappPingOnOpen()), strconv.FormatBool(next.GetHappPingOnOpen())
			facts = append(facts, changeFact("happ_ping_on_open", from+" -> "+to, from, to))
		}
		if len(facts) == 0 {
			return nil, errors.New("nothing to change: the settings already look like this")
		}
		a.SettingsHash = cur.hash
		return &planned{Summary: "Change the user page options or Happ ping behavior. The owner must approve this shared change.", Facts: facts, Danger: []string{dangerPage}, Params: a}, nil
	},
	apply: func(c *call, a subsPageArgs, _ Plan) (done, error) {
		cur, err := c.subscriptionSnapshot()
		if err != nil {
			return done{}, err
		}
		if cur.hash != a.SettingsHash {
			return done{}, changedSincePlan()
		}
		next := proto.Clone(cur.settings).(*adminv1.SubscriptionSettings)
		page := next.UserPage
		if page == nil {
			page = &adminv1.UserPageOptions{}
			next.UserPage = page
		}
		if a.ShowAnnouncement != nil {
			page.ShowAnnouncement = *a.ShowAnnouncement
		}
		if a.ShowSupport != nil {
			page.ShowSupport = *a.ShowSupport
		}
		if a.ShowQR != nil {
			page.ShowQr = *a.ShowQR
		}
		if a.DeviceSelfService != nil {
			page.AllowDeviceSelfService = a.DeviceSelfService
		}
		if a.PagePassword != nil {
			page.RequirePagePassword = a.PagePassword
		}
		if a.DNSChoice != nil {
			page.AllowDnsChoice = a.DNSChoice
		}
		if a.RUDirectKey != nil {
			page.AllowRuDirectKey = a.RUDirectKey
		}
		if a.HappPing != nil {
			if *a.HappPing == "tcp" {
				next.HappPing = adminv1.HappPing_HAPP_PING_TCP
			} else {
				next.HappPing = adminv1.HappPing_HAPP_PING_UNSPECIFIED
			}
		}
		if a.HappPingOnOpen != nil {
			next.HappPingOnOpen = *a.HappPingOnOpen
		}
		if err := c.saveSubscriptionSettings(a.SettingsHash, next); err != nil {
			return done{}, err
		}
		return doneWith("subscription_page_updated", "Subscription page options updated."), nil
	},
}

func happPingWord(p adminv1.HappPing) string {
	if p == adminv1.HappPing_HAPP_PING_TCP {
		return "tcp"
	}
	return "off"
}

type dnsSettingsArgs struct {
	ReasonField
	DefaultPresetID     *string   `json:"default_preset_id,omitempty" jsonschema:"instance default DNS preset; empty uses the built-in default"`
	LinkPresetIDs       *[]string `json:"link_preset_ids,omitempty" jsonschema:"ordered DNS preset choices offered to link apps; empty offers no choice"`
	LinkDefaultPresetID *string   `json:"link_default_preset_id,omitempty" jsonschema:"default choice for link apps; empty follows the instance default"`
	SettingsHash        string    `json:"settings_hash,omitempty" jsonschema:"-"`
}

var dnsSettingsSet = changeSpec[dnsSettingsArgs]{
	name: "dns_settings_set", min: ProfileOperator, danger: true,
	procs: procs(adminv1connect.SubscriptionServiceGetSubscriptionSettingsProcedure, adminv1connect.SubscriptionServiceUpdateSubscriptionSettingsProcedure, adminv1connect.DnsServiceListDnsPresetsProcedure),
	desc:  "Set the instance-default DNS preset and the ordered DNS choices offered to apps that take subscription links. Users may choose from the offer if DNS choices are enabled on their page. Always waits for owner approval.",
	plan: func(c *call, a dnsSettingsArgs) (*planned, error) {
		if a.DefaultPresetID == nil && a.LinkPresetIDs == nil && a.LinkDefaultPresetID == nil {
			return nil, errors.New("give a default preset or link DNS offer to change")
		}
		cur, err := c.subscriptionSnapshot()
		if err != nil {
			return nil, err
		}
		presets, err := c.cl.DNS.ListDnsPresets(c.ctx, connect.NewRequest(&adminv1.ListDnsPresetsRequest{}))
		if err != nil {
			return nil, apiError(err)
		}
		valid := map[string]bool{}
		for _, p := range presets.Msg.GetPresets() {
			valid[p.GetId()] = true
		}
		check := func(id string) error {
			if id != "" && !valid[id] {
				return errors.New("DNS preset id is not a preset of this panel")
			}
			return nil
		}
		if a.DefaultPresetID != nil {
			if err := check(*a.DefaultPresetID); err != nil {
				return nil, err
			}
			if err := rejectSecretShaped("default_preset_id", *a.DefaultPresetID); err != nil {
				return nil, err
			}
		}
		if a.LinkPresetIDs != nil {
			ids, err := uniqueIDs("link_preset_ids", *a.LinkPresetIDs, true)
			if err != nil {
				return nil, err
			}
			if len(ids) > 20 {
				return nil, errors.New("link_preset_ids: at most 20 presets")
			}
			for _, id := range ids {
				if err := check(id); err != nil {
					return nil, err
				}
				if err := rejectSecretShaped("link_preset_ids", id); err != nil {
					return nil, err
				}
			}
			*a.LinkPresetIDs = ids
		}
		if a.LinkDefaultPresetID != nil {
			if err := check(*a.LinkDefaultPresetID); err != nil {
				return nil, err
			}
			if err := rejectSecretShaped("link_default_preset_id", *a.LinkDefaultPresetID); err != nil {
				return nil, err
			}
			if *a.LinkDefaultPresetID != "" && (a.LinkPresetIDs == nil || !slices.Contains(*a.LinkPresetIDs, *a.LinkDefaultPresetID)) && !slices.Contains(cur.settings.GetLinkDns().GetPresetIds(), *a.LinkDefaultPresetID) {
				return nil, errors.New("link_default_preset_id must be in link_preset_ids")
			}
		}
		next := proto.Clone(cur.settings).(*adminv1.SubscriptionSettings)
		facts := []Fact{}
		if a.DefaultPresetID != nil && next.GetDefaultDnsPresetId() != *a.DefaultPresetID {
			facts = append(facts, safeTextChange("default_preset_id", next.GetDefaultDnsPresetId(), *a.DefaultPresetID))
			next.DefaultDnsPresetId = *a.DefaultPresetID
		}
		link := &adminv1.LinkDnsOptions{}
		if old := next.GetLinkDns(); old != nil {
			link = proto.Clone(old).(*adminv1.LinkDnsOptions)
		}
		if a.LinkPresetIDs != nil {
			link.PresetIds = slices.Clone(*a.LinkPresetIDs)
		}
		if a.LinkDefaultPresetID != nil {
			link.DefaultPresetId = *a.LinkDefaultPresetID
		}
		if a.LinkPresetIDs != nil || a.LinkDefaultPresetID != nil {
			next.LinkDns = link
			oldLink := cur.settings.GetLinkDns()
			if !slices.Equal(oldLink.GetPresetIds(), link.GetPresetIds()) || oldLink.GetDefaultPresetId() != link.GetDefaultPresetId() {
				facts = append(facts, Fact{Key: "link_dns_presets", Value: strings.Join(cleanIDs(link.GetPresetIds()), ", ") + "; default=" + valueOrDefault(clean(link.GetDefaultPresetId(), 60), "instance default"), Untrusted: true})
			}
		}
		if len(facts) == 0 {
			return nil, errors.New("nothing to change: the DNS defaults and offer already look like this")
		}
		if err := checked(next); err != nil {
			return nil, err
		}
		a.SettingsHash = cur.hash
		return &planned{Summary: "Change the instance DNS default or the DNS offer for link apps. The owner must approve this shared change.", Facts: facts, Danger: []string{dangerPage}, Params: a}, nil
	},
	apply: func(c *call, a dnsSettingsArgs, _ Plan) (done, error) {
		cur, err := c.subscriptionSnapshot()
		if err != nil {
			return done{}, err
		}
		if cur.hash != a.SettingsHash {
			return done{}, changedSincePlan()
		}
		next := proto.Clone(cur.settings).(*adminv1.SubscriptionSettings)
		if a.DefaultPresetID != nil {
			next.DefaultDnsPresetId = *a.DefaultPresetID
		}
		if a.LinkPresetIDs != nil || a.LinkDefaultPresetID != nil {
			link := &adminv1.LinkDnsOptions{}
			if old := next.GetLinkDns(); old != nil {
				link = proto.Clone(old).(*adminv1.LinkDnsOptions)
			}
			if a.LinkPresetIDs != nil {
				link.PresetIds = slices.Clone(*a.LinkPresetIDs)
			}
			if a.LinkDefaultPresetID != nil {
				link.DefaultPresetId = *a.LinkDefaultPresetID
			}
			next.LinkDns = link
		}
		if err := c.saveSubscriptionSettings(a.SettingsHash, next); err != nil {
			return done{}, err
		}
		return doneWith("dns_settings_updated", "DNS defaults and link-app offer updated."), nil
	},
}
