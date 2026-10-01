// Package subsettings holds the subscription settings of the installation (SubscriptionService): names and
// texts, the apps the public user page recommends, the server-name template and the format rules. They are
// stored as one protojson value in the setting table under Key, validated on the way in, and read at request
// time through a small cache that Update invalidates.
//
// default_dns_preset_id is not part of the stored document: the DNS module owns the instance default preset
// (one source of truth), and SubscriptionService reads and writes it through that module.
package subsettings

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// Key is the setting-table key of the stored document.
const Key = "subscription_settings"

// Limits enforced by Validate.
const (
	MaxRules = 50
	MaxApps  = 30

	maxTitle        = 100
	maxAnnouncement = 1000
	maxURL          = 2048
	maxTemplate     = 1000
	maxName         = 100
	maxDescription  = 80
	maxUA           = 100
	maxIntervalH    = 24 * 30
)

// ErrInvalid wraps every validation failure (the RPC maps it to INVALID_ARGUMENT).
var ErrInvalid = errors.New("invalid subscription settings")

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// DefaultNameTemplate is the server-name template of a fresh install and of an empty template: the country, as a
// friend knows it ("🇩🇪 Germany"), not the node's name. Migration 00026 moves installs off the old "{flag} {node}".
const DefaultNameTemplate = "{flag} {country}"

// Official AmneziaVPN downloads (amnezia.org/downloads): the stores on phones, the site everywhere else.
const (
	amneziaIOS     = "https://apps.apple.com/us/app/amneziavpn/id1600529900"
	amneziaAndroid = "https://play.google.com/store/apps/details?id=org.amnezia.vpn"
	amneziaSite    = "https://amnezia.org/downloads"
)

// DefaultUpdateHours is what an interval of 0 means.
const DefaultUpdateHours = 12

// m3Rules are the format rules of the Mihomo profile: the Mihomo core names itself `mihomo/<version>` (Mihomo-based
// and Clash Meta apps) and the Clash family `clash...`; both get the Mihomo YAML.
var m3Rules = []string{"mihomo", "clash"}

// Defaults are the settings of a fresh install: Happ on iOS, Android, Windows and macOS with
// the happ://add link, recommended (it leads the page); AmneziaVPN for the Amnezia kind on every platform, from the
// store on a phone; Mihomo clients get the YAML profile; users may manage their own AmneziaWG devices on the page.
func Defaults() *adminv1.SubscriptionSettings {
	happ := func(p adminv1.Platform, dl string) *adminv1.PlatformApp {
		return &adminv1.PlatformApp{Platform: p, Kind: adminv1.App_APP_HAPP, Name: "Happ", DownloadUrl: dl, AddLinkTemplate: "happ://add/{url}", Recommended: true}
	}
	amnezia := func(p adminv1.Platform) *adminv1.PlatformApp {
		dl := map[adminv1.Platform]string{adminv1.Platform_PLATFORM_IOS: amneziaIOS, adminv1.Platform_PLATFORM_ANDROID: amneziaAndroid}[p]
		if dl == "" {
			dl = amneziaSite
		}
		return &adminv1.PlatformApp{Platform: p, Kind: adminv1.App_APP_AMNEZIA, Name: "AmneziaVPN", DownloadUrl: dl}
	}
	var rules []*adminv1.ServeRule
	for _, ua := range m3Rules {
		rules = append(rules, &adminv1.ServeRule{UaContains: ua, Format: adminv1.SubFormat_SUB_FORMAT_MIHOMO_YAML})
	}
	return &adminv1.SubscriptionSettings{
		Rules:               rules,
		UpdateIntervalHours: DefaultUpdateHours,
		ServerNameTemplate:  DefaultNameTemplate,
		Apps: []*adminv1.PlatformApp{
			happ(adminv1.Platform_PLATFORM_IOS, "https://apps.apple.com/us/app/happ-proxy-utility/id6504287215"),
			happ(adminv1.Platform_PLATFORM_ANDROID, "https://play.google.com/store/apps/details?id=com.happproxy"),
			happ(adminv1.Platform_PLATFORM_WINDOWS, "https://github.com/Happ-proxy/happ-desktop/releases/latest/download/setup-Happ.x64.exe"),
			happ(adminv1.Platform_PLATFORM_MACOS, "https://github.com/Happ-proxy/happ-desktop/releases/latest/download/Happ.macOS.universal.dmg"),
			amnezia(adminv1.Platform_PLATFORM_IOS), amnezia(adminv1.Platform_PLATFORM_ANDROID), amnezia(adminv1.Platform_PLATFORM_WINDOWS),
			amnezia(adminv1.Platform_PLATFORM_MACOS), amnezia(adminv1.Platform_PLATFORM_LINUX),
		},
		UserPage: &adminv1.UserPageOptions{ShowAnnouncement: true, ShowSupport: true, ShowQr: true, AllowDeviceSelfService: proto.Bool(true), RequirePagePassword: proto.Bool(true)},
	}
}

// SelfService reports whether users may add, rotate and revoke their own AmneziaWG devices on the public page.
// Settings stored before the key existed have none: absent means on.
func SelfService(s *adminv1.SubscriptionSettings) bool {
	o := s.GetUserPage()
	return o == nil || o.AllowDeviceSelfService == nil || *o.AllowDeviceSelfService
}

// PagePassword reports whether the user's page asks for its password (see package pagepass). Settings stored before
// the key existed have none: absent means on, so an upgrade protects every page.
func PagePassword(s *adminv1.SubscriptionSettings) bool {
	o := s.GetUserPage()
	return o == nil || o.RequirePagePassword == nil || *o.RequirePagePassword
}

// m3RulesKey marks an installation whose stored settings were given the Mihomo format rules (see MigrateM3).
const m3RulesKey = "sub_rules_m3"

// MigrateM3 gives an installation whose saved settings predate them the two Mihomo format rules (mihomo, clash ->
// Mihomo YAML), once: the marker setting sub_rules_m3 = 1 is written in the same transaction, so a rule the admin
// deletes later is not added back. An installation that never saved settings needs nothing (Defaults() has the
// rules); it only gets the marker. Idempotent and safe to call at every start.
func MigrateM3(ctx context.Context, st *store.Store) error {
	if _, err := st.Setting(ctx, m3RulesKey); err == nil {
		return nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	kv := map[string]string{m3RulesKey: "1"}
	switch v, err := st.Setting(ctx, Key); {
	case errors.Is(err, store.ErrNotFound): // never saved
	case err != nil:
		return err
	default:
		s := &adminv1.SubscriptionSettings{}
		if err := unmarshal.Unmarshal([]byte(v), s); err != nil {
			return fmt.Errorf("stored subscription settings: %w", err)
		}
		if addM3Rules(s) {
			b, err := marshal.Marshal(s)
			if err != nil {
				return err
			}
			kv[Key] = string(b)
		}
	}
	return st.SetSettings(ctx, kv)
}

// addM3Rules appends the Mihomo format rules the document does not have yet (same User-Agent text, any case); it reports
// whether it changed the document. The list stays within MaxRules.
func addM3Rules(s *adminv1.SubscriptionSettings) bool {
	changed := false
	for _, ua := range m3Rules {
		has := false
		for _, e := range s.Rules {
			if strings.EqualFold(strings.TrimSpace(e.GetUaContains()), ua) {
				has = true
				break
			}
		}
		if !has && len(s.Rules) < MaxRules {
			s.Rules = append(s.Rules, &adminv1.ServeRule{UaContains: ua, Format: adminv1.SubFormat_SUB_FORMAT_MIHOMO_YAML})
			changed = true
		}
	}
	return changed
}

var (
	nameKnown = map[string]bool{"{flag}": true, "{country}": true, "{node}": true, "{profile}": true}
	addKnown  = map[string]bool{"{url}": true, "{url_enc}": true, "{name_enc}": true}
	braceRe   = regexp.MustCompile(`\{[^{}]*\}`)
	schemeRe  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]{1,31}:`)
)

// checkPlaceholders rejects a {placeholder} that is not in known and stray braces.
func checkPlaceholders(what, s string, known map[string]bool) error {
	for _, m := range braceRe.FindAllString(s, -1) {
		if !known[m] {
			return invalid("%s: unknown placeholder %s", what, m)
		}
	}
	if strings.Count(s, "{") != strings.Count(s, "}") || strings.Count(s, "{") != len(braceRe.FindAllString(s, -1)) {
		return invalid("%s: unbalanced braces", what)
	}
	return nil
}

func noControl(s string) bool {
	return !strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 || r == 0x7f })
}

// httpURL checks an optional link: empty, or an absolute http(s) URL without spaces or control characters.
func httpURL(what, s string) error {
	if s == "" {
		return nil
	}
	if len(s) > maxURL || !noControl(s) || strings.ContainsAny(s, " \t") {
		return invalid("%s: not a valid link", what)
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return invalid("%s: must be an http(s) link", what)
	}
	return nil
}

func text(what, s string, max int) error {
	if utf8.RuneCountInString(s) > max || !utf8.ValidString(s) {
		return invalid("%s: too long (at most %d characters)", what, max)
	}
	return nil
}

// Validate checks a whole document; nil = storable.
func Validate(s *adminv1.SubscriptionSettings) error {
	if s == nil {
		return invalid("settings missing")
	}
	if err := text("title", s.Title, maxTitle); err != nil {
		return err
	}
	if !noControl(s.Title) {
		return invalid("title: control characters are not allowed")
	}
	if err := text("announcement", s.Announcement, maxAnnouncement); err != nil {
		return err
	}
	if err := httpURL("support_url", s.SupportUrl); err != nil {
		return err
	}
	if s.UpdateIntervalHours > maxIntervalH {
		return invalid("update_interval_hours: at most %d", maxIntervalH)
	}
	if err := text("server_name_template", s.ServerNameTemplate, maxName); err != nil {
		return err
	}
	if err := checkPlaceholders("server_name_template", s.ServerNameTemplate, nameKnown); err != nil {
		return err
	}
	if !noControl(s.ServerNameTemplate) {
		return invalid("server_name_template: control characters are not allowed")
	}
	if len(s.Apps) > MaxApps {
		return invalid("apps: at most %d", MaxApps)
	}
	for i, a := range s.Apps {
		w := fmt.Sprintf("apps[%d]", i)
		switch {
		case a == nil:
			return invalid("%s: missing", w)
		case a.Platform < adminv1.Platform_PLATFORM_IOS || a.Platform > adminv1.Platform_PLATFORM_LINUX:
			return invalid("%s: unknown platform", w)
		case a.Kind != adminv1.App_APP_HAPP && a.Kind != adminv1.App_APP_AMNEZIA:
			return invalid("%s: unknown kind", w)
		case strings.TrimSpace(a.Name) == "" || !noControl(a.Name):
			return invalid("%s: name is required", w)
		}
		if err := text(w+".name", a.Name, maxName); err != nil {
			return err
		}
		if err := httpURL(w+".download_url", a.DownloadUrl); err != nil {
			return err
		}
		// A card line of plain text (the page shows it as text, never as markup): no control characters, no newline.
		if err := text(w+".description", a.Description, maxDescription); err != nil {
			return err
		}
		if !noControl(a.Description) {
			return invalid("%s.description: control characters are not allowed", w)
		}
		if t := a.AddLinkTemplate; t != "" {
			if len(t) > maxTemplate || !noControl(t) || strings.ContainsAny(t, " \t") {
				return invalid("%s.add_link_template: not a valid link template", w)
			}
			if err := checkPlaceholders(w+".add_link_template", t, addKnown); err != nil {
				return err
			}
			// The template becomes an <a href> on the public page: a custom app scheme (happ://) is the point,
			// but never a scheme that runs or embeds content.
			if !schemeRe.MatchString(t) {
				return invalid("%s.add_link_template: must start with a scheme such as happ://", w)
			}
			switch strings.ToLower(t[:strings.IndexByte(t, ':')]) {
			case "javascript", "data", "vbscript", "file", "blob", "about":
				return invalid("%s.add_link_template: this scheme is not allowed", w)
			}
		}
	}
	if o := s.UserPage; o == nil {
		return invalid("user_page missing")
	}
	if len(s.Rules) > MaxRules {
		return invalid("rules: at most %d", MaxRules)
	}
	for i, r := range s.Rules {
		w := fmt.Sprintf("rules[%d]", i)
		switch {
		case r == nil || strings.TrimSpace(r.UaContains) == "" || !noControl(r.UaContains):
			return invalid("%s: ua_contains is required", w)
		case utf8.RuneCountInString(r.UaContains) > maxUA:
			return invalid("%s: ua_contains is too long", w)
		case r.Format < adminv1.SubFormat_SUB_FORMAT_BASE64_URIS || r.Format > adminv1.SubFormat_SUB_FORMAT_MIHOMO_YAML:
			return invalid("%s: unknown format", w)
		}
	}
	return nil
}

// Normalize trims the free-text fields in place (what Update stores).
func Normalize(s *adminv1.SubscriptionSettings) {
	s.Title = strings.TrimSpace(s.Title)
	s.Announcement = strings.TrimSpace(s.Announcement)
	s.SupportUrl = strings.TrimSpace(s.SupportUrl)
	s.ServerNameTemplate = strings.TrimSpace(s.ServerNameTemplate)
	for _, a := range s.Apps {
		if a != nil {
			a.Name, a.DownloadUrl, a.AddLinkTemplate = strings.TrimSpace(a.Name), strings.TrimSpace(a.DownloadUrl), strings.TrimSpace(a.AddLinkTemplate)
			a.Description = strings.TrimSpace(a.Description)
		}
	}
	for _, r := range s.Rules {
		if r != nil {
			r.UaContains = strings.TrimSpace(r.UaContains)
		}
	}
}

var (
	marshal   = protojson.MarshalOptions{UseProtoNames: true}
	unmarshal = protojson.UnmarshalOptions{DiscardUnknown: true}
)

// Load reads the stored settings; an install that never saved any gets Defaults().
func Load(ctx context.Context, st *store.Store) (*adminv1.SubscriptionSettings, error) {
	v, err := st.Setting(ctx, Key)
	if errors.Is(err, store.ErrNotFound) {
		return Defaults(), nil
	} else if err != nil {
		return nil, err
	}
	s := &adminv1.SubscriptionSettings{}
	if err := unmarshal.Unmarshal([]byte(v), s); err != nil {
		return nil, fmt.Errorf("stored subscription settings: %w", err)
	}
	if s.UserPage == nil {
		s.UserPage = &adminv1.UserPageOptions{}
	}
	s.DefaultDnsPresetId = ""
	return s, nil
}

// Cache serves the settings to the subscription handler: one database read per TTL, dropped by Update.
type Cache struct {
	st  *store.Store
	log *slog.Logger
	now func() time.Time
	ttl time.Duration

	mu  sync.Mutex
	cur *adminv1.SubscriptionSettings
	at  time.Time
}

// NewCache makes the cache. log may be nil. It also runs MigrateM3 (the panel builds the cache once at start); a
// failure is logged and the next start tries again.
func NewCache(st *store.Store, log *slog.Logger) *Cache {
	if log == nil {
		log = slog.Default()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := MigrateM3(ctx, st); err != nil {
		log.Warn("subscription settings: the Mihomo format rules were not added", "err", err)
	}
	return &Cache{st: st, log: log, now: time.Now, ttl: time.Minute}
}

// Get returns the settings; the result is shared and must not be modified. A stored document that cannot be
// read yields the defaults (logged once per refresh): a subscription fetch must not fail over settings.
func (c *Cache) Get(ctx context.Context) *adminv1.SubscriptionSettings {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cur != nil && c.now().Sub(c.at) < c.ttl {
		return c.cur
	}
	s, err := Load(ctx, c.st)
	if err != nil {
		c.log.Warn("subscription settings unreadable, using the defaults", "err", err)
		s = Defaults()
	}
	c.cur, c.at = s, c.now()
	return s
}

// Invalidate drops the cached copy.
func (c *Cache) Invalidate() {
	c.mu.Lock()
	c.cur = nil
	c.mu.Unlock()
}

// Update validates, stores and caches s (a copy is stored; s itself is not kept).
func (c *Cache) Update(ctx context.Context, s *adminv1.SubscriptionSettings) (*adminv1.SubscriptionSettings, error) {
	s = proto.Clone(s).(*adminv1.SubscriptionSettings)
	s.DefaultDnsPresetId = "" // lives in the DNS module
	Normalize(s)
	if err := Validate(s); err != nil {
		return nil, err
	}
	b, err := marshal.Marshal(s)
	if err != nil {
		return nil, err
	}
	if err := c.st.SetSettings(ctx, map[string]string{Key: string(b)}); err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.cur, c.at = s, c.now()
	c.mu.Unlock()
	return s, nil
}
