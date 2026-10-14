package globalping

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeGlobalping is a Globalping API on a local server. The tests never reach the real one. Its probes are a fixed list;
// a measurement asks for some of them (by country and tag, or by the id of an earlier measurement) and answers from tables.
type fakeGlobalping struct {
	t   *testing.T
	srv *httptest.Server

	mu   sync.Mutex
	reqs []recorded   // as the server read them
	sent atomic.Int32 // as the panel sent them (counted by client): one sent before a cancel can be read after it
	defs map[string]*fakeDef
	next int

	probes   []fakeProbe
	icmp     map[string]bool // target address -> ICMP answers
	tcp      map[string]bool // "address:port" -> connects
	control  string          // host of the control site
	failNode map[int]string  // probe index -> status the node measurements get instead ("offline", "failed")

	rateLimited bool // POST answers 429
	keepRunning bool // GETs never finish
	token       string
	limitsCode  int
}

type fakeProbe struct {
	Network, City string
	ASN           uint32
	Eyeball       bool
	ControlOK     bool
	Offline       bool
}

type recorded struct {
	Method, Path, Auth, UA string
	Body                   string
}

type fakeDef struct {
	body    map[string]any
	probes  []int // indexes into fake.probes
	started bool  // the first GET has been answered "in-progress"
}

func newFakeGlobalping(t *testing.T) *fakeGlobalping {
	f := &fakeGlobalping{t: t, defs: map[string]*fakeDef{}, icmp: map[string]bool{}, tcp: map[string]bool{},
		control: "ya.ru", failNode: map[int]string{}, limitsCode: 200,
		probes: []fakeProbe{
			{Network: "Rostelecom", City: "Moscow", ASN: 12389, Eyeball: true, ControlOK: true},
			{Network: "ER-Telecom", City: "Perm", ASN: 9049, Eyeball: true, ControlOK: true},
			{Network: "Beeline", City: "Kazan", ASN: 3216, Eyeball: true, ControlOK: true},
			{Network: "Selectel", City: "Saint Petersburg", ASN: 49505, Eyeball: false, ControlOK: true},
		}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

// client is the server's own HTTP client, counting each request in sent as it leaves.
func (f *fakeGlobalping) client() *http.Client {
	tr := f.srv.Client().Transport
	return &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		f.sent.Add(1)
		return tr.RoundTrip(r)
	})}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func (f *fakeGlobalping) requests() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.reqs...)
}

func (f *fakeGlobalping) count() int { return len(f.requests()) }

func (f *fakeGlobalping) posts() []recorded {
	var out []recorded
	for _, r := range f.requests() {
		if r.Method == http.MethodPost {
			out = append(out, r)
		}
	}
	return out
}

func (f *fakeGlobalping) serve(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	f.reqs = append(f.reqs, recorded{r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("User-Agent"), string(b)})
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/measurements":
		f.create(w, b)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/measurements/"):
		f.get(w, strings.TrimPrefix(r.URL.Path, "/v1/measurements/"))
	case r.Method == http.MethodGet && r.URL.Path == "/v1/limits":
		f.limits(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeGlobalping) limits(w http.ResponseWriter, r *http.Request) {
	if f.limitsCode != 200 {
		w.WriteHeader(f.limitsCode)
		fmt.Fprint(w, `{"error":{"type":"unauthorized","message":"nope"}}`)
		return
	}
	limit, typ := 250, "ip"
	if r.Header.Get("Authorization") != "" {
		limit, typ = 500, "user"
	}
	fmt.Fprintf(w, `{"rateLimit":{"measurements":{"create":{"type":%q,"limit":%d,"remaining":%d,"reset":3000}}},"credits":{"remaining":7}}`, typ, limit, limit-5)
}

func (f *fakeGlobalping) create(w http.ResponseWriter, body []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rateLimited {
		w.Header().Set("X-RateLimit-Reset", "1800")
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"error":{"type":"rate_limit_exceeded","message":"x"}}`)
		return
	}
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	var idx []int
	switch loc := req["locations"].(type) {
	case string: // the probes of an earlier measurement, in its order
		prev, ok := f.defs[loc]
		if !ok {
			w.WriteHeader(http.StatusUnprocessableEntity)
			fmt.Fprint(w, `{"error":{"type":"no_probes_found","message":"x"}}`)
			return
		}
		idx = prev.probes
	case []any:
		l := loc[0].(map[string]any)
		limit := int(l["limit"].(float64))
		tagged := l["tags"] != nil
		for i, p := range f.probes {
			if len(idx) < limit && (!tagged || p.Eyeball) {
				idx = append(idx, i)
			}
		}
		if len(idx) == 0 {
			w.WriteHeader(http.StatusUnprocessableEntity)
			fmt.Fprint(w, `{"error":{"type":"no_probes_found","message":"x"}}`)
			return
		}
	}
	f.next++
	id := "m" + strconv.Itoa(f.next)
	f.defs[id] = &fakeDef{body: req, probes: idx}
	w.WriteHeader(http.StatusAccepted)
	fmt.Fprintf(w, `{"id":%q,"probesCount":%d}`, id, len(idx))
}

func (f *fakeGlobalping) get(w http.ResponseWriter, id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.defs[id]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if !d.started || f.keepRunning { // the first read of every measurement finds it still running
		d.started = true
		fmt.Fprintf(w, `{"id":%q,"status":"in-progress","probesCount":%d,"results":[]}`, id, len(d.probes))
		return
	}
	var results []map[string]any
	for _, i := range d.probes {
		p := f.probes[i]
		results = append(results, map[string]any{
			"probe":  map[string]any{"city": p.City, "asn": p.ASN, "network": p.Network, "country": "RU", "tags": []string{"eyeball-network"}},
			"result": f.result(i, d),
		})
	}
	b, _ := json.Marshal(map[string]any{"id": id, "status": "finished", "probesCount": len(d.probes), "results": results})
	w.Write(b)
}

func (f *fakeGlobalping) result(i int, d *fakeDef) map[string]any {
	p := f.probes[i]
	target, _ := d.body["target"].(string)
	opts, _ := d.body["measurementOptions"].(map[string]any)
	isControl := target == f.control
	if p.Offline {
		return map[string]any{"status": "offline", "rawOutput": ""}
	}
	if !isControl {
		switch f.failNode[i] {
		case "offline":
			return map[string]any{"status": "offline", "rawOutput": ""}
		case "failed":
			return map[string]any{"status": "failed", "failureSource": "internal", "rawOutput": ""}
		}
	}
	ok := false
	switch {
	case isControl:
		ok = p.ControlOK
	case opts["protocol"] == "TCP":
		ok = f.tcp[fmt.Sprintf("%s:%d", target, int(opts["port"].(float64)))]
	default:
		ok = f.icmp[target]
	}
	rcv := 0
	var avg any
	if ok {
		rcv, avg = 3, 23.4
	}
	return map[string]any{"status": "finished", "rawOutput": "", "stats": map[string]any{"avg": avg, "total": 3, "rcv": rcv, "drop": 3 - rcv, "loss": 0}}
}
