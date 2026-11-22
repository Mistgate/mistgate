//go:build !js

// Package decoyphotos fetches the photographs of generated decoy sites from Pexels (pexels.com) or Pixabay (pixabay.com) and makes
// them ready for a site: cropped to their slot, cut down, re-encoded as JPEG. Each source has its own hosts, spoken to over https
// only and without following a redirect to any other host. The key is a vault.Redacted. Pexels takes it in the Authorization header
// of the search; Pixabay wants it in the query of the search, so no URL of a request is ever logged or put into an error here.
//
// It is for the panel's server alone: the edge Workers and the node never carry it (sitegen's tests check that).
package decoyphotos

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mistgate/mistgate/internal/panel/httpheaders"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

const (
	apiHost = "api.pexels.com"
	imgHost = "images.pexels.com"

	maxJSON = 1 << 20
	maxFile = 8 << 20
)

// The sources; the values are what the panel's API says.
const (
	SourcePexels  = "pexels"
	SourcePixabay = "pixabay"
)

// The time one search and one file may take (variables only so that a test need not wait for them).
var searchTimeout, fileTimeout = 8 * time.Second, 15 * time.Second

// Codes of the errors the client returns; they are what the panel's API says.
const (
	CodeLimit       = "decoy_photos_limit"       // the source answered 429
	CodeKey         = "decoy_photos_key"         // the source refused the key
	CodeUnavailable = "decoy_photos_unavailable" // anything else: no answer, a bad answer, a file that is too big
)

// Error is a failure of a source or of the way to it. It says a code, never the text of a request or an answer.
type Error struct {
	Code string
	// RetryAfter is how long the source asked to wait (a 429), 0 when it did not say.
	RetryAfter time.Duration
}

func (e *Error) Error() string { return e.Code }

// CodeOf is the code of err, "" when it is not an *Error.
func CodeOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

func unavailable() error { return &Error{Code: CodeUnavailable} }

// Photo is one search result: the files the panel may take and what the source says the picture shows.
type Photo struct {
	ID   int64
	Text string // Pexels' description or Pixabay's tags: what the people filter reads
	// Small and Big are https URLs on the source's file hosts, up to about 640 and about 1280 px wide (Pexels: 940 and 1880).
	// SmallW x SmallH and BigW x BigH are the sizes of those files in pixels, 0 when the source does not say (the file is trusted).
	Small, Big     string
	SmallW, SmallH int
	BigW, BigH     int
}

// fetcher sends the requests of one source: only to its hosts, over https, and without following a redirect anywhere else.
type fetcher struct {
	hc *http.Client
	// resetSecs: a 429 may say how long to wait in X-RateLimit-Reset (seconds), not only in Retry-After (Pixabay).
	resetSecs bool
	// keyOn400: a 400 whose text speaks of the key is a refused key (Pixabay answers a missing or a wrong key so).
	keyOn400 bool
}

// newFetcher makes a fetcher over rt (nil is the default transport; tests pass a fake, so that nothing reaches the network).
func newFetcher(rt http.RoundTripper, hosts ...string) *fetcher {
	if rt == nil {
		rt = http.DefaultTransport
	}
	return &fetcher{hc: &http.Client{Transport: rt, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 || !allowedURL(req.URL, hosts...) {
			return http.ErrUseLastResponse
		}
		return nil
	}}}
}

// allowedURL is true for an https URL with no credentials and no port on one of the hosts.
func allowedURL(u *url.URL, hosts ...string) bool {
	if u == nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return false
	}
	for _, h := range hosts {
		if strings.EqualFold(u.Hostname(), h) {
			return true
		}
	}
	return false
}

// cleanURL is raw when it is an https URL on one of the hosts, "" otherwise.
func cleanURL(raw string, hosts ...string) string {
	u, err := url.Parse(raw)
	if err != nil || !allowedURL(u, hosts...) {
		return ""
	}
	return u.String()
}

// get sends one request and returns the body, at most limit bytes. auth, when not empty, is the Authorization header. An answer
// other than 200 is an *Error. u may carry a key (Pixabay): no error returned here says anything of u.
func (f *fetcher) get(ctx context.Context, u, auth, accept string, limit int64, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, unavailable() // not err itself: net/url puts the URL into it
	}
	req.Header.Set("User-Agent", httpheaders.MistgateUserAgent)
	req.Header.Set("Accept", accept)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := f.hc.Do(req)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return nil, fmt.Errorf("decoyphotos: %w", cerr) // the caller's cancel, kept so that it can be told apart
		}
		return nil, unavailable() // net/http puts the URL into its error: say nothing of it
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		e := &Error{Code: CodeLimit}
		wait := strings.TrimSpace(resp.Header.Get("Retry-After"))
		if wait == "" && f.resetSecs {
			wait = strings.TrimSpace(resp.Header.Get("X-RateLimit-Reset"))
		}
		if n, err := strconv.Atoi(wait); err == nil && n > 0 {
			e.RetryAfter = time.Duration(n) * time.Second
		}
		return nil, e
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, &Error{Code: CodeKey}
	case resp.StatusCode == http.StatusBadRequest && f.keyOn400:
		text, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		if strings.Contains(strings.ToLower(string(text)), "key") {
			return nil, &Error{Code: CodeKey}
		}
		return nil, unavailable()
	case resp.StatusCode != http.StatusOK:
		return nil, unavailable() // a redirect that CheckRedirect refused ends up here as well
	}
	if resp.ContentLength > limit {
		return nil, unavailable()
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return nil, fmt.Errorf("decoyphotos: %w", cerr)
		}
		return nil, unavailable()
	}
	if int64(len(data)) > limit {
		return nil, unavailable()
	}
	return data, nil
}

// Client talks to Pexels.
type Client struct{ f *fetcher }

// New makes a Pexels client over rt (nil is the default transport). A redirect is followed only to the two hosts above, over https.
func New(rt http.RoundTripper) *Client { return &Client{f: newFetcher(rt, apiHost, imgHost)} }

type searchAnswer struct {
	Photos []struct {
		ID  int64  `json:"id"`
		Alt string `json:"alt"`
		Src struct {
			Large  string `json:"large"`
			Large2 string `json:"large2x"`
		} `json:"src"`
	} `json:"photos"`
}

// Search is one page of results for a query, landscape or portrait, perPage of them (1 to 80). Results whose files are not on the
// image host over https are dropped. The key goes in the Authorization header.
func (c *Client) Search(ctx context.Context, key vault.Redacted, query string, portrait bool, page, perPage int) ([]Photo, error) {
	orient := "landscape"
	if portrait {
		orient = "portrait"
	}
	q := url.Values{"query": {query}, "orientation": {orient}, "per_page": {strconv.Itoa(perPage)}, "page": {strconv.Itoa(page)}}
	data, err := c.f.get(ctx, "https://"+apiHost+"/v1/search?"+q.Encode(), key.Reveal(), "application/json", maxJSON, searchTimeout)
	if err != nil {
		return nil, err
	}
	var ans searchAnswer
	if json.Unmarshal(data, &ans) != nil {
		return nil, unavailable()
	}
	var out []Photo
	for _, p := range ans.Photos {
		large, large2 := cleanURL(p.Src.Large, imgHost), cleanURL(p.Src.Large2, imgHost)
		if p.ID == 0 || large == "" {
			continue
		}
		if large2 == "" {
			large2 = large
		}
		out = append(out, Photo{ID: p.ID, Text: p.Alt, Small: large, Big: large2})
	}
	return out, nil
}

// Download is the file at an image URL, at most 8 MiB. The key is not sent.
func (c *Client) Download(ctx context.Context, rawURL string) ([]byte, error) {
	if cleanURL(rawURL, imgHost) == "" {
		return nil, unavailable()
	}
	return c.f.get(ctx, rawURL, "", "image/jpeg,image/png", maxFile, fileTimeout)
}
