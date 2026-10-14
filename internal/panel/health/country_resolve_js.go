//go:build js

package health

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"time"
)

// The edge has no system resolver: ask Cloudflare DNS over HTTPS (the JSON API) for the A records.
func defaultResolve(ctx context.Context, host string) ([]netip.Addr, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://cloudflare-dns.com/dns-query?type=A&name="+url.QueryEscape(host), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/dns-json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("dns over https answered " + resp.Status)
	}
	var out struct {
		Answer []struct {
			Type int    `json:"type"`
			Data string `json:"data"`
		} `json:"Answer"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&out); err != nil {
		return nil, err
	}
	var addrs []netip.Addr
	for _, a := range out.Answer {
		if ip, err := netip.ParseAddr(a.Data); err == nil && a.Type == 1 {
			addrs = append(addrs, ip)
		}
	}
	return addrs, nil
}
