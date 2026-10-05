// Package subs serves the public subscription endpoint: GET <prefix>/<token> returns base64 of the user's
// client lines (hysteria2:// URIs) with the headers Happ and other Xray/sing-box apps read, or, for a client the
// settings send to the Mihomo format, a Mihomo YAML profile (mihomo.go); a browser gets the user page. Under the same
// prefix the page's self-service endpoints manage the user's AmneziaWG devices (devices.go). Unknown tokens get the
// decoy, so the endpoint cannot be told from any other path of the site.
//
// Abuse protection, all in memory and bounded, none of it logging a token or an address:
//   - token guessing: after MissLimit unknown tokens from one client network within MissWindow, that
//     network gets only the decoy for BlockFor, even for valid tokens;
//   - per token: fetches within MinInterval are answered from a cache of the rendered response (no
//     database work), and more than MaxPerHour fetches an hour get a 429;
//   - link sharing: distinct client networks (/24, /48) seen per token per day are counted (never stored
//     as addresses), and above SharedNets one subscription_shared_suspect event is written for that day;
//   - self-service writes (add, configs, rotate, revoke, rename a device, pick a DNS): a separate per-token counter,
//     MaxWritesPerHour an hour, so that the fetch budget above is not shared with them.
package subs

import (
	"context"
	"encoding/base64"
	"errors"
	"hash/maphash"
	"io/fs"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/access"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/httpserver/ratelimit"
	"github.com/mistgate/mistgate/internal/panel/instance"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
	"github.com/mistgate/mistgate/internal/plugin"
)

// Source resolves a token; *access.Service implements it.
type Source interface {
	Subscription(ctx context.Context, token string) (access.SubView, error)
}

// FormatSource is a Source that can also render a client format (the Mihomo profile needs the proxies of the Mihomo
// format); *access.Service implements it. A Source without it serves the Mihomo rule the base64 list.
type FormatSource interface {
	SubscriptionWith(ctx context.Context, token string, opt access.SubOptions) (access.SubView, error)
}

// Events receives the "link shared" signal; *store.Store implements it.
type Events interface {
	InsertEvent(ctx context.Context, e store.EventRow) error
}

// Routing supplies the Happ `routing` header value for a user (the effective DNS preset rendered as a Happ
// routing profile); the DNS module implements it. "" = send no header.
type Routing interface {
	HappRouting(ctx context.Context, userID string) (string, error)
}

// EventSharedSuspect is the event code written when one link is used from many networks.
const EventSharedSuspect = "subscription_shared_suspect"

// Config of the handler. Zero values of the limits mean the defaults below; a negative value switches
// that protection off.
type Config struct {
	// Prefix is the secret path prefix the handler is mounted under ("/k3xq8"). It is stripped from the
	// request path when present, so the handler works whether or not the mux already stripped it.
	Prefix string
	// Title is the subscription name apps show (profile-title) when neither the settings nor Brand give one.
	Title string
	// BaseURL is the public subscription address up to and including the prefix ("https://sub.example.com/k3xq8");
	// the user's link is BaseURL + "/" + token. Empty: derived from the request (Host, TLS), good enough for tests.
	BaseURL string

	// Settings is the subscription settings cache (what the admin edits); nil = the built-in defaults.
	Settings *subsettings.Cache
	// Brand returns the instance brand at request time (a rename shows up without a restart, within
	// BrandTTL); nil = a brand named Title.
	Brand func(ctx context.Context) (instance.Settings, error)
	// Routing, if set, adds the Happ `routing` header for Happ-like clients.
	Routing Routing
	// DNS resolves the effective DNS preset of a user for the dns section of the Mihomo profile; nil = the Routing
	// when it can do that (HappRouting's can), else the profile has no dns section.
	DNS Effective
	// Dist holds the build with sub.html (the user page); nil = the embedded web/dist. When the page is not
	// there or has the wrong shape, browsers get the base64 list and one line is logged.
	Dist fs.FS

	// ClientIP returns the client address of a request; the default is auth.ClientIPFrom of the request
	// context (the public listener resolves it under the trusted-proxy rules). An invalid address
	// (unknown client) is never limited or counted.
	ClientIP func(*http.Request) netip.Addr
	// Events, if set, receives subscription_shared_suspect events. Log, if set, gets their write errors.
	Events Events
	Log    *slog.Logger
	// Now is the clock (tests); default time.Now.
	Now func() time.Time

	MissLimit   int           // unknown tokens per MissWindow before the block; default 20
	MissWindow  time.Duration // default 1 minute
	BlockFor    time.Duration // default 15 minutes
	MinInterval time.Duration // cache lifetime of a token's data; default 10 seconds
	MaxPerHour  int           // fetches per token per hour; default 60
	SharedNets  int           // distinct client networks per token per day before the event; default 8
	MaxKeys     int           // clients and tokens tracked per table (memory bound); default 10000
	// MaxWritesPerHour is the self-service writes (device add, configs, rotate, revoke, rename, pick a DNS) per token an hour;
	// default 20, negative = no limit. Separate from MaxPerHour.
	MaxWritesPerHour int
	BrandTTL         time.Duration // how long a read of the brand is reused; default 5 seconds

	// PageKey is the key of the page passwords and their cookies (vault.Derive(pagepass.KeyLabel)). Empty = the
	// page never asks for a password. With a key, the page and its self-service calls are gated while the
	// settings say so (subsettings.PagePassword); the subscription itself never is.
	PageKey []byte
	// UnlockTries is how many password entries one token, and one client network, get within UnlockWindow before
	// the answer is "try later"; defaults 5 and 10 minutes, UnlockTries negative = no limit.
	UnlockTries  int
	UnlockWindow time.Duration
}

func (c *Config) defaults() {
	if c.ClientIP == nil {
		c.ClientIP = func(r *http.Request) netip.Addr { return auth.ClientIPFrom(r.Context()) }
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.MissLimit == 0 {
		c.MissLimit = 20
	}
	if c.MissWindow == 0 {
		c.MissWindow = time.Minute
	}
	if c.BlockFor == 0 {
		c.BlockFor = 15 * time.Minute
	}
	if c.MinInterval == 0 {
		c.MinInterval = 10 * time.Second
	}
	if c.MaxPerHour == 0 {
		c.MaxPerHour = 60
	}
	if c.SharedNets == 0 {
		c.SharedNets = 8
	}
	if c.MaxKeys <= 0 {
		c.MaxKeys = 10000
	}
	if c.MaxWritesPerHour == 0 {
		c.MaxWritesPerHour = 20
	}
	if c.DNS == nil {
		c.DNS, _ = c.Routing.(Effective)
	}
	if c.UnlockTries == 0 {
		c.UnlockTries = 5
	}
	if c.UnlockWindow == 0 {
		c.UnlockWindow = 10 * time.Minute
	}
	if c.BrandTTL == 0 {
		c.BrandTTL = 5 * time.Second
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
}

const (
	// Hours between automatic refreshes by the app for users without access: short, so that an admin's fix
	// (quota raised, term extended) reaches them soon. Fixed value, not a setting.
	updateHoursNoAccess = 1

	// announceMax is the longest announcement Happ displays (characters).
	announceMax = 200

	tokenMin, tokenMax = 20, 64 // the issued tokens are 43 characters; anything outside is not ours
)

// tokenState is what is remembered about one valid token. Nothing in it is an address.
type tokenState struct {
	mu       sync.Mutex
	userName string

	cached map[plugin.ClientFormat]cachedView // the data of the last fetch per view format, reused for MinInterval

	winStart time.Time // start of the current hour; hits counts the fetches in it
	hits     int

	wStart time.Time // start of the current hour of self-service writes; writes counts them
	writes int

	day      int64               // unix day of nets
	nets     map[uint64]struct{} // keyed hashes of the networks seen today, capped
	reported bool                // the event for this day was written
}

// cachedView is one cached fetch.
type cachedView struct {
	v       *access.SubView
	at      time.Time
	touched bool // an app's fetch made it: the device was marked as having received the subscription
}

// clientState is the token-guessing record of one client network.
type clientState struct {
	mu           sync.Mutex
	misses       int
	winStart     time.Time
	blockedUntil time.Time
}

type handler struct {
	*common
	src    Source
	fsrc   FormatSource // src when it can render a format, else nil
	dev    Devices      // src when it manages devices, else nil
	pdns   PageDNS      // src when it takes the DNS pick of the page, else nil
	cop    *http.CrossOriginProtection
	decoy  http.Handler
	prefix string

	seed    maphash.Seed
	tokens  *ratelimit.Map[*tokenState]
	clients *ratelimit.Map[*clientState]
	tries   *ratelimit.Map[*tryState] // password entries, keyed "t:<token>" and "c:<client network>"
}

// Handler returns the subscription endpoint. decoy answers everything that is not a valid request for a
// known token.
func Handler(src Source, decoy http.Handler, cfg Config) http.Handler {
	cfg.defaults()
	c := newCommon(cfg)
	h := &handler{
		common: c, src: src, decoy: decoy, cop: http.NewCrossOriginProtection(),
		prefix:  strings.TrimRight(cfg.Prefix, "/"),
		seed:    maphash.MakeSeed(),
		tokens:  ratelimit.NewMap[*tokenState](cfg.MaxKeys),
		clients: ratelimit.NewMap[*clientState](cfg.MaxKeys),
		tries:   ratelimit.NewMap[*tryState](cfg.MaxKeys),
	}
	h.fsrc, _ = src.(FormatSource)
	h.dev, _ = src.(Devices)
	h.pdns, _ = src.(PageDNS)
	if o := originOf(cfg.BaseURL); o != "" {
		// Behind a proxy that rewrites Host the Origin of a browser is still the public address.
		if err := h.cop.AddTrustedOrigin(o); err != nil {
			cfg.Log.Warn("subscription base URL is not a usable origin", "err", err)
		}
	}
	return h
}

// originOf is "scheme://host[:port]" of a base URL, "" when it has none.
func originOf(base string) string {
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	now := h.cfg.Now()
	ip := h.cfg.ClientIP(r)
	client := ""
	if ip.IsValid() {
		client = auth.SourceKey(ip)
		if h.blocked(client, now) {
			h.decoy.ServeHTTP(w, r) // even for a valid token: guessing gets nothing out of this
			return
		}
	}
	token, sub, ok := routeOf(r, h.prefix)
	if !ok {
		h.miss(client, now)
		h.decoy.ServeHTTP(w, r)
		return
	}
	switch {
	case sub == "unlock":
		h.serveUnlock(w, r, token, client, now)
		return
	case sub == "dns":
		h.serveDNS(w, r, token, client, now)
		return
	case sub != "":
		h.serveDevices(w, r, token, sub, client, now)
		return
	}

	// What the client gets decides which view of the user is built: the Mihomo profile needs the proxies of its
	// own format (and gives the implicit device its AWG credentials), everything else the URI list.
	set := h.settings(r.Context())
	_, sf, _ := Choose(set, r.UserAgent())
	format := plugin.FormatURIList
	if sf == adminv1.SubFormat_SUB_FORMAT_MIHOMO_YAML && h.fsrc != nil {
		format = plugin.FormatMihomo
	}

	// Only an app counts as "the app received the subscription": a browser (the page, or the decoy) does not.
	touch := sf != adminv1.SubFormat_SUB_FORMAT_USER_PAGE && sf != adminv1.SubFormat_SUB_FORMAT_DECOY
	st, known := h.tokens.Get(token)
	admitted := false
	if known {
		res, retry, suspect := st.admit(h, ip, now, format, touch)
		if retry > 0 {
			tooMany(w, retry)
			return
		}
		if suspect != "" {
			h.suspect(suspect)
		}
		if res != nil {
			h.respond(w, r, token, *res, set, sf, format)
			return
		}
		admitted = true
	}

	v, err := h.fetch(r.Context(), token, format, touch)
	if errors.Is(err, access.ErrUnknownToken) {
		h.tokens.Delete(token) // a rotated or deleted link stops being remembered
		h.miss(client, now)
		h.decoy.ServeHTTP(w, r)
		return
	}
	if err != nil {
		// Not logged here: the access module logged the cause, and this handler must never write a
		// token or an address anywhere.
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !admitted {
		st = h.tokens.GetOrCreate(token, func() *tokenState { return &tokenState{} })
		if _, retry, suspect := st.admit(h, ip, now, format, touch); retry > 0 {
			tooMany(w, retry)
			return
		} else if suspect != "" {
			h.suspect(suspect)
		}
	}
	st.remember(v, format, now, touch)
	h.respond(w, r, token, v, set, sf, format)
}

// fetch builds the view of a token in a format; a Source that cannot render formats gets the URI list. touch: an app
// asked, so the device is marked as having received the subscription; the page view and its calls do not.
func (h *handler) fetch(ctx context.Context, token string, format plugin.ClientFormat, touch bool) (access.SubView, error) {
	switch {
	case format == plugin.FormatMihomo && h.fsrc != nil:
		return h.fsrc.SubscriptionWith(ctx, token, access.SubOptions{Format: plugin.FormatMihomo})
	case !touch && h.fsrc != nil:
		return h.fsrc.SubscriptionWith(ctx, token, access.SubOptions{NoTouch: true})
	}
	return h.src.Subscription(ctx, token)
}

// remember keeps the user name and the data of a fetch (for MinInterval, per view format); touched: an app's fetch made it.
func (st *tokenState) remember(v access.SubView, format plugin.ClientFormat, now time.Time, touched bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	st.userName = v.UserName
	if st.cached == nil {
		st.cached = map[plugin.ClientFormat]cachedView{}
	}
	st.cached[format] = cachedView{v: &v, at: now, touched: touched}
}

// respond renders the answer for a valid token from its data, the settings and the brand of this moment
// (only the data is cached, so an edit of the settings or a rename shows up without waiting for MinInterval).
// sf is the format chosen for this request, format the one the view was built in (the URI list unless the Source can
// render the Mihomo one).
func (h *handler) respond(w http.ResponseWriter, r *http.Request, token string, v access.SubView, set *adminv1.SubscriptionSettings, sf adminv1.SubFormat, format plugin.ClientFormat) {
	switch sf {
	case adminv1.SubFormat_SUB_FORMAT_DECOY:
		h.decoy.ServeHTTP(w, r) // "pretend the link does not exist"
		return
	case adminv1.SubFormat_SUB_FORMAT_USER_PAGE:
		if h.page != nil {
			if h.gated(set) && !h.unlocked(r, token) {
				h.writeLocked(w, r, h.link(r, token))
				return
			}
			h.writePage(w, r, v, h.link(r, token), "'none'", false)
			return
		}
		h.noPage.Do(func() {
			h.cfg.Log.Warn("user page is not available, serving the base64 list to browsers", "err", h.pageErr)
		})
	case adminv1.SubFormat_SUB_FORMAT_MIHOMO_YAML:
		if format == plugin.FormatMihomo { // a Source without formats leaves the rule on the base64 list
			h.writeMihomo(w, r, token, v, set)
			return
		}
	}
	h.writeList(w, r, token, v, set)
}

// headers sets what every subscription answer carries: the name, usage and term the apps show, the refresh
// interval, the user's page and the announcement and support link.
func (h *handler) headers(w http.ResponseWriter, r *http.Request, token string, v access.SubView, set *adminv1.SubscriptionSettings, b instance.Settings) {
	ctx := r.Context()
	hd := w.Header()
	hd.Set("Cache-Control", "no-store")
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Profile-Title", b64(h.title(set, b)))
	hd.Set("Subscription-Userinfo", userinfo(v))
	hours := int(set.GetUpdateIntervalHours())
	if hours == 0 {
		hours = subsettings.DefaultUpdateHours
	}
	if v.Status != access.StatusActive {
		hours = updateHoursNoAccess
	}
	hd.Set("Profile-Update-Interval", strconv.Itoa(hours))
	hd.Set("Profile-Web-Page-Url", h.link(r, token))
	ann := set.GetAnnouncement()
	if note := stateNote(v, b.Language, h.cfg.Now()); note != "" {
		ann = note // the reason the app stopped working is the announcement that matters now
	}
	if a := cutAnnounce(ann, announceMax); a != "" {
		hd.Set("Announce", b64(a))
	}
	if u := set.GetSupportUrl(); u != "" {
		hd.Set("Support-Url", u)
	}
	if h.cfg.Routing != nil && v.Status == access.StatusActive && v.UserID != "" && isHapp(r.UserAgent()) {
		if val, err := h.cfg.Routing.HappRouting(ctx, v.UserID); err != nil {
			h.cfg.Log.Warn("happ routing header skipped", "err", err) // no user id, no token in the log
		} else if val != "" {
			hd.Set("Routing", val)
		}
	}
}

// writeMihomo answers with the Mihomo YAML profile: the proxies of the view (named like the base64 list names its
// servers, never with the load percentage: see remarks), one select group, the dns section of the user's effective preset. gzip when the client accepts it.
func (h *handler) writeMihomo(w http.ResponseWriter, r *http.Request, token string, v access.SubView, set *adminv1.SubscriptionSettings) {
	ctx := r.Context()
	b := h.brand(ctx)
	title := h.title(set, b)
	p := mihomoProfile{title: title, lines: v.Lines, log: h.cfg.Log}
	if note := stateNote(v, b.Language, h.cfg.Now()); note != "" {
		p.lines, p.names = []string{placeholderProxy}, []string{note}
	} else if len(v.Servers) == len(v.Lines) && len(v.Servers) > 0 {
		p.names = remarks(v.Servers, set.GetServerNameTemplate(), b.Language)
	}
	if h.cfg.DNS != nil && v.UserID != "" {
		if pre, _, err := h.cfg.DNS.Effective(ctx, v.UserID); err != nil {
			h.cfg.Log.Warn("mihomo profile: no dns section", "err", err)
		} else {
			p.preset = &pre
		}
	}
	body, err := p.build()
	if err != nil {
		h.cfg.Log.Error("mihomo profile", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	h.headers(w, r, token, v, set, b)
	hd := w.Header()
	hd.Set("Content-Type", "text/yaml; charset=utf-8")
	hd.Set("Content-Disposition", "attachment; filename*=UTF-8''"+pctEncode(title))
	hd.Add("Vary", "Accept-Encoding")
	if acceptsGzip(r) {
		body = gzipped(body)
		hd.Set("Content-Encoding", "gzip")
	}
	w.Write(body)
}

// writeList answers with the base64 list of share links and the headers apps read.
func (h *handler) writeList(w http.ResponseWriter, r *http.Request, token string, v access.SubView, set *adminv1.SubscriptionSettings) {
	b := h.brand(r.Context())
	h.headers(w, r, token, v, set, b)
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(base64.StdEncoding.EncodeToString([]byte(strings.Join(h.lines(v, set, b, isHapp(r.UserAgent())), "\n")))))
}

// lines are the share links with their remarks (server names) rendered from the template; a view without
// Servers (a fake source) keeps its Lines as they are. A person without access gets the one entry that says why.
// happ: the client is Happ, whose names carry the load percentage.
func (h *handler) lines(v access.SubView, set *adminv1.SubscriptionSettings, b instance.Settings, happ bool) []string {
	if note := stateNote(v, b.Language, h.cfg.Now()); note != "" {
		return []string{withRemark(placeholderURI, note)}
	}
	if len(v.Servers) == 0 {
		return v.Lines
	}
	names := happRemarks(v.Servers, set.GetServerNameTemplate(), b.Language, happ)
	out := make([]string, len(v.Servers))
	for i, s := range v.Servers {
		out[i] = withRemark(s.URI, names[i])
	}
	return out
}

func b64(s string) string { return "base64:" + base64.StdEncoding.EncodeToString([]byte(s)) }

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// tooMany is the answer to a valid link fetched too often. Only the holder of the token can get it.
func tooMany(w http.ResponseWriter, retry time.Duration) {
	secs := int((retry + time.Second - 1) / time.Second)
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	http.Error(w, "too many requests", http.StatusTooManyRequests)
}

// blocked reports whether the client network is in its BlockFor period.
func (h *handler) blocked(client string, now time.Time) bool {
	if h.cfg.MissLimit < 0 {
		return false
	}
	c, ok := h.clients.Get(client)
	if !ok {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return now.Before(c.blockedUntil)
}

// miss counts one unknown token (or a request under the prefix that is no token at all) against the client.
func (h *handler) miss(client string, now time.Time) {
	if client == "" || h.cfg.MissLimit < 0 {
		return
	}
	c := h.clients.GetOrCreate(client, func() *clientState { return &clientState{} })
	c.mu.Lock()
	defer c.mu.Unlock()
	if now.Sub(c.winStart) >= h.cfg.MissWindow {
		c.winStart, c.misses = now, 0
	}
	if c.misses++; c.misses >= h.cfg.MissLimit {
		c.blockedUntil, c.misses = now.Add(h.cfg.BlockFor), 0
	}
}

// admit counts one fetch of the token: against the hourly cap (retry > 0: refused), towards the day's
// distinct networks (suspect is the user name, non-empty once per day when the threshold is crossed), and
// returns the cached response when it is younger than MinInterval.
func (st *tokenState) admit(h *handler, ip netip.Addr, now time.Time, format plugin.ClientFormat, touch bool) (res *access.SubView, retry time.Duration, suspect string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if h.cfg.MaxPerHour > 0 {
		if now.Sub(st.winStart) >= time.Hour {
			st.winStart, st.hits = now, 0
		}
		if st.hits >= h.cfg.MaxPerHour {
			return nil, st.winStart.Add(time.Hour).Sub(now), ""
		}
		st.hits++
	}
	if h.cfg.SharedNets > 0 && ip.IsValid() {
		if day := now.UTC().Unix() / 86400; day != st.day {
			st.day, st.nets, st.reported = day, nil, false
		}
		// A keyed hash of the network, in memory only; the set stops growing one past the
		// threshold, so a token costs at most SharedNets+1 words however many networks use it.
		if len(st.nets) <= h.cfg.SharedNets {
			if st.nets == nil {
				st.nets = map[uint64]struct{}{}
			}
			st.nets[maphash.String(h.seed, networkOf(ip))] = struct{}{}
		}
		if len(st.nets) > h.cfg.SharedNets && !st.reported {
			st.reported = true
			suspect = st.userName
			if suspect == "" {
				suspect = "?"
			}
		}
	}
	// A view the page left does not stand in for an app's fetch: that one must reach the device.
	if c, ok := st.cached[format]; ok && h.cfg.MinInterval > 0 && now.Sub(c.at) < h.cfg.MinInterval && (c.touched || !touch) {
		res = c.v
	}
	return res, 0, suspect
}

// networkOf is the /24 (IPv4) or /48 (IPv6) the address belongs to.
func networkOf(ip netip.Addr) string {
	ip = ip.Unmap()
	bits := 48
	if ip.Is4() {
		bits = 24
	}
	return netip.PrefixFrom(ip, bits).Masked().String()
}

// suspect writes the once-a-day "link shared" event: a count, never an address.
func (h *handler) suspect(user string) {
	if h.cfg.Events == nil {
		return
	}
	if user == "?" {
		user = ""
	}
	e := store.EventRow{
		Time: h.cfg.Now(), Severity: 2, Code: EventSharedSuspect, Source: "panel",
		Params: map[string]string{"user": user, "networks": strconv.Itoa(h.cfg.SharedNets + 1), "period": "day"},
	}
	go func() { // off the request: the event table has one writer
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := h.cfg.Events.InsertEvent(ctx, e); err != nil && h.cfg.Log != nil {
			h.cfg.Log.Warn("write subscription event", "err", err)
		}
	}()
}

// routeOf splits a request under the prefix into the token and what follows it: GET/HEAD <prefix>/<token> is the
// subscription itself (sub ""), POST <prefix>/<token>/devices[/<id>/<action>] a self-service call (sub "devices..."), POST <prefix>/<token>/dns the pick of a DNS (sub "dns"), POST <prefix>/<token>/unlock the page password.
// Anything else is not ours (ok false).
func routeOf(r *http.Request, prefix string) (token, sub string, ok bool) {
	p := r.URL.Path
	if prefix != "" {
		p = strings.TrimPrefix(p, prefix)
	}
	p, found := strings.CutPrefix(p, "/")
	if !found {
		return "", "", false
	}
	token, sub, _ = strings.Cut(p, "/")
	if len(token) < tokenMin || len(token) > tokenMax {
		return "", "", false
	}
	for i := 0; i < len(token); i++ {
		c := token[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return "", "", false
		}
	}
	switch {
	case sub == "" && (r.Method == http.MethodGet || r.Method == http.MethodHead):
		return token, "", true
	case r.Method == http.MethodPost && (sub == "unlock" || sub == "dns" || sub == "devices" || strings.HasPrefix(sub, "devices/")):
		return token, sub, true
	}
	return "", "", false
}

// userinfo is the de-facto standard header: used traffic, quota (0 = unlimited) and term end (0 = never).
func userinfo(v access.SubView) string {
	var exp int64
	if !v.Expires.IsZero() {
		exp = v.Expires.Unix()
	}
	return "upload=" + strconv.FormatUint(v.Up, 10) + "; download=" + strconv.FormatUint(v.Down, 10) +
		"; total=" + strconv.FormatUint(v.Total, 10) + "; expire=" + strconv.FormatInt(exp, 10)
}
