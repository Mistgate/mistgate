// Package clientprobe is the source "kl!ck reports" of the Reachability page: the local server check of the kl!ck VPN app,
// which a person starts in the app and, with the "send to the panel" switch on, sends to the panel that issued the
// subscription (client_probe.proto). This file reads and checks a report; the subscription endpoint (subs, probe.go) takes it
// under the link, matches its servers against the person's subscription and stores the result (store, migration 00075);
// service.go is the owner's switch and the counts the page shows.
//
// A report is untrusted input from anyone who holds a link: every field is checked against the contract of the app (kl!ck,
// crates/klick-service/src/probe.rs PanelReport), and anything else is refused.
package clientprobe

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/mistgate/mistgate/internal/panel/store"
)

const (
	// Version is the newest report version the panel takes: the value of the Probe-Report header.
	Version = 1
	// MaxBody is the largest report body; a full one (MaxServers results) is about 60 KB.
	MaxBody = 128 << 10
	// MaxServers is the most server results one report may carry.
	MaxServers = 256
	// Every is how often one subscription may send a report (the app sends at most once in 10 minutes).
	Every = 10 * time.Minute
	// Keep is how long a report is stored (the health retention sweep).
	Keep = 7 * 24 * time.Hour

	maxText  = 128   // operator and region, in characters
	maxTries = 16    // TCP attempts per server
	maxMS    = 120e3 // a check of the app ends after 90 s
	maxBytes = 64 << 20
)

// Report is a report as the app sends it (v1). The pointers are values that were not measured when nil.
type Report struct {
	V             int      `json:"v"`
	VPN           bool     `json:"vpn"`
	DirectChecked bool     `json:"direct_checked"`
	Network       Network  `json:"network"`
	Servers       []Server `json:"servers"`
}

// Network is the network the person was on, as the app saw it; empty fields are unknown.
type Network struct {
	ASN      uint32 `json:"asn"`
	Operator string `json:"operator"`
	Country  string `json:"country"`
	Region   string `json:"region"`
	Type     string `json:"type"`
}

// Server is one server of the subscription as the app checked it. Host and Port are as the subscription gave them.
type Server struct {
	Type    string `json:"type"`
	Host    string `json:"host"`
	Port    uint16 `json:"port"`
	Verdict string `json:"verdict"`
	Detail
}

// Detail is what was measured of a server: stored as it is (detail_json).
type Detail struct {
	Flaky     bool    `json:"flaky"`
	TCPOK     *uint8  `json:"tcp_ok,omitempty"`
	TCPTries  *uint8  `json:"tcp_tries,omitempty"`
	TCPMS     *uint32 `json:"tcp_ms,omitempty"`
	DelayMS   *uint32 `json:"delay_ms,omitempty"`
	DLBytes   *uint64 `json:"dl_bytes,omitempty"`
	DLStalled *bool   `json:"dl_stalled,omitempty"`
	DNS       *string `json:"dns,omitempty"`
}

// The words of the contract.
var (
	verdicts     = set("ok", "freeze", "tcp_blocked", "handshake_failed", "no_answer")
	dnsAnswers   = set("same", "different", "bogus", "nxdomain")
	networkTypes = set("mobile", "home", "business", "hosting", "vpn")
)

func set(words ...string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

// Parse reads and checks a report body: one JSON object of version 1 with no unknown field, every value in its range and
// every word one the contract knows. Text is trimmed and the country upper-cased; host and type lower-cased. The error names
// the first field that is wrong (it carries no value from the body).
func Parse(body []byte) (Report, error) {
	var r Report
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return Report{}, errors.New("not a report")
	}
	if dec.More() {
		return Report{}, errors.New("not a report")
	}
	return r, r.check()
}

func (r *Report) check() error {
	if r.V != Version {
		return errors.New("v")
	}
	n := &r.Network
	n.Operator, n.Region, n.Country = strings.TrimSpace(n.Operator), strings.TrimSpace(n.Region), strings.ToUpper(strings.TrimSpace(n.Country))
	switch {
	case !text(n.Operator):
		return errors.New("network.operator")
	case !text(n.Region):
		return errors.New("network.region")
	case n.Country != "" && !(len(n.Country) == 2 && 'A' <= n.Country[0] && n.Country[0] <= 'Z' && 'A' <= n.Country[1] && n.Country[1] <= 'Z'):
		return errors.New("network.country")
	case n.Type != "" && !networkTypes[n.Type]:
		return errors.New("network.type")
	case len(r.Servers) > MaxServers:
		return errors.New("servers")
	}
	for i := range r.Servers {
		if field := r.Servers[i].check(); field != "" {
			return fmt.Errorf("servers[%d].%s", i, field)
		}
	}
	return nil
}

// check names the first wrong field of a server ("" = none).
func (s *Server) check() string {
	s.Type, s.Host = strings.ToLower(s.Type), strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(s.Host, "["), "]"))
	switch {
	case !word(s.Type, 32):
		return "type"
	case !host(s.Host):
		return "host"
	case s.Port == 0:
		return "port"
	case !verdicts[s.Verdict]:
		return "verdict"
	case s.TCPTries != nil && (*s.TCPTries == 0 || *s.TCPTries > maxTries):
		return "tcp_tries"
	case s.TCPOK != nil && *s.TCPOK > maxTries, s.TCPOK != nil && s.TCPTries != nil && *s.TCPOK > *s.TCPTries:
		return "tcp_ok"
	case s.TCPMS != nil && *s.TCPMS > maxMS:
		return "tcp_ms"
	case s.DelayMS != nil && *s.DelayMS > maxMS:
		return "delay_ms"
	case s.DLBytes != nil && *s.DLBytes > maxBytes:
		return "dl_bytes"
	case s.DNS != nil && !dnsAnswers[*s.DNS]:
		return "dns"
	}
	return ""
}

// text is free text of the network: short, no control characters or line breaks.
func text(s string) bool {
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > maxText {
		return false
	}
	for _, c := range s {
		if !unicode.IsGraphic(c) {
			return false
		}
	}
	return true
}

// word is a protocol type: lower-case letters, digits and dashes.
func word(s string, max int) bool {
	if s == "" || len(s) > max {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// host is a server address as a subscription carries it: a DNS name, an IPv4 or an IPv6 address (without brackets).
func host(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '.' || c == '-' || c == '_' || c == ':') {
			return false
		}
	}
	return true
}

// Match finds the node of a server of the person's subscription by its host (lower case) and port.
type Match func(host string, port uint16) (nodeID string, ok bool)

// Results are the server results of a report that matched a node, as stored; a result for anything else is dropped, and so
// is a second result for the same node, type and port.
func Results(r Report, match Match) []store.ClientProbeResult {
	type key struct {
		node, kind string
		port       uint16
	}
	seen := map[key]bool{}
	var out []store.ClientProbeResult
	for _, s := range r.Servers {
		node, ok := match(s.Host, s.Port)
		k := key{node, s.Type, s.Port}
		if !ok || seen[k] {
			continue
		}
		seen[k] = true
		d, _ := json.Marshal(s.Detail) // plain values only: cannot fail
		out = append(out, store.ClientProbeResult{NodeID: node, Kind: s.Type, Port: s.Port, Verdict: s.Verdict, DetailJSON: string(d)})
	}
	return out
}

// NetworkOf is the network of a report as stored.
func NetworkOf(r Report) store.ClientProbeNetwork {
	n := r.Network
	return store.ClientProbeNetwork{VPN: r.VPN, DirectChecked: r.DirectChecked, ASN: n.ASN, Operator: n.Operator, Country: n.Country, Region: n.Region, NetworkType: n.Type}
}

// NewID is the id of one stored report: random, it only groups the report's rows.
func NewID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b) // crypto/rand.Read never fails
	return hex.EncodeToString(b)
}
