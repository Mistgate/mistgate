//go:build !js

package ops

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	opsv1 "github.com/mistgate/mistgate/gen/mistgate/ops/v1"
	"github.com/mistgate/mistgate/internal/panel/capacity"
	"github.com/mistgate/mistgate/internal/panel/rpcstat"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The panel's own cost: its process, the database with its WAL, the nodes and the agents connected, and the admin API's
// calls the middleware counted. The database is measured once a minute, not per call.
func TestPanelResources(t *testing.T) {
	calls := rpcstat.New(nil)
	e := newEnv(t, func(c *Config) { c.RPC = calls })
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO node (id, name, address, state, created_at) VALUES ('nod_1', 'de1', 'de1.example.com', 'active', 1)`,
		`INSERT INTO node (id, name, address, state, created_at) VALUES ('nod_2', 'nl1', 'nl1.example.com', 'active', 1)`,
		`INSERT INTO node (id, name, address, state, created_at) VALUES ('nod_3', 'fi1', 'fi1.example.com', 'retired', 1)`,
	} {
		if _, err := e.st.W.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	calls.Record(200, 12*time.Millisecond, false)
	calls.Record(500, 40*time.Millisecond, false)
	calls.Record(403, time.Millisecond, false)

	r, err := e.cl.GetPanelResources(ctx, connect.NewRequest(&opsv1.GetPanelResourcesRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	m := r.Msg
	if p := m.Process; p == nil || p.Goroutines == 0 || p.GoTotalBytes == 0 || p.GoHeapBytes == 0 || p.UptimeS > uint64(time.Since(processStart).Seconds())+1 {
		t.Fatalf("process = %+v", p)
	}
	if runtime.GOOS == "linux" && (m.Process.RssBytes == 0 || m.Process.Threads == 0 || m.Process.OpenFds == 0 || m.Host.RamTotalBytes == 0 || m.Host.DiskTotalBytes == 0) {
		t.Fatalf("on Linux the process and the machine are read: %+v %+v", m.Process, m.Host)
	}
	if m.Host == nil || m.Host.Cpus != uint32(runtime.NumCPU()) {
		t.Fatalf("host = %+v", m.Host)
	}
	d := m.Database
	if d == nil || d.PageCount == 0 || d.PageSize == 0 || d.FileBytes == 0 || d.MeasuredUnix == 0 {
		t.Fatalf("database = %+v", d)
	}
	if m.Nodes != 2 || m.AgentsConnected != 0 {
		t.Fatalf("nodes %d, connected %d", m.Nodes, m.AgentsConnected)
	}
	if c := m.Rpc; c == nil || c.Requests != 3 || c.ServerErrors != 1 || c.ClientErrors != 1 || c.WindowS != 300 || c.P95Ms <= 0 {
		t.Fatalf("rpc = %+v", c)
	}

	// Within the minute the database is not measured again, whatever changed.
	first := e.svc.res.dbAt
	if _, err := e.cl.GetPanelResources(ctx, connect.NewRequest(&opsv1.GetPanelResourcesRequest{})); err != nil {
		t.Fatal(err)
	}
	if !e.svc.res.dbAt.Equal(first) {
		t.Fatal("the database was measured twice within a minute")
	}

	// Without a counter (the edge wiring, a test) the load is empty, not an error.
	plain := newEnv(t)
	r, err = plain.cl.GetPanelResources(ctx, connect.NewRequest(&opsv1.GetPanelResourcesRequest{}))
	if err != nil || r.Msg.Rpc.Requests != 0 || r.Msg.Rpc.P95Ms != 0 {
		t.Fatalf("no counter: %+v %v", r, err)
	}
}

func TestFleetLoad(t *testing.T) {
	var asked []int
	hrs := func(n int, cpu, people float64) []store.HostHour {
		out := make([]store.HostHour, n)
		for i := range out {
			out[i] = store.HostHour{HourStart: int64(1_800_000_000/3600+i) * 3600, Samples: 360, CPUAvg: cpu + float64(i%24), CPUMax: cpu + 20,
				SessionsAvg: people + float64(i%24), SessionsMax: uint32(people) + 24, RAMUsedMax: 1 << 29, RAMTotal: 1 << 31}
		}
		return out
	}
	readErr := false
	e := newEnv(t, func(c *Config) {
		c.Capacity = func(_ context.Context, hours int) ([]capacity.Node, error) {
			asked = append(asked, hours)
			if readErr {
				return nil, errors.New("database is locked")
			}
			return []capacity.Node{
				{ID: "nod_1", Name: "de1", Connected: true, Hours: hrs(48, 60, 40)}, // busy hour 83%: overloaded
				{ID: "nod_2", Name: "nl1", Connected: true, Hours: hrs(48, 5, 0)},
				{ID: "nod_3", Name: "fi1"},
			}, nil
		}
	})
	ctx := context.Background()
	r, err := e.cl.GetFleetLoad(ctx, connect.NewRequest(&opsv1.GetFleetLoadRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	m := r.Msg
	if m.Hours != 168 || len(asked) != 1 || asked[0] != 168 || len(m.Nodes) != 3 {
		t.Fatalf("hours %d asked %v nodes %d", m.Hours, asked, len(m.Nodes))
	}
	busy := m.Nodes[0]
	if busy.NodeName != "de1" || busy.Status != capacity.StatusOverloaded || busy.Bottleneck != capacity.ResCPU || busy.Fit != capacity.FitOK ||
		busy.HeadroomPeople == nil || *busy.HeadroomPeople != 0 || busy.CpuPctPerHundredPeople == nil || *busy.CpuPctPerHundredPeople != 100 {
		t.Fatalf("de1 = %+v", busy)
	}
	// An agent that predates the resource fields: not known, never zero.
	if busy.StealP95Pct != nil || busy.SwapUsedMaxBytes != nil || busy.AgentCpuAvgPct != nil || busy.ConntrackPeakPct != nil {
		t.Fatalf("an old agent's unknowns came out as values: %+v", busy)
	}
	codes := map[string]*opsv1.LoadRecommendation{}
	for _, x := range busy.Recommendations {
		codes[x.Code] = x
	}
	if c := codes[capacity.RecCPUBusy]; c == nil || c.Severity != capacity.SevCrit || c.Params["target"] != "nl1" || codes[capacity.RecUpdateAgent] == nil {
		t.Fatalf("recommendations %+v", busy.Recommendations)
	}
	if last := m.Nodes[2]; last.NodeName != "fi1" || last.Status != capacity.StatusUnknown || last.HeadroomPeople != nil {
		t.Fatalf("fi1 = %+v", last)
	}
	s := m.Summary
	if s.Overloaded != 1 || s.Ok != 1 || s.Unknown != 1 || len(s.MostLoaded) == 0 || s.MostLoaded[0].NodeName != "de1" || len(s.MostFree) != 1 || s.MostFree[0].NodeName != "nl1" {
		t.Fatalf("summary %+v", s)
	}

	// The answer is kept for a minute per window.
	if _, err := e.cl.GetFleetLoad(ctx, connect.NewRequest(&opsv1.GetFleetLoadRequest{Hours: 168})); err != nil || len(asked) != 1 {
		t.Fatalf("cached: asked %v, %v", asked, err)
	}
	if _, err := e.cl.GetFleetLoad(ctx, connect.NewRequest(&opsv1.GetFleetLoadRequest{Hours: 24})); err != nil || len(asked) != 2 || asked[1] != 24 {
		t.Fatalf("another window: asked %v, %v", asked, err)
	}
	for _, h := range []uint32{1, 2, 3, 4, 5} { // more windows than the cache keeps: it stays bounded
		if _, err := e.cl.GetFleetLoad(ctx, connect.NewRequest(&opsv1.GetFleetLoadRequest{Hours: h})); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(e.svc.load.entries); n > loadCacheMax {
		t.Fatalf("the cache holds %d windows", n)
	}
	if _, err := e.cl.GetFleetLoad(ctx, connect.NewRequest(&opsv1.GetFleetLoadRequest{Hours: 721})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("721 hours: %v", err)
	}
	readErr = true
	_, err = e.cl.GetFleetLoad(ctx, connect.NewRequest(&opsv1.GetFleetLoadRequest{Hours: 720}))
	if connect.CodeOf(err) != connect.CodeInternal || strings.Contains(err.Error(), "locked") {
		t.Fatalf("a failed read: %v", err)
	}

	// Without the reader (a test wiring) the answer is empty, not an error.
	plain := newEnv(t)
	r, err = plain.cl.GetFleetLoad(ctx, connect.NewRequest(&opsv1.GetFleetLoadRequest{Hours: 24}))
	if err != nil || len(r.Msg.Nodes) != 0 || r.Msg.Summary == nil {
		t.Fatalf("no reader: %+v %v", r, err)
	}
}

// /metrics adds the panel process's memory, goroutines and database size, and no other series that names a node.
func TestMetricsCarryThePanelProcess(t *testing.T) {
	var fleetH http.Handler
	e := newEnv(t, func(c *Config) {
		_, fleetH = adminv1connect.NewFleetServiceHandler(fakeFleet{})
		c.Fleet = fleetH
	})
	ctx := context.Background()
	on := true
	r, err := e.cl.SetMetricsSettings(ctx, connect.NewRequest(&opsv1.SetMetricsSettingsRequest{Enabled: &on}))
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer "+r.Msg.Token)
	w := httptest.NewRecorder()
	e.svc.MetricsHandler()(w, req)
	body := w.Body.String()
	for _, want := range []string{"# TYPE go_goroutines gauge", "\ngo_goroutines ", "# TYPE mistgate_db_bytes gauge", "\nmistgate_db_bytes "} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics lack %q:\n%s", want, body)
		}
	}
	if runtime.GOOS == "linux" && !strings.Contains(body, "\nprocess_resident_memory_bytes ") {
		t.Errorf("no resident memory on Linux:\n%s", body)
	}
	// Whether node names may appear in /metrics is under review: no series beyond the three that had them.
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, `node="`) && !strings.HasPrefix(line, "mistgate_node_down_bps{") && !strings.HasPrefix(line, "mistgate_node_up_bps{") &&
			!strings.HasPrefix(line, "mistgate_node_cpu_percent{") {
			t.Errorf("a new node-labelled series: %s", line)
		}
	}
}
