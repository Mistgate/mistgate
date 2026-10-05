package mcp

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
)

// The subscription page's app list. The settings are what every user's page shows: names, texts, public download links
// and add-link templates, no credential. Reading them is a read; a change waits for the owner, because every user sees it.
// A plan and its apply each read the whole document and change one app in it. The plan pins a hash of the document it
// read and the apply refuses when the document changed since, so an edit the owner saved meanwhile is never overwritten
// by a stale copy.

// dangerPage: it changes what every user sees on their subscription page.
const dangerPage = "user_page"

func subscriptionTools() []toolDef {
	return slices.Concat(
		[]toolDef{readTool("subscription_settings_get", ProfileReadonly, procs(adminv1connect.SubscriptionServiceGetSubscriptionSettingsProcedure),
			"The subscription page every user sees: its apps per platform in display order (name, kind, download link, add-link template, description, "+
				"recommended), the page options, the server-name template, and the subscription title, announcement, support link and refresh interval. "+
				"No user's link: this is the shared page. Links show query strings and long path segments as [redacted]; give a field to "+
				"subscription_app_upsert_plan only when you change it.",
			func(c *call, _ noArgs) (any, error) {
				r, err := c.cl.Subs.GetSubscriptionSettings(c.ctx, connect.NewRequest(&adminv1.GetSubscriptionSettingsRequest{}))
				if err != nil {
					return nil, apiError(err)
				}
				return subsSettingsView(r.Msg), nil
			})},
		change(subsAppUpsert), change(subsAppRemove),
	)
}

// ---------------------------------------------------------------------------------------------------------------------
// the view

// SubsSettingsV is what subscription_settings_get returns.
type SubsSettingsV struct {
	Title               string `json:"title"` // "" = the brand
	EffectiveTitle      string `json:"effective_title"`
	Announcement        string `json:"announcement"`
	SupportURL          string `json:"support_url"`
	UpdateIntervalHours uint32 `json:"update_interval_hours"` // 0 = 12
	ServerNameTemplate  string `json:"server_name_template"`
	UserPage            struct {
		ShowAnnouncement  bool `json:"show_announcement"`
		ShowSupport       bool `json:"show_support"`
		ShowQR            bool `json:"show_qr"`
		DeviceSelfService bool `json:"device_self_service"`
		PagePassword      bool `json:"page_password"`
		DNSChoice         bool `json:"dns_choice"` // people pick the DNS of each server on their page (off when absent)
	} `json:"user_page"`
	Apps map[string][]subsAppV `json:"apps"` // by platform, in display order
}

type subsAppV struct {
	Name            string `json:"name"`
	Kind            string `json:"kind"` // happ: takes the subscription link; amnezia: takes an AmneziaWG key
	DownloadURL     string `json:"download_url"`
	AddLinkTemplate string `json:"add_link_template"`
	Description     string `json:"description"`
	Recommended     bool   `json:"recommended"`
}

func subsSettingsView(r *adminv1.GetSubscriptionSettingsResponse) *SubsSettingsV {
	s := r.GetSettings()
	v := &SubsSettingsV{
		Title: nm(s.GetTitle()), EffectiveTitle: nm(r.GetEffectiveTitle()), Announcement: clean(s.GetAnnouncement(), 1000),
		SupportURL: clean(s.GetSupportUrl(), 500), UpdateIntervalHours: s.GetUpdateIntervalHours(), ServerNameTemplate: nm(s.GetServerNameTemplate()),
		Apps: map[string][]subsAppV{},
	}
	o := s.GetUserPage()
	v.UserPage.ShowAnnouncement, v.UserPage.ShowSupport, v.UserPage.ShowQR = o.GetShowAnnouncement(), o.GetShowSupport(), o.GetShowQr()
	v.UserPage.DeviceSelfService, v.UserPage.PagePassword, v.UserPage.DNSChoice = subsettings.SelfService(s), subsettings.PagePassword(s), subsettings.DNSChoice(s)
	for _, a := range s.GetApps() {
		p := platformWord(a.GetPlatform())
		v.Apps[p] = append(v.Apps[p], subsAppV{
			Name: nm(a.GetName()), Kind: kindWord(a.GetKind()), DownloadURL: clean(a.GetDownloadUrl(), 500),
			AddLinkTemplate: clean(a.GetAddLinkTemplate(), 500), Description: clean(a.GetDescription(), 200), Recommended: a.GetRecommended(),
		})
	}
	return v
}

// ---------------------------------------------------------------------------------------------------------------------
// words

func platformWord(p adminv1.Platform) string { return enumName("PLATFORM_", p.String()) }

func kindWord(k adminv1.App) string { return enumName("APP_", k.String()) }

// platformTitle is how the page names a platform (the same in every language).
func platformTitle(p adminv1.Platform) string {
	return map[adminv1.Platform]string{
		adminv1.Platform_PLATFORM_IOS: "iOS", adminv1.Platform_PLATFORM_ANDROID: "Android", adminv1.Platform_PLATFORM_WINDOWS: "Windows",
		adminv1.Platform_PLATFORM_MACOS: "macOS", adminv1.Platform_PLATFORM_LINUX: "Linux",
	}[p]
}

func kindText(k adminv1.App) string {
	if k == adminv1.App_APP_AMNEZIA {
		return "AmneziaWG key"
	}
	return "by subscription link"
}

func platformOf(s string) (adminv1.Platform, error) {
	if v := adminv1.Platform_value["PLATFORM_"+strings.ToUpper(strings.TrimSpace(s))]; v != 0 {
		return adminv1.Platform(v), nil
	}
	return 0, errors.New("platform: ios, android, windows, macos or linux")
}

func kindOf(s string) (adminv1.App, error) {
	if v := adminv1.App_value["APP_"+strings.ToUpper(strings.TrimSpace(s))]; v != 0 {
		return adminv1.App(v), nil
	}
	return 0, errors.New("kind: happ (the app takes the subscription link) or amnezia (it takes an AmneziaWG key)")
}

// ---------------------------------------------------------------------------------------------------------------------
// reading and changing the document

// subsSettings reads the stored settings and the hash a plan pins.
func (c *call) subsSettings() (*adminv1.SubscriptionSettings, string, error) {
	r, err := c.cl.Subs.GetSubscriptionSettings(c.ctx, connect.NewRequest(&adminv1.GetSubscriptionSettingsRequest{}))
	if err != nil {
		return nil, "", apiError(err)
	}
	s := r.Msg.GetSettings()
	if s == nil {
		return nil, "", errors.New("the panel sent no subscription settings")
	}
	b, err := proto.MarshalOptions{Deterministic: true}.Marshal(s)
	if err != nil {
		return nil, "", errors.New("internal error")
	}
	h := sha256.Sum256(b)
	return s, hex.EncodeToString(h[:16]), nil
}

// ponytail: the hash check and the save are two calls, so an admin save landing in the milliseconds between them would be
// overwritten; a compare-and-set in UpdateSubscriptionSettings closes that if it ever matters.
func changedSincePlan() error {
	return failure("changed_since_plan", "the subscription settings changed after the plan (someone saved them in the admin panel): make a new plan")
}

// findApp is the index of the app with this platform and name (ignoring case), -1 for none.
func findApp(apps []*adminv1.PlatformApp, p adminv1.Platform, name string) (int, error) {
	at := -1
	for i, a := range apps {
		if a.GetPlatform() == p && strings.EqualFold(strings.TrimSpace(a.GetName()), name) {
			if at >= 0 {
				return 0, errors.New("several apps on this platform have this name: change them in the admin panel")
			}
			at = i
		}
	}
	return at, nil
}

func appName(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("name is required")
	}
	return s, visible("name", s)
}

// visible refuses an invisible formatting character (a bidi override, a zero-width space) in the agent's text: the owner
// approves what the card shows, and such a character could make it read as something else. Stricter than the admin's
// own form, where the owner types the text.
func visible(what, s string) error {
	if strings.ContainsFunc(s, func(r rune) bool { return unicode.Is(unicode.Cf, r) }) {
		return errors.New(what + ": invisible formatting characters are not allowed")
	}
	return nil
}

// checked normalizes and validates the changed document exactly as the admin's save does.
func checked(s *adminv1.SubscriptionSettings) error {
	subsettings.Normalize(s)
	if err := subsettings.Validate(s); err != nil {
		return errors.New(strings.TrimPrefix(err.Error(), subsettings.ErrInvalid.Error()+": "))
	}
	return nil
}

type subsAppUpsertArgs struct {
	ReasonField
	Platform        string  `json:"platform" jsonschema:"ios, android, windows, macos or linux"`
	Name            string  `json:"name" jsonschema:"the app's name on the page; with the platform it says which app (ignoring case). An app that is not there yet is added at the end"`
	Kind            *string `json:"kind,omitempty" jsonschema:"happ: the app takes the subscription link; amnezia: it takes a per-device AmneziaWG key. Required for a new app"`
	DownloadURL     *string `json:"download_url,omitempty" jsonschema:"an http(s) link where people get the app; empty = none"`
	AddLinkTemplate *string `json:"add_link_template,omitempty" jsonschema:"the one-tap add link: an app scheme (myapp://...) or http(s), with the placeholders {url}, {url_enc} (URL-encoded) and {name_enc} (the URL-encoded subscription title); empty = the page offers to copy the link"`
	Description     *string `json:"description,omitempty" jsonschema:"one plain-text line on the app's card, at most 80 characters; empty = none"`
	Recommended     *bool   `json:"recommended,omitempty" jsonschema:"a recommended badge on the card"`
	// The hash of the settings the plan read (set by the plan, not the agent).
	SettingsHash string `json:"settings_hash,omitempty" jsonschema:"-"`
}

// upsert is the document with one app added or changed; before is nil for an add.
func (a subsAppUpsertArgs) upsert(s *adminv1.SubscriptionSettings) (next *adminv1.SubscriptionSettings, before, after *adminv1.PlatformApp, err error) {
	p, err := platformOf(a.Platform)
	if err != nil {
		return nil, nil, nil, err
	}
	name, err := appName(a.Name)
	if err != nil {
		return nil, nil, nil, err
	}
	next = proto.Clone(s).(*adminv1.SubscriptionSettings)
	at, err := findApp(next.Apps, p, name)
	if err != nil {
		return nil, nil, nil, err
	}
	if at < 0 {
		if a.Kind == nil {
			return nil, nil, nil, errors.New("kind is required for a new app: happ or amnezia")
		}
		after = &adminv1.PlatformApp{Platform: p, Name: name}
		next.Apps = append(next.Apps, after)
	} else {
		after = next.Apps[at]
		before = proto.Clone(after).(*adminv1.PlatformApp)
	}
	if a.Kind != nil {
		if after.Kind, err = kindOf(*a.Kind); err != nil {
			return nil, nil, nil, err
		}
	}
	for _, f := range []struct {
		what string
		in   *string
		to   *string
	}{{"download_url", a.DownloadURL, &after.DownloadUrl}, {"add_link_template", a.AddLinkTemplate, &after.AddLinkTemplate}, {"description", a.Description, &after.Description}} {
		if f.in != nil {
			if err := visible(f.what, *f.in); err != nil {
				return nil, nil, nil, err
			}
			*f.to = *f.in
		}
	}
	if a.Recommended != nil {
		after.Recommended = *a.Recommended
	}
	if err := checked(next); err != nil {
		return nil, nil, nil, err
	}
	if before != nil && proto.Equal(before, after) {
		return nil, nil, nil, errors.New("nothing to change: the app already looks like this")
	}
	return next, before, after, nil
}

type subsAppRemoveArgs struct {
	ReasonField
	Platform     string `json:"platform" jsonschema:"ios, android, windows, macos or linux"`
	Name         string `json:"name" jsonschema:"the app's name on the page (ignoring case)"`
	SettingsHash string `json:"settings_hash,omitempty" jsonschema:"-"`
}

func (a subsAppRemoveArgs) remove(s *adminv1.SubscriptionSettings) (next *adminv1.SubscriptionSettings, gone *adminv1.PlatformApp, err error) {
	p, err := platformOf(a.Platform)
	if err != nil {
		return nil, nil, err
	}
	name, err := appName(a.Name)
	if err != nil {
		return nil, nil, err
	}
	next = proto.Clone(s).(*adminv1.SubscriptionSettings)
	at, err := findApp(next.Apps, p, name)
	if err != nil {
		return nil, nil, err
	}
	if at < 0 {
		return nil, nil, errors.New("no app with this name on this platform (subscription_settings_get lists them)")
	}
	gone = next.Apps[at]
	next.Apps = append(next.Apps[:at], next.Apps[at+1:]...)
	if err := checked(next); err != nil {
		return nil, nil, err
	}
	return next, gone, nil
}

// ---------------------------------------------------------------------------------------------------------------------
// facts: the owner's card shows every field in full (values from the agent are untrusted and quoted there); an update
// shows only the fields that change, before -> after.

func appIdentity(a *adminv1.PlatformApp) []Fact {
	return []Fact{
		{Key: "platform", Value: platformTitle(a.GetPlatform())},
		{Key: "app", Value: a.GetName(), Untrusted: true},
	}
}

func appFields(a *adminv1.PlatformApp) []Fact {
	return []Fact{
		codedFact("kind", kindText(a.GetKind()), kindWord(a.GetKind())),
		{Key: "download_url", Value: a.GetDownloadUrl(), Untrusted: true},
		{Key: "add_link_template", Value: a.GetAddLinkTemplate(), Untrusted: true},
		{Key: "description", Value: a.GetDescription(), Untrusted: true},
		{Key: "recommended", Value: strconv.FormatBool(a.GetRecommended())},
	}
}

// textChange is "from -> to" of a field whose values came from data: both sides are untrusted params of the coded fact.
func textChange(key, from, to string) Fact {
	f := changeFact(key, strconv.Quote(from)+" -> "+strconv.Quote(to), from, to)
	f.UntrustedParams = []string{"from", "to"}
	return f
}

func appChanges(before, after *adminv1.PlatformApp) []Fact {
	var out []Fact
	if before.GetKind() != after.GetKind() {
		out = append(out, changeFact("kind", kindText(before.GetKind())+" -> "+kindText(after.GetKind()), kindWord(before.GetKind()), kindWord(after.GetKind())))
	}
	for _, f := range []struct{ key, from, to string }{
		{"download_url", before.GetDownloadUrl(), after.GetDownloadUrl()},
		{"add_link_template", before.GetAddLinkTemplate(), after.GetAddLinkTemplate()},
		{"description", before.GetDescription(), after.GetDescription()},
	} {
		if f.from != f.to {
			out = append(out, textChange(f.key, f.from, f.to))
		}
	}
	if before.GetRecommended() != after.GetRecommended() {
		from, to := strconv.FormatBool(before.GetRecommended()), strconv.FormatBool(after.GetRecommended())
		out = append(out, changeFact("recommended", from+" -> "+to, from, to))
	}
	return out
}

var subsProcs = procs(adminv1connect.SubscriptionServiceGetSubscriptionSettingsProcedure, adminv1connect.SubscriptionServiceUpdateSubscriptionSettingsProcedure)

var subsAppUpsert = changeSpec[subsAppUpsertArgs]{
	name: "subscription_app_upsert", min: ProfileOperator, danger: true, procs: subsProcs,
	desc: "Add one app to the subscription page every user sees, or change one, identified by platform and name. For a change give only the fields " +
		"that change; the rest stay. Checked exactly like a save in the admin panel. Always needs the owner's approval in the admin panel.",
	plan: func(c *call, a subsAppUpsertArgs) (*planned, error) {
		cur, hash, err := c.subsSettings()
		if err != nil {
			return nil, err
		}
		_, before, after, err := a.upsert(cur)
		if err != nil {
			return nil, err
		}
		where := platformTitle(after.GetPlatform())
		facts := appIdentity(after)
		var summary string
		if before == nil {
			summary = "Add an app for " + where + " to every user's subscription page. The owner has to approve it first."
			facts = append(append(facts, codedFact("effect", "a new app card on every user's subscription page", "app_add")), appFields(after)...)
		} else {
			changes := appChanges(before, after)
			summary = "Change " + plural(len(changes), "field", "fields") + " of an app for " + where + " on every user's subscription page. The owner has to approve it first."
			facts = append(append(facts, codedFact("effect", "the app's card changes on every user's subscription page", "app_update")), changes...)
		}
		a.SettingsHash = hash
		return &planned{Summary: summary, Facts: facts, Danger: []string{dangerPage}, Params: a}, nil
	},
	apply: func(c *call, a subsAppUpsertArgs, _ Plan) (done, error) {
		cur, hash, err := c.subsSettings()
		if err != nil {
			return done{}, err
		}
		if hash != a.SettingsHash {
			return done{}, changedSincePlan()
		}
		next, before, _, err := a.upsert(cur)
		if err != nil {
			return done{}, err
		}
		if _, err := c.cl.Subs.UpdateSubscriptionSettings(c.ctx, connect.NewRequest(&adminv1.UpdateSubscriptionSettingsRequest{Settings: next})); err != nil {
			return done{}, apiError(err)
		}
		if before == nil {
			return doneWith("subscription_app_added", "App added to the subscription page."), nil
		}
		return doneWith("subscription_app_updated", "App changed on the subscription page."), nil
	},
}

var subsAppRemove = changeSpec[subsAppRemoveArgs]{
	name: "subscription_app_remove", min: ProfileOperator, danger: true, procs: subsProcs,
	desc: "Remove one app, identified by platform and name, from the subscription page every user sees. Always needs the owner's approval in the admin panel.",
	plan: func(c *call, a subsAppRemoveArgs) (*planned, error) {
		cur, hash, err := c.subsSettings()
		if err != nil {
			return nil, err
		}
		_, gone, err := a.remove(cur)
		if err != nil {
			return nil, err
		}
		facts := append(append(appIdentity(gone), codedFact("effect", "the app's card disappears from every user's subscription page", "app_remove")), appFields(gone)...)
		a.SettingsHash = hash
		return &planned{
			Summary: "Remove an app for " + platformTitle(gone.GetPlatform()) + " from every user's subscription page. The owner has to approve it first.",
			Facts:   facts, Danger: []string{dangerPage}, Params: a,
		}, nil
	},
	apply: func(c *call, a subsAppRemoveArgs, _ Plan) (done, error) {
		cur, hash, err := c.subsSettings()
		if err != nil {
			return done{}, err
		}
		if hash != a.SettingsHash {
			return done{}, changedSincePlan()
		}
		next, _, err := a.remove(cur)
		if err != nil {
			return done{}, err
		}
		if _, err := c.cl.Subs.UpdateSubscriptionSettings(c.ctx, connect.NewRequest(&adminv1.UpdateSubscriptionSettingsRequest{Settings: next})); err != nil {
			return done{}, apiError(err)
		}
		return doneWith("subscription_app_removed", "App removed from the subscription page."), nil
	},
}
