package subs

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/access"
	"github.com/mistgate/mistgate/internal/panel/instance"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
	"github.com/mistgate/mistgate/web"
)

// defaultSettings serves a handler built without a settings cache (tests, tools). Read-only.
var defaultSettings = subsettings.Defaults()

// common is what the public handler and the admin preview share: settings, brand and the user page.
type common struct {
	cfg    Config
	prefix string

	page    *pageTemplate // nil when sub.html is missing or has the wrong shape
	pageErr error
	noPage  sync.Once // the "page unavailable" line is logged once

	brandMu sync.Mutex
	brandV  instance.Settings
	brandAt time.Time
	brandOK bool
}

func newCommon(cfg Config) *common {
	c := &common{cfg: cfg, prefix: strings.TrimRight(cfg.Prefix, "/")}
	dist := cfg.Dist
	if dist == nil {
		dist = web.Dist()
	}
	c.page, c.pageErr = loadPage(dist)
	return c
}

func (c *common) settings(ctx context.Context) *adminv1.SubscriptionSettings {
	if c.cfg.Settings == nil {
		return defaultSettings
	}
	return c.cfg.Settings.Get(ctx)
}

// brand reads the instance brand, reusing a read for BrandTTL (negative = always read). A failing read keeps
// the last good value, or a brand named after Config.Title.
func (c *common) brand(ctx context.Context) instance.Settings {
	c.brandMu.Lock()
	defer c.brandMu.Unlock()
	now := c.cfg.Now()
	if c.brandOK && c.cfg.BrandTTL > 0 && now.Sub(c.brandAt) < c.cfg.BrandTTL {
		return c.brandV
	}
	if c.cfg.Brand != nil {
		v, err := c.cfg.Brand(ctx)
		if err == nil {
			c.brandV, c.brandAt, c.brandOK = v, now, true
			return v
		}
		c.cfg.Log.Warn("read brand for subscription", "err", err)
		if c.brandOK {
			return c.brandV
		}
	}
	v := instance.Defaults()
	v.BrandHead, v.BrandTail = c.cfg.Title, ""
	if c.cfg.Brand == nil { // nothing to re-read: keep it
		c.brandV, c.brandAt, c.brandOK = v, now, true
	}
	return v
}

// title is the subscription name apps show: the settings' title, else the brand, else Config.Title.
func (c *common) title(set *adminv1.SubscriptionSettings, b instance.Settings) string {
	if t := set.GetTitle(); t != "" {
		return t
	}
	if n := b.BrandName(); n != "" {
		return n
	}
	return c.cfg.Title
}

// link is the subscription URL of a token.
func (c *common) link(r *http.Request, token string) string {
	if c.cfg.BaseURL != "" {
		return strings.TrimRight(c.cfg.BaseURL, "/") + "/" + token
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host + c.prefix + "/" + token
}

// pageLang is the language of the page: the first of ru/en in Accept-Language, else the instance language.
func pageLang(r *http.Request, def string) string {
	for _, part := range strings.Split(r.Header.Get("Accept-Language"), ",") {
		tag, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(part)), ";")
		switch {
		case strings.HasPrefix(tag, "ru"):
			return "ru"
		case strings.HasPrefix(tag, "en"):
			return "en"
		}
	}
	if def == "" {
		return "en"
	}
	return def
}

// writePage answers with the user page of v. ancestors is the frame-ancestors source: 'none' for the public
// page, 'self' for the admin preview (which shows the page in a frame of its own origin).
func (c *common) writePage(w http.ResponseWriter, r *http.Request, v access.SubView, link, ancestors string, preview bool) {
	ctx := r.Context()
	set, b := c.settings(ctx), c.brand(ctx)
	c.writeData(w, r, buildPageData(v, link, c.title(set, b), pageLang(r, b.Language), set, b, c.cfg.Now(), preview), ancestors)
}

// writeLocked answers with the page of a token whose password was not entered yet: the brand and the form, nothing
// about the user.
func (c *common) writeLocked(w http.ResponseWriter, r *http.Request, link string) {
	ctx := r.Context()
	set, b := c.settings(ctx), c.brand(ctx)
	c.writeData(w, r, lockedPageData(link, c.title(set, b), pageLang(r, b.Language), b), "'none'")
}

func (c *common) writeData(w http.ResponseWriter, r *http.Request, d pageData, ancestors string) {
	html, err := c.page.render(d)
	if err != nil {
		c.cfg.Log.Error("render user page", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	hd := w.Header()
	hd.Set("Content-Type", "text/html; charset=utf-8")
	hd.Set("Cache-Control", "no-store")
	hd.Set("X-Content-Type-Options", "nosniff")
	hd.Set("Referrer-Policy", "no-referrer")
	hd.Set("X-Robots-Tag", "noindex, nofollow")
	hd.Set("Content-Security-Policy", c.page.csp(ancestors))
	if ancestors == "'self'" {
		hd.Set("X-Frame-Options", "SAMEORIGIN") // replaces the admin's DENY
	} else {
		hd.Set("X-Frame-Options", "DENY")
	}
	w.Write(html)
}

// Previewer supplies a user's data for the admin preview; *access.Service implements it.
type Previewer interface {
	PreviewSubscription(ctx context.Context, userID string) (access.SubView, string, error)
}

// PreviewHandler returns the admin preview of the user page: what the user with this id would see, framed by
// the admin UI (frame-ancestors 'self'). The caller checks the admin session; the handler never sees a token.
func PreviewHandler(src Previewer, cfg Config) func(w http.ResponseWriter, r *http.Request, userID string) {
	cfg.defaults()
	c := newCommon(cfg)
	return func(w http.ResponseWriter, r *http.Request, userID string) {
		if c.page == nil {
			http.Error(w, "the user page is not built (web/dist/sub.html)", http.StatusNotFound)
			return
		}
		v, link, err := src.PreviewSubscription(r.Context(), userID)
		if errors.Is(err, access.ErrUnknownToken) {
			http.NotFound(w, r)
			return
		}
		if err != nil {
			cfg.Log.Error("user page preview", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		c.writePage(w, r, v, link, "'self'", true)
	}
}
