package provision

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

// dohServer answers each query type with the given JSON body (status 200), HTTP 500 for a type it has no answer for, and
// records the requests.
func dohServer(t *testing.T, answers map[string]string, requests *[]*http.Request) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests != nil {
			*requests = append(*requests, r)
		}
		body, ok := answers[r.URL.Query().Get("type")]
		if !ok {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestDoHLookupMergesAAndAAAAAndSkipsCNAME(t *testing.T) {
	var requests []*http.Request
	server := dohServer(t, map[string]string{
		"A": `{"Status":0,"Answer":[{"name":"www.example.com","type":5,"data":"node.example.com."},` +
			`{"name":"node.example.com","type":1,"data":"198.51.100.7"},{"name":"node.example.com","type":1,"data":"198.51.100.8"}]}`,
		"AAAA": `{"Status":0,"Answer":[{"name":"node.example.com","type":28,"data":"2001:db8::7"}]}`,
	}, &requests)
	got, err := dohLookup(context.Background(), server.Client(), server.URL, "www.example.com")
	if err != nil {
		t.Fatal(err)
	}
	want := []netip.Addr{netip.MustParseAddr("198.51.100.7"), netip.MustParseAddr("198.51.100.8"), netip.MustParseAddr("2001:db8::7")}
	if len(got) != len(want) {
		t.Fatalf("addresses = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("addresses = %v, want %v", got, want)
		}
	}
	if len(requests) != 2 {
		t.Fatalf("%d queries, want one A and one AAAA", len(requests))
	}
	for i, typ := range []string{"A", "AAAA"} {
		r := requests[i]
		if r.Method != http.MethodGet || r.URL.Query().Get("name") != "www.example.com" || r.URL.Query().Get("type") != typ ||
			r.Header.Get("Accept") != "application/dns-json" {
			t.Fatalf("query %d = %s %s accept %q", i, r.Method, r.URL.String(), r.Header.Get("Accept"))
		}
	}
}

func TestDoHLookupWithOnlyOneFamily(t *testing.T) {
	server := dohServer(t, map[string]string{
		"A":    `{"Status":0}`,
		"AAAA": `{"Status":0,"Answer":[{"type":28,"data":"2001:db8::9"}]}`,
	}, nil)
	got, err := dohLookup(context.Background(), server.Client(), server.URL, "v6.example.com")
	if err != nil || len(got) != 1 || got[0] != netip.MustParseAddr("2001:db8::9") {
		t.Fatalf("lookup = %v, %v", got, err)
	}
}

func TestDoHLookupNoAddressIsErrNoAddress(t *testing.T) {
	for name, answers := range map[string]map[string]string{
		"NXDOMAIN":      {"A": `{"Status":3}`, "AAAA": `{"Status":3}`},
		"empty answers": {"A": `{"Status":0}`, "AAAA": `{"Status":0,"Answer":[]}`},
		"CNAME only":    {"A": `{"Status":0,"Answer":[{"type":5,"data":"x.example.com."}]}`, "AAAA": `{"Status":0}`},
	} {
		t.Run(name, func(t *testing.T) {
			server := dohServer(t, answers, nil)
			if _, err := dohLookup(context.Background(), server.Client(), server.URL, "gone.example.com"); !errors.Is(err, ErrNoAddress) {
				t.Fatalf("error = %v, want ErrNoAddress", err)
			}
		})
	}
}

// A query that fails is an error, never a short answer: the caller checks every returned address is public.
func TestDoHLookupFailuresAreErrors(t *testing.T) {
	ok := `{"Status":0,"Answer":[{"type":1,"data":"198.51.100.7"}]}`
	for name, answers := range map[string]map[string]string{
		"AAAA query gets HTTP 500": {"A": ok},
		"A query gets HTTP 500":    {"AAAA": `{"Status":0}`},
		"SERVFAIL":                 {"A": ok, "AAAA": `{"Status":2}`},
		"not JSON":                 {"A": ok, "AAAA": `<html>`},
		"IPv6 in an A record":      {"A": `{"Status":0,"Answer":[{"type":1,"data":"2001:db8::1"}]}`, "AAAA": `{"Status":0}`},
		"junk address":             {"A": `{"Status":0,"Answer":[{"type":1,"data":"not-an-ip"}]}`, "AAAA": `{"Status":0}`},
		"oversized answer":         {"A": ok, "AAAA": `{"Status":0,"Comment":"` + strings.Repeat("x", dohMaxBody) + `"}`},
	} {
		t.Run(name, func(t *testing.T) {
			server := dohServer(t, answers, nil)
			addrs, err := dohLookup(context.Background(), server.Client(), server.URL, "node.example.com")
			if err == nil || errors.Is(err, ErrNoAddress) {
				t.Fatalf("lookup = %v, %v; want a failure", addrs, err)
			}
		})
	}
}

func TestDoHLookupStopsWithTheContext(t *testing.T) {
	server := dohServer(t, map[string]string{"A": `{"Status":0}`, "AAAA": `{"Status":0}`}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := dohLookup(ctx, server.Client(), server.URL, "node.example.com"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}
