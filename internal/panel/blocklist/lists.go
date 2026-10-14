package blocklist

import (
	"bytes"
	"cmp"
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"time"
)

// The public lists (plan 2.1). All of them are fetched from the panel with a conditional request and parsed here; a node's
// address is only ever compared with them in memory and never put into a request.
const (
	// IPSumURL is the RKN registry summed up to /24 subnets (antifilter.download). IPSumMirrorURL is the same list on
	// antifilter.network, tried when the main address fails.
	IPSumURL       = "https://antifilter.download/list/ipsum.lst"
	IPSumMirrorURL = "https://antifilter.network/download/ipsum.lst"
	// CommunityURL is the addresses the community found cut (about 900 prefixes).
	CommunityURL = "https://community.antifilter.download/list/community.lst"
	// CDNURL is the ranges of CDN and hosting providers, "provider,cidr,region" with a header line. Context only.
	CDNURL = "https://raw.githubusercontent.com/123jjck/cdn-ip-ranges/refs/heads/main/all/all.csv"

	userAgent = "mistgate-panel (+https://github.com/mistgate/mistgate)"

	// Size caps: about three times what the lists weigh today (ipsum 314 KiB, community 14 KiB, CDN 670 KiB). A larger answer is
	// refused and the copy in memory is kept.
	maxIPSum     = 1 << 20
	maxCommunity = 128 << 10
	maxCDN       = 2 << 20

	// A list with garbage in it is refused when more than this many lines (plus 1 % of the list) do not parse.
	garbageLines = 16
	maxField     = 64 // provider and region names, bytes
)

// The names of the lists as the screens and the stored results show them.
const (
	ListIPSum     = "antifilter_ipsum"
	ListCommunity = "antifilter_community"
)

// URLs are the addresses of the lists; a zero field takes the default (tests point them at a fake server).
type URLs struct{ IPSum, IPSumMirror, Community, CDN string }

func (u URLs) withDefaults() URLs {
	if u.IPSum == "" {
		u.IPSum = IPSumURL
	}
	if u.IPSumMirror == "" {
		u.IPSumMirror = IPSumMirrorURL
	}
	if u.Community == "" {
		u.Community = CommunityURL
	}
	if u.CDN == "" {
		u.CDN = CDNURL
	}
	return u
}

// ---------------------------------------------------------------------------------------------------
// Prefix sets

// prefix is one IPv4 range of a list; v indexes the metadata of a CDN range (unused by the registry lists).
type prefix struct {
	p netip.Prefix
	v int
}

// prefixSet is a sorted slice of IPv4 prefixes with a longest-prefix lookup: one binary search per prefix length that occurs
// in the set, longest first, so nested ranges (a /16 of a provider with a /24 inside it) resolve to the most specific. IPv6 is
// not stored.
type prefixSet struct {
	e    []prefix
	lens [33]bool
}

func cmpPrefix(e prefix, p netip.Prefix) int {
	if c := e.p.Addr().Compare(p.Addr()); c != 0 {
		return c
	}
	return cmp.Compare(e.p.Bits(), p.Bits())
}

func newPrefixSet(e []prefix) prefixSet {
	slices.SortStableFunc(e, func(a, b prefix) int { return cmpPrefix(a, b.p) })
	e = slices.CompactFunc(e, func(a, b prefix) bool { return a.p == b.p }) // the first of equal prefixes stays
	s := prefixSet{e: e}
	for _, x := range e {
		s.lens[x.p.Bits()] = true
	}
	return s
}

// longest is the most specific prefix that holds a. IPv6 and the zero Addr match nothing.
func (s prefixSet) longest(a netip.Addr) (prefix, bool) {
	if !a.Is4() {
		return prefix{}, false
	}
	for bits := 32; bits >= 0; bits-- {
		if !s.lens[bits] {
			continue
		}
		p, err := a.Prefix(bits)
		if err != nil {
			continue
		}
		if i, ok := slices.BinarySearchFunc(s.e, p, cmpPrefix); ok {
			return s.e[i], true
		}
	}
	return prefix{}, false
}

// ---------------------------------------------------------------------------------------------------
// Parsers

// tolerated says whether a list with bad lines out of total is still usable. A list with no usable line never is: an HTML
// error page or an empty answer must not replace a good copy.
func tolerated(good, bad, total int) bool {
	return good > 0 && bad <= garbageLines+total/100
}

// parseCIDRs reads one prefix or bare address per line (the antifilter lists). Blank lines and lines starting with # are
// skipped, IPv6 is ignored, host bits are cleared. A line that is none of these is garbage.
func parseCIDRs(body []byte) (prefixSet, []cdnMeta, error) {
	var e []prefix
	var bad, total int
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		total++
		p, ok := parseRange(line)
		switch {
		case !ok:
			bad++
		case p.Addr().Is4():
			e = append(e, prefix{p: p})
		}
	}
	if !tolerated(len(e), bad, total) {
		return prefixSet{}, nil, fmt.Errorf("not a list of prefixes: %d of %d lines are garbage, %d prefixes", bad, total, len(e))
	}
	return newPrefixSet(e), nil, nil
}

// parseRange reads "a.b.c.d/n", "2001:db8::/32" or a bare address; the result is masked.
func parseRange(s string) (netip.Prefix, bool) {
	if len(s) > 64 {
		return netip.Prefix{}, false
	}
	if !strings.Contains(s, "/") {
		a, err := netip.ParseAddr(s)
		if err != nil {
			return netip.Prefix{}, false
		}
		a = a.Unmap()
		return netip.PrefixFrom(a, a.BitLen()), true
	}
	p, err := netip.ParsePrefix(s)
	if err != nil {
		return netip.Prefix{}, false
	}
	return p.Masked(), true
}

// cdnMeta is the provider and region of a CDN range.
type cdnMeta struct{ provider, region string }

// parseCDN reads "provider,cidr,region" lines of the CDN list; the header line ("provider,cidr,region") is skipped.
func parseCDN(body []byte) (prefixSet, []cdnMeta, error) {
	r := csv.NewReader(bytes.NewReader(body))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true
	var e []prefix
	var meta []cdnMeta
	index := map[cdnMeta]int{}
	var bad, total int
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			total++
			bad++
			continue
		}
		if len(rec) == 0 || (len(rec) == 1 && strings.TrimSpace(rec[0]) == "") {
			continue
		}
		if total == 0 && strings.EqualFold(strings.TrimSpace(rec[0]), "provider") {
			total++ // the header
			continue
		}
		total++
		if len(rec) < 2 {
			bad++
			continue
		}
		p, ok := parseRange(strings.TrimSpace(rec[1]))
		if !ok {
			bad++
			continue
		}
		if !p.Addr().Is4() {
			continue
		}
		m := cdnMeta{provider: clean(rec[0])}
		if len(rec) > 2 {
			m.region = clean(rec[2])
		}
		if m.provider == "" {
			bad++
			continue
		}
		i, seen := index[m]
		if !seen {
			i = len(meta)
			index[m] = i
			meta = append(meta, m)
		}
		e = append(e, prefix{p: p, v: i})
	}
	if !tolerated(len(e), bad, total) {
		return prefixSet{}, nil, fmt.Errorf("not a CDN list: %d of %d lines are garbage, %d ranges", bad, total, len(e))
	}
	return newPrefixSet(e), meta, nil
}

// clean trims a name, drops control characters and cuts it to maxField bytes.
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(s))
	if len(s) > maxField {
		s = strings.ToValidUTF8(s[:maxField], "")
	}
	return s
}

// ---------------------------------------------------------------------------------------------------
// Downloads

// list is one downloaded list and the validators of the copy in memory. The validators belong to url: a copy from the mirror
// is not asked for with the main address's ETag.
type list struct {
	max   int64
	urls  []string
	parse func([]byte) (prefixSet, []cdnMeta, error)

	url, etag, modified string
	date, checked       time.Time // the list's own date (Last-Modified, else when it was downloaded); the last successful request
	set                 prefixSet
	meta                []cdnMeta
	loaded              bool
}

// errTooLarge is an answer over the size cap.
var errTooLarge = errors.New("the list is larger than the size cap")

// refresh asks the list's addresses in order until one answers: 304 keeps the copy (and only moves checked), 200 is parsed
// and replaces it. A failure of every address leaves the copy as it was. usedAlt says an address other than the first served.
func (l *list) refresh(ctx context.Context, hc *http.Client, now time.Time) (usedAlt bool, err error) {
	var errs []error
	for i, u := range l.urls {
		req, rerr := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if rerr != nil {
			errs = append(errs, rerr)
			continue
		}
		req.Header.Set("User-Agent", userAgent)
		if l.loaded && l.url == u {
			if l.etag != "" {
				req.Header.Set("If-None-Match", l.etag)
			}
			if l.modified != "" {
				req.Header.Set("If-Modified-Since", l.modified)
			}
		}
		resp, derr := hc.Do(req)
		if derr != nil {
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			errs = append(errs, errors.New("request failed")) // net/http puts the URL in its error: say nothing of it
			continue
		}
		body, rerr := readCapped(resp, l.max)
		switch {
		case rerr != nil:
			errs = append(errs, rerr)
			if ctx.Err() != nil {
				return false, ctx.Err()
			}
			continue
		case resp.StatusCode == http.StatusNotModified && l.loaded && l.url == u:
			l.checked = now
			return i > 0, nil
		case resp.StatusCode != http.StatusOK:
			errs = append(errs, fmt.Errorf("HTTP %d", resp.StatusCode))
			continue
		}
		set, meta, perr := l.parse(body)
		if perr != nil {
			errs = append(errs, perr)
			continue
		}
		date := now
		if t, terr := http.ParseTime(resp.Header.Get("Last-Modified")); terr == nil {
			date = t
		}
		l.url, l.etag, l.modified = u, resp.Header.Get("ETag"), resp.Header.Get("Last-Modified")
		l.date, l.checked, l.set, l.meta, l.loaded = date, now, set, meta, true
		return i > 0, nil
	}
	return false, errors.Join(errs...)
}

// readCapped reads a response body of at most max bytes and closes it. A 304 has none.
func readCapped(resp *http.Response, max int64) ([]byte, error) {
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, errors.New("reading the list failed")
	}
	if int64(len(b)) > max {
		return nil, errTooLarge
	}
	return b, nil
}

// ---------------------------------------------------------------------------------------------------
// Lookup

// listing is what the lists say of one address.
type listing struct {
	inRKN       bool
	subnet      netip.Prefix // the most specific registry prefix that holds the address
	listedBy    []string
	cdnProvider string
	cdnRegion   string
}

// match looks an address up in the registry lists and the CDN list.
func match(ipsum, community, cdn *list, a netip.Addr) listing {
	var out listing
	for _, l := range []struct {
		name string
		l    *list
	}{{ListIPSum, ipsum}, {ListCommunity, community}} {
		if !l.l.loaded {
			continue
		}
		if p, ok := l.l.set.longest(a); ok {
			out.inRKN = true
			out.listedBy = append(out.listedBy, l.name)
			if !out.subnet.IsValid() || p.p.Bits() > out.subnet.Bits() {
				out.subnet = p.p
			}
		}
	}
	if cdn.loaded {
		if p, ok := cdn.set.longest(a); ok {
			out.cdnProvider, out.cdnRegion = cdn.meta[p.v].provider, cdn.meta[p.v].region
		}
	}
	return out
}
