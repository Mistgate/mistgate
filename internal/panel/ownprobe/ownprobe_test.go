package ownprobe

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	ownprobev1 "github.com/mistgate/mistgate/gen/mistgate/ownprobe/v1"
	"github.com/mistgate/mistgate/internal/panel/access"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/countryreach"
	"github.com/mistgate/mistgate/internal/panel/httpserver"
	"github.com/mistgate/mistgate/internal/panel/securitylimit"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/subs"
)

const (
	testJobID = "0123456789abcdef"
	homeIP    = "192.0.2.10"    // ExampleNet
	otherIP   = "198.51.100.10" // OtherNet
)

var (
	testToken  = "mgp_" + strings.Repeat("a", 52)
	testSecret = "mgps_" + strings.Repeat("a", 43)
)

type fixture struct {
	*Service
	st      *store.Store
	now     time.Time
	targets []Target
	logs    *bytes.Buffer
}

var defaultTargets = []Target{{NodeID: "nod_de1", IP: "203.0.113.12", Port: 443, Kind: "tls", SNI: "target.example.test", Path: "/"}}

func probeFixture(t *testing.T, mutate ...func(*Config)) *fixture {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "probe.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	f := &fixture{st: st, now: time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC), targets: defaultTargets, logs: &bytes.Buffer{}}
	var mu sync.Mutex
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return f.now }
	cfg := Config{
		Store: st, Now: clock, Limiter: securitylimit.NewMemory(clock, 256),
		PublicURL: "https://panel.example.test", ProbePrefix: "/aaaaaaaaaaaaaaaaaaaaaaaa/",
		Country: func(context.Context) string { return "test" },
		ASN: func(ip netip.Addr) uint32 {
			if ip.String() == otherIP {
				return 64513
			}
			return 64512
		},
		Operator: func(_ string, asn uint32) string {
			if asn == 64513 {
				return "OtherNet"
			}
			return "ExampleNet"
		},
		References: func(string) (References, bool) {
			return References{ControlIP: "192.0.2.1", ControlSNI: "control.example.test", BlockedIP: "198.51.100.1"}, true
		},
		SyncBurst: 1000,
		Targets:   func(context.Context) ([]Target, error) { return f.targets, nil },
		Log:       slog.New(slog.NewTextHandler(f.logs, nil)),
	}
	for _, m := range mutate {
		m(&cfg)
	}
	f.Service = New(cfg)
	return f
}

// probe makes an active probe; its secret is testSecret+name.
func (f *fixture) probe(t *testing.T, id, name string) (store.OwnProbe, string) {
	t.Helper()
	token := "mgp_" + strings.Repeat("b", 52) + name // storage takes only the digest
	tokenHash := sha256.Sum256([]byte(token))
	secret := testSecret + name
	secretHash := sha256.Sum256([]byte(secret))
	if err := f.st.CreateOwnProbe(context.Background(), store.OwnProbe{ID: id, Name: name, City: "de1", Kind: "home",
		TokenHash: tokenHash[:], TokenExpires: f.now.Add(time.Hour), CreatedAt: f.now, CreatedBy: "adm_test"}); err != nil {
		t.Fatal(err)
	}
	p, err := f.st.EnrollOwnProbe(context.Background(), tokenHash[:], secretHash[:], 64512, "ExampleNet", "ExampleNet", ScriptVersion(), f.now)
	if err != nil {
		t.Fatal(err)
	}
	return p, secret
}

func (f *fixture) enable(t *testing.T) {
	t.Helper()
	if err := f.st.SetSettings(context.Background(), map[string]string{settingsEnabled: "true", settingsInterval: "60"}); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) setJob(t *testing.T, id, job string, targets []Target) {
	t.Helper()
	if err := f.st.SetOwnProbeJob(context.Background(), id, job, encodeJob(targets), f.now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) advance(d time.Duration) { f.now = f.now.Add(d) }

func post(h http.Handler, path, remote, authorization, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.RemoteAddr = remote + ":1234"
	if authorization != "" {
		r.Header.Set("Authorization", authorization)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func get(h http.Handler, path, remote string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.RemoteAddr = remote + ":1234"
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// report is the text of a report: the header lines of ParseSync and one result line per row ("<n> <tcp> <ms> <tls> <ms> <fetch> <bytes>").
func report(job, control, reference string, rows ...string) string {
	return reportFrom("abcdef012345", job, control, reference, rows...)
}

func reportFrom(version, job, control, reference string, rows ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "mgp 1\nv %s\njob %s\nctl %s 10\nref %s\n", version, job, control, reference)
	for _, row := range rows {
		b.WriteString("r " + row + "\n")
	}
	b.WriteString("end\n")
	return b.String()
}

// formatReport is the one canonical text of a parsed report; a parser that is strict has no other.
func formatReport(r SyncReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "mgp 1\nv %s\njob %s\nctl %s %d\nref %s\n", r.Version, r.JobID, r.Control, r.ControlMS, r.Reference)
	for _, x := range r.Results {
		fmt.Fprintf(&b, "r %d %s %d %s %d %s %d\n", x.Index, x.TCP, x.TCPMS, x.TLS, x.TLSMS, x.Fetch, x.Bytes)
	}
	b.WriteString("end\n")
	return b.String()
}

const (
	rowReach   = "1 ok 10 ok 20 full 65536"
	rowTLSOnly = "1 ok 10 ok 20 - 0"
	rowDark    = "1 timeout 5000 - 0 - 0"
)

func TestParseSyncStrict(t *testing.T) {
	valid := report(testJobID, "ok", "blocked", "1 ok 10 ok 20 full 65536", "2 timeout 5000 - 0 - 0", "32 refused 3 - 0 - 0")
	parsed, err := ParseSync([]byte(valid))
	if err != nil || parsed.JobID != testJobID || len(parsed.Results) != 3 || parsed.Results[0].Bytes != 65536 || parsed.Results[2].Index != 32 || parsed.Results[2].TCP != "refused" {
		t.Fatalf("ParseSync(valid) = %+v, %v", parsed, err)
	}
	if got := formatReport(parsed); got != valid {
		t.Fatalf("round trip = %q", got)
	}
	if _, err := ParseSync([]byte(report(noJobID, "fail", "fail"))); err != nil {
		t.Fatalf("an empty report of the zero job: %v", err)
	}
	for name, bad := range map[string]string{
		"unknown line":          strings.Replace(valid, "end\n", "upd x\nend\n", 1),
		"unknown line first":    "upd x\n" + valid,
		"blank line":            strings.Replace(valid, "ref blocked\n", "ref blocked\n\n", 1),
		"no trailing newline":   strings.TrimSuffix(valid, "\n"),
		"two trailing newlines": valid + "\n",
		"missing end":           strings.Replace(valid, "end\n", "", 1),
		"CRLF":                  strings.ReplaceAll(valid, "\n", "\r\n"),
		"tab":                   strings.Replace(valid, "ctl ok", "ctl\tok", 1),
		"NUL":                   strings.Replace(valid, "ctl ok", "ctl o\x00k", 1),
		"double space":          strings.Replace(valid, "ctl ok 10", "ctl  ok 10", 1),
		"trailing space":        strings.Replace(valid, "ref blocked", "ref blocked ", 1),
		"version 2":             strings.Replace(valid, "mgp 1", "mgp 2", 1),
		"upper hex job":         strings.Replace(valid, testJobID, strings.ToUpper(testJobID), 1),
		"short job":             strings.Replace(valid, testJobID, "0123456789abcde", 1),
		"short version":         strings.Replace(valid, "abcdef012345", "abcdef01234", 1),
		"order of header":       strings.Replace(valid, "ctl ok 10\nref blocked\n", "ref blocked\nctl ok 10\n", 1),
		"ctl value":             strings.Replace(valid, "ctl ok", "ctl maybe", 1),
		"ref value":             strings.Replace(valid, "ref blocked", "ref gone", 1),
		"ctl ms leading zero":   strings.Replace(valid, "ctl ok 10", "ctl ok 010", 1),
		"ctl ms too big":        strings.Replace(valid, "ctl ok 10", "ctl ok 120001", 1),
		"ctl ms negative":       strings.Replace(valid, "ctl ok 10", "ctl ok -1", 1),
		"tcp word":              strings.Replace(valid, "1 ok 10", "1 maybe 10", 1),
		"tls word":              strings.Replace(valid, "ok 20 full", "weird 20 full", 1),
		"fetch word":            strings.Replace(valid, "full 65536", "whole 65536", 1),
		"bytes over 64 KiB":     strings.Replace(valid, "full 65536", "full 65537", 1),
		"index zero":            strings.Replace(valid, "r 1 ok", "r 0 ok", 1),
		"index 33":              strings.Replace(valid, "r 32 refused", "r 33 refused", 1),
		"index leading zero":    strings.Replace(valid, "r 1 ok", "r 01 ok", 1),
		"index repeated":        report(testJobID, "ok", "blocked", rowReach, rowReach),
		"index backwards":       report(testJobID, "ok", "blocked", "2 ok 10 ok 20 full 1", "1 ok 10 ok 20 full 1"),
		"too few fields":        report(testJobID, "ok", "blocked", "1 ok 10 ok 20 full"),
		"too many fields":       report(testJobID, "ok", "blocked", rowReach+" 1"),
		"labelled fields":       report(testJobID, "ok", "blocked", "1 tcp: ok 10 tls: ok 20 fetch: full 65536"),
		"TLS after dead TCP":    report(testJobID, "ok", "blocked", "1 timeout 5000 ok 20 - 0"),
		"page after dead TLS":   report(testJobID, "ok", "blocked", "1 ok 10 reset 20 full 100"),
		"ms of an unrun TLS":    report(testJobID, "ok", "blocked", "1 refused 10 - 20 - 0"),
		"bytes of no page":      report(testJobID, "ok", "blocked", "1 ok 10 ok 20 - 5"),
		"results for job zero":  report(noJobID, "ok", "blocked", rowReach),
		"empty":                 "",
		"only end":              "end\n",
	} {
		if _, err := ParseSync([]byte(bad)); err == nil {
			t.Errorf("%s: accepted %q", name, bad)
		}
	}
	// 33 results can never fit the 32 targets of a job.
	var many []string
	for i := 1; i <= 33; i++ {
		many = append(many, fmt.Sprintf("%d ok 1 ok 1 - 0", i))
	}
	if _, err := ParseSync([]byte(report(testJobID, "ok", "blocked", many...))); err == nil {
		t.Error("accepted 33 results")
	}
	if _, err := ParseSync(bytes.Repeat([]byte("x"), maxSyncBody+1)); err == nil {
		t.Error("accepted an oversized body")
	}
}

// FuzzSyncParse: whatever the bytes are, a report that is accepted is exactly the canonical text of what it parsed to (no
// whitespace, case or number normalisation slipped through) and carries only values the panel's code relies on.
func FuzzSyncParse(f *testing.F) {
	f.Add([]byte(report(testJobID, "ok", "blocked", rowReach, "2 timeout 5000 - 0 - 0")))
	f.Add([]byte(report(noJobID, "fail", "fail")))
	f.Add([]byte(report(testJobID, "ok", "open", "3 refused 7 - 0 - 0", "4 ok 1 reset 2 - 0", "5 ok 1 ok 2 freeze 100")))
	f.Add([]byte("mgp 1\nunknown field\nend\n"))
	f.Add([]byte(strings.ReplaceAll(report(testJobID, "ok", "blocked", rowReach), "\n", "\r\n")))
	f.Fuzz(func(t *testing.T, body []byte) {
		got, err := ParseSync(body)
		if err != nil {
			return
		}
		if formatReport(got) != string(body) {
			t.Fatalf("accepted %q, canonical form is %q", body, formatReport(got))
		}
		if !lowerHex(got.Version, 12) || !lowerHex(got.JobID, 16) || len(got.Results) > maxTargets {
			t.Fatalf("accepted invalid identifiers: %+v", got)
		}
		last := 0
		for _, r := range got.Results {
			if r.Index <= last || r.Index > maxTargets || r.TCPMS > 120000 || r.TLSMS > 120000 || r.Bytes > 65536 {
				t.Fatalf("accepted an out-of-range result: %+v", got.Results)
			}
			last = r.Index
		}
	})
}

func TestEnrollmentSingleUseExpiryLimitHashOnlyAndRevoke(t *testing.T) {
	f := probeFixture(t)
	h := f.PublicHandler("")
	ctx := context.Background()
	tokenHash := sha256.Sum256([]byte(testToken))
	if err := f.st.CreateOwnProbe(ctx, store.OwnProbe{ID: "prb_de1", Name: "probe-de1", City: "de1", Kind: "home",
		TokenHash: tokenHash[:], TokenExpires: f.now.Add(24 * time.Hour), CreatedAt: f.now, CreatedBy: "adm_test"}); err != nil {
		t.Fatal(err)
	}
	w := post(h, "/enroll", homeIP, "", testToken+"\n")
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Body.String(), "mgp 1\nid prb_de1\nsecret mgps_") || !strings.HasSuffix(w.Body.String(), "\nend\n") {
		t.Fatalf("enroll = %d %q", w.Code, w.Body.String())
	}
	secret := strings.TrimPrefix(strings.Split(w.Body.String(), "\n")[2], "secret ")
	if len(secret) != len("mgps_")+43 { // 256 bits, base64url
		t.Fatalf("secret %q is not 256 bits", secret)
	}
	var storedToken, storedSecret []byte
	if err := f.st.R.QueryRowContext(ctx, `SELECT token_hash, secret_hash FROM own_probe WHERE id = ?`, "prb_de1").Scan(&storedToken, &storedSecret); err != nil {
		t.Fatal(err)
	}
	secretHash := sha256.Sum256([]byte(secret))
	if len(storedToken) != 0 || !bytes.Equal(storedSecret, secretHash[:]) {
		t.Fatalf("stored credentials are not the hash of the secret and nothing else: token=%x secret=%x", storedToken, storedSecret)
	}
	if again := post(h, "/enroll", homeIP, "", testToken); again.Code != http.StatusNotFound {
		t.Fatalf("second enrolment = %d, want the decoy's 404", again.Code)
	}
	expired := "mgp_" + strings.Repeat("c", 52)
	expiredHash := sha256.Sum256([]byte(expired))
	if err := f.st.CreateOwnProbe(ctx, store.OwnProbe{ID: "prb_fi1", Name: "probe-fi1", City: "fi1", Kind: "mobile",
		TokenHash: expiredHash[:], TokenExpires: f.now.Add(-time.Second), CreatedAt: f.now, CreatedBy: "adm_test"}); err != nil {
		t.Fatal(err)
	}
	if r := post(h, "/enroll", "192.0.2.11", "", expired); r.Code != http.StatusNotFound {
		t.Fatalf("expired token = %d, want 404", r.Code)
	}
	for name, body := range map[string]string{"empty": "", "short": "mgp_abc", "other prefix": "tok_" + strings.Repeat("a", 52),
		"line break inside": "mgp_" + strings.Repeat("a", 26) + "\n" + strings.Repeat("a", 25), "huge": strings.Repeat("a", 5000)} {
		if r := post(h, "/enroll", "192.0.2.12", "", body); r.Code != http.StatusNotFound {
			t.Errorf("enrol %s = %d, want 404", name, r.Code)
		}
	}
	// the tenth failure is the last that gets the 404; the next ones get the decoy site's 429
	var limited *httptest.ResponseRecorder
	for range 12 {
		r := post(h, "/enroll", "192.0.2.20", "", "mgp_"+strings.Repeat("z", 52))
		if r.Code == http.StatusTooManyRequests {
			limited = r
			break
		}
	}
	if limited == nil || !strings.Contains(limited.Body.String(), "Too Many Requests") || limited.Header().Get("Retry-After") == "" {
		t.Fatalf("per-source failure limit did not answer with the decoy's 429: %+v", limited)
	}
	// even the right token is refused from a source that is locked out
	live := "mgp_" + strings.Repeat("d", 52)
	liveHash := sha256.Sum256([]byte(live))
	if err := f.st.CreateOwnProbe(ctx, store.OwnProbe{ID: "prb_nl1", Name: "probe-nl1", City: "nl1", Kind: "home",
		TokenHash: liveHash[:], TokenExpires: f.now.Add(time.Hour), CreatedAt: f.now, CreatedBy: "adm_test"}); err != nil {
		t.Fatal(err)
	}
	if r := post(h, "/enroll", "192.0.2.20", "", live); r.Code != http.StatusTooManyRequests {
		t.Fatalf("locked-out source = %d", r.Code)
	}
	if r := post(h, "/enroll", "192.0.2.21", "", live); r.Code != http.StatusOK {
		t.Fatalf("another source = %d", r.Code)
	}

	// revocation is immediate: the very next sync is a stranger's
	f.enable(t)
	if r := post(h, "/sync", homeIP, "Bearer "+secret, report(noJobID, "ok", "blocked")); r.Code != http.StatusOK {
		t.Fatalf("sync before revoking = %d %q", r.Code, r.Body.String())
	}
	if err := f.st.RevokeOwnProbe(ctx, "prb_de1"); err != nil {
		t.Fatal(err)
	}
	if r := post(h, "/sync", homeIP, "Bearer "+secret, report(noJobID, "ok", "blocked")); r.Code != http.StatusNotFound {
		t.Fatalf("revoked sync = %d, want the decoy's 404", r.Code)
	}
}

func TestEnrollmentTokenIsConsumedOnceUnderRace(t *testing.T) {
	f := probeFixture(t)
	h := f.PublicHandler("")
	tokenHash := sha256.Sum256([]byte(testToken))
	if err := f.st.CreateOwnProbe(context.Background(), store.OwnProbe{ID: "prb_de1", Name: "probe-de1", City: "de1", Kind: "home",
		TokenHash: tokenHash[:], TokenExpires: f.now.Add(time.Hour), CreatedAt: f.now, CreatedBy: "adm_test"}); err != nil {
		t.Fatal(err)
	}
	const racers = 8
	codes := make(chan int, racers)
	secrets := make(chan string, racers)
	var wg sync.WaitGroup
	for i := range racers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := post(h, "/enroll", fmt.Sprintf("192.0.2.%d", 100+i), "", testToken)
			codes <- w.Code
			if w.Code == http.StatusOK {
				secrets <- w.Body.String()
			}
		}()
	}
	wg.Wait()
	close(codes)
	close(secrets)
	won := 0
	for code := range codes {
		if code == http.StatusOK {
			won++
		} else if code != http.StatusNotFound {
			t.Errorf("a loser got %d", code)
		}
	}
	if won != 1 || len(secrets) != 1 {
		t.Fatalf("%d enrolments of one token succeeded, want exactly one", won)
	}
}

// What a stranger sees is the site: the same status, headers and body for an unknown path outside the prefix, an unknown
// subscription token, an unknown path or method under the probe prefix, a wrong enrolment token, no secret, a wrong secret and a
// revoked one. Measured through the public listener, next to the real subscription handler.
type noToken struct{}

func (noToken) Subscription(context.Context, string) (access.SubView, error) {
	return access.SubView{}, access.ErrUnknownToken
}

func TestStrangersGetTheSameSiteAsEveryOtherSecretPath(t *testing.T) {
	f := probeFixture(t)
	f.enable(t)
	_, secret := f.probe(t, "prb_de1", "de1")
	if err := f.st.RevokeOwnProbe(context.Background(), "prb_de1"); err != nil {
		t.Fatal(err)
	}
	const subPrefix = "/bbbbbbbbbbbbbbbbbbbbbbbb/"
	srv, err := httpserver.NewPublic(httpserver.Config{
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Limits: httpserver.Limits{Public: httpserver.Rate{PerSecond: -1}},
		PublicMounts: map[string]http.Handler{
			subPrefix:         subs.Handler(noToken{}, httpserver.NewDecoy(""), subs.Config{Title: "t", MissLimit: 6}),
			f.cfg.ProbePrefix: f.PublicHandler(""),
		},
	}, f.st)
	if err != nil {
		t.Fatal(err)
	}
	site := httptest.NewServer(srv.Public())
	defer site.Close()
	type response struct {
		status int
		header http.Header
		body   string
	}
	do := func(method, path, authorization, body string) response {
		req, _ := http.NewRequest(method, site.URL+path, strings.NewReader(body))
		if authorization != "" {
			req.Header.Set("Authorization", authorization)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		res.Header.Del("Date")
		return response{res.StatusCode, res.Header, string(b)}
	}
	p := f.cfg.ProbePrefix
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		want := do(method, "/"+strings.Repeat("u", 24)+"/sync", "", "")
		if want.status != http.StatusNotFound {
			t.Fatalf("%s: the reference answer is %d", method, want.status)
		}
		cases := map[string]response{
			"unknown subscription token": do(method, subPrefix+strings.Repeat("u", 43), "", ""),
			"unknown path under prefix":  do(method, p+"nothing", "", ""),
			"bare prefix":                do(method, p, "", ""),
			"enroll, no body":            do(method, p+"enroll", "", ""),
			"sync, no secret":            do(method, p+"sync", "", ""),
			"sync, wrong secret":         do(method, p+"sync", "Bearer mgps_"+strings.Repeat("x", 43), report(noJobID, "ok", "blocked")),
			"sync, revoked secret":       do(method, p+"sync", "Bearer "+secret, report(noJobID, "ok", "blocked")),
			"sync, malformed bearer":     do(method, p+"sync", "Basic abc", ""),
		}
		if method == http.MethodGet {
			cases["probe.sh, wrong method"] = do(http.MethodPost, p+"probe.sh", "", "")
		} else {
			cases["enroll, wrong token"] = do(method, p+"enroll", "", "mgp_"+strings.Repeat("q", 52))
			cases["enroll, bad token"] = do(method, p+"enroll", "", "x")
			cases["sync, GET"] = do(http.MethodGet, p+"sync", "Bearer "+secret, "")
		}
		for name, got := range cases {
			if got.status != want.status || got.body != want.body || fmt.Sprint(got.header) != fmt.Sprint(want.header) {
				t.Errorf("%s %s differs from an unknown path:\n got  %d %v %q\n want %d %v %q", method, name, got.status, got.header, got.body, want.status, want.header, want.body)
			}
		}
	}
	// the script itself is public under the prefix
	if got := do(http.MethodGet, p+"probe.sh", "", ""); got.status != http.StatusOK || got.header.Get("Etag") != `"`+ScriptHash()+`"` {
		t.Fatalf("probe.sh = %d %v", got.status, got.header)
	}
}

// Everything the answer can say is a line of one of the shapes the probe's shell matches with `case`; a target whose parts are not
// in those shapes is left out, never sent.
var (
	ipv4Re = `(?:[0-9]{1,3}\.){3}[0-9]{1,3}`
	sniRe  = `[A-Za-z0-9](?:[A-Za-z0-9.-]{0,251}[A-Za-z0-9])?`
	pathRe = `/[A-Za-z0-9._~/-]{0,128}`
	lineRe = regexp.MustCompile(`^(?:mgp 1|next [0-9]{3,4}|state (?:active|paused)|job [0-9a-f]{16}|ctl ` + ipv4Re + ` 443 ` + sniRe +
		`|ref ` + ipv4Re + ` 443|t [0-9]{1,2} ` + ipv4Re + ` [0-9]{1,5} (?:tcp|tls|fetch) (?:-|` + sniRe + `) (?:-|` + pathRe + `)|end)$`)
)

func checkAnswerShape(t *testing.T, body string) []string {
	t.Helper()
	if !strings.HasSuffix(body, "\n") || strings.Contains(body, "\r") {
		t.Fatalf("answer does not end in exactly one newline: %q", body)
	}
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	for i, line := range lines {
		if !lineRe.MatchString(line) {
			t.Errorf("line %d %q has a shape the probe does not expect", i+1, line)
		}
	}
	var targets []string
	for _, line := range lines {
		if strings.HasPrefix(line, "t ") {
			targets = append(targets, line)
		}
	}
	return targets
}

func TestAnswerHasOnlyTheDeclaredShapesAndSkipsDirtyTargets(t *testing.T) {
	good := Target{NodeID: "nod_de1", IP: "203.0.113.12", Port: 443, Kind: "tls", SNI: "Target.example.test", Path: "/xhttp-path_1.x~"}
	f := probeFixture(t)
	f.enable(t)
	dirty := []Target{
		{NodeID: "nod_a", IP: "203.0.113.20", Port: 443, Kind: "tls", SNI: "$(touch /tmp/x).example.test", Path: "/"},
		{NodeID: "nod_a", IP: "203.0.113.20", Port: 443, Kind: "tls", SNI: "a;b.example.test", Path: "/"},
		{NodeID: "nod_a", IP: "203.0.113.20", Port: 443, Kind: "tls", SNI: "`id`.example.test", Path: "/"},
		{NodeID: "nod_a", IP: "203.0.113.20", Port: 443, Kind: "tls", SNI: "a.example.test\nt 9 203.0.113.9 1 tcp - -", Path: "/"},
		{NodeID: "nod_a", IP: "203.0.113.20", Port: 443, Kind: "tls", SNI: "a.example.test\x00", Path: "/"},
		{NodeID: "nod_a", IP: "203.0.113.20", Port: 443, Kind: "tls", SNI: "--output=/tmp/x", Path: "/"},
		{NodeID: "nod_a", IP: "203.0.113.20", Port: 443, Kind: "tls", SNI: "-a.example.test", Path: "/"},
		{NodeID: "nod_a", IP: "203.0.113.20", Port: 443, Kind: "tls", SNI: "a b.example.test", Path: "/"},
		{NodeID: "nod_a", IP: "203.0.113.20", Port: 443, Kind: "tls", SNI: "пример.example.test", Path: "/"},
		{NodeID: "nod_a", IP: "203.0.113.20", Port: 443, Kind: "tls", SNI: "a.example.test.", Path: "/"},
		{NodeID: "nod_a", IP: "203.0.113.20", Port: 443, Kind: "tls", SNI: "a_b.example.test", Path: "/"},
		{NodeID: "nod_a", IP: "203.0.113.20", Port: 443, Kind: "tls", SNI: "192.0.2.5", Path: "/"},
		{NodeID: "nod_a", IP: "203.0.113.20", Port: 443, Kind: "tls", SNI: strings.Repeat("a", 64) + ".example.test", Path: "/"},
		{NodeID: "nod_a", IP: "203.0.113.20", Port: 443, Kind: "tls", SNI: strings.Repeat("a.", 130) + "test", Path: "/"},
		{NodeID: "nod_a", IP: "203.0.113.20", Port: 443, Kind: "tls", SNI: "", Path: "/"},
		{NodeID: "nod_a", IP: "203.0.113.20", Port: 443, Kind: "fetch", SNI: "ok.example.test", Path: "/a b"},
		{NodeID: "nod_a", IP: "203.0.113.20", Port: 443, Kind: "fetch", SNI: "ok.example.test", Path: "/a?x=1"},
		{NodeID: "nod_a", IP: "203.0.113.20", Port: 443, Kind: "fetch", SNI: "ok.example.test", Path: "/$(id)"},
		{NodeID: "nod_a", IP: "203.0.113.20", Port: 443, Kind: "fetch", SNI: "ok.example.test", Path: "a"},
		{NodeID: "nod_a", IP: "203.0.113.20", Port: 443, Kind: "fetch", SNI: "ok.example.test", Path: "/" + strings.Repeat("a", 129)},
		{NodeID: "nod_a", IP: "203.0.113.20", Port: 443, Kind: "tcp", SNI: "stray.example.test"},
		{NodeID: "nod_a", IP: "203.0.113.20", Port: 443, Kind: "tcp", Path: "/stray"},
		{NodeID: "nod_a", IP: "203.0.113.20", Port: 443, Kind: "quic", SNI: "ok.example.test"},
		{NodeID: "nod_a", IP: "203.0.113.20", Port: 443, Kind: "tls; touch x", SNI: "ok.example.test"},
		{NodeID: "nod_a", IP: "203.0.113.20", Port: 0, Kind: "tcp"},
		{NodeID: "nod_a", IP: "10.0.0.5", Port: 443, Kind: "tcp"},
		{NodeID: "nod_a", IP: "127.0.0.1", Port: 443, Kind: "tcp"},
		{NodeID: "nod_a", IP: "169.254.1.1", Port: 443, Kind: "tcp"},
		{NodeID: "nod_a", IP: "100.64.0.1", Port: 443, Kind: "tcp"},
		{NodeID: "nod_a", IP: "198.18.0.1", Port: 443, Kind: "tcp"},
		{NodeID: "nod_a", IP: "0.1.2.3", Port: 443, Kind: "tcp"},
		{NodeID: "nod_a", IP: "240.0.0.1", Port: 443, Kind: "tcp"},
		{NodeID: "nod_a", IP: "224.0.0.1", Port: 443, Kind: "tcp"},
		{NodeID: "nod_a", IP: "203.000.113.020", Port: 443, Kind: "tcp"},
		{NodeID: "nod_a", IP: "2001:db8::1", Port: 443, Kind: "tcp"},
		{NodeID: "nod_a", IP: "203.0.113.20 ", Port: 443, Kind: "tcp"},
		{NodeID: "nod_a", IP: "203.0.113.20;id", Port: 443, Kind: "tcp"},
		{NodeID: "", IP: "203.0.113.20", Port: 443, Kind: "tcp"},
	}
	f.targets = append(append([]Target{}, dirty...), good,
		Target{NodeID: "nod_nl1", IP: "203.0.113.21", Port: 8443, Kind: "tcp"},
		Target{NodeID: "nod_nl1", IP: "203.0.113.21", Port: 443, Kind: "fetch", SNI: "ok.example.test", Path: "/"},
		Target{NodeID: "nod_nl1", IP: "203.0.113.21", Port: 2053, Kind: "tls", SNI: "ok.example.test"}) // a third target of a node: left out
	_, secret := f.probe(t, "prb_de1", "de1")
	h := f.PublicHandler("")
	w := post(h, "/sync", homeIP, "Bearer "+secret, report(noJobID, "ok", "blocked"))
	if w.Code != http.StatusOK {
		t.Fatalf("sync = %d %q", w.Code, w.Body.String())
	}
	targets := checkAnswerShape(t, w.Body.String())
	want := []string{
		"t 1 203.0.113.12 443 tls Target.example.test /xhttp-path_1.x~",
		"t 2 203.0.113.21 8443 tcp - -",
		"t 3 203.0.113.21 443 fetch ok.example.test /",
	}
	if strings.Join(targets, "|") != strings.Join(want, "|") {
		t.Fatalf("targets:\n got %q\nwant %q\nanswer %q", targets, want, w.Body.String())
	}
	// a control host name the shell would not match pauses the probe: the panel answers 503 rather than a line the probe rejects
	for _, name := range []string{"", "a;b.example.test", "$(id)", "x y"} {
		g := probeFixture(t, func(c *Config) {
			c.References = func(string) (References, bool) {
				return References{ControlIP: "192.0.2.1", ControlSNI: name, BlockedIP: "198.51.100.1"}, true
			}
		})
		g.enable(t)
		_, s := g.probe(t, "prb_de1", "de1")
		if r := post(g.PublicHandler(""), "/sync", homeIP, "Bearer "+s, report(noJobID, "ok", "blocked")); r.Code != http.StatusServiceUnavailable {
			t.Errorf("control name %q: sync = %d %q", name, r.Code, r.Body.String())
		}
	}
	// the references of a country that has none: paused as well
	g := probeFixture(t, func(c *Config) { c.References = func(string) (References, bool) { return References{}, false } })
	g.enable(t)
	_, s := g.probe(t, "prb_de1", "de1")
	if r := post(g.PublicHandler(""), "/sync", homeIP, "Bearer "+s, report(noJobID, "ok", "blocked")); r.Code != http.StatusServiceUnavailable || r.Header().Get("Retry-After") == "" {
		t.Errorf("no references: sync = %d", r.Code)
	}
}

func TestPausedAnswersCarryNoJob(t *testing.T) {
	f := probeFixture(t) // probes are off
	p, secret := f.probe(t, "prb_de1", "de1")
	f.setJob(t, p.ID, testJobID, defaultTargets)
	w := post(f.PublicHandler(""), "/sync", homeIP, "Bearer "+secret, report(noJobID, "ok", "blocked"))
	if w.Code != http.StatusOK || checkAnswerShape(t, w.Body.String()) != nil || !strings.Contains(w.Body.String(), "state paused\njob "+noJobID+"\n") {
		t.Fatalf("answer while off = %d %q", w.Code, w.Body.String())
	}
	f.enable(t)
	w = post(f.PublicHandler(""), "/sync", homeIP, "Bearer "+secret, report(noJobID, "ok", "blocked"))
	if !strings.Contains(w.Body.String(), "state active\njob "+testJobID+"\n") || len(checkAnswerShape(t, w.Body.String())) != 1 {
		t.Fatalf("answer while on = %q", w.Body.String())
	}
}

// A probe is given only the kinds its version is known to run; a kind that is new to the protocol never reaches a script that
// would reject the whole answer for it.
func TestKindsFollowTheProbeVersion(t *testing.T) {
	kindsByVersion["0123456789ab"] = []string{"quic"}
	t.Cleanup(func() { delete(kindsByVersion, "0123456789ab") })
	f := probeFixture(t)
	f.enable(t)
	f.targets = []Target{
		{NodeID: "nod_de1", IP: "203.0.113.12", Port: 443, Kind: "tls", SNI: "a.example.test"},
		{NodeID: "nod_de1", IP: "203.0.113.12", Port: 8443, Kind: "quic", SNI: "a.example.test"},
	}
	_, oldSecret := f.probe(t, "prb_old", "old")
	_, newSecret := f.probe(t, "prb_new", "new")
	h := f.PublicHandler("")
	oldAnswer := post(h, "/sync", homeIP, "Bearer "+oldSecret, report(noJobID, "ok", "blocked"))
	if got := strings.Count(oldAnswer.Body.String(), "\nt "); got != 1 || strings.Contains(oldAnswer.Body.String(), "quic") {
		t.Fatalf("an old probe was given %d targets: %q", got, oldAnswer.Body.String())
	}
	newAnswer := post(h, "/sync", homeIP, "Bearer "+newSecret, reportFrom("0123456789ab", noJobID, "ok", "blocked"))
	if got := strings.Count(newAnswer.Body.String(), "\nt "); got != 2 || !strings.Contains(newAnswer.Body.String(), " quic ") {
		t.Fatalf("a probe that runs quic was given %d targets: %q", got, newAnswer.Body.String())
	}
	// a job saved for a version that is no longer the probe's is dropped and made again
	f.advance(time.Second)
	f.setJob(t, "prb_old", testJobID, f.targets)
	answer := post(h, "/sync", homeIP, "Bearer "+oldSecret, report(noJobID, "ok", "blocked"))
	if strings.Contains(answer.Body.String(), "quic") || strings.Contains(answer.Body.String(), "job "+testJobID) {
		t.Fatalf("a job with a kind the probe cannot run was sent: %q", answer.Body.String())
	}
}

func TestReportsAreMatchedToTheSavedJobOnly(t *testing.T) {
	f := probeFixture(t)
	f.enable(t)
	p, secret := f.probe(t, "prb_nl1", "nl1")
	h := f.PublicHandler("")
	f.setJob(t, p.ID, testJobID, defaultTargets)
	count := func() int {
		rows, err := f.st.RecentExtChecks(context.Background(), 10, countryreach.SourceProbes)
		if err != nil {
			t.Fatal(err)
		}
		return len(rows)
	}
	// another job id: answered with the job the panel has, nothing folded
	if r := post(h, "/sync", homeIP, "Bearer "+secret, report("fedcba9876543210", "ok", "blocked", rowReach)); r.Code != http.StatusOK || !strings.Contains(r.Body.String(), "job "+testJobID) {
		t.Fatalf("foreign job: %d %q", r.Code, r.Body.String())
	}
	if got, _ := f.st.OwnProbe(context.Background(), p.ID); got.JobID != testJobID || !got.LastResult.IsZero() || count() != 0 {
		t.Fatalf("a report of another job changed something: %+v rows=%d", got, count())
	}
	// the job zero is never a job
	if r := post(h, "/sync", homeIP, "Bearer "+secret, report(noJobID, "ok", "blocked")); r.Code != http.StatusOK || count() != 0 {
		t.Fatalf("job zero: %d rows=%d", r.Code, count())
	}
	// an index the job does not have, and a result shape the target's kind cannot give: the whole report is refused
	for name, row := range map[string]string{"index 2 of 1": "2 ok 10 ok 20 - 0", "a page for a tls target": rowReach} {
		if r := post(h, "/sync", homeIP, "Bearer "+secret, report(testJobID, "ok", "blocked", row)); r.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, r.Code)
		}
	}
	if got, _ := f.st.OwnProbe(context.Background(), p.ID); got.JobID != testJobID || count() != 0 {
		t.Fatalf("a refused report changed something: %+v rows=%d", got, count())
	}
	// the right one
	if r := post(h, "/sync", homeIP, "Bearer "+secret, report(testJobID, "ok", "blocked", rowTLSOnly)); r.Code != http.StatusOK || count() != 1 {
		t.Fatalf("the report of the saved job: %d rows=%d", r.Code, count())
	}
	got, _ := f.st.OwnProbe(context.Background(), p.ID)
	if got.JobID != "" || got.LastResult.IsZero() || got.ChecksDay != 1 || got.Version != "abcdef012345" {
		t.Fatalf("job not consumed: %+v", got)
	}
	// a replay of it is a report of a job nobody waits for
	if r := post(h, "/sync", homeIP, "Bearer "+secret, report(testJobID, "ok", "blocked", rowTLSOnly)); r.Code != http.StatusOK {
		t.Fatalf("replay: %d", r.Code)
	}
	if got, _ := f.st.OwnProbe(context.Background(), p.ID); got.ChecksDay != 1 {
		t.Fatalf("a replay counted: %+v", got)
	}
	// switched off: the job is not folded
	f.setJob(t, p.ID, "1111111111111111", defaultTargets)
	if err := f.st.SetSettings(context.Background(), map[string]string{settingsEnabled: "false"}); err != nil {
		t.Fatal(err)
	}
	before := count()
	post(h, "/sync", homeIP, "Bearer "+secret, report("1111111111111111", "ok", "blocked", rowTLSOnly))
	if got, _ := f.st.OwnProbe(context.Background(), p.ID); got.JobID != "1111111111111111" || count() != before {
		t.Fatalf("a report folded while the probes are off: %+v", got)
	}
}

func TestAnOutstandingJobExpiresAndANewOneIsMade(t *testing.T) {
	f := probeFixture(t)
	f.enable(t)
	_, secret := f.probe(t, "prb_nl1", "nl1")
	h := f.PublicHandler("")
	first := post(h, "/sync", homeIP, "Bearer "+secret, report(noJobID, "ok", "blocked")).Body.String()
	job := regexp.MustCompile(`job ([0-9a-f]{16})`).FindStringSubmatch(first)
	if job == nil || job[1] == noJobID || !strings.Contains(first, "\nt 1 ") {
		t.Fatalf("first answer has no job: %q", first)
	}
	f.advance(10 * time.Minute) // the probe is silent but its job is not due yet: the same one
	if again := post(h, "/sync", homeIP, "Bearer "+secret, report(noJobID, "ok", "blocked")).Body.String(); !strings.Contains(again, "job "+job[1]) {
		t.Fatalf("job changed before its time: %q", again)
	}
	f.advance(2 * time.Hour) // past the interval: it is replaced
	if later := post(h, "/sync", homeIP, "Bearer "+secret, report(noJobID, "ok", "blocked")).Body.String(); strings.Contains(later, "job "+job[1]) || !strings.Contains(later, "\nt 1 ") {
		t.Fatalf("an unanswered job lived on: %q", later)
	}
}

func extRows(t *testing.T, f *fixture) map[string]countryreach.ExtDetail {
	t.Helper()
	rows, err := f.st.RecentExtChecks(context.Background(), 1, countryreach.SourceProbes)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]countryreach.ExtDetail{}
	for _, row := range rows {
		var d countryreach.ExtDetail
		if err := json.Unmarshal([]byte(row.DetailJSON), &d); err != nil {
			t.Fatal(err)
		}
		out[row.NodeID] = d
	}
	return out
}

func stateOf(d countryreach.ExtDetail, id string) string {
	for _, v := range d.Vantages {
		if v.Key == "probe:"+id {
			return v.State
		}
	}
	return ""
}

func TestFoldReplacesAPointKeepsOthersAndForgetsTheStale(t *testing.T) {
	f := probeFixture(t)
	ctx := context.Background()
	first, _ := f.probe(t, "prb_de1", "de1")
	second, _ := f.probe(t, "prb_nl1", "nl1")
	target := defaultTargets[0]
	interval := time.Hour
	dark := SyncReport{JobID: testJobID, Control: "ok", Reference: "blocked", Results: []SyncResult{{Index: 1, TCP: "timeout", TLS: "-", Fetch: "-"}}}
	reach := SyncReport{JobID: testJobID, Control: "ok", Reference: "blocked", Results: []SyncResult{{Index: 1, TCP: "ok", TLS: "ok", Fetch: "-"}}}
	fold := func(p store.OwnProbe, r SyncReport) {
		t.Helper()
		f.mu.Lock()
		defer f.mu.Unlock()
		if err := f.foldReport(ctx, p, r, []Target{target}, f.now, interval); err != nil {
			t.Fatal(err)
		}
	}
	fold(first, dark)
	f.advance(time.Minute)
	fold(second, reach)
	rows := extRows(t, f)
	if d := rows["nod_de1"]; len(d.Vantages) != 2 || stateOf(d, first.ID) != "dark" || stateOf(d, second.ID) != "reach" {
		t.Fatalf("two points: %+v", d)
	}
	if row, _ := f.st.RecentExtChecks(ctx, 1, countryreach.SourceProbes); row[0].Verdict != countryreach.VerdictPartial {
		t.Fatalf("verdict %q, want partial", row[0].Verdict)
	}
	// the first probe again, now reaching: its dark point is replaced, not kept beside the new one
	f.advance(time.Minute)
	fold(first, reach)
	if d := extRows(t, f)["nod_de1"]; len(d.Vantages) != 2 || stateOf(d, first.ID) != "reach" {
		t.Fatalf("a probe's old point stayed beside its new one: %+v", d)
	}
	// no control: the point is "unknown", the results are not read
	f.advance(time.Minute)
	fold(first, SyncReport{JobID: testJobID, Control: "fail", Reference: "blocked", Results: dark.Results})
	if d := extRows(t, f)["nod_de1"]; stateOf(d, first.ID) != "uncertain" || d.Vantages[len(d.Vantages)-1].Why != "no_control" {
		t.Fatalf("no control: %+v", d)
	}
	// stale points and points of probes that are not active do not count
	d := countryreach.ExtDetail{Vantages: []countryreach.Vantage{
		{Key: "probe:" + first.ID, State: "dark", At: f.now.Add(-4 * interval).Unix()},
		{Key: "probe:" + second.ID, State: "reach", At: f.now.Add(-time.Minute).Unix()},
		{Key: "probe:prb_gone", State: "dark", At: f.now.Unix()},
		{Key: "probe:" + first.ID + "x", State: "dark", At: f.now.Add(time.Hour).Unix()},
	}}
	raw, _ := json.Marshal(d)
	live := liveVantages(store.ExtCheck{At: f.now, DetailJSON: string(raw)}, map[string]bool{first.ID: true, second.ID: true}, f.now, interval, "")
	if len(live) != 1 || live[0].Key != "probe:"+second.ID {
		t.Fatalf("staleness filter = %+v", live)
	}
}

func TestPointVerdictsOfAProbe(t *testing.T) {
	probe := store.OwnProbe{ID: "prb_de1", Name: "probe-de1", City: "de1", Kind: "home", Operator: "ExampleNet", ASN: 64512}
	tcp := Target{Kind: "tcp"}
	tls := Target{Kind: "tls", SNI: "a.example.test"}
	fetch := Target{Kind: "fetch", SNI: "a.example.test"}
	r := func(tcpWord, tlsWord, fetchWord string) SyncResult {
		return SyncResult{TCP: tcpWord, TLS: tlsWord, Fetch: fetchWord}
	}
	for name, c := range map[string]struct {
		items     []targetResult
		reference string
		state     string
		why       string
	}{
		"tls ok":                      {[]targetResult{{tls, r("ok", "ok", "-")}}, "blocked", "reach", ""},
		"tcp ok alone proves nothing": {[]targetResult{{tcp, r("ok", "-", "-")}}, "blocked", "uncertain", ""},
		"tcp ok, tls reset":           {[]targetResult{{tcp, r("ok", "-", "-")}, {tls, r("ok", "reset", "-")}}, "blocked", "dark", "tls_reset"},
		"tcp timeout":                 {[]targetResult{{tcp, r("timeout", "-", "-")}}, "blocked", "dark", "tcp_timeout"},
		"unreach":                     {[]targetResult{{tls, r("unreach", "-", "-")}}, "blocked", "dark", "tcp_timeout"},
		"tls silent":                  {[]targetResult{{tls, r("ok", "timeout", "-")}}, "blocked", "dark", "tls_timeout"},
		"freeze":                      {[]targetResult{{fetch, r("ok", "ok", "freeze")}}, "blocked", "dark", "freeze"},
		"full page":                   {[]targetResult{{fetch, r("ok", "ok", "full")}}, "blocked", "reach", ""},
		"refused is the node's":       {[]targetResult{{tls, r("refused", "-", "-")}}, "blocked", "uncertain", "tcp_reset"},
		"alert is no verdict":         {[]targetResult{{tls, r("ok", "alert", "-")}}, "blocked", "uncertain", ""},
		"one target reaches":          {[]targetResult{{tls, r("timeout", "-", "-")}, {fetch, r("ok", "ok", "full")}}, "blocked", "reach", ""},
		"reference open, reach":       {[]targetResult{{tls, r("ok", "ok", "-")}}, "open", "uncertain", "no_filter"},
		"reference open, dark":        {[]targetResult{{tls, r("timeout", "-", "-")}}, "open", "dark", "tcp_timeout"},
		"reference open, freeze":      {[]targetResult{{fetch, r("ok", "ok", "freeze")}}, "open", "dark", "freeze"},
		"reference open, tcp only":    {[]targetResult{{tcp, r("ok", "-", "-")}}, "open", "uncertain", ""},
	} {
		v := judgeProbe(probe, c.items, c.reference, time.Unix(1_700_000_000, 0))
		if v.State != c.state || v.Why != c.why || v.Key != "probe:prb_de1" || v.Kind != "home" || v.At != 1_700_000_000 {
			t.Errorf("%s: %+v", name, v)
		}
	}
}

// A revocation or a network change takes the point out of every recent row, not just the newest: the alerts read ten.
func TestRefoldRewritesTheHistoryInPlace(t *testing.T) {
	f := probeFixture(t)
	ctx := context.Background()
	a, _ := f.probe(t, "prb_de1", "de1")
	b, _ := f.probe(t, "prb_nl1", "nl1")
	mk := func(id string, at time.Time, state string) countryreach.Vantage {
		return countryreach.Vantage{Key: "probe:" + id, State: state, At: at.Unix(), Kind: "home"}
	}
	var rows []store.ExtCheck
	for i := range 3 {
		at := f.now.Add(time.Duration(i-5) * time.Minute)
		check, err := extCheck("nod_de1", []countryreach.Vantage{mk(a.ID, at, "dark"), mk(b.ID, at, "reach")}, at)
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, check)
	}
	if err := f.st.InsertExtChecks(ctx, rows); err != nil {
		t.Fatal(err)
	}
	if err := f.st.RevokeOwnProbe(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	err := f.refold(ctx)
	f.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	all, err := f.st.RecentExtChecks(ctx, 10, countryreach.SourceProbes)
	if err != nil || len(all) != 3 {
		t.Fatalf("rows after refold = %d, %v (rewritten in place, none added)", len(all), err)
	}
	for _, row := range all {
		var d countryreach.ExtDetail
		_ = json.Unmarshal([]byte(row.DetailJSON), &d)
		if len(d.Vantages) != 1 || d.Vantages[0].Key != "probe:"+b.ID || row.Verdict != countryreach.VerdictOK {
			t.Fatalf("revoked point left in the history: %+v", row)
		}
	}
	// the last one goes: nothing of the source is left
	if err := f.st.RevokeOwnProbe(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	err = f.refold(ctx)
	f.mu.Unlock()
	if left, _ := f.st.RecentExtChecks(ctx, 10, countryreach.SourceProbes); err != nil || len(left) != 0 {
		t.Fatalf("rows left with no probe: %d, %v", len(left), err)
	}
}

func TestNetworkPinPausesUntilConfirmedAndRemovesThePoints(t *testing.T) {
	f := probeFixture(t)
	f.enable(t)
	p, secret := f.probe(t, "prb_nl1", "nl1")
	h := f.PublicHandler("")
	f.setJob(t, p.ID, testJobID, defaultTargets)
	detail, _ := json.Marshal(countryreach.ExtDetail{Vantages: []countryreach.Vantage{{Key: "probe:" + p.ID, Kind: "home", State: "reach", At: f.now.Unix()}}})
	if err := f.st.InsertExtChecks(context.Background(), []store.ExtCheck{{NodeID: "nod_de1", Source: countryreach.SourceProbes, At: f.now, Verdict: "ok", DetailJSON: string(detail)}}); err != nil {
		t.Fatal(err)
	}
	// the same secret from another operator: network_changed, job gone, point gone, results ignored
	w := post(h, "/sync", otherIP, "Bearer "+secret, report(testJobID, "ok", "blocked", rowTLSOnly))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "state paused\njob "+noJobID+"\n") {
		t.Fatalf("network-change sync = %d %q", w.Code, w.Body.String())
	}
	got, _ := f.st.OwnProbe(context.Background(), p.ID)
	if got.State != "network_changed" || got.PinnedOperator != "ExampleNet" || got.Operator != "OtherNet" || got.JobID != "" || !got.LastResult.IsZero() {
		t.Fatalf("after the change: %+v", got)
	}
	if rows, _ := f.st.RecentExtChecks(context.Background(), 1, countryreach.SourceProbes); len(rows) != 0 {
		t.Fatalf("the point stayed: %+v", rows)
	}
	// back on the pinned network it stays paused: only the owner restores it
	if w := post(h, "/sync", homeIP, "Bearer "+secret, report(noJobID, "ok", "blocked")); !strings.Contains(w.Body.String(), "state paused") {
		t.Fatalf("a probe that moved came back by itself: %q", w.Body.String())
	}
	ctx := auth.WithAdmin(context.Background(), store.Admin{ID: "adm_owner", Role: store.RoleOwner})
	confirmed, err := rpc{f.Service}.ConfirmNetwork(ctx, connect.NewRequest(&ownprobev1.OwnProbeServiceConfirmNetworkRequest{Id: p.ID}))
	if err != nil || confirmed.Msg.Probe.State != "active" || confirmed.Msg.Probe.PinnedOperator != "ExampleNet" {
		t.Fatalf("confirm = %+v, %v", confirmed, err)
	}
	if _, err := (rpc{f.Service}).ConfirmNetwork(ctx, connect.NewRequest(&ownprobev1.OwnProbeServiceConfirmNetworkRequest{Id: p.ID})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("confirming a probe that did not move: %v", err)
	}
}

// Two reports at once: one job is folded once; and two probes reporting at once both keep their points.
func TestConcurrentReports(t *testing.T) {
	f := probeFixture(t)
	f.enable(t)
	h := f.PublicHandler("")
	p, secret := f.probe(t, "prb_msk1", "msk1")
	f.setJob(t, p.ID, testJobID, defaultTargets)
	body := report(testJobID, "ok", "blocked", "1 timeout 5000 - 0 - 0")
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if w := post(h, "/sync", homeIP, "Bearer "+secret, body); w.Code != http.StatusOK {
				t.Errorf("concurrent sync = %d %q", w.Code, w.Body.String())
			}
		}()
	}
	wg.Wait()
	if rows, err := f.st.RecentExtChecks(context.Background(), 10, countryreach.SourceProbes); err != nil || len(rows) != 1 {
		t.Fatalf("one job folded into %d rows, %v", len(rows), err)
	}
	if got, _ := f.st.OwnProbe(context.Background(), p.ID); got.ChecksDay != 1 || got.JobID != "" {
		t.Fatalf("one job consumed twice: %+v", got)
	}

	g := probeFixture(t)
	g.enable(t)
	gh := g.PublicHandler("")
	secrets := map[string]string{}
	for _, name := range []string{"aa", "bb", "cc", "dd"} {
		pr, s := g.probe(t, "prb_"+name, name)
		g.setJob(t, pr.ID, testJobID, defaultTargets)
		secrets[pr.ID] = s
	}
	for id, s := range secrets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if w := post(gh, "/sync", homeIP, "Bearer "+s, report(testJobID, "ok", "blocked", rowTLSOnly)); w.Code != http.StatusOK {
				t.Errorf("%s: %d %q", id, w.Code, w.Body.String())
			}
		}()
	}
	wg.Wait()
	d := extRows(t, g)["nod_de1"]
	if len(d.Vantages) != 4 {
		t.Fatalf("four probes reported at once and the row has %d points: %+v", len(d.Vantages), d)
	}
	// and a revocation racing a report leaves no point of the revoked probe
	var wg2 sync.WaitGroup
	g.setJob(t, "prb_aa", "2222222222222222", defaultTargets)
	wg2.Add(2)
	go func() {
		defer wg2.Done()
		post(gh, "/sync", homeIP, "Bearer "+secrets["prb_aa"], report("2222222222222222", "ok", "blocked", rowTLSOnly))
	}()
	go func() {
		defer wg2.Done()
		g.mu.Lock()
		defer g.mu.Unlock()
		_ = g.st.RevokeOwnProbe(context.Background(), "prb_aa")
		_ = g.refold(context.Background())
	}()
	wg2.Wait()
	if d := extRows(t, g)["nod_de1"]; stateOf(d, "prb_aa") != "" {
		t.Fatalf("revoked probe's point survived: %+v", d)
	}
}

// The panel never keeps or logs the address a probe came from: only the ASN and the operator survive the request.
func TestNoAddressIsStoredOrLogged(t *testing.T) {
	f := probeFixture(t)
	f.enable(t)
	h := f.PublicHandler("")
	const secretIP = "203.0.113.77"
	tokenHash := sha256.Sum256([]byte(testToken))
	if err := f.st.CreateOwnProbe(context.Background(), store.OwnProbe{ID: "prb_de1", Name: "probe-de1", City: "de1", Kind: "home",
		TokenHash: tokenHash[:], TokenExpires: f.now.Add(time.Hour), CreatedAt: f.now, CreatedBy: "adm_test"}); err != nil {
		t.Fatal(err)
	}
	w := post(h, "/enroll", secretIP, "", testToken)
	secret := strings.TrimPrefix(strings.Split(w.Body.String(), "\n")[2], "secret ")
	post(h, "/sync", secretIP, "Bearer "+secret, report(noJobID, "ok", "blocked"))
	post(h, "/sync", secretIP, "Bearer "+secret, "garbage")
	post(h, "/sync", secretIP, "Bearer mgps_wrong", "")
	post(h, "/enroll", secretIP, "", "mgp_"+strings.Repeat("q", 52))
	needle := []byte(secretIP)
	rows, err := f.st.R.Query(`SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		tables = append(tables, n)
	}
	rows.Close()
	for _, table := range tables {
		data, err := f.st.R.Query(`SELECT * FROM "` + table + `"`)
		if err != nil {
			continue // virtual or internal
		}
		cols, _ := data.Columns()
		for data.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if data.Scan(ptrs...) != nil {
				continue
			}
			for _, v := range vals {
				var raw []byte
				switch x := v.(type) {
				case string:
					raw = []byte(x)
				case []byte:
					raw = x
				}
				if bytes.Contains(raw, needle) {
					t.Fatalf("the address is in table %s: %q", table, raw)
				}
			}
		}
		data.Close()
	}
	if bytes.Contains(f.logs.Bytes(), needle) {
		t.Fatalf("the address is in the log: %s", f.logs.String())
	}
	if got, _ := f.st.OwnProbe(context.Background(), "prb_de1"); got.ASN != 64512 || got.Operator != "ExampleNet" {
		t.Fatalf("the network was not recorded: %+v", got)
	}
}

func TestBuildInstallCommand(t *testing.T) {
	hash := ScriptHash()
	token := "mgp_" + strings.Repeat("a", 52)
	prefix := "/aaaaaaaaaaaaaaaaaaaaaaaa/"
	command, err := BuildInstallCommand("https://panel.example.test", prefix, token, hash)
	if err != nil {
		t.Fatal(err)
	}
	want := "opkg update && opkg install curl ca-bundle coreutils-sha256sum && cd /tmp && curl -fsS --connect-timeout 15 -m 120 -o mgp.sh 'https://panel.example.test/aaaaaaaaaaaaaaaaaaaaaaaa/probe.sh'" +
		" && echo '" + hash + "  mgp.sh' | sha256sum -c - && sh mgp.sh install 'https://panel.example.test/aaaaaaaaaaaaaaaaaaaaaaaa' '" + token + "'"
	if command != want {
		t.Fatalf("command:\n got %s\nwant %s", command, want)
	}
	// whatever the settings hold, the one line has the same skeleton and nothing of the inputs outside single quotes
	quoted := regexp.MustCompile(`'(?:[^']|'\\'')*'`)
	skeleton := quoted.ReplaceAllString(want, "Q")
	for _, publicURL := range []string{
		"https://panel.example.test/'; touch /tmp/x; '", "https://panel.example.test/$(touch x)", "https://panel.example.test/`id`",
		"https://panel.example.test/a b", "https://panel.example.test/\n touch x", "https://user:pw@panel.example.test", "https://panel.example.test?x=1",
		"https://panel.example.test#f", "http://panel.example.test", "panel.example.test", "https://", "https://pa'nel.example.test", "https://panel.example.test/a'b/",
	} {
		got, err := BuildInstallCommand(publicURL, prefix, token, hash)
		if err != nil {
			continue
		}
		if strings.ContainsAny(got, "\n\r\x00") || quoted.ReplaceAllString(got, "Q") != skeleton {
			t.Errorf("public URL %q gave a command that escapes its quotes: %s", publicURL, got)
		}
	}
	for name, c := range map[string][3]string{
		"prefix with a command": {"/aaaaaaaaaaaaaaaaaaaa;touch/", token, hash}, "short prefix": {"/aaaa/", token, hash}, "no slashes": {"aaaaaaaaaaaaaaaaaaaaaaaaaa", token, hash},
		"upper case prefix": {"/AAAAAAAAAAAAAAAAAAAAAAAA/", token, hash}, "token with a quote": {prefix, "mgp_" + strings.Repeat("a", 51) + "'", hash},
		"short token": {prefix, "mgp_aaaa", hash}, "hash with a command": {prefix, token, strings.Repeat("a", 60) + ";id;"}, "short hash": {prefix, token, "abc"},
		"upper case hash": {prefix, token, strings.ToUpper(hash)},
	} {
		if _, err := BuildInstallCommand("https://panel.example.test", c[0], c[1], c[2]); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestScriptHashMatchesTheServedFileAndETag(t *testing.T) {
	f := probeFixture(t)
	h := f.PublicHandler("")
	w := get(h, "/probe.sh", homeIP)
	sum := sha256.Sum256(w.Body.Bytes())
	if w.Code != http.StatusOK || hex.EncodeToString(sum[:]) != ScriptHash() || w.Header().Get("ETag") != `"`+ScriptHash()+`"` || ScriptVersion() != ScriptHash()[:12] {
		t.Fatalf("script response = %d etag=%q sha=%x", w.Code, w.Header().Get("ETag"), sum)
	}
	r := httptest.NewRequest(http.MethodGet, "/probe.sh", nil)
	r.Header.Set("If-None-Match", w.Header().Get("ETag"))
	cached := httptest.NewRecorder()
	h.ServeHTTP(cached, r)
	if cached.Code != http.StatusNotModified || cached.Body.Len() != 0 {
		t.Fatalf("conditional get = %d", cached.Code)
	}
	command, _ := BuildInstallCommand("https://panel.example.test", "/aaaaaaaaaaaaaaaaaaaaaaaa/", "mgp_"+strings.Repeat("a", 52), ScriptHash())
	if !strings.Contains(command, hex.EncodeToString(sum[:])+"  mgp.sh") {
		t.Fatalf("the command checks another hash than the served file's: %s", command)
	}
}

func TestAdminRPCs(t *testing.T) {
	f := probeFixture(t)
	ctx := auth.WithAdmin(context.Background(), store.Admin{ID: "adm_owner", Role: store.RoleOwner})
	r := rpc{f.Service}
	defaults, err := f.settings(ctx)
	if err != nil || defaults.Enabled || defaults.IntervalMin != defaultInterval || defaults.FallbackURL != "" {
		t.Fatalf("default settings = %+v, %v", defaults, err)
	}
	// only the owner, and no API token or MCP session
	for name, c := range map[string]context.Context{
		"no admin": context.Background(), "admin role": auth.WithAdmin(context.Background(), store.Admin{ID: "adm_x", Role: store.RoleHelper}),
		"token": auth.WithAdmin(context.Background(), store.Admin{ID: "token:abc", Role: store.RoleOwner}),
		"mcp":   auth.WithAdmin(context.Background(), store.Admin{ID: "mcp:abc", Role: store.RoleOwner}),
	} {
		if _, err := r.Get(c, connect.NewRequest(&ownprobev1.OwnProbeServiceGetRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Errorf("Get as %s: %v", name, err)
		}
		if _, err := r.Create(c, connect.NewRequest(&ownprobev1.OwnProbeServiceCreateRequest{Name: "p", City: "c", Kind: "home"})); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Errorf("Create as %s: %v", name, err)
		}
	}
	for name, s := range map[string]*ownprobev1.OwnProbeSettings{"interval low": {IntervalMin: 29}, "interval high": {IntervalMin: 241},
		"plain http": {IntervalMin: 60, FallbackUrl: "http://fallback.example.test/"}, "userinfo": {IntervalMin: 60, FallbackUrl: "https://u:p@fallback.example.test/"},
		"line break": {IntervalMin: 60, FallbackUrl: "https://fallback.example.test/\nx"}, "nil": nil} {
		if _, err := r.Update(ctx, connect.NewRequest(&ownprobev1.OwnProbeServiceUpdateRequest{Settings: s})); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("Update %s: %v", name, err)
		}
	}
	updated, err := r.Update(ctx, connect.NewRequest(&ownprobev1.OwnProbeServiceUpdateRequest{Settings: &ownprobev1.OwnProbeSettings{Enabled: true, IntervalMin: 30, FallbackUrl: "https://fallback.example.test/probe"}}))
	if err != nil || !updated.Msg.Settings.Enabled || updated.Msg.Settings.IntervalMin != 30 {
		t.Fatalf("Update = %+v, %v", updated, err)
	}
	for _, bad := range []*ownprobev1.OwnProbeServiceCreateRequest{{Name: "", City: "c", Kind: "home"}, {Name: "p", City: "c", Kind: "office"},
		{Name: "p\nq", City: "c", Kind: "home"}, {Name: strings.Repeat("n", 81), City: "c", Kind: "home"}} {
		if _, err := r.Create(ctx, connect.NewRequest(bad)); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("Create %+v: %v", bad, err)
		}
	}
	created, err := r.Create(ctx, connect.NewRequest(&ownprobev1.OwnProbeServiceCreateRequest{Name: "probe-fi1", City: "fi1", Kind: "home"}))
	if err != nil || created.Msg.Probe.State != "pending" || !strings.HasPrefix(created.Msg.Token, "mgp_") || len(created.Msg.Token) != 56 ||
		!strings.Contains(created.Msg.Command, created.Msg.Token) || created.Msg.ScriptSha256 != ScriptHash() {
		t.Fatalf("Create = %+v, %v", created, err)
	}
	var tokenHash []byte
	if err := f.st.R.QueryRowContext(ctx, `SELECT token_hash FROM own_probe WHERE id = ?`, created.Msg.Probe.Id).Scan(&tokenHash); err != nil || !bytes.Equal(tokenHash, hashCredential(created.Msg.Token)) {
		t.Fatalf("stored token = %x, %v", tokenHash, err)
	}
	// the token is shown once: Get does not have it
	got, err := r.Get(ctx, connect.NewRequest(&ownprobev1.OwnProbeServiceGetRequest{}))
	if err != nil || len(got.Msg.Probes) != 1 || strings.Contains(fmt.Sprint(got.Msg), created.Msg.Token) {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	// enrol it through the public endpoint, then CheckNow, Revoke, Disconnect
	enrolled := post(f.PublicHandler(""), "/enroll", homeIP, "", created.Msg.Token)
	if enrolled.Code != http.StatusOK {
		t.Fatalf("enrol = %d", enrolled.Code)
	}
	secret, _, _ := strings.Cut(strings.SplitN(enrolled.Body.String(), "secret ", 2)[1], "\n")
	checked, err := r.CheckNow(ctx, connect.NewRequest(&ownprobev1.OwnProbeServiceCheckNowRequest{Id: created.Msg.Probe.Id}))
	if err != nil || checked.Msg.Probe.Id != created.Msg.Probe.Id {
		t.Fatalf("CheckNow = %+v, %v", checked, err)
	}
	if _, err := r.CheckNow(ctx, connect.NewRequest(&ownprobev1.OwnProbeServiceCheckNowRequest{Id: "prb_nope"})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("CheckNow of an unknown probe: %v", err)
	}
	revoked, err := r.Revoke(ctx, connect.NewRequest(&ownprobev1.OwnProbeServiceRevokeRequest{Id: created.Msg.Probe.Id}))
	if err != nil || revoked.Msg.Probe.State != "revoked" {
		t.Fatalf("Revoke = %+v, %v", revoked, err)
	}
	disconnected, err := r.Disconnect(ctx, connect.NewRequest(&ownprobev1.OwnProbeServiceDisconnectRequest{}))
	if err != nil || disconnected.Msg.Settings.Enabled {
		t.Fatalf("Disconnect = %+v, %v", disconnected, err)
	}
	// every owner action leaves an audit row, and no row holds the token, the secret, the install command or the URL
	rows, err := f.st.ListAudit(ctx, "", 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, row := range rows {
		seen[row.Action] = true
		for _, bad := range []string{created.Msg.Token, secret, created.Msg.Command, "fallback.example.test", "panel.example.test", homeIP} {
			if strings.Contains(row.Params, bad) || strings.Contains(row.Action, bad) || strings.Contains(row.IP, bad) {
				t.Errorf("audit row %q holds %q: %s", row.Action, bad, row.Params)
			}
		}
	}
	for _, action := range []string{"own_probe.create", "own_probe.update", "own_probe.revoke", "own_probe.disconnect"} {
		if !seen[action] {
			t.Errorf("no audit row %q in %v", action, seen)
		}
	}
	// the owner's second lock: a refused step-up stops Update, Create, Revoke and Disconnect before anything changes
	denied := errStepUp
	g := probeFixture(t, func(c *Config) { c.StepUp = func(context.Context) error { return denied } })
	gr := rpc{g.Service}
	if _, err := gr.Create(ctx, connect.NewRequest(&ownprobev1.OwnProbeServiceCreateRequest{Name: "p", City: "c", Kind: "home"})); err != denied {
		t.Errorf("Create without step-up: %v", err)
	}
	if _, err := gr.Update(ctx, connect.NewRequest(&ownprobev1.OwnProbeServiceUpdateRequest{Settings: &ownprobev1.OwnProbeSettings{IntervalMin: 60}})); err != denied {
		t.Errorf("Update without step-up: %v", err)
	}
	if _, err := gr.Revoke(ctx, connect.NewRequest(&ownprobev1.OwnProbeServiceRevokeRequest{Id: "prb_x"})); err != denied {
		t.Errorf("Revoke without step-up: %v", err)
	}
	if _, err := gr.Disconnect(ctx, connect.NewRequest(&ownprobev1.OwnProbeServiceDisconnectRequest{})); err != denied {
		t.Errorf("Disconnect without step-up: %v", err)
	}
	if rows, _ := g.st.OwnProbes(ctx); len(rows) != 0 {
		t.Errorf("a refused Create left a row: %+v", rows)
	}
}

var errStepUp = connect.NewError(connect.CodePermissionDenied, fmt.Errorf("step-up required"))

// One probe may report three times in a row and then once in five minutes; the others are not slowed down by it.
func TestSyncLimitPerProbe(t *testing.T) {
	f := probeFixture(t, func(c *Config) { c.SyncBurst = 0 })
	f.enable(t)
	_, secret := f.probe(t, "prb_de1", "de1")
	_, other := f.probe(t, "prb_nl1", "nl1")
	h := f.PublicHandler("")
	var codes []int
	for range 5 {
		codes = append(codes, post(h, "/sync", homeIP, "Bearer "+secret, report(noJobID, "ok", "blocked")).Code)
	}
	if fmt.Sprint(codes) != "[200 200 200 429 429]" {
		t.Fatalf("codes %v", codes)
	}
	if w := post(h, "/sync", homeIP, "Bearer "+other, report(noJobID, "ok", "blocked")); w.Code != http.StatusOK {
		t.Fatalf("another probe = %d", w.Code)
	}
	f.advance(5*time.Minute + time.Second)
	if w := post(h, "/sync", homeIP, "Bearer "+secret, report(noJobID, "ok", "blocked")); w.Code != http.StatusOK {
		t.Fatalf("after the refill = %d", w.Code)
	}
}
