package subs_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/subs"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
)

// The reports of the kl!ck probe: the Probe-Report header of the subscription answer and POST <link>/probe, against the real
// access module and database (one node with a Hysteria2 and an AmneziaWG server).

// probeIP is where every request of these tests comes from: it must never reach the database.
var probeIP = netip.MustParseAddr("198.51.100.77")

type probeRig struct {
	*m3rig
	h     http.Handler
	cache *subsettings.Cache
}

func newProbeRig(t *testing.T) *probeRig {
	t.Helper()
	m := newM3Rig(t)
	h, cache := m.handler(func(c *subs.Config) {
		c.ProbeReports = m.st
		c.ClientIP = func(*http.Request) netip.Addr { return probeIP }
	})
	return &probeRig{m3rig: m, h: h, cache: cache}
}

func (r *probeRig) switchProbe(on bool) {
	r.t.Helper()
	v := "0"
	if on {
		v = "1"
	}
	if err := r.st.SetSettings(r.ctx, map[string]string{store.SettingClientProbe: v}); err != nil {
		r.t.Fatal(err)
	}
	r.cache.Invalidate() // what clientprobe.Service does after the switch
}

func (r *probeRig) post(token, contentType, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/"+token+"/probe", strings.NewReader(body))
	req.Header.Set("User-Agent", "klick/0.9.0")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	r.h.ServeHTTP(rec, req)
	return rec
}

type probeServer struct {
	Type   string `yaml:"type" json:"type"`
	Server string `yaml:"server" json:"host"`
	Port   uint16 `yaml:"port" json:"port"`
}

// servers are the servers of the person's Mihomo profile, as kl!ck reads them.
func (r *probeRig) servers(token string) []probeServer {
	r.t.Helper()
	rec := fetch(r.h, "/"+token, "mihomo/1.19.12")
	var doc struct{ Proxies []probeServer }
	if err := yaml.Unmarshal(rec.Body.Bytes(), &doc); err != nil || len(doc.Proxies) == 0 {
		r.t.Fatalf("no Mihomo profile (%v): %d %q", err, rec.Code, rec.Body.String())
	}
	return doc.Proxies
}

// report is a v1 body as kl!ck sends it: every server with the verdict of verdicts (cycled), measured in full.
func report(servers []probeServer, verdicts ...string) string {
	type srv struct {
		probeServer
		Verdict   string `json:"verdict"`
		Flaky     bool   `json:"flaky"`
		TCPOK     int    `json:"tcp_ok"`
		TCPTries  int    `json:"tcp_tries"`
		TCPMS     int    `json:"tcp_ms"`
		DelayMS   int    `json:"delay_ms"`
		DLBytes   int    `json:"dl_bytes"`
		DLStalled bool   `json:"dl_stalled"`
		DNS       string `json:"dns"`
	}
	out := []srv{}
	for i, s := range servers {
		v := verdicts[i%len(verdicts)]
		out = append(out, srv{probeServer: s, Verdict: v, TCPOK: 3, TCPTries: 3, TCPMS: 52, DelayMS: 140, DLBytes: 10985, DLStalled: v == "freeze", DNS: "same"})
	}
	b, _ := json.Marshal(map[string]any{
		"v": 1, "vpn": false, "direct_checked": true,
		"network": map[string]any{"asn": 8359, "operator": "MTS PJSC", "country": "RU", "region": "Moscow", "type": "mobile"},
		"servers": out,
	})
	return string(b)
}

func TestProbeReportHeader(t *testing.T) {
	r := newProbeRig(t)
	_, tok := r.newUser("alice", nil)
	for _, ua := range []string{"mihomo/1.19.12", "Happ/3.2.1"} {
		if got := fetch(r.h, "/"+tok, ua).Header().Get("Probe-Report"); got != "" {
			t.Errorf("%s, source off: Probe-Report %q", ua, got)
		}
	}
	r.switchProbe(true)
	for _, ua := range []string{"mihomo/1.19.12", "Happ/3.2.1"} {
		if got := fetch(r.h, "/"+tok, ua).Header().Get("Probe-Report"); got != "1" {
			t.Errorf("%s, source on: Probe-Report %q, want 1", ua, got)
		}
	}
	// the page is not an app: it never says so
	if got := fetch(r.h, "/"+tok, chrome).Header().Get("Probe-Report"); got != "" {
		t.Errorf("the page carries Probe-Report %q", got)
	}
	// without a store the panel takes nothing, whatever the switch says
	h, _ := r.handler(nil)
	if got := fetch(h, "/"+tok, "mihomo/1.19.12").Header().Get("Probe-Report"); got != "" {
		t.Errorf("no store: Probe-Report %q", got)
	}
}

func TestProbeReportAccepted(t *testing.T) {
	r := newProbeRig(t)
	_, tok := r.newUser("alice", nil)
	r.switchProbe(true)
	servers := r.servers(tok)
	if len(servers) != 2 {
		t.Fatalf("want the Hysteria2 and the AmneziaWG server, got %+v", servers)
	}
	// a server that is not in the subscription is dropped; the same server twice counts once; case and brackets do not matter
	sent := append(append([]probeServer{}, servers...),
		probeServer{Type: "vless", Server: "elsewhere.example.net", Port: 443},
		probeServer{Type: servers[0].Type, Server: strings.ToUpper(servers[0].Server), Port: servers[0].Port})
	rec := r.post(tok, "application/json", report(sent, "freeze", "ok"))
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"accepted":2}` {
		t.Fatalf("report: %d %q", rec.Code, rec.Body.String())
	}

	rows, err := r.st.R.Query(`SELECT node_id, kind, port, verdict, detail_json, vpn, direct_checked, asn, operator, country, region, net_type
		FROM client_probe_report ORDER BY kind`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var node, kind, verdict, detail, op, cc, region, nt string
		var port, vpn, direct, asn int
		if err := rows.Scan(&node, &kind, &port, &verdict, &detail, &vpn, &direct, &asn, &op, &cc, &region, &nt); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s %s %d %s %s %d %d %d %s %s %s %s", node, kind, port, verdict, detail, vpn, direct, asn, op, cc, region, nt))
	}
	rows.Close()
	want := map[string]string{}
	for i, s := range servers {
		v, stalled := "freeze", "true"
		if i == 1 {
			v, stalled = "ok", "false"
		}
		want[s.Type] = fmt.Sprintf(`nod_1 %s %d %s {"flaky":false,"tcp_ok":3,"tcp_tries":3,"tcp_ms":52,"delay_ms":140,"dl_bytes":10985,"dl_stalled":%s,"dns":"same"} 0 1 8359 MTS PJSC RU Moscow mobile`, s.Type, s.Port, v, stalled)
	}
	if len(got) != 2 || got[0] != want[strings.Fields(got[0])[1]] || got[1] != want[strings.Fields(got[1])[1]] {
		t.Errorf("stored rows:\n%s\nwant:\n%v", strings.Join(got, "\n"), want)
	}
	assertNoAddress(t, r.st)

	// what the source shows of it
	counts, err := r.st.ClientProbeCounts(r.ctx, time.Now().Add(-time.Hour))
	if err != nil || len(counts) != 1 || counts[0].Reports != 1 || counts[0].Freeze != 1 || counts[0].OK != 0 || counts[0].Operator != "MTS PJSC" {
		t.Errorf("counts %+v (%v): one report, one freeze, not ok", counts, err)
	}
}

// assertNoAddress looks through every text of every table: the address a report came from is stored nowhere.
func assertNoAddress(t *testing.T, st *store.Store) {
	t.Helper()
	tables, err := st.R.Query(`SELECT name FROM sqlite_master WHERE type = 'table'`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for tables.Next() {
		var n string
		tables.Scan(&n)
		names = append(names, n)
	}
	tables.Close()
	needle := probeIP.String()
	prefix := needle[:strings.LastIndex(needle, ".")+1] // the /24 too
	for _, n := range names {
		rows, err := st.R.Query(`SELECT * FROM "` + n + `"`)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			rows.Scan(ptrs...)
			for i, v := range vals {
				var s string
				switch x := v.(type) {
				case string:
					s = x
				case []byte:
					s = string(x)
				}
				if strings.Contains(s, prefix) {
					t.Errorf("%s.%s holds the sender's address: %q", n, cols[i], s)
				}
			}
		}
		rows.Close()
	}
}

func TestProbeReportRefusals(t *testing.T) {
	r := newProbeRig(t)
	_, tok := r.newUser("alice", nil)
	body := report(r.servers(tok), "ok")

	// off (the default): 403 probe_off, which kl!ck reads as "the panel takes no reports"
	if rec := r.post(tok, "application/json", body); rec.Code != 403 || !strings.Contains(rec.Body.String(), `"probe_off"`) {
		t.Errorf("off: %d %q", rec.Code, rec.Body.String())
	}
	// an unknown link gets the decoy, on or off
	for _, on := range []bool{false, true} {
		r.switchProbe(on)
		if rec := r.post("unknownTokenUnknownTokenUnknownToken0", "application/json", body); rec.Code != 404 || rec.Body.String() != decoyBody {
			t.Errorf("unknown token (on %v): %d %q", on, rec.Code, rec.Body.String())
		}
	}
	// GET is not the endpoint
	if rec := fetch(r.h, "/"+tok+"/probe", "klick/0.9.0"); rec.Code != 404 || rec.Body.String() != decoyBody {
		t.Errorf("GET: %d %q", rec.Code, rec.Body.String())
	}

	// each of these spends the budget of a fresh link (two at once, then one in 10 minutes)
	fresh := func(name string) string {
		_, tok := r.newUser(name, nil)
		return tok
	}
	cases := []struct {
		name, contentType, body string
		code                    int
		errCode                 string
	}{
		{"not JSON", "application/json", "{", 400, "bad_request"},
		{"unknown field", "application/json", strings.Replace(body, `"vpn":false`, `"vpn":false,"hwid":"x"`, 1), 400, "bad_request"},
		{"newer version", "application/json", strings.Replace(body, `"v":1`, `"v":2`, 1), 400, "bad_request"},
		{"unknown verdict", "application/json", strings.Replace(body, `"verdict":"ok"`, `"verdict":"great"`, 1), 400, "bad_request"},
		{"no content type", "", body, 415, "unsupported_media_type"},
		{"too large", "application/json", `{"v":1,"pad":"` + strings.Repeat("x", 200<<10) + `"}`, 413, "too_large"},
	}
	for i, c := range cases {
		rec := r.post(fresh("u"+strconv.Itoa(i)), c.contentType, c.body)
		if rec.Code != c.code || !strings.Contains(rec.Body.String(), `"`+c.errCode+`"`) {
			t.Errorf("%s: %d %q, want %d %s", c.name, rec.Code, rec.Body.String(), c.code, c.errCode)
		}
	}

	// a person without access has no servers to report on
	id, off := r.newUser("bob", nil)
	r.st.W.Exec(`UPDATE user SET disabled = 1 WHERE id = ?`, id)
	if rec := r.post(off, "application/json", body); rec.Code != 409 || !strings.Contains(rec.Body.String(), `"user_inactive"`) {
		t.Errorf("disabled user: %d %q", rec.Code, rec.Body.String())
	}

	// the budget: two at once, then 429 with Retry-After until the 10 minutes are up
	for i := 0; i < 2; i++ {
		if rec := r.post(tok, "application/json", body); rec.Code != 200 {
			t.Fatalf("report %d: %d %q", i+1, rec.Code, rec.Body.String())
		}
	}
	rec := r.post(tok, "application/json", body)
	retry, _ := strconv.Atoi(rec.Header().Get("Retry-After"))
	if rec.Code != 429 || retry < 1 || retry > 600 {
		t.Errorf("third report: %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	assertNoAddress(t, r.st)
}
