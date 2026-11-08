//go:build !js

package mcp

import (
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/connectiondiag"
)

// links/3 caps of a current agent, and the unsupported list GetFleetConnections gives each kind of inbound.
var caps3 = []string{"links/1", "links/2", connectiondiag.Links3Capability, connectiondiag.VLESSUnknownKeysCapability}

func un3(proto string, v connectiondiag.Variant) []string {
	return connectiondiag.UnsupportedCountersFor(proto, caps3, v)
}

var (
	realityTCP = connectiondiag.Variant{Security: "reality", Transport: "tcp"}
	userspace  = connectiondiag.Variant{AwgBackend: "userspace"}
	kernel     = connectiondiag.Variant{AwgBackend: "kernel"}
)

// a node in fi1 with a working server of the other UDP protocol and TCP open from the country
func reasonsFx() (*fxb, string, string, string) {
	f := newFx().node("fi1", "FI", "online").group("g", "prf_awg", "prf_hy2", "prf_vless").users("g", 3, 1699990000)
	a := f.inbound("fi1", "prf_awg", "awg", 51820)
	h := f.inbound("fi1", "prf_hy2", "hysteria2", 443)
	v := f.inbound("fi1", "prf_vless", "vless", 8443)
	f.cc("fi1", "ok", func(c *accCountry) {
		c.TSPU = ports(443, "open", 8443, "open")
		c.Vantages = []accVantage{vant("MTS", "home", "reach", true)}
	})
	return f, a, h, v
}

func TestReasonsAWGAnswersLost(t *testing.T) {
	f, a, h, v := reasonsFx()
	f.in.Totals[a] = accTotals{Protocol: "awg", Initiations: 24, Responses: 20, ResponsesUnmatched: 19, Unsupported: un3("awg", userspace)}
	f.in.Totals[h] = hy2Good()
	f.in.Totals[v] = accTotals{Protocol: "vless", Connects: 10, Hellos: 12, Unsupported: un3("vless", realityTCP)}
	r := buildAccess(f.in)
	c := classOf(r, "fi1", clsFingerprinted)
	if c == nil || c.protocol != "awg" || c.Confidence != confMedium || c.Detail["responses_unmatched"] != "19" || len(c.Unknown) != 0 ||
		!strings.Contains(c.Explain, "answers do not come back") {
		t.Fatalf("AWG with its answers lost: %+v", c)
	}
	for _, cell := range r.Matrix {
		if cell.Protocol == "awg" && (cell.RejectSplit == nil || cell.RejectSplit.AnswersLost != 19 || !hasSignal(cell.Signals, "answers_lost")) {
			t.Fatalf("awg cell: %+v", cell)
		}
	}
	// the same counts from the kernel backend are not known: the class stays weak and says why
	f.in.Totals[a] = accTotals{Protocol: "awg", Stalls: 12, KeysStalled: 4, KeysTried: 4, Unsupported: un3("awg", kernel)}
	c = classOf(buildAccess(f.in), "fi1", clsFingerprinted)
	if c == nil || c.Confidence != confLow || len(c.Unknown) == 0 || !strings.Contains(c.Unknown[0], "kernel AmneziaWG backend") {
		t.Fatalf("AWG on the kernel backend: %+v", c)
	}
}

func TestReasonsAWGUnansweredIsOurs(t *testing.T) {
	f, a, h, _ := reasonsFx()
	f.in.Totals[a] = accTotals{Protocol: "awg", Initiations: 30, Responses: 2, Unsupported: un3("awg", userspace)}
	f.in.Totals[h] = hy2Good()
	r := buildAccess(f.in)
	c := classOf(r, "fi1", clsBroken)
	if c == nil || c.Scope != "inbound:"+a || c.Confidence != confHigh || c.Detail["reason"] != "initiations_unanswered" || !c.hard {
		t.Fatalf("initiations without answers: %+v", c)
	}
	if classOf(r, "fi1", clsUDPBlocked) != nil {
		t.Fatal("our own failure to answer read as a blocked UDP")
	}
}

func TestReasonsHysteria2NoReplyAndCertificate(t *testing.T) {
	f, a, h, _ := reasonsFx()
	f.in.Totals[a] = accTotals{Protocol: "awg", Connects: 9, Initiations: 9, Responses: 9, Unsupported: un3("awg", userspace)}
	f.in.Totals[h] = accTotals{Protocol: "hysteria2", Hellos: 40, Accepted: 4, Connects: 2, HsUnfinished: 33, Unsupported: un3("hysteria2", connectiondiag.Variant{})}
	c := classOf(buildAccess(f.in), "fi1", clsFingerprinted)
	if c == nil || c.protocol != "hysteria2" || c.Confidence != confMedium || c.Detail["hs_unfinished"] != "33" {
		t.Fatalf("QUIC handshakes whose answer is lost: %+v", c)
	}
	// apps that refuse the node's certificate: ours, not a block
	f.in.Totals[h] = accTotals{Protocol: "hysteria2", Hellos: 30, Accepted: 2, TLSAlertsRecv: 14, TLSCertRefused: 12, Unsupported: un3("hysteria2", connectiondiag.Variant{})}
	r := buildAccess(f.in)
	c = classOf(r, "fi1", clsBroken)
	if c == nil || c.Detail["reason"] != "certificate_refused" || c.Confidence != confHigh || c.Scope != "inbound:"+h {
		t.Fatalf("refused certificate: %+v", c)
	}
	for _, cell := range r.Matrix {
		if cell.Protocol == "hysteria2" && (cell.RejectSplit == nil || cell.RejectSplit.CertRefused != 12 || !hasSignal(cell.Signals, "cert_refused")) {
			t.Fatalf("hysteria2 cell: %+v", cell)
		}
	}
	// an agent without links/3: the same hellos say only that handshakes fail, and the class names what is not known
	f.in.Totals[h] = accTotals{Protocol: "hysteria2", Hellos: 40, Accepted: 0, Unsupported: connectiondiag.UnsupportedCountersForNode("hysteria2", []string{"links/1", "links/2"})}
	c = classOf(buildAccess(f.in), "fi1", clsFingerprinted)
	if c == nil || c.Confidence != confLow || len(c.Unknown) == 0 || !strings.Contains(c.Unknown[0], "links/3") {
		t.Fatalf("old agent: %+v", c)
	}
}

func TestReasonsVLESSFreezeAfter16K(t *testing.T) {
	f, _, h, v := reasonsFx()
	f.in.Totals[h] = hy2Good()
	f.in.Totals[v] = accTotals{Protocol: "vless", Connects: 10, Hellos: 12, TCPStalls: 8, TCPStalls16K: 7, OnlineS: 900, Unsupported: un3("vless", realityTCP)}
	r := buildAccess(f.in)
	c := classOf(r, "fi1", clsTCPThrottled)
	if c == nil || c.protocol != "vless" || c.Confidence != confHigh || c.Detail["tcp_stalls_16k"] != "7" || c.Detail["udp_works"] != "yes" {
		t.Fatalf("VLESS frozen after 16 KiB, without kl!ck: %+v", c)
	}
	for _, cell := range r.Matrix {
		if cell.Protocol == "vless" && (cell.TCPStalls16K != 7 || !hasSignal(cell.Signals, "tcp_freeze_16k") || cell.Verdict != "degraded") {
			t.Fatalf("vless cell: %+v", cell)
		}
	}
	// stalls late in the stream are no freeze
	f.in.Totals[v] = accTotals{Protocol: "vless", Connects: 10, Hellos: 12, TCPStalls: 8, TCPStalls16K: 1, OnlineS: 900, Unsupported: un3("vless", realityTCP)}
	if c := classOf(buildAccess(f.in), "fi1", clsTCPThrottled); c != nil {
		t.Fatalf("late stalls read as the 16 KiB freeze: %+v", c)
	}
	// kl!ck sees the freeze and the node's agent counts no stalls: the unknown says so
	f.in.Totals[v] = accTotals{Protocol: "vless", OnlineS: 900, Unsupported: connectiondiag.UnsupportedCountersForNode("vless", []string{"links/1"})}
	f.in.Klick["nod_fi1"] = &accKlick{accKlickTally: accKlickTally{Reports: 6, OK: 2, Freeze: 4}, Kinds: []accKlickKind{
		{Kind: "vless", accKlickTally: accKlickTally{Reports: 5, OK: 1, Freeze: 4}, DLChecked: 5, DLStalled: 4}}}
	c = classOf(buildAccess(f.in), "fi1", clsTCPThrottled)
	if c == nil || len(c.Unknown) == 0 || !strings.Contains(c.Unknown[0], "count no TCP stalls") {
		t.Fatalf("kl!ck with an old agent: %+v", c)
	}
	// and with the node's own stalls the kl!ck class is confirmed
	f.in.Totals[v] = accTotals{Protocol: "vless", Connects: 10, TCPStalls: 6, TCPStalls16K: 5, OnlineS: 900, Unsupported: un3("vless", realityTCP)}
	c = classOf(buildAccess(f.in), "fi1", clsTCPThrottled)
	if c == nil || c.Confidence != confHigh || len(c.Unknown) != 0 || c.Detail["tcp_stalls_16k"] != "5" || !hasSignal(c.Evidence, "conn:"+v) {
		t.Fatalf("kl!ck and the node agree: %+v", c)
	}
}

func TestReasonsRealityAuthFailedAndCut(t *testing.T) {
	f, _, h, v := reasonsFx()
	f.in.Totals[h] = hy2Good()
	// hellos with our name that fail REALITY authentication and no client gets in: a key the apps do not have
	f.in.Totals[v] = accTotals{Protocol: "vless", Hellos: 20, RealityFallback: 13, AuthFailed: 12, Unsupported: un3("vless", realityTCP)}
	r := buildAccess(f.in)
	c := classOf(r, "fi1", clsBroken)
	if c == nil || c.Detail["reason"] != "reality_auth_failed" || c.Confidence != confMedium || !c.hard {
		t.Fatalf("REALITY authentication failing: %+v", c)
	}
	// with people getting in, the same failures are probes: weak, and not a failing way
	f.in.Totals[v] = accTotals{Protocol: "vless", Hellos: 60, Connects: 40, RealityFallback: 13, AuthFailed: 12, Unsupported: un3("vless", realityTCP)}
	if c := classOf(buildAccess(f.in), "fi1", clsBroken); c != nil {
		t.Fatalf("probes next to working clients read as ours: %+v", c)
	}
	// REALITY answers and the clients never finish: cut after the ClientHello
	f.in.Totals[v] = accTotals{Protocol: "vless", Hellos: 30, HsUnfinished: 25, Connects: 1, Unsupported: un3("vless", realityTCP)}
	c = classOf(buildAccess(f.in), "fi1", clsFingerprinted)
	if c == nil || c.Scope != "protocol:vless" || c.Confidence != confMedium || c.Detail["hs_unfinished"] != "25" {
		t.Fatalf("REALITY cut after the hello: %+v", c)
	}
}

func hasSignal(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
