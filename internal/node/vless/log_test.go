package vless

import (
	"context"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	xlog "github.com/xtls/xray-core/common/log"

	"github.com/mistgate/mistgate/internal/node/engine"
)

func TestProtocolVersionAndCapabilities(t *testing.T) {
	instance, err := New(engine.Env{})
	if err != nil {
		t.Fatal(err)
	}
	defer instance.Close(context.Background())
	if got := instance.Protocol(); got != "vless" {
		t.Fatalf("Protocol() = %q", got)
	}
	if got := instance.Version(); got != "xray-core v26.11.9" {
		t.Fatalf("Version() = %q", got)
	}
	if got := instance.Capabilities(); !got.RateLimitPerCred || !got.HardExpiry {
		t.Fatalf("Capabilities() = %+v", got)
	}
}

// captureGate is a logger handler whose Debug level is raised from outside, as the agent's capture raises it for the sources it wants.
type captureGate struct {
	slog.Handler
	on *atomic.Bool
}

func (g captureGate) Enabled(_ context.Context, l slog.Level) bool {
	return l >= slog.LevelInfo || g.on.Load()
}
func (g captureGate) WithAttrs(as []slog.Attr) slog.Handler {
	return captureGate{g.Handler.WithAttrs(as), g.on}
}
func (g captureGate) WithGroup(n string) slog.Handler {
	return captureGate{g.Handler.WithGroup(n), g.on}
}

// gatedXrayLog sends the xray handler to a logger that writes every level it is given; on is the capture switch.
func gatedXrayLog(t *testing.T, on *atomic.Bool) *lockedLogBuffer {
	t.Helper()
	out := &lockedLogBuffer{}
	installXrayLogging(slog.New(captureGate{slog.NewTextHandler(out, &slog.HandlerOptions{Level: slog.LevelDebug}), on}))
	return out
}

func feedXrayLines(h xrayLogHandler) {
	h.Handle(&xlog.AccessMessage{From: "client", To: "tcp:203.0.113.10:443", Status: xlog.AccessAccepted, Email: "alice"})
	h.Handle(&xlog.GeneralMessage{Severity: xlog.Severity_Info, Content: "info destination=203.0.113.10:443 email=alice"})
	h.Handle(&xlog.GeneralMessage{Severity: xlog.Severity_Debug, Content: "debug"})
	h.Handle(&xlog.GeneralMessage{Severity: xlog.Severity_Warning, Content: "[77] session warning"})
	h.Handle(&xlog.GeneralMessage{Severity: xlog.Severity_Warning, Content: "warning"})
	h.Handle(&xlog.GeneralMessage{Severity: xlog.Severity_Error, Content: "error"})
}

func TestXrayLogHandlerDropsAccessAndBelowWarning(t *testing.T) {
	out := gatedXrayLog(t, new(atomic.Bool))
	h := xrayLogHandler{}
	feedXrayLines(h)

	got := out.String()
	if strings.Contains(got, "203.0.113.10") || strings.Contains(got, "alice") {
		t.Fatalf("sensitive access data reached slog: %s", got)
	}
	if strings.Contains(got, "info") || strings.Contains(got, "debug") || strings.Contains(got, "session warning") || !strings.Contains(got, "warning") || !strings.Contains(got, "error") {
		t.Fatalf("unexpected Xray severity filtering: %s", got)
	}
	if strings.Count(got, "source=xray") != 2 {
		t.Fatalf("warning and error records must carry source=xray: %s", got)
	}
	if h.Severity() != xlog.Severity_Info {
		t.Fatalf("Xray is asked for %v outside a capture, want Info (REALITY's verdict)", h.Severity())
	}
}

func TestXrayLogHandlerKeepsConnectionLinesOnlyInACapture(t *testing.T) {
	on := new(atomic.Bool)
	out := gatedXrayLog(t, on)
	h := xrayLogHandler{}

	on.Store(true)
	if h.Severity() != xlog.Severity_Debug {
		t.Fatalf("Xray is asked for %v in a capture, want Debug", h.Severity())
	}
	feedXrayLines(h)
	got := out.String()
	for _, want := range []string{"[Access] accepted tcp:<ip>:443", "[Info] info destination=<ip>:port email=<user>", "[Debug] debug", "[Warning] [77] session warning"} {
		if !strings.Contains(got, want) {
			t.Errorf("capture lacks %q:\n%s", want, got)
		}
	}
	// the always-on warning and error keep their level and are not repeated at Debug
	if strings.Count(got, "level=DEBUG") != 4 || strings.Count(got, "level=WARN") != 1 || strings.Count(got, "level=ERROR") != 1 {
		t.Errorf("captured lines are Debug; the always-on warning and error keep their level:\n%s", got)
	}
	for _, secret := range []string{"203.0.113.10", "alice"} {
		if strings.Contains(got, secret) {
			t.Errorf("%q reached the capture:\n%s", secret, got)
		}
	}

	on.Store(false)
	again := gatedXrayLog(t, on) // a fresh buffer; the capture is off
	feedXrayLines(h)
	if got := again.String(); strings.Contains(got, "level=DEBUG") || strings.Contains(got, "[Access]") || strings.Contains(got, "session warning") {
		t.Fatalf("lines kept after the capture ended:\n%s", got)
	}
	if h.Severity() != xlog.Severity_Info {
		t.Fatalf("Xray still asked for %v after the capture", h.Severity())
	}
}

func TestXrayLogHandlerKeepsCountingRealityVerdicts(t *testing.T) {
	on := new(atomic.Bool)
	gatedXrayLog(t, on)
	now := time.Now()
	for _, capture := range []bool{false, true} {
		on.Store(capture)
		c := &inboundCounters{}
		rememberNetworkClose("192.0.2.31:4000", c, 0, "", nil, now)
		xrayLogHandler{}.Handle(&xlog.GeneralMessage{Severity: xlog.Severity_Info,
			Content: "[5] transport/internet/tcp: REALITY: processed invalid connection from 192.0.2.31:4000: authentication failed or validation criteria not met"})
		if c.authFailed.Load() != 1 || c.realityFallback.Load() != 1 {
			t.Errorf("capture=%v: verdict not counted (auth %d, fallback %d)", capture, c.authFailed.Load(), c.realityFallback.Load())
		}
	}
}

// The capture switch flips while lines are handled (run it with -race): a line that falls on either side of the flip keeps nothing it must not.
func TestXrayLogHandlerCaptureSwitchRaces(t *testing.T) {
	on := new(atomic.Bool)
	out := gatedXrayLog(t, on)
	h := xrayLogHandler{}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				feedXrayLines(h)
				_ = h.Severity()
			}
		}()
	}
	for j := 0; j < 400; j++ {
		on.Store(j%2 == 0)
		runtime.Gosched()
	}
	wg.Wait()
	if got := out.String(); strings.Contains(got, "203.0.113.10") || strings.Contains(got, "alice") {
		t.Fatalf("a client's data reached the log:\n%s", got)
	}
}

func TestRedactXrayCapture(t *testing.T) {
	cases := []struct {
		name, line string
		gone, want []string
	}{
		{"destination ip and port", "[Info] [3501244517] proxy/vless/inbound: received request for tcp:203.0.113.45:443",
			[]string{"203.0.113.45"}, []string{"[3501244517]", "received request for tcp:<ip>:443"}},
		{"destination ipv6 in brackets", "[Info] [31] proxy/vless/inbound: received request for udp:[2001:db8::a1]:8443",
			[]string{"2001:db8", "a1]"}, []string{"[31]", "udp:<ip>:8443"}},
		{"hyphen and punycode host", "[Info] [32] proxy/vless/inbound: received request for tcp:cdn-edge-1.xn--80ak6aa92e.example.com:443",
			[]string{"cdn-edge-1", "xn--80ak6aa92e", "example.com"}, []string{"tcp:<host>:443"}},
		{"unicode host", "[Info] [33] proxy/vless/inbound: received request for tcp:\u043f\u0440\u0438\u043c\u0435\u0440.example:443",
			[]string{"\u043f\u0440\u0438\u043c\u0435\u0440"}, []string{"tcp:<host>:443"}},
		{"host without a dot", "[Info] [34] proxy/vless/inbound: received request for tcp:intranet-box:22",
			[]string{"intranet-box"}, []string{"tcp:<host>:22"}},
		{"sniffed domain and route", "[Info] [35] app/dispatcher: sniffed domain: Shop-Example.COM\n[Info] [35] app/dispatcher: taking detour [direct] for [tcp:shop-example.com:443]",
			[]string{"Shop-Example", "shop-example"}, []string{"sniffed domain: <host>", "for [tcp:<host>:443]"}},
		{"uuids in any case and an unknown key", "[Warning] [77] proxy/vless/inbound: invalid request from 203.0.113.9:51514 > invalid request user id: 66AD4540-B58C-4ad2-9926-EA63445A9B57; also 66ad4540-b58c-4ad2-9926-ea63445a9b57",
			[]string{"66AD4540", "66ad4540", "203.0.113.9", "51514"}, []string{"[77]", "invalid request from <ip>:port", "invalid request user id: [redacted]"}},
		{"client address with an ipv6 and a port", "[Info] [78] app/proxyman/inbound: connection from [2001:db8:0:1::7f]:44312 ends",
			[]string{"2001:db8", "44312"}, []string{"[78]", "connection from <ip>:port ends"}},
		{"reality refusal with an ipv6 client and a bare name", "[Info] [79] transport/internet/tcp: REALITY: processed invalid connection from [2001:db8::7]:44312: server name mismatch: Intranet-Only",
			[]string{"2001:db8", "44312", "Intranet-Only"}, []string{"[79]", "REALITY: processed invalid connection from <ip>:port: server name mismatch: <sni>"}},
		{"reality refusal, dotted name", "[Info] [80] transport/internet/tcp: REALITY: processed invalid connection from 198.51.100.2:5000: server name mismatch: Mail.Example.org",
			[]string{"198.51.100.2", "Mail.Example"}, []string{"server name mismatch: <sni>"}},
		{"outbound failure", "[Info] [88] app/proxyman/inbound: connection ends > proxy/vless/inbound: failed to dial tcp:db-1.internal.example.com:5432 > dial tcp 192.0.2.77:5432: connect: connection refused",
			[]string{"db-1", "internal.example", "192.0.2.77"}, []string{"[88]", "tcp:<host>:5432", "connect: connection refused"}},
		{"timeout", "[Info] [89] app/proxyman/inbound: connection ends > read tcp 192.0.2.10:443->198.51.100.77:50321: i/o timeout",
			[]string{"192.0.2.10", "198.51.100.77", "50321"}, []string{"[89]", "i/o timeout"}},
		{"account", "[Info] [90] proxy/vless/inbound: user Alice@Example.com email: alice-9f2 is over the limit",
			[]string{"Alice", "alice-9f2", "Example.com"}, []string{"[90]", "is over the limit"}},
		{"fallback name and path", "[Info] [91] proxy/vless/inbound: realName = Private-Site.example.org\n[Info] [91] proxy/vless/inbound: realPath = /secret/page?id=7",
			[]string{"Private-Site", "/secret/page"}, []string{"[91]", "realName = <sni>", "realPath = <path>"}},
		{"uuid and sni value in a debug line", "[Debug] [92] app/x: id 66ad4540-b58c-4ad2-9926-ea63445a9b57 sni=www.Example.net",
			[]string{"66ad4540", "Example.net"}, []string{"[92]", "sni=<sni>"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := redactXrayCapture(c.line)
			for _, bad := range c.gone {
				if strings.Contains(strings.ToLower(got), strings.ToLower(bad)) {
					t.Errorf("%q survived: %s", bad, got)
				}
			}
			for _, want := range c.want {
				if !strings.Contains(got, want) {
					t.Errorf("missing %q in: %s", want, got)
				}
			}
			if again := redactXrayCapture(got); again != got {
				t.Errorf("not idempotent:\n%s\n%s", got, again)
			}
		})
	}
}

func TestXrayAccessLineLeavesOutClientAndAccount(t *testing.T) {
	on := new(atomic.Bool)
	out := gatedXrayLog(t, on)
	on.Store(true)
	h := xrayLogHandler{}
	h.Handle(&xlog.AccessMessage{From: "198.51.100.5:4444", To: "tcp:www.example.com:443", Status: xlog.AccessAccepted, Detour: "in >> direct", Email: "alice"})
	h.Handle(&xlog.AccessMessage{From: "[2001:db8::5]:1", Status: xlog.AccessRejected, Reason: "invalid request user id: 66ad4540-b58c-4ad2-9926-ea63445a9b57", Email: "alice@example.com"})
	got := out.String()
	for _, want := range []string{`"[Access] accepted tcp:<host>:443 [in >> direct]"`, `"[Access] rejected invalid request user id: [redacted]"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}
	for _, bad := range []string{"198.51.100.5", "4444", "2001:db8", "alice", "example.com", "66ad4540"} {
		if strings.Contains(got, bad) {
			t.Errorf("%q survived:\n%s", bad, got)
		}
	}
}

func TestXrayLogHandlerDropsSessionsAndNoiseAndRedactsAddresses(t *testing.T) {
	out := gatedXrayLog(t, new(atomic.Bool))
	handler := xrayLogHandler{}
	handler.Handle(&xlog.GeneralMessage{Severity: xlog.Severity_Warning, Content: "[1799322037] account 66ad4540-b58c-4ad2-9926-ea63445a9b57 is rejected from 203.0.113.10 for destination.example.com"})
	handler.Handle(&xlog.GeneralMessage{Severity: xlog.Severity_Warning, Content: "account 66ad4540-b58c-4ad2-9926-ea63445a9b57 failed from 203.0.113.10 via [2001:db8::10]:443"})
	handler.Handle(&xlog.GeneralMessage{Severity: xlog.Severity_Warning, Content: "core: Xray 1.8.25 started"})
	handler.Handle(&xlog.GeneralMessage{Severity: xlog.Severity_Error, Content: "failed to serve HTTP for XHTTP > accept tcp 127.0.0.1:443: use of closed network connection"})
	handler.Handle(&xlog.GeneralMessage{Severity: xlog.Severity_Warning, Content: "safe warning"})

	got := out.String()
	for _, sensitive := range []string{"1799322037", "66ad4540-b58c-4ad2-9926-ea63445a9b57", "203.0.113.10", "2001:db8::10", "127.0.0.1", "destination.example.com", "core: Xray", "failed to serve HTTP for XHTTP"} {
		if strings.Contains(got, sensitive) {
			t.Errorf("Xray log retained %q: %s", sensitive, got)
		}
	}
	if !strings.Contains(got, "account [redacted]") || !strings.Contains(got, "safe warning") {
		t.Fatalf("safe warning content was not forwarded after redaction: %s", got)
	}
}
