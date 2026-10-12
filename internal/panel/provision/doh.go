package provision

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"net/url"
)

const (
	dohEndpoint = "https://cloudflare-dns.com/dns-query"
	dohMaxBody  = 64 << 10
	dnsTypeA    = 1
	dnsTypeAAAA = 28
	dnsNXDomain = 3
)

// DoHLookup resolves host through Cloudflare's DNS-over-HTTPS JSON API, for a runtime with no system resolver (the edge
// edition). It asks for A and AAAA records and returns every address of both; CNAME records in an answer are skipped
// (the resolver has followed them). A name that does not exist, or has no address, is ErrNoAddress. A failure of either
// query is an error rather than a partial answer: the caller checks that every address is public, and an AAAA record
// that failed to arrive could be the one that is not. A nil client uses http.DefaultClient.
func DoHLookup(client *http.Client) func(ctx context.Context, host string) ([]netip.Addr, error) {
	return func(ctx context.Context, host string) ([]netip.Addr, error) {
		return dohLookup(ctx, client, dohEndpoint, host)
	}
}

func dohLookup(ctx context.Context, client *http.Client, endpoint, host string) ([]netip.Addr, error) {
	if client == nil {
		client = http.DefaultClient
	}
	var addrs []netip.Addr
	for _, q := range []struct {
		name string
		code int
	}{{"A", dnsTypeA}, {"AAAA", dnsTypeAAAA}} {
		got, err := dohQuery(ctx, client, endpoint, host, q.name, q.code)
		if err != nil {
			return nil, err
		}
		addrs = append(addrs, got...)
	}
	if len(addrs) == 0 {
		return nil, ErrNoAddress
	}
	return addrs, nil
}

func dohQuery(ctx context.Context, client *http.Client, endpoint, host, typeName string, typeCode int) ([]netip.Addr, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+url.Values{"name": {host}, "type": {typeName}}.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/dns-json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("provision: DNS-over-HTTPS %s query: %w", typeName, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("provision: DNS-over-HTTPS %s query returned HTTP %d", typeName, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, dohMaxBody+1))
	if err != nil {
		return nil, fmt.Errorf("provision: DNS-over-HTTPS %s query: %w", typeName, err)
	}
	if len(body) > dohMaxBody {
		return nil, errors.New("provision: DNS-over-HTTPS answer is too large")
	}
	var answer struct {
		Status int
		Answer []struct {
			Type int
			Data string
		}
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return nil, errors.New("provision: DNS-over-HTTPS answer is not valid JSON")
	}
	switch answer.Status {
	case 0:
	case dnsNXDomain:
		return nil, nil
	default:
		return nil, fmt.Errorf("provision: DNS-over-HTTPS %s query failed with DNS status %d", typeName, answer.Status)
	}
	var out []netip.Addr
	for _, record := range answer.Answer {
		if record.Type != typeCode {
			continue
		}
		addr, err := netip.ParseAddr(record.Data)
		if err != nil || typeCode == dnsTypeA && !addr.Is4() || typeCode == dnsTypeAAAA && !addr.Is6() {
			return nil, fmt.Errorf("provision: DNS-over-HTTPS %s answer holds a malformed address", typeName)
		}
		out = append(out, addr)
	}
	return out, nil
}
