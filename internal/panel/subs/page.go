package subs

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/access"
	"github.com/mistgate/mistgate/internal/panel/instance"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
)

// The user page is one self-contained HTML response: web/dist/sub.html carries one
// inline module script and one <!--MG_DATA--> marker; the server replaces the marker with the page data and
// allows exactly that script by hash in the CSP. No asset is ever requested under the subscription prefix.

const (
	pageFile   = "sub.html"
	pageMarker = "<!--MG_DATA-->"
)

var scriptRe = regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script>`)
var linkRe = regexp.MustCompile(`(?is)<link\b[^>]*\brel\s*=\s*["']?(?:stylesheet|modulepreload)`)

// pageTemplate is sub.html split at the marker, with the hash of its inline script.
type pageTemplate struct {
	head, tail string
	hash       string // base64 of the sha256 of the script text, for the CSP
}

// loadPage reads and checks sub.html from dist. A build that is missing or does not have the expected shape
// is an error (the caller falls back to the base64 list): better than serving a page that cannot run.
func loadPage(dist fs.FS) (*pageTemplate, error) {
	raw, err := fs.ReadFile(dist, pageFile)
	if err != nil {
		return nil, err
	}
	html := string(raw)
	if strings.Count(html, pageMarker) != 1 {
		return nil, errors.New("sub.html must contain exactly one " + pageMarker)
	}
	if linkRe.MatchString(html) {
		return nil, errors.New("sub.html links a stylesheet or module: the page must be self-contained")
	}
	var script string
	n := 0
	for _, m := range scriptRe.FindAllStringSubmatch(html, -1) {
		attrs := strings.ToLower(m[1])
		if strings.Contains(attrs, "src=") || strings.Contains(attrs, "src ") {
			return nil, errors.New("sub.html loads an external script: the page must be self-contained")
		}
		if !strings.Contains(attrs, "module") {
			return nil, errors.New("sub.html has an inline script that is not the module")
		}
		script, n = m[2], n+1
	}
	if n != 1 {
		return nil, errors.New("sub.html must contain exactly one inline module script")
	}
	sum := sha256.Sum256([]byte(script))
	head, tail, _ := strings.Cut(html, pageMarker)
	return &pageTemplate{head: head, tail: tail, hash: base64.StdEncoding.EncodeToString(sum[:])}, nil
}

// csp is the Content-Security-Policy of the page; ancestors is 'none' for the public page and 'self' for the
// admin preview.
func (p *pageTemplate) csp(ancestors string) string {
	// connect-src 'self' is for the self-service calls of the Amnezia section (devices.go), and nothing else.
	return "default-src 'none'; script-src 'sha256-" + p.hash + "'; style-src 'unsafe-inline'; img-src 'self' data:; " +
		"font-src data:; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors " + ancestors
}

// render puts the page data in place of the marker. json.Marshal escapes <, > and & as < ..., so nothing
// in the data (a user named "</script>...") can end the data block or open a comment.
func (p *pageTemplate) render(d pageData) ([]byte, error) {
	b, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	return []byte(p.head + `<script type="application/json" id="mg-data">` + string(b) + `</script>` + p.tail), nil
}

// ---- page data, version 1 ----

type pageData struct {
	V               int          `json:"v"`
	Lang            string       `json:"lang"`
	Brand           pageBrand    `json:"brand"`
	Title           string       `json:"title"`
	SubscriptionURL string       `json:"subscription_url"`
	ServerCount     int          `json:"server_count"` // servers the link gives Happ ("all your servers (3) appear in Happ")
	ServerLoads     []pageServer `json:"server_loads"`
	User            pageUser     `json:"user"`
	Announcement    string       `json:"announcement"`
	SupportURL      string       `json:"support_url"`
	Options         pageOptions  `json:"options"`
	Apps            []pageApp    `json:"apps"`
	Access          pageAccess   `json:"access"`
	Devices         []pageDevice `json:"devices"`
	Amnezia         *pageAmnezia `json:"amnezia"` // null unless the user has the Amnezia app and a usable AWG profile
	// Locked is set on the page of a token whose password was not entered: only Lang, Brand and Title are real, the
	// rest is empty, and UnlockURL is where the form posts the password.
	Locked    bool   `json:"locked"`
	UnlockURL string `json:"unlock_url"`
}

type pageServer struct {
	Name         string `json:"name"`
	LoadPercent  *int   `json:"load_percent,omitempty"`
	RxBps        uint64 `json:"rx_bps"`
	TxBps        uint64 `json:"tx_bps"`
	CapacityMbps int    `json:"capacity_mbps"`
}

type pageBrand struct {
	Parts   []string `json:"parts"`
	LogoSVG string   `json:"logo_svg"`
	Accent  string   `json:"accent"`
}

type pageUser struct {
	Name          string `json:"name"`
	Status        string `json:"status"`
	ExpiresUnix   int64  `json:"expires_unix"`
	UsedBytes     uint64 `json:"used_bytes"`
	QuotaBytes    uint64 `json:"quota_bytes"`
	QuotaReset    string `json:"quota_reset"`
	NextResetUnix int64  `json:"next_reset_unix"`
	DeviceLimit   int    `json:"device_limit"`
	DevicesUsed   int    `json:"devices_used"`
}

type pageOptions struct {
	ShowAnnouncement bool `json:"show_announcement"`
	ShowSupport      bool `json:"show_support"`
	ShowQR           bool `json:"show_qr"`
}

type pageApp struct {
	Platform    string `json:"platform"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	DownloadURL string `json:"download_url"`
	AddURL      string `json:"add_url"`
	Description string `json:"description"` // one plain-text line for the card; the page shows it as text
	Recommended bool   `json:"recommended"`
}

type pageAccess struct {
	Happ    bool `json:"happ"`
	Amnezia bool `json:"amnezia"`
}

type pageDevice struct {
	ID           string `json:"id"`
	Platform     string `json:"platform"`
	Model        string `json:"model"`
	App          string `json:"app"`
	LastSeenUnix int64  `json:"last_seen_unix"`
	Online       bool   `json:"online"`
}

var platformName = map[adminv1.Platform]string{
	adminv1.Platform_PLATFORM_IOS: "ios", adminv1.Platform_PLATFORM_ANDROID: "android", adminv1.Platform_PLATFORM_WINDOWS: "windows",
	adminv1.Platform_PLATFORM_MACOS: "macos", adminv1.Platform_PLATFORM_LINUX: "linux",
}

// buildPageData assembles the data of one user's page. link is the subscription URL, title the effective
// subscription title.
// preview is the admin's framed preview: nothing on it can write.
func buildPageData(v access.SubView, link, title, lang string, set *adminv1.SubscriptionSettings, b instance.Settings, now time.Time, preview bool) pageData {
	pageName := v.SubscriptionName
	if pageName == "" {
		pageName = v.UserName
	}
	parts := []string{b.BrandHead}
	if b.BrandTail != "" {
		parts = append(parts, b.BrandTail)
	}
	opt := set.GetUserPage()
	d := pageData{
		V: 1, Lang: lang, Title: title, SubscriptionURL: link, ServerCount: len(v.Lines), ServerLoads: serverLoads(v.Servers, lang),
		Brand: pageBrand{Parts: parts, LogoSVG: b.LogoSVG, Accent: b.Accent},
		User: pageUser{
			Name: pageName, Status: v.Status, ExpiresUnix: unixOrZero(v.Expires), UsedBytes: v.Up + v.Down, QuotaBytes: v.Total,
			QuotaReset: v.QuotaReset, NextResetUnix: unixOrZero(v.NextReset), DeviceLimit: v.DeviceLimit, DevicesUsed: len(v.Devices),
		},
		Announcement: set.GetAnnouncement(), SupportURL: set.GetSupportUrl(),
		Options: pageOptions{ShowAnnouncement: opt.GetShowAnnouncement(), ShowSupport: opt.GetShowSupport(), ShowQR: opt.GetShowQr()},
		Apps:    []pageApp{}, Devices: []pageDevice{},
		Access: pageAccess{Happ: v.AccessHapp, Amnezia: v.AccessAmnezia},
	}
	if d.User.QuotaReset == "" {
		d.User.QuotaReset = access.ResetNone
	}
	for _, a := range set.GetApps() {
		kind := "happ"
		if a.Kind == adminv1.App_APP_AMNEZIA {
			kind = "amnezia"
		}
		d.Apps = append(d.Apps, pageApp{
			Platform: platformName[a.Platform], Kind: kind, Name: a.Name, DownloadURL: a.DownloadUrl,
			AddURL: addURL(a.AddLinkTemplate, link, title), Description: a.Description, Recommended: a.Recommended,
		})
	}
	for _, dv := range v.Devices {
		d.Devices = append(d.Devices, pageDevice{ID: dv.ID, Platform: dv.Platform, Model: dv.Model, App: dv.App, LastSeenUnix: unixOrZero(dv.LastSeen), Online: dv.Online})
	}
	d.Amnezia = amneziaData(v, link, subsettings.SelfService(set), preview, now)
	return d
}

// serverLoads returns one row per eligible node, not per protocol profile. Fresh rates and each node's share of the
// current traffic across measured subscription nodes are supplied by the access view.
func serverLoads(servers []access.SubServer, lang string) []pageServer {
	indexes := map[string]int{}
	out := make([]pageServer, 0, len(servers))
	for _, server := range servers {
		if server.MetricsAt.IsZero() {
			continue
		}
		key := server.NodeID
		if key == "" {
			key = server.Node
		}
		if _, ok := indexes[key]; ok {
			continue // multiple Hysteria/AWG profiles on one machine share its host-level measurement
		}
		name := access.CountryName(server.CountryCode, lang)
		place := strings.TrimSpace(server.Location)
		if place == "" {
			place = server.Node
		}
		if place != "" && place != name {
			if name == "" {
				name = place
			} else {
				name += " · " + place
			}
		}
		if name == "" {
			name = "Server"
		}
		indexes[key] = len(out)
		out = append(out, pageServer{Name: name, LoadPercent: server.LoadPercent, RxBps: server.NetworkRxBps,
			TxBps: server.NetworkTxBps, CapacityMbps: server.BandwidthMbps})
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.LoadPercent != nil && b.LoadPercent != nil {
			return *a.LoadPercent > *b.LoadPercent
		}
		if a.LoadPercent != nil {
			return true
		}
		if b.LoadPercent != nil {
			return false
		}
		return max(a.RxBps, a.TxBps) > max(b.RxBps, b.TxBps)
	})
	return out
}

// lockedPageData is the page data of a locked page: the brand and the unlock address, no user, no apps, no link.
func lockedPageData(link, title, lang string, b instance.Settings) pageData {
	parts := []string{b.BrandHead}
	if b.BrandTail != "" {
		parts = append(parts, b.BrandTail)
	}
	return pageData{
		V: 1, Lang: lang, Title: title, Brand: pageBrand{Parts: parts, LogoSVG: b.LogoSVG, Accent: b.Accent},
		Apps: []pageApp{}, Devices: []pageDevice{}, ServerLoads: []pageServer{}, Locked: true, UnlockURL: link + "/unlock",
	}
}

// addURL renders an app's one-tap "add" link from its template: {url} is the subscription URL as it is,
// {url_enc} the URL percent-encoded, {name_enc} the subscription title percent-encoded. One pass, so a title
// that itself contains "{url}" is not expanded. "" when the template or the link is empty (the page then
// offers to copy the link).
func addURL(tmpl, link, title string) string {
	if tmpl == "" || link == "" {
		return ""
	}
	return strings.NewReplacer("{url}", link, "{url_enc}", pctEncode(link), "{name_enc}", pctEncode(title)).Replace(tmpl)
}

// pctEncode is url.QueryEscape with %20 for a space (a "+" is not a space in a path or in app schemes).
func pctEncode(s string) string { return strings.ReplaceAll(url.QueryEscape(s), "+", "%20") }
