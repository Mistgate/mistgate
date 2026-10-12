//go:build !js

package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/mistgate/mistgate/internal/buildinfo"
	nodeprovision "github.com/mistgate/mistgate/internal/panel/provision"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func seedProvisionJob(t *testing.T, p *Panel, id, state string) {
	t.Helper()
	ctx := context.Background()
	at := time.Unix(1_800_000_000, 0).UTC()
	job := store.NodeProvisionJob{ID: id, NodeID: "nod_" + id, Name: strings.ReplaceAll(id, "_", "-"), Address: "198.51.100.5",
		SSHHost: "node.example.com", SSHPort: 22, HostFingerprint: "SHA256:pin", Secret: []byte("sealed"), CreatedBy: "adm_test",
		CreatedAt: at, UpdatedAt: at}
	if err := p.Store.CreateNodeProvisionJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Store.W.ExecContext(ctx, `UPDATE node_provision_job SET state = ? WHERE id = ?`, state, id); err != nil {
		t.Fatal(err)
	}
}

// The cron pass wakes the Provisioner while an install is waiting, running or being cancelled, and not otherwise; a
// failed wake is reported by the tick.
func TestEdgeTickWakesTheProvisionerWhileAnInstallIsPending(t *testing.T) {
	wakes := 0
	var wakeErr error
	p, _ := buildPanelWith(t, &edgeTickRemote{}, func(c *Config) {
		c.ProvisionKick = func(context.Context) error { wakes++; return wakeErr }
	})
	ctx := context.Background()
	tick := func() error { return p.EdgeTick(ctx, time.Date(2026, 10, 12, 12, 3, 0, 0, time.UTC)) }

	if err := tick(); err != nil || wakes != 0 {
		t.Fatalf("no job: err %v, wakes %d", err, wakes)
	}
	seedProvisionJob(t, p, "prv_done", "failed")
	seedProvisionJob(t, p, "prv_ok", "completed")
	if err := tick(); err != nil || wakes != 0 {
		t.Fatalf("only finished jobs: err %v, wakes %d", err, wakes)
	}
	for i, state := range []string{"queued", "running", "cancel_requested"} {
		id := "prv_" + state
		seedProvisionJob(t, p, id, state)
		if err := tick(); err != nil || wakes != i+1 {
			t.Fatalf("%s: err %v, wakes %d, want %d", state, err, wakes, i+1)
		}
		if _, err := p.Store.W.ExecContext(ctx, `UPDATE node_provision_job SET state = 'failed' WHERE id = ?`, id); err != nil {
			t.Fatal(err)
		}
	}
	seedProvisionJob(t, p, "prv_again", "queued")
	wakeErr = errors.New("provisioner unreachable")
	if err := tick(); err == nil || !strings.Contains(err.Error(), "provision wake") {
		t.Fatalf("a failed wake = %v, want it reported by the tick", err)
	}
}

// Without a way to wake the Provisioner the tick has nothing to do for installs (the VPS edition has its own loop).
func TestEdgeTickWithoutAKickLeavesInstallsAlone(t *testing.T) {
	p, _ := buildPanel(t, &edgeTickRemote{})
	seedProvisionJob(t, p, "prv_wait", "queued")
	if err := p.EdgeTick(context.Background(), time.Date(2026, 10, 12, 12, 3, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
}

// Forwarding replaces the service and the page by the marker, behind the sign-in check; whoever is not signed in gets the
// ordinary answer of the admin, with no marker in it.
func TestProvisionForwardAnswersAMarkerOnlyBehindSignIn(t *testing.T) {
	const rpc = "/admin/api/mistgate.admin.v1.ProvisioningService/GetNodeProvision"
	const page = "/admin/nodes/install"
	for _, tc := range []struct {
		name    string
		forward bool
	}{{"forwarding", true}, {"serving", false}} {
		p, _ := buildPanelWith(t, &edgeTickRemote{}, func(c *Config) { c.ProvisionForward = tc.forward })
		for _, request := range []*http.Request{
			httptest.NewRequest(http.MethodPost, "https://example.com"+rpc, strings.NewReader("{}")),
			httptest.NewRequest(http.MethodGet, "https://example.com"+page, nil),
		} {
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Origin", "https://example.com")
			recorder := httptest.NewRecorder()
			p.Handler.ServeHTTP(recorder, request)
			if recorder.Code == http.StatusNoContent || recorder.Header().Get(nodeprovision.ForwardHeader) != "" {
				t.Errorf("%s, not signed in, %s: %d with marker %q", tc.name, request.URL.Path, recorder.Code, recorder.Header().Get(nodeprovision.ForwardHeader))
			}
		}
	}
}

func TestForwardMountsAreTheMarkerOrTheService(t *testing.T) {
	service := func(...connect.HandlerOption) (string, http.Handler) {
		return "/svc/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	}
	call := func(h http.Handler) *httptest.ResponseRecorder {
		recorder := httptest.NewRecorder()
		h.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "https://example.com/svc/Do", nil))
		return recorder
	}
	path, handler := forwardProvision(true, service)
	got := call(handler)
	if path != "/svc/" || got.Code != http.StatusNoContent || got.Header().Get(nodeprovision.ForwardHeader) != "provision;v="+buildinfo.Version {
		t.Fatalf("forwarding: %q %d %v", path, got.Code, got.Header())
	}
	if page := call(forwardPage(true, http.NotFoundHandler())); page.Code != http.StatusNoContent || page.Header().Get(nodeprovision.ForwardHeader) == "" {
		t.Fatalf("forwarded page: %d %v", page.Code, page.Header())
	}
	_, handler = forwardProvision(false, service)
	if got := call(handler); got.Code != http.StatusTeapot || got.Header().Get(nodeprovision.ForwardHeader) != "" {
		t.Fatalf("serving: %d %v", got.Code, got.Header())
	}
	if page := call(forwardPage(false, http.NotFoundHandler())); page.Code != http.StatusNotFound {
		t.Fatalf("served page: %d", page.Code)
	}
}
