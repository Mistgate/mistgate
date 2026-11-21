//go:build !js

// Package decoyphotos fetches the photographs of generated decoy sites from Pexels (pexels.com) and makes them ready for a site:
// cropped to their slot, cut down, re-encoded as JPEG. It speaks to two hosts only, over https and without following a redirect
// to any other host. The key is a vault.Redacted and is sent in the Authorization header of the search and nowhere else.
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

// The time one search and one file may take (variables only so that a test need not wait for them).
var searchTimeout, fileTimeout = 8 * time.Second, 15 * time.Second

// Codes of the errors the client returns; they are what the panel's API says.
const (
	CodeLimit       = "decoy_photos_limit"       // Pexels answered 429
	CodeKey         = "decoy_photos_key"         // Pexels refused the key (401 or 403)
	CodeUnavailable = "decoy_photos_unavailable" // anything else: no answer, a bad answer, a file that is too big
)

// Error is a failure of Pexels or of the way to it. It says a code, never the text of a request or an answer.
type Error struct {
	Code string
	// RetryAfter is how long Pexels asked to wait (a 429), 0 when it did not say.
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

// Photo is one search result: the files the panel may take and what Pexels says the picture shows.
type Photo struct {
	ID            int64
	Alt           string
	Large, Large2 string // https URLs on the image host: about 940 and 1880 px wide
	Width, Height int
}

// Client talks to Pexels.
type Client struct {
	hc *http.Client
}

// New makes a client. rt carries the requests; nil is the default transport (tests pass a fake, so that nothing reaches the
// network). A redirect is followed only to the two hosts above and only over https.
func New(rt http.RoundTripper) *Client {
	if rt == nil {
		rt = http.DefaultTransport
	}
	return &Client{hc: &http.Client{Transport: rt, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 || !allowedURL(req.URL, apiHost, imgHost) {
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

// get sends one request and returns the body, at most limit bytes. An answer other than 200 is an *Error.
func (c *Client) get(ctx context.Context, u string, key vault.Redacted, accept string, limit int64, timeout time.Duration) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, unavailable()
	}
	req.Header.Set("User-Agent", httpheaders.MistgateUserAgent)
	req.Header.Set("Accept", accept)
	if key != "" {
		req.Header.Set("Authorization", key.Reveal())
	}
	resp, err := c.hc.Do(req)
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
		if n, err := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); err == nil && n > 0 {
			e.RetryAfter = time.Duration(n) * time.Second
		}
		return nil, e
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, &Error{Code: CodeKey}
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

type searchAnswer struct {
	Photos []struct {
		ID     int64  `json:"id"`
		Width  int    `json:"width"`
		Height int    `json:"height"`
		Alt    string `json:"alt"`
		Src    struct {
			Large  string `json:"large"`
			Large2 string `json:"large2x"`
		} `json:"src"`
	} `json:"photos"`
}

// Search is one page of results for a query, landscape or portrait, perPage of them (1 to 80). Results whose files are not on the
// image host over https are dropped.
func (c *Client) Search(ctx context.Context, key vault.Redacted, query string, portrait bool, page, perPage int) ([]Photo, error) {
	orient := "landscape"
	if portrait {
		orient = "portrait"
	}
	q := url.Values{"query": {query}, "orientation": {orient}, "per_page": {strconv.Itoa(perPage)}, "page": {strconv.Itoa(page)}}
	data, err := c.get(ctx, "https://"+apiHost+"/v1/search?"+q.Encode(), key, "application/json", maxJSON, searchTimeout)
	if err != nil {
		return nil, err
	}
	var ans searchAnswer
	if json.Unmarshal(data, &ans) != nil {
		return nil, unavailable()
	}
	var out []Photo
	for _, p := range ans.Photos {
		large, large2 := cleanImageURL(p.Src.Large), cleanImageURL(p.Src.Large2)
		if p.ID == 0 || large == "" {
			continue
		}
		if large2 == "" {
			large2 = large
		}
		out = append(out, Photo{ID: p.ID, Alt: p.Alt, Large: large, Large2: large2, Width: p.Width, Height: p.Height})
	}
	return out, nil
}

// cleanImageURL is raw when it is an https URL on the image host, "" otherwise.
func cleanImageURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || !allowedURL(u, imgHost) {
		return ""
	}
	return u.String()
}

// Download is the file at an image URL, at most 8 MiB. The key is not sent.
func (c *Client) Download(ctx context.Context, rawURL string) ([]byte, error) {
	if cleanImageURL(rawURL) == "" {
		return nil, unavailable()
	}
	return c.get(ctx, rawURL, "", "image/jpeg,image/png", maxFile, fileTimeout)
}
