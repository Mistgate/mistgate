package access

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// The client catalogue: which version of which app sends a post-quantum key share (X25519MLKEM768) in its REALITY handshake,
// so that it keeps working when a profile goes "post-quantum only". Knowledge for the owner (the list of devices below the
// line); nothing in a subscription depends on it. The built-in table is data in this file and moves with releases; the owner's
// changes are stored as the difference from it (store.SettingClientCompat), so a row the owner never touched follows the release.
//
// A rule is one app on one platform. Two thresholds, because the fingerprint of the profile decides which hello an app builds:
// MinChrome for the Chrome-like one, MinOther for Firefox and Safari. ios, edge and random always send a classical share. An
// empty threshold means "not known", and "not known" is a state of its own (unknown), never "outdated".

// The cores an app can run on. xray, mihomo and singbox send a classical share for a classical fingerprint without exception;
// own (an own implementation) and unknown say nothing about it.
const (
	CoreXray    = "xray"
	CoreMihomo  = "mihomo"
	CoreSingbox = "singbox"
	CoreOwn     = "own"
	CoreUnknown = "unknown"
)

// The states of a device against the table.
const (
	ClientOK          = "ok"
	ClientBelow       = "below_minimum"
	ClientUnsupported = "unsupported"
	ClientUnknown     = "unknown"
)

// The reasons of a state other than ok.
const (
	ReasonVersion     = "version"      // below the lowest known good version
	ReasonApp         = "app"          // no version of the app works
	ReasonFingerprint = "fingerprint"  // the profile's fingerprint is classical
	ReasonAppUnknown  = "app_unknown"  // the table does not know the app
	ReasonNoThreshold = "no_threshold" // a known app, but no known good version for this case
	ReasonNoVersion   = "no_version"   // the device reported no version
)

// ClientRule is one row of the table. Title is output only (the name the UI shows); it is not stored.
type ClientRule struct {
	App         string `json:"app"`
	Platform    string `json:"platform,omitempty"`
	Core        string `json:"core"`
	Unsupported bool   `json:"unsupported,omitempty"`
	MinChrome   string `json:"min_chrome,omitempty"`
	MinOther    string `json:"min_other,omitempty"`
	Title       string `json:"-"`
}

func (r ClientRule) key() string { return r.App + "\x00" + r.Platform }

// same: the rule says what o says (the title does not count).
func (r ClientRule) same(o ClientRule) bool {
	r.Title, o.Title = "", ""
	return r == o
}

// clientDefaults is the built-in table. Every threshold is a release of the app (or of the core inside it) that was read in the
// release notes or the source; the numbers are in the plan of the compatibility work (core-compat-plan, section 2.3):
//
//	Xray-core: chrome fingerprint from v25.4.30 (uTLS 1.7: Chrome 131 carries the share), firefox and safari from v26.3.23.
//	Happ Android: 3.1.1 ships Xray 25.8.3, 3.20.0 ships 26.3.27 (2.7.1 ships 25.3.6: bad). Versions between are not documented;
//	  the rule says "need 3.1.1", which is true for all of them.
//	Happ Desktop (Windows, macOS, Linux): alpha 0.3.6 ships 25.8.3, 2.10.0 ships 26.3.27 (2.5.2 ships 26.2.6: bad). The App Store
//	  build for macOS numbers like the iPhone one (4.x) and is not told apart from Desktop by its User-Agent.
//	Happ iOS: 4.8.0 ships 26.3.27 (the chrome threshold of older ones is not found, so it stays empty).
//	v2rayNG: 1.10.1 ships 25.4.30 (1.9.45 ships 25.3.31: bad), 2.0.16 ships 26.3.23.
//	v2rayN: the app does not pin the core (it can be updated separately); the bundle of 7.12.6 has 25.5.16, of 7.20.0 has 26.3.27.
//	Mihomo: the core sends the share only from 1.19.9 on and only when the profile asks for it (support-x25519mlkem768), which
//	  a strict profile does. Older cores never.
//	kl!ck: Mihomo unmodified; its first release (0.4.0) asks as mihomo/1.19.31, so every release has the share. The device row keeps
//	  kl!ck's own version, not its core's.
//	sing-box (SFA, SFI, SFM, SFT), Karing, NekoBox: the REALITY client removes the share unconditionally; no version helps.
//	Hiddify (its sing-box fork added a key_share option on 2026-10-27), Streisand, FoXray, v2RayTun, INCY, OneXray, Shadowrocket,
//	FlClash, Clash Verge, Mihomo Party, Clash Meta for Android: the version-to-core mapping is not published; unknown.
var clientDefaults = []ClientRule{
	{App: "happ", Platform: "android", Core: CoreXray, MinChrome: "3.1.1", MinOther: "3.20.0", Title: "Happ"},
	{App: "happ", Platform: "windows", Core: CoreXray, MinChrome: "0.3.6", MinOther: "2.10.0", Title: "Happ"},
	{App: "happ", Platform: "macos", Core: CoreXray, MinChrome: "0.3.6", MinOther: "2.10.0", Title: "Happ"},
	{App: "happ", Platform: "linux", Core: CoreXray, MinChrome: "0.3.6", MinOther: "2.10.0", Title: "Happ"},
	{App: "happ", Platform: "ios", Core: CoreXray, MinOther: "4.8.0", Title: "Happ"},
	{App: "v2rayng", Core: CoreXray, MinChrome: "1.10.1", MinOther: "2.0.16", Title: "v2rayNG"},
	{App: "v2rayn", Core: CoreXray, MinChrome: "7.12.6", MinOther: "7.20.0", Title: "v2rayN"},
	{App: "klick", Core: CoreMihomo, MinChrome: "0.4.0", MinOther: "0.4.0", Title: "kl!ck"},
	{App: "mihomo", Core: CoreMihomo, MinChrome: "1.19.9", MinOther: "1.19.9", Title: "Mihomo core"},
	{App: "singbox", Core: CoreSingbox, Unsupported: true, Title: "sing-box (SFA, SFI, SFM)"},
	{App: "karing", Core: CoreSingbox, Unsupported: true, Title: "Karing"},
	{App: "nekobox", Core: CoreSingbox, Unsupported: true, Title: "NekoBox"},
	{App: "hiddify", Core: CoreSingbox, Title: "Hiddify"},
	{App: "streisand", Core: CoreXray, Title: "Streisand"},
	{App: "foxray", Core: CoreXray, Title: "FoXray"},
	{App: "v2raytun", Core: CoreXray, Title: "v2RayTun"},
	{App: "incy", Core: CoreXray, Title: "INCY"},
	{App: "onexray", Core: CoreXray, Title: "OneXray"},
	{App: "shadowrocket", Core: CoreOwn, Title: "Shadowrocket"},
	{App: "flclash", Core: CoreMihomo, Title: "FlClash"},
	{App: "clash-verge", Core: CoreMihomo, Title: "Clash Verge"},
	{App: "mihomo-party", Core: CoreMihomo, Title: "Mihomo Party"},
	{App: "cmfa", Core: CoreMihomo, Title: "Clash Meta for Android"},
}

// clientAliases maps the name an app puts first in its User-Agent (lower-cased) to the app id of the table, where they differ.
var clientAliases = map[string]string{
	"kl!ck": "klick", "sfa": "singbox", "sfi": "singbox", "sfm": "singbox", "sft": "singbox", "sing-box": "singbox",
	"clash.meta": "mihomo", "clash-meta": "mihomo", "clashmeta": "mihomo", "hiddifynext": "hiddify",
	"clash-verge-rev": "clash-verge", "mihomo.party": "mihomo-party", "clashmetaforandroid": "cmfa",
}

// ClientDefaults is a copy of the built-in table.
func ClientDefaults() []ClientRule { return slices.Clone(clientDefaults) }

var (
	clientPlatforms = []string{"", "ios", "android", "windows", "macos", "linux"}
	clientCores     = []string{CoreXray, CoreMihomo, CoreSingbox, CoreOwn, CoreUnknown}
)

var clientVersionRE = regexp.MustCompile(`^\d{1,9}(\.\d{1,9}){0,3}$`)

const (
	maxClientRules  = 100 // rules in the owner's table (built-in and own)
	maxClientAppLen = 40
)

// ErrClientRules wraps every refusal of a table (the RPC maps it to INVALID_ARGUMENT).
var ErrClientRules = errors.New("invalid client table")

func badRule(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrClientRules, fmt.Sprintf(format, a...))
}

// cleanClientRule normalizes one rule (lower-case app, trimmed versions with a leading "v" dropped) and refuses what cannot be one.
func cleanClientRule(r ClientRule) (ClientRule, error) {
	r.App = strings.ToLower(strings.TrimSpace(r.App))
	r.Platform = strings.ToLower(strings.TrimSpace(r.Platform))
	r.Core = strings.ToLower(strings.TrimSpace(r.Core))
	if r.App == "" || len(r.App) > maxClientAppLen {
		return r, badRule("app: 1 to %d characters", maxClientAppLen)
	}
	for i := 0; i < len(r.App); i++ {
		if c := r.App[i]; !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || (i > 0 && strings.IndexByte("._+-!", c) >= 0)) {
			return r, badRule("app %q: lower-case letters and digits, then . _ + - !", r.App)
		}
	}
	if !slices.Contains(clientPlatforms, r.Platform) {
		return r, badRule("platform %q: ios, android, windows, macos, linux or empty", r.Platform)
	}
	if !slices.Contains(clientCores, r.Core) {
		return r, badRule("core %q: xray, mihomo, singbox, own or unknown", r.Core)
	}
	for _, v := range []*string{&r.MinChrome, &r.MinOther} {
		*v = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(*v), "v"), "V")
		if *v == "" {
			continue
		}
		if !clientVersionRE.MatchString(*v) {
			return r, badRule("version %q: digits and dots, like 1.19.9", *v)
		}
	}
	if r.Unsupported && (r.MinChrome != "" || r.MinOther != "") {
		return r, badRule("%s: an app that does not work has no minimum version", r.App)
	}
	r.Title = ""
	return r, nil
}

// ClientTable is the table in force: the built-in rows with the owner's changes.
type ClientTable struct {
	rules  []ClientRule
	byKey  map[string]int
	titles map[string]string // app id -> title
}

// NewClientTable lays the owner's changes over the built-in table: a change replaces the built-in row of the same app and
// platform, any other is added. It does not validate; ParseClientOverrides does.
func NewClientTable(overrides []ClientRule) ClientTable {
	t := ClientTable{byKey: map[string]int{}, titles: map[string]string{}}
	add := func(r ClientRule) {
		if i, ok := t.byKey[r.key()]; ok {
			t.rules[i] = r
			return
		}
		t.byKey[r.key()] = len(t.rules)
		t.rules = append(t.rules, r)
	}
	for _, r := range clientDefaults {
		add(r)
		t.titles[r.App] = r.Title
	}
	for _, r := range overrides {
		if t.titles[r.App] != "" {
			r.Title = t.titles[r.App]
		} else {
			r.Title = r.App
			t.titles[r.App] = r.App
		}
		add(r)
	}
	return t
}

// Rules are the rows in force, built-in order first and the owner's own after.
func (t ClientTable) Rules() []ClientRule { return slices.Clone(t.rules) }

// ParseClientOverrides reads the stored list. Rows it cannot read are an error: the caller then keeps the built-in table.
func ParseClientOverrides(raw string) ([]ClientRule, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var in []ClientRule
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return nil, badRule("stored table: %v", err)
	}
	return cleanClientRules(in)
}

func cleanClientRules(in []ClientRule) ([]ClientRule, error) {
	if len(in) > maxClientRules {
		return nil, badRule("at most %d rows", maxClientRules)
	}
	out := make([]ClientRule, 0, len(in))
	seen := map[string]bool{}
	for _, r := range in {
		c, err := cleanClientRule(r)
		if err != nil {
			return nil, err
		}
		if seen[c.key()] {
			return nil, badRule("%s on %q twice", c.App, c.Platform)
		}
		seen[c.key()] = true
		out = append(out, c)
	}
	return out, nil
}

// ClientOverridesOf validates the whole table the owner wants and returns what to store: the rows that differ from the built-in
// table or are not in it. A built-in row missing from the table goes back to its built-in value.
func ClientOverridesOf(want []ClientRule) ([]ClientRule, error) {
	rows, err := cleanClientRules(want)
	if err != nil {
		return nil, err
	}
	def := map[string]ClientRule{}
	for _, r := range clientDefaults {
		def[r.key()] = r
	}
	var out []ClientRule
	for _, r := range rows {
		if d, ok := def[r.key()]; !ok || !d.same(r) {
			out = append(out, r)
		}
	}
	return out, nil
}

// MarshalClientOverrides is the stored form: "" for no changes.
func MarshalClientOverrides(rows []ClientRule) (string, error) {
	if len(rows) == 0 {
		return "", nil
	}
	b, err := json.Marshal(rows)
	return string(b), err
}

// ClientApp is the app id of a stored client string ("Happ/3.4.2", "kl!ck/0.5.1", "NekoBox/Android") and the version it reports
// ("" when it reports none).
func ClientApp(client string) (app, version string) {
	name, rest, _ := strings.Cut(strings.TrimSpace(client), "/")
	name = strings.ToLower(name)
	if a, ok := clientAliases[name]; ok {
		name = a
	}
	return name, rest
}

// ClientVerdict is what the table says about one device against one fingerprint.
type ClientVerdict struct {
	State, Reason string
	Need          string // ClientBelow: the lowest version known to work
	Title         string // the app's name in the table, "" for an app it does not know
}

func (t ClientTable) rule(app, platform string) (ClientRule, bool) {
	if i, ok := t.byKey[app+"\x00"+platform]; ok && platform != "" {
		return t.rules[i], true
	}
	i, ok := t.byKey[app+"\x00"]
	if !ok {
		return ClientRule{}, false
	}
	return t.rules[i], true
}

// classical reports a fingerprint that builds a hello without the post-quantum share, whatever the app: the iOS 14 and Edge
// 85 parrots have none, and random picks a classical parrot more often than not.
func classical(fp string) bool { return fp == "ios" || fp == "edge" || fp == "random" }

// Verdict judges a stored client string on a platform against a REALITY fingerprint of a profile ("" = chrome, the default).
func (t ClientTable) Verdict(client, platform, fingerprint string) ClientVerdict {
	app, ver := ClientApp(client)
	fp := strings.ToLower(strings.TrimSpace(fingerprint))
	title := t.titles[app]
	if app == "" || title == "" {
		return ClientVerdict{State: ClientUnknown, Reason: ReasonAppUnknown}
	}
	r, ok := t.rule(app, platform)
	switch {
	case !ok:
		return ClientVerdict{State: ClientUnknown, Reason: ReasonNoThreshold, Title: title}
	case r.Unsupported:
		return ClientVerdict{State: ClientUnsupported, Reason: ReasonApp, Title: title}
	case classical(fp):
		if r.Core == CoreXray || r.Core == CoreMihomo || r.Core == CoreSingbox {
			return ClientVerdict{State: ClientUnsupported, Reason: ReasonFingerprint, Title: title}
		}
		return ClientVerdict{State: ClientUnknown, Reason: ReasonNoThreshold, Title: title}
	}
	v, ok := parseVersion(ver)
	if !ok {
		return ClientVerdict{State: ClientUnknown, Reason: ReasonNoVersion, Title: title}
	}
	// Chrome-like fingerprints need the weaker threshold: a version known to work with firefox or safari works with chrome as
	// well (the cores only gain), but not the other way round.
	own, other := r.MinChrome, r.MinOther
	if fp == "firefox" || fp == "safari" {
		own, other = r.MinOther, r.MinChrome
	}
	switch {
	case own != "":
		if min, _ := parseVersion(own); compareVersions(v, min) >= 0 {
			return ClientVerdict{State: ClientOK, Title: title}
		}
		return ClientVerdict{State: ClientBelow, Reason: ReasonVersion, Need: own, Title: title}
	case other != "" && (fp != "firefox" && fp != "safari"):
		if min, _ := parseVersion(other); compareVersions(v, min) >= 0 {
			return ClientVerdict{State: ClientOK, Title: title}
		}
	}
	return ClientVerdict{State: ClientUnknown, Reason: ReasonNoThreshold, Title: title}
}

// severity orders the states by how much the owner should look at them.
func clientSeverity(state string) int {
	switch state {
	case ClientUnsupported:
		return 3
	case ClientBelow:
		return 2
	case ClientUnknown:
		return 1
	}
	return 0
}

// parseVersion reads the leading dotted number of a version ("1.19.33", "v7.12.6", "2.0.16-beta": its numbers 2, 0, 16). false when
// there is none. At most 5 numbers of at most 9 digits each.
func parseVersion(s string) ([]int, bool) {
	s = strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(s), "v"), "V")
	var out []int
	for len(out) < 5 {
		n, digits := 0, 0
		for digits < len(s) && s[digits] >= '0' && s[digits] <= '9' {
			if digits == 9 {
				return nil, false
			}
			n = n*10 + int(s[digits]-'0')
			digits++
		}
		if digits == 0 {
			break
		}
		out = append(out, n)
		s = s[digits:]
		if len(s) < 2 || s[0] != '.' {
			break
		}
		s = s[1:]
	}
	return out, len(out) > 0
}

// compareVersions: -1, 0 or 1; a shorter version is padded with zeros ("1.19" equals "1.19.0").
func compareVersions(a, b []int) int {
	for i := 0; i < max(len(a), len(b)); i++ {
		var x, y int
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}
