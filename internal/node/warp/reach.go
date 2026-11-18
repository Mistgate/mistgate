package warp

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/mistgate/mistgate/internal/plugin"
)

// Reach asks, through the WARP tunnel, whether some sites show their block page to Cloudflare's WARP addresses (agent.proto
// "WARP REACH"). Some sites refuse a WARP colo and not another, while the tunnel itself is perfectly healthy, so the health
// check cannot see it. The domains are the owner's; every request goes out through probeDialContext, which resolves with the
// node's own resolvers and refuses any address that is private, shared, link-local, loopback or the node's own, so a name
// that points inside the node's network reaches nothing.

const (
	reachBodyLimit = 64 << 10
	reachTimeout   = 12 * time.Second
	reachUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"
)

// Verdicts of one domain.
const (
	ReachOK          = "ok"          // the site answered with something that is not a block page
	ReachBlocked     = "blocked"     // the answer is a block page
	ReachUnreachable = "unreachable" // no answer: DNS, connect, TLS or the time ran out
	ReachError       = "error"       // an answer that is neither: a server error, a rate limit, a 403 that no table row knows, a bad name
)

// Errors of Reach: the agent turns each into the word of its CommandResult.
var (
	ErrReachBusy    = errors.New("warp reach: a run is going")
	ErrReachTooSoon = errors.New("warp reach: the last run was less than a health interval ago")
	ErrReachBadName = errors.New("warp reach: not a domain name")
)

// ReachItem is the answer for one domain: the verdict, the HTTP status of the page that decided it (0 = none) and how long
// the request took.
type ReachItem struct {
	Verdict string
	Code    int
	Ms      uint32
}

// ReachReport is one run: the colo the trace says the tunnel exits at now ("" when the trace failed) and one item per domain,
// in the order asked.
type ReachReport struct {
	Colo  string
	Items []ReachItem
}

// Reach runs the check. The tunnel must be up; a run is refused while another goes and within one health interval of the last
// one, so the check never costs more than the health check does.
func (m *Manager) Reach(ctx context.Context, domains []string) (ReachReport, error) {
	if len(domains) == 0 || len(domains) > plugin.MaxWarpReachDomains {
		return ReachReport{}, ErrReachBadName
	}
	for _, d := range domains {
		if !plugin.ValidWarpReachDomain(d) {
			return ReachReport{}, ErrReachBadName
		}
	}
	if !m.reachMu.TryLock() {
		return ReachReport{}, ErrReachBusy
	}
	defer m.reachMu.Unlock()
	m.mu.Lock()
	up, now := m.spec != nil && m.spec.Enabled && m.state == StateUp, m.now()
	tooSoon := !m.reachAt.IsZero() && now.Sub(m.reachAt) < m.o.Interval
	if up && !tooSoon {
		m.reachAt = now
	}
	m.mu.Unlock()
	switch {
	case !up:
		return ReachReport{}, ErrNotActive
	case tooSoon:
		return ReachReport{}, ErrReachTooSoon
	}

	rep := ReachReport{Items: make([]ReachItem, len(domains))}
	var wg sync.WaitGroup
	wg.Add(len(domains) + 1)
	go func() { // the colo of this very run: the verdicts below are about that exit
		defer wg.Done()
		if _, colo, err := m.probe.A(ctx); err == nil {
			rep.Colo = colo
		}
	}()
	for i, d := range domains {
		go func() {
			defer wg.Done()
			rep.Items[i] = m.reachOne(ctx, d)
		}()
	}
	wg.Wait()
	return rep, nil
}

func (m *Manager) reachOne(ctx context.Context, domain string) ReachItem {
	ctx, cancel := context.WithTimeout(ctx, reachTimeout)
	defer cancel()
	t0 := time.Now() // real clock: a duration
	status, header, body, err := m.reachGet(ctx, domain)
	ms := uint32(time.Since(t0) / time.Millisecond)
	if err != nil {
		return ReachItem{Verdict: ReachUnreachable, Ms: ms}
	}
	return ReachItem{Verdict: classifyReach(status, header, body), Code: status, Ms: ms}
}

// reachGet fetches https://domain/ with one redirect at most (a site that sends "example.com" to "www.example.com" is the
// usual case) and at most reachBodyLimit bytes of the page. The dial is the guarded one, also for the redirect's host.
func (m *Manager) reachGet(ctx context.Context, domain string) (status int, header http.Header, body []byte, err error) {
	dial := m.reachDial
	if dial == nil {
		dial = m.probeDialContext
	}
	tlsConfig := m.reachTLS
	if tlsConfig == nil {
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	client := &http.Client{
		Transport: &http.Transport{
			Proxy:                 nil,
			DisableKeepAlives:     true,
			DialContext:           dial,
			TLSClientConfig:       tlsConfig,
			TLSHandshakeTimeout:   reachTimeout,
			ResponseHeaderTimeout: reachTimeout,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 1 || req.URL.Scheme != "https" {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+domain+"/", nil)
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("User-Agent", reachUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml;q=0.9,*/*;q=0.8")
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	body, _ = io.ReadAll(io.LimitReader(resp.Body, reachBodyLimit)) // a cut-off page still has its status and its head
	return resp.StatusCode, resp.Header, body, nil
}

// A block page is told by its words, and only on a 403: a 451 is blocked by its status alone, and any other status is judged
// by the status. Every rule needs ALL of its patterns, so a page that merely talks about a block (a news article about error
// 1020) is not one, and a 403 that no rule knows is "error": the table names the pages seen in the wild, and a bare 403 is as
// likely to be a site's own bot filter as WARP's colo. Add a row when a new page turns up.
var reachBlockRules = []struct {
	name string
	all  []*regexp.Regexp
}{
	// Cloudflare's own error pages: 1006-1008 (IP banned), 1009 (country or region), 1010 (browser signature), 1012, 1020 (a
	// firewall rule), and the "Sorry, you have been blocked" page of a managed rule.
	{"cloudflare_error", []*regexp.Regexp{
		regexp.MustCompile(`(?i)cloudflare`),
		regexp.MustCompile(`(?i)(error( code)?[^a-z0-9]{0,8}|cf-error-code[^0-9]{0,8}|<span>)10(06|07|08|09|10|12|20)\b`),
	}},
	{"cloudflare_blocked", []*regexp.Regexp{
		regexp.MustCompile(`(?i)cloudflare`),
		regexp.MustCompile(`(?i)(sorry, you have been blocked|you are unable to access|used cloudflare to restrict access|has banned (your|the country))`),
	}},
	// Akamai's edge: "Access Denied ... You don't have permission to access ... Reference #18.xxxx".
	{"akamai", []*regexp.Regexp{
		regexp.MustCompile(`(?i)access denied`),
		regexp.MustCompile(`(?i)you don't have permission to access`),
		regexp.MustCompile(`(?i)reference(&#32;|\s)(#|&#35;)\d`),
	}},
	// CloudFront's geographic restriction.
	{"cloudfront_geo", []*regexp.Regexp{
		regexp.MustCompile(`(?i)configured to block access from your (country|location)`),
	}},
	// A site's own words about the place the visitor is in.
	{"region_text", []*regexp.Regexp{
		regexp.MustCompile(`(?i)(not available|unavailable|blocked|restricted) (in|from) your (country|region|location)`),
	}},
}

// classifyReach is the verdict for an answer. body is at most reachBodyLimit bytes.
func classifyReach(status int, header http.Header, body []byte) string {
	switch {
	case status == http.StatusUnavailableForLegalReasons:
		return ReachBlocked
	case status == http.StatusForbidden:
		if strings.EqualFold(header.Get("CF-Mitigated"), "challenge") {
			return ReachError // a challenge asks for a person, it does not refuse one
		}
		for _, rule := range reachBlockRules {
			if matchesAll(rule.all, body) {
				return ReachBlocked
			}
		}
		return ReachError
	case status == http.StatusTooManyRequests || status >= 500 || status < 100:
		return ReachError
	}
	return ReachOK
}

func matchesAll(res []*regexp.Regexp, body []byte) bool {
	for _, re := range res {
		if !re.Match(body) {
			return false
		}
	}
	return true
}
