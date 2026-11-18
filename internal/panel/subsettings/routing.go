package subsettings

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// The owner's own routing profiles for Happ and INCY (SubscriptionSettings.routing_profiles). Both apps take the same profile: a
// JSON object of known fields, which they get as a link `<scheme>://routing/(add|onadd)/<base64 of the JSON>` in the `routing`
// header or on a line of the subscription body (Happ: happ.su/main/dev-docs/routing; INCY: docs.incy.cc/en/routing, read
// 2026-11-18; neither says where the app stops reading).
//
// What a save keeps. The owner pastes a link, a bare base64 or the JSON; the panel reads it into RoutingDoc, which holds only the
// fields the two apps document, with the types the docs show, and stores the JSON it writes out of that, never the pasted text.
// A field it does not know is dropped and named to the owner (an app may add one; it cannot get through unread, and a profile of
// someone else's tool shows what was left out). A value of a known field that has the wrong type, or an address that is not
// https where the app downloads a file, is a refusal that says which one. What the owner's text cannot do is put any other text
// in the answer: the header is the fixed prefix and base64.
//
// Size. A profile is at most MaxRoutingProfileBytes of JSON as stored. The link goes in the `routing` header while it is at
// most MaxRoutingHeaderValue bytes: a reverse proxy buffers the headers of an answer in 4 or 8 KiB, the panel's own take about
// 1 KiB, and the owner's own response headers may take 4096 more; what is left of 8 KiB is 3 KiB. A longer link goes on a line of
// the body, which both apps read (the docs of both show a routing line beside the servers).
const (
	MaxRoutingProfiles     = 20
	MaxRoutingProfileBytes = 32 << 10
	MaxRoutingInput        = 128 << 10
	MaxRoutingName         = 60
	MaxRoutingHeaderValue  = 3000

	maxRoutingItem     = 255
	maxRoutingHosts    = 200
	maxRoutingDropped  = 10
	routingDefaultName = "Routing"
)

// RoutingDoc is the profile as the panel writes it: the fields of the docs, in their order. The six rule lists are always
// written (an empty list is `[]`), the rest only when the profile has them.
type RoutingDoc struct {
	Name              string            `json:"Name"`
	GlobalProxy       string            `json:"GlobalProxy,omitempty"`
	LastUpdated       string            `json:"LastUpdated,omitempty"`
	RemoteDNSType     string            `json:"RemoteDNSType,omitempty"`
	RemoteDNSDomain   string            `json:"RemoteDNSDomain,omitempty"`
	RemoteDNSIP       string            `json:"RemoteDNSIP,omitempty"`
	RemoteDns         string            `json:"RemoteDns,omitempty"`
	DomesticDNSType   string            `json:"DomesticDNSType,omitempty"`
	DomesticDNSDomain string            `json:"DomesticDNSDomain,omitempty"`
	DomesticDNSIP     string            `json:"DomesticDNSIP,omitempty"`
	DomesticDns       string            `json:"DomesticDns,omitempty"`
	DnsHosts          map[string]string `json:"DnsHosts,omitempty"`
	FakeDNS           string            `json:"FakeDNS,omitempty"`
	DirectSites       []string          `json:"DirectSites"`
	DirectIp          []string          `json:"DirectIp"`
	ProxySites        []string          `json:"ProxySites"`
	ProxyIp           []string          `json:"ProxyIp"`
	BlockSites        []string          `json:"BlockSites"`
	BlockIp           []string          `json:"BlockIp"`
	Geoipurl          string            `json:"Geoipurl,omitempty"`
	Geositeurl        string            `json:"Geositeurl,omitempty"`
	DomainStrategy    string            `json:"DomainStrategy,omitempty"`
	UseChunkFiles     *bool             `json:"useChunkFiles,omitempty"`
}

// RoutingDNS are the DNS fields a person's DNS preset puts into the owner's profile (dns.ProfileDNS, without the import).
type RoutingDNS struct {
	RemoteType, RemoteDomain, RemoteIP, DomesticType, DomesticDomain, DomesticIP string
	Hosts                                                                        map[string]string
}

// WithDNS is the profile with the resolvers of a person's preset in place of its own (and without the older aliases of the
// address, which would contradict them); the host pins of the preset are added to the profile's, the preset's winning. The rules
// stay the owner's. A zero x (no preset the app can take) leaves the profile as it is.
func (d RoutingDoc) WithDNS(x RoutingDNS) RoutingDoc {
	if x.RemoteType == "" && x.DomesticType == "" {
		return d
	}
	d.RemoteDNSType, d.RemoteDNSDomain, d.RemoteDNSIP, d.RemoteDns = x.RemoteType, x.RemoteDomain, x.RemoteIP, ""
	d.DomesticDNSType, d.DomesticDNSDomain, d.DomesticDNSIP, d.DomesticDns = x.DomesticType, x.DomesticDomain, x.DomesticIP, ""
	if len(x.Hosts) > 0 {
		hosts := make(map[string]string, len(d.DnsHosts)+len(x.Hosts))
		for k, v := range d.DnsHosts {
			hosts[k] = v
		}
		for k, v := range x.Hosts {
			hosts[k] = v
		}
		d.DnsHosts = hosts
	}
	return d
}

// Link is the link of the profile for an app: scheme "happ" or "incy", `onadd` (add and turn on) or `add` (add only). Standard
// padded base64, as in the examples of both apps. The scheme is the caller's constant, never data.
func (d RoutingDoc) Link(scheme string, onlyAdd bool) string {
	verb := "onadd"
	if onlyAdd {
		verb = "add"
	}
	return scheme + "://routing/" + verb + "/" + base64.StdEncoding.EncodeToString([]byte(d.Canonical()))
}

// Canonical is the JSON the panel stores and sends for the profile.
func (d RoutingDoc) Canonical() string {
	b, _ := json.Marshal(d) // strings, lists of strings and a map of strings cannot fail to encode
	return string(b)
}

// RoutingParsed is what an input gives: the profile, the mode a link said ("add", "onadd", "" for a bare profile) and the names
// of the fields that were left out.
type RoutingParsed struct {
	Doc     RoutingDoc
	Mode    string
	Dropped []string
}

var routingKnown = []string{"Name", "GlobalProxy", "LastUpdated", "RemoteDNSType", "RemoteDNSDomain", "RemoteDNSIP", "RemoteDns",
	"DomesticDNSType", "DomesticDNSDomain", "DomesticDNSIP", "DomesticDns", "DnsHosts", "FakeDNS", "DirectSites", "DirectIp",
	"ProxySites", "ProxyIp", "BlockSites", "BlockIp", "Geoipurl", "Geositeurl", "DomainStrategy", "useChunkFiles"}

// ParseRoutingInput reads what the owner pasted: an app link (`happ://routing/onadd/<base64>`, `incy://…`, `://routing/add/…`:
// the scheme may be anything), a bare base64 of the JSON, or the JSON itself. The errors say what is wrong with the text.
func ParseRoutingInput(in string) (RoutingParsed, error) {
	in = strings.TrimSpace(in)
	if in == "" {
		return RoutingParsed{}, invalid("the profile is empty")
	}
	if len(in) > MaxRoutingInput {
		return RoutingParsed{}, invalid("the text is %d bytes, at most %d", len(in), MaxRoutingInput)
	}
	var out RoutingParsed
	var raw []byte
	switch {
	case in[0] == '{':
		raw = []byte(in)
	case strings.Contains(in, "://"):
		payload, mode, err := linkPayload(in)
		if err != nil {
			return RoutingParsed{}, err
		}
		out.Mode = mode
		if raw, err = decodeBase64Text(payload); err != nil {
			return RoutingParsed{}, err
		}
	default:
		var err error
		if raw, err = decodeBase64Text(in); err != nil {
			return RoutingParsed{}, err
		}
	}
	raw = bytes.TrimPrefix(bytes.TrimSpace(raw), []byte("\xef\xbb\xbf"))
	if len(raw) == 0 || raw[0] != '{' {
		return RoutingParsed{}, invalid("the profile is not a JSON object (it must start with {)")
	}
	doc, dropped, err := readRoutingJSON(raw)
	if err != nil {
		return RoutingParsed{}, err
	}
	out.Doc, out.Dropped = doc, dropped
	if n := len(out.Doc.Canonical()); n > MaxRoutingProfileBytes {
		return RoutingParsed{}, invalid("the profile is %d bytes, at most %d", n, MaxRoutingProfileBytes)
	}
	return out, nil
}

// linkPayload takes an app link apart: the scheme is any name or none (`://routing/onadd/…` as the INCY header has it), the path
// is `routing/<add|onadd>/<payload>`.
func linkPayload(in string) (payload, mode string, err error) {
	scheme, rest, _ := strings.Cut(in, "://")
	if len(scheme) > 32 || strings.ContainsFunc(scheme, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '+' || r == '-' || r == '.')
	}) {
		return "", "", invalid("this is not an app link: the part before :// is not a scheme")
	}
	if i := strings.IndexAny(rest, "?#"); i >= 0 {
		rest = rest[:i]
	}
	parts := strings.SplitN(strings.Trim(rest, "/"), "/", 3)
	switch {
	case len(parts) > 0 && strings.EqualFold(parts[0], "autorouting"):
		return "", "", invalid("this is an auto-updating link (autorouting): it holds an address of a profile, not a profile; paste the routing link or the JSON")
	case len(parts) > 1 && strings.EqualFold(parts[0], "routing") && strings.EqualFold(parts[1], "off"):
		return "", "", invalid("this link only turns routing off; paste a link with a profile (routing/add or routing/onadd)")
	case len(parts) != 3 || !strings.EqualFold(parts[0], "routing") || (!strings.EqualFold(parts[1], "add") && !strings.EqualFold(parts[1], "onadd")):
		return "", "", invalid("this is not a routing link: it must look like happ://routing/onadd/<profile>")
	}
	p, perr := url.PathUnescape(parts[2])
	if perr != nil {
		return "", "", invalid("the profile in the link is not valid: %v", perr)
	}
	return p, strings.ToLower(parts[1]), nil
}

// decodeBase64Text decodes base64 as the apps and 3x-ui write it: standard or URL alphabet, with or without padding, line breaks
// and spaces ignored.
func decodeBase64Text(s string) ([]byte, error) {
	s = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimRight(s, "=")
	enc := base64.RawStdEncoding
	if strings.ContainsAny(s, "-_") {
		enc = base64.RawURLEncoding
	}
	b, err := enc.DecodeString(s)
	if err != nil {
		return nil, invalid("this is not a link, JSON or base64 text of a profile")
	}
	return b, nil
}

// readRoutingJSON reads the object into a profile: the known fields, with their types checked; the rest is named and dropped.
func readRoutingJSON(raw []byte) (RoutingDoc, []string, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	var top map[string]json.RawMessage
	if err := dec.Decode(&top); err != nil {
		return RoutingDoc{}, nil, invalid("the profile is not valid JSON: %v", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return RoutingDoc{}, nil, invalid("the profile has text after the JSON object")
	}
	var dropped []string
	known := 0
	for k := range top {
		if slices.Contains(routingKnown, k) {
			known++
		} else {
			dropped = append(dropped, k)
		}
	}
	if known == 0 {
		return RoutingDoc{}, nil, invalid("no routing field is in the profile (Name, DirectSites, ProxySites, BlockSites and so on)")
	}
	slices.Sort(dropped)
	if len(dropped) > maxRoutingDropped {
		dropped = dropped[:maxRoutingDropped]
	}
	for i, k := range dropped {
		dropped[i] = cleanFieldName(k)
	}
	r := routingReader{top: top}
	var d RoutingDoc
	d.Name = r.text("Name", textOnly)
	d.GlobalProxy = r.text("GlobalProxy", boolText)
	d.LastUpdated = r.text("LastUpdated", numberText)
	d.RemoteDNSType = r.text("RemoteDNSType", textOnly)
	d.RemoteDNSDomain = r.text("RemoteDNSDomain", textOnly)
	d.RemoteDNSIP = r.text("RemoteDNSIP", textOnly)
	d.RemoteDns = r.text("RemoteDns", textOnly)
	d.DomesticDNSType = r.text("DomesticDNSType", textOnly)
	d.DomesticDNSDomain = r.text("DomesticDNSDomain", textOnly)
	d.DomesticDNSIP = r.text("DomesticDNSIP", textOnly)
	d.DomesticDns = r.text("DomesticDns", textOnly)
	d.FakeDNS = r.text("FakeDNS", boolText)
	d.Geoipurl = r.text("Geoipurl", textOnly)
	d.Geositeurl = r.text("Geositeurl", textOnly)
	d.DomainStrategy = r.text("DomainStrategy", textOnly)
	d.DirectSites, d.DirectIp = r.list("DirectSites"), r.list("DirectIp")
	d.ProxySites, d.ProxyIp = r.list("ProxySites"), r.list("ProxyIp")
	d.BlockSites, d.BlockIp = r.list("BlockSites"), r.list("BlockIp")
	d.DnsHosts = r.hosts("DnsHosts")
	d.UseChunkFiles = r.flag("useChunkFiles")
	if r.err != nil {
		return RoutingDoc{}, nil, r.err
	}
	if err := checkRoutingDoc(&d); err != nil {
		return RoutingDoc{}, nil, err
	}
	return d, dropped, nil
}

// cleanFieldName is a field name of the pasted text made safe to show: printable ASCII, at most 40 characters.
func cleanFieldName(k string) string {
	var b strings.Builder
	for _, r := range k {
		if b.Len() >= 40 {
			break
		}
		if r >= 0x21 && r < 0x7f {
			b.WriteRune(r)
		} else {
			b.WriteByte('?')
		}
	}
	return b.String()
}

type textKind int

const (
	textOnly   textKind = iota
	boolText            // a JSON true or false is taken as "true" or "false": the docs show the strings
	numberText          // a JSON number is taken as its digits: LastUpdated is a Unix time
)

// routingReader reads fields of the object; the first error stays and the later reads return zero values.
type routingReader struct {
	top map[string]json.RawMessage
	err error
}

func (r *routingReader) fail(key, what string) {
	if r.err == nil {
		r.err = invalid("%s: %s", key, what)
	}
}

func (r *routingReader) text(key string, kind textKind) string {
	raw, ok := r.top[key]
	if !ok || r.err != nil || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if !utf8.ValidString(s) || strings.ContainsFunc(s, unicode.IsControl) {
			r.fail(key, "control characters are not allowed")
			return ""
		}
		return strings.TrimSpace(s)
	}
	switch kind {
	case boolText:
		var b bool
		if json.Unmarshal(raw, &b) == nil {
			return fmt.Sprint(b)
		}
	case numberText:
		var n json.Number
		if json.Unmarshal(raw, &n) == nil && n.String() != "" && strings.Trim(n.String(), "0123456789") == "" {
			return n.String()
		}
	}
	r.fail(key, "must be text")
	return ""
}

func plainItem(s string) bool {
	return s != "" && len(s) <= maxRoutingItem && utf8.ValidString(s) &&
		!strings.ContainsFunc(s, func(c rune) bool { return unicode.IsControl(c) || unicode.IsSpace(c) })
}

func (r *routingReader) list(key string) []string {
	out := []string{}
	raw, ok := r.top[key]
	if !ok || r.err != nil || string(raw) == "null" {
		return out
	}
	var items []string
	if err := json.Unmarshal(raw, &items); err != nil {
		r.fail(key, "must be a list of text")
		return out
	}
	for _, it := range items {
		it = strings.TrimSpace(it)
		if !plainItem(it) {
			r.fail(key, fmt.Sprintf("an entry is empty, longer than %d bytes, or has a space or a control character", maxRoutingItem))
			return out
		}
		out = append(out, it)
	}
	return out
}

func (r *routingReader) hosts(key string) map[string]string {
	raw, ok := r.top[key]
	if !ok || r.err != nil || string(raw) == "null" {
		return nil
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		r.fail(key, `must be an object of text, {"domain": "ip"}`)
		return nil
	}
	if len(m) > maxRoutingHosts {
		r.fail(key, fmt.Sprintf("at most %d entries", maxRoutingHosts))
		return nil
	}
	for k, v := range m {
		if !plainItem(k) || !plainItem(v) {
			r.fail(key, "a host or its address is empty, too long, or has a space")
			return nil
		}
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

func (r *routingReader) flag(key string) *bool {
	raw, ok := r.top[key]
	if !ok || r.err != nil || string(raw) == "null" {
		return nil
	}
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return &b
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "true":
			b = true
			return &b
		case "false":
			return &b
		}
	}
	r.fail(key, "must be true or false")
	return nil
}

// checkRoutingDoc checks the values of the fields, and gives a profile without a name the default one.
func checkRoutingDoc(d *RoutingDoc) error {
	if d.Name == "" {
		d.Name = routingDefaultName
	}
	if utf8.RuneCountInString(d.Name) > MaxRoutingName {
		return invalid("Name: at most %d characters", MaxRoutingName)
	}
	for _, f := range []struct{ key, v string }{{"GlobalProxy", d.GlobalProxy}, {"FakeDNS", d.FakeDNS}} {
		if f.v != "" && f.v != "true" && f.v != "false" {
			return invalid(`%s: "true" or "false"`, f.key)
		}
	}
	if len(d.LastUpdated) > 20 || strings.Trim(d.LastUpdated, "0123456789") != "" {
		return invalid("LastUpdated: a Unix time in seconds (digits only)")
	}
	for _, f := range []struct{ key, v string }{{"RemoteDNSType", d.RemoteDNSType}, {"DomesticDNSType", d.DomesticDNSType}} {
		if f.v != "" && f.v != "DoH" && f.v != "DoU" {
			return invalid(`%s: "DoH" or "DoU"`, f.key)
		}
	}
	for _, f := range []struct{ key, v string }{{"RemoteDNSDomain", d.RemoteDNSDomain}, {"DomesticDNSDomain", d.DomesticDNSDomain}, {"Geoipurl", d.Geoipurl}, {"Geositeurl", d.Geositeurl}} {
		if f.v != "" {
			if err := httpsOnly(f.key, f.v); err != nil {
				return err
			}
		}
	}
	for _, f := range []struct{ key, v string }{{"RemoteDNSIP", d.RemoteDNSIP}, {"RemoteDns", d.RemoteDns}, {"DomesticDNSIP", d.DomesticDNSIP}, {"DomesticDns", d.DomesticDns}} {
		if f.v != "" {
			if _, err := netip.ParseAddr(f.v); err != nil {
				return invalid("%s: an IP address", f.key)
			}
		}
	}
	if d.DomainStrategy != "" && !slices.Contains([]string{"AsIs", "IPIfNonMatch", "IPOnDemand"}, d.DomainStrategy) {
		return invalid("DomainStrategy: AsIs, IPIfNonMatch or IPOnDemand")
	}
	return nil
}

// httpsOnly: an address the app downloads from or asks must be https, with a host, no credentials and no spaces.
func httpsOnly(key, s string) error {
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || len(s) > maxURL || strings.ContainsAny(s, " \t\r\n") {
		return invalid("%s: an https address (https://host/path)", key)
	}
	return nil
}

// RoutingInfoOf says in numbers what a profile holds (what the admin shows); the profile is the stored JSON, dropped the names
// a preview of a pasted text found (nil for a stored entry).
func RoutingInfoOf(profile string, dropped []string) (*adminv1.RoutingInfo, error) {
	p, err := ParseRoutingInput(profile)
	if err != nil {
		return nil, err
	}
	d := p.Doc
	pick := func(domain, ip, alias string) string {
		switch {
		case domain != "":
			return domain
		case ip != "":
			return ip
		}
		return alias
	}
	return &adminv1.RoutingInfo{
		Name:            d.Name,
		DirectSites:     uint32(len(d.DirectSites)),
		DirectIp:        uint32(len(d.DirectIp)),
		ProxySites:      uint32(len(d.ProxySites)),
		ProxyIp:         uint32(len(d.ProxyIp)),
		BlockSites:      uint32(len(d.BlockSites)),
		BlockIp:         uint32(len(d.BlockIp)),
		RemoteDnsType:   d.RemoteDNSType,
		RemoteDns:       pick(d.RemoteDNSDomain, d.RemoteDNSIP, d.RemoteDns),
		DomesticDnsType: d.DomesticDNSType,
		DomesticDns:     pick(d.DomesticDNSDomain, d.DomesticDNSIP, d.DomesticDns),
		Hosts:           uint32(len(d.DnsHosts)),
		GeoipAddress:    d.Geoipurl,
		GeositeAddress:  d.Geositeurl,
		GlobalProxy:     d.GlobalProxy,
		DomainStrategy:  d.DomainStrategy,
		FakeDns:         d.FakeDNS,
		LastUpdated:     d.LastUpdated,
		UseChunkFiles:   d.UseChunkFiles != nil && *d.UseChunkFiles,
		DroppedFields:   dropped,
		ProfileBytes:    uint32(len(d.Canonical())),
		HeaderBytes:     uint32(len(d.Link("happ", false))),
	}, nil
}

// RoutingProfileOf reads an input into the entry the settings store, with its info: the canonical JSON, the mode of a link, the
// fields left out. The groups are the caller's.
func RoutingProfileOf(input string) (*adminv1.RoutingProfile, error) {
	p, err := ParseRoutingInput(input)
	if err != nil {
		return nil, err
	}
	e := &adminv1.RoutingProfile{Profile: p.Doc.Canonical(), OnlyAdd: p.Mode == "add"}
	if e.Info, err = RoutingInfoOf(e.Profile, p.Dropped); err != nil {
		return nil, err
	}
	return e, nil
}

// RoutingProfileFor is the profile of a person by their main group: the profile that names the group, else the one for all
// groups, else none. A person in several groups is decided by the main group alone (the extra groups name nothing here), the way
// the own response headers and the Mihomo templates are. The save keeps a group in one profile and one profile for all groups, so
// a person gets one profile at most.
func RoutingProfileFor(s *adminv1.SubscriptionSettings, groupID string) *adminv1.RoutingProfile {
	list := s.GetRoutingProfiles()
	if groupID != "" {
		for _, e := range list {
			if e.GetOnlyGroups() && slices.Contains(e.GetGroupIds(), groupID) {
				return e
			}
		}
	}
	for _, e := range list {
		if !e.GetOnlyGroups() {
			return e
		}
	}
	return nil
}

// RoutingDocOf is the profile of an entry (the stored JSON read back; false for an entry that does not read).
func RoutingDocOf(e *adminv1.RoutingProfile) (RoutingDoc, bool) {
	p, err := ParseRoutingInput(e.GetProfile())
	return p.Doc, err == nil
}

// validateRoutingProfiles is the check of a save: the list, each profile as the parser reads it, the names and the groups.
func validateRoutingProfiles(s *adminv1.SubscriptionSettings) error {
	list := s.GetRoutingProfiles()
	if len(list) > MaxRoutingProfiles {
		return invalid("routing_profiles: at most %d", MaxRoutingProfiles)
	}
	names := map[string]bool{}
	owner := map[string]string{} // group id -> the profile that has it
	forAll := ""
	for i, e := range list {
		w := fmt.Sprintf("routing_profiles[%d]", i)
		if e == nil {
			return invalid("%s: missing", w)
		}
		p, err := ParseRoutingInput(e.Profile)
		if err != nil {
			return invalid("%s.profile: %s", w, strings.TrimPrefix(err.Error(), ErrInvalid.Error()+": "))
		}
		name := p.Doc.Name
		if key := strings.ToLower(name); names[key] {
			return invalid("%s.profile: the name %q is used by another profile (names are compared without regard to case)", w, name)
		} else {
			names[key] = true
		}
		if !e.OnlyGroups && len(e.GroupIds) > 0 {
			return invalid("%s.group_ids: given without only_groups", w)
		}
		if len(e.GroupIds) > maxHeaderGroups {
			return invalid("%s.group_ids: at most %d", w, maxHeaderGroups)
		}
		if !e.OnlyGroups {
			if forAll != "" {
				return invalid("%s: %q and %q are both for all groups; a person gets one profile", w, forAll, name)
			}
			forAll = name
		}
		own := map[string]bool{}
		for _, id := range e.GroupIds {
			if !idChars(id) || own[id] {
				return invalid("%s.group_ids: a repeated or malformed group id", w)
			}
			own[id] = true
			if other, ok := owner[id]; ok {
				return invalid("%s.group_ids: the group %q is already in the profile %q (a group has one profile)", w, id, other)
			}
			owner[id] = name
		}
	}
	return nil
}

// normalizeRoutingProfiles stores each readable entry as the JSON the panel writes and drops the info (it is read-only, filled
// when the settings are shown). An entry that does not read stays as sent, for the validation to refuse.
func normalizeRoutingProfiles(list []*adminv1.RoutingProfile) {
	for _, e := range list {
		if e == nil {
			continue
		}
		e.Info = nil
		if p, err := ParseRoutingInput(e.Profile); err == nil {
			e.Profile = p.Doc.Canonical()
		}
	}
}

// keepValidRoutingProfiles is what a stored document is read with: an entry a save would refuse now is not served, and never
// fails a fetch.
func keepValidRoutingProfiles(s *adminv1.SubscriptionSettings) {
	var out []*adminv1.RoutingProfile
	for _, e := range s.RoutingProfiles {
		probe := &adminv1.SubscriptionSettings{RoutingProfiles: append(slices.Clone(out), e)}
		if validateRoutingProfiles(probe) == nil {
			out = probe.RoutingProfiles
		}
	}
	s.RoutingProfiles = out
}

// FillRoutingInfo sets the info of each entry of the settings (shown to the admin; never stored).
func FillRoutingInfo(s *adminv1.SubscriptionSettings) {
	for _, e := range s.GetRoutingProfiles() {
		e.Info, _ = RoutingInfoOf(e.Profile, nil)
	}
}
