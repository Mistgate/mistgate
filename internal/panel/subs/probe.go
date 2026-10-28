package subs

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/mistgate/mistgate/internal/panel/access"
	"github.com/mistgate/mistgate/internal/panel/clientprobe"
	"github.com/mistgate/mistgate/internal/panel/securitylimit"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

// ProbeStore keeps the reports of the kl!ck probe (clientprobe); *store.Store implements it.
type ProbeStore interface {
	InsertClientProbeReport(ctx context.Context, report string, at time.Time, n store.ClientProbeNetwork, rows []store.ClientProbeResult) (int64, error)
}

// probeBucket is one report per subscription in clientprobe.Every; a second right away is let through, for an app that sent
// one through the VPN and, after an error, again directly.
var probeBucket = securitylimit.Bucket{Name: "subscription-probe", Burst: 2, Refill: clientprobe.Every}

// probeOn says whether the panel takes the reports: the owner's switch, read with the subscription settings.
func (h *handler) probeOn(ctx context.Context) bool {
	return h.cfg.ProbeReports != nil && h.cfg.Settings != nil && h.cfg.Settings.ProbeReports(ctx)
}

// serveProbe takes a report of the kl!ck probe: POST <prefix>/<token>/probe (client_probe.proto). Order: the token (unknown = the
// decoy and a miss, as for the subscription; the page password does not apply, the app sends it the way it fetches the
// subscription), the cross-origin check, the owner's switch (403 probe_off), the budget of the subscription (429 + Retry-After),
// the body (clientprobe.Parse), then the person's own servers: a result whose host and port are not one of them is dropped.
// Nothing here reads or keeps the address the report came from, and nothing is logged but a failed write.
func (h *handler) serveProbe(w http.ResponseWriter, r *http.Request, token, client string, now time.Time) {
	ctx := r.Context()
	if !h.confirmCachedToken(ctx, w, r, token, client) {
		return
	}
	if err := h.cop.Check(r); err != nil {
		jsonError(w, http.StatusForbidden, "cross_origin", "")
		return
	}
	if !h.probeOn(ctx) {
		jsonError(w, http.StatusForbidden, "probe_off", "")
		return
	}
	if d, err := h.cfg.Limiter.Take(ctx, probeBucket, "t:"+token); err != nil {
		tooManyWrites(w, time.Minute) // the shared limiter did not answer: "later", not a free pass
		return
	} else if !d.Allowed {
		tooManyWrites(w, d.RetryAfter)
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, clientprobe.MaxBody))
	if err != nil {
		jsonError(w, http.StatusRequestEntityTooLarge, "too_large", "")
		return
	}
	if mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		jsonError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "")
		return
	}
	rep, err := clientprobe.Parse(raw)
	if err != nil {
		jsonError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	// The servers as the app got them: the Mihomo view has every one (AmneziaWG too), without writing anything.
	format := plugin.FormatURIList
	if h.fsrc != nil {
		format = plugin.FormatMihomo
	}
	v, err := h.fetch(ctx, token, format, access.DeviceHint{}, false, false)
	if errors.Is(err, access.ErrUnknownToken) { // rotated a moment ago
		h.tokens.Delete(token)
		h.miss(ctx, client)
		h.decoy.ServeHTTP(w, r)
		return
	}
	if err != nil { // the access module logged the cause
		jsonError(w, http.StatusInternalServerError, "internal", "")
		return
	}
	if v.Status != access.StatusActive {
		jsonError(w, http.StatusConflict, "user_inactive", v.Status)
		return
	}
	rows := clientprobe.Results(rep, serversOf(v))
	stored, err := h.cfg.ProbeReports.InsertClientProbeReport(ctx, clientprobe.NewID(), now, clientprobe.NetworkOf(rep), rows)
	if err != nil {
		h.cfg.Log.Error("store a kl!ck report", "err", err) // no token, no address
		jsonError(w, http.StatusInternalServerError, "internal", "")
		return
	}
	if stored == 0 && len(rows) > 0 { // switched off since the check above (the cache is a minute old at most)
		jsonError(w, http.StatusForbidden, "probe_off", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"accepted": len(rows)})
}

// serversOf finds the node of a server of the view by the host and port its line carries.
func serversOf(v access.SubView) clientprobe.Match {
	type key struct {
		host string
		port uint16
	}
	nodes := map[key]string{}
	for _, s := range v.Servers {
		if s.Mark != "" {
			continue // a twin carries its exit's node id but dials the entry: the entry's address is not the exit's
		}
		if host, port, ok := serverAddress(s.URI); ok && s.NodeID != "" {
			if _, dup := nodes[key{host, port}]; !dup {
				nodes[key{host, port}] = s.NodeID
			}
		}
	}
	return func(host string, port uint16) (string, bool) {
		id, ok := nodes[key{host, port}]
		return id, ok
	}
}

// serverAddress is the server host (lower case, no brackets) and port of a line: a share link (scheme://…@host:port…) or a Mihomo
// proxy (a YAML list of one entry with server and port).
func serverAddress(line string) (string, uint16, bool) {
	if u, err := url.Parse(line); err == nil && u.Scheme != "" && u.Host != "" {
		p, err := strconv.ParseUint(u.Port(), 10, 16)
		return strings.ToLower(u.Hostname()), uint16(p), err == nil && p > 0
	}
	var proxy []struct {
		Server string `yaml:"server"`
		Port   uint16 `yaml:"port"`
	}
	if yaml.Unmarshal([]byte(line), &proxy) != nil || len(proxy) != 1 || proxy[0].Server == "" || proxy[0].Port == 0 {
		return "", 0, false
	}
	return strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(proxy[0].Server, "["), "]")), proxy[0].Port, true
}
