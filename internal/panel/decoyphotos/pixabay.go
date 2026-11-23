//go:build !js

package decoyphotos

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	"github.com/mistgate/mistgate/internal/panel/vault"
)

// Pixabay (pixabay.com/api/docs). Search is GET https://pixabay.com/api/ with the key in the query (the one place Pixabay takes it);
// the files it names are on pixabay.com ("/get/...") or on cdn.pixabay.com. Without "full API access" a result has the 640 px
// file (webformatURL) and the 1280 px one (largeImageURL). Pixabay asks that search answers are cached for 24 hours (the Maker
// does) and that files are copied to the site instead of linked (that is what the panel does).
const (
	pixabayAPI  = "pixabay.com"
	pixabayCDN  = "cdn.pixabay.com"
	pixabayMaxQ = 100 // the longest search text Pixabay takes
	pixabayBig  = 1280
)

// PixabayClient talks to Pixabay.
type PixabayClient struct{ f *fetcher }

// NewPixabay makes a Pixabay client over rt (nil is the default transport).
func NewPixabay(rt http.RoundTripper) *PixabayClient {
	f := newFetcher(rt, pixabayAPI, pixabayCDN)
	f.resetSecs, f.keyOn400 = true, true
	return &PixabayClient{f: f}
}

type pixabayAnswer struct {
	Hits []struct {
		ID         int64  `json:"id"`
		Type       string `json:"type"`
		Tags       string `json:"tags"`
		Webformat  string `json:"webformatURL"`
		WebformatW int    `json:"webformatWidth"`
		WebformatH int    `json:"webformatHeight"`
		Large      string `json:"largeImageURL"`
		ImageW     int    `json:"imageWidth"`
		ImageH     int    `json:"imageHeight"`
	} `json:"hits"`
}

// Search is one page of results for a query, horizontal or vertical photos, perPage of them (Pixabay takes 3 to 200). Results whose
// files are not on a Pixabay host over https, or that say no size, are dropped. The key goes in the query of the request.
func (c *PixabayClient) Search(ctx context.Context, key vault.Redacted, query string, portrait bool, page, perPage int) ([]Photo, error) {
	orient := "horizontal"
	if portrait {
		orient = "vertical"
	}
	if r := []rune(query); len(r) > pixabayMaxQ {
		query = string(r[:pixabayMaxQ])
	}
	q := url.Values{"key": {key.Reveal()}, "q": {query}, "image_type": {"photo"}, "orientation": {orient}, "safesearch": {"true"},
		"per_page": {strconv.Itoa(min(max(perPage, 3), 200))}, "page": {strconv.Itoa(page)}}
	data, err := c.f.get(ctx, "https://"+pixabayAPI+"/api/?"+q.Encode(), "", "application/json", maxJSON, searchTimeout)
	if err != nil {
		return nil, err
	}
	var ans pixabayAnswer
	if json.Unmarshal(data, &ans) != nil {
		return nil, unavailable()
	}
	var out []Photo
	for _, h := range ans.Hits {
		small := cleanURL(h.Webformat, pixabayAPI, pixabayCDN)
		if h.ID == 0 || small == "" || h.WebformatW <= 0 || h.WebformatH <= 0 || (h.Type != "" && h.Type != "photo") {
			continue
		}
		p := Photo{ID: h.ID, Text: h.Tags, Small: small, SmallW: h.WebformatW, SmallH: h.WebformatH, Big: small, BigW: h.WebformatW, BigH: h.WebformatH}
		if big := cleanURL(h.Large, pixabayAPI, pixabayCDN); big != "" {
			// the large file is the original cut to 1280 px on its longer side, or the original itself when that is smaller
			p.Big, p.BigW, p.BigH = big, h.WebformatW*2, h.WebformatH*2
			if h.ImageW > 0 && h.ImageH > 0 {
				p.BigW, p.BigH = h.ImageW, h.ImageH
				if long := max(h.ImageW, h.ImageH); long > pixabayBig {
					p.BigW, p.BigH = h.ImageW*pixabayBig/long, h.ImageH*pixabayBig/long
				}
			}
		}
		out = append(out, p)
	}
	return out, nil
}

// Download is the file at a Pixabay file URL, at most 8 MiB. The key is not sent.
func (c *PixabayClient) Download(ctx context.Context, rawURL string) ([]byte, error) {
	if cleanURL(rawURL, pixabayAPI, pixabayCDN) == "" {
		return nil, unavailable()
	}
	return c.f.get(ctx, rawURL, "", "image/jpeg,image/png", maxFile, fileTimeout)
}
