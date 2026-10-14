//go:build !js

package app

import (
	"context"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/edition"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// Every deferred edge job takes some admin feature away, and every feature the admin lists as unavailable is behind a
// deferred job: the two lists cannot drift apart.
func TestEdgeDeferredJobsMatchUnavailableFeatures(t *testing.T) {
	jobs := slices.Sorted(maps.Keys(edgeDeferred))
	mapped := slices.Sorted(maps.Keys(edgeDeferredFeatures))
	if !slices.Equal(jobs, mapped) {
		t.Fatalf("deferred jobs %v, jobs mapped to features %v: map every deferred job (and only those) in edgeDeferredFeatures", jobs, mapped)
	}
	covered := map[string]bool{}
	for job, ids := range edgeDeferredFeatures {
		if len(ids) == 0 {
			t.Errorf("deferred job %q is mapped to no feature id", job)
		}
		for _, id := range ids {
			covered[id] = true
		}
	}
	listed := edition.Unavailable(true)
	for _, id := range listed {
		if !covered[id] && id != edition.DecoyOwn {
			t.Errorf("unavailable id %q has no deferred job behind it", id)
		}
	}
	for id := range covered {
		if !slices.Contains(listed, id) {
			t.Errorf("deferred job maps to %q, which Me does not list as unavailable", id)
		}
	}
	if len(listed) != len(covered)+1 || !slices.Contains(listed, edition.DecoyOwn) {
		t.Errorf("unavailable ids %v, ids behind jobs %v plus %q", listed, slices.Sorted(maps.Keys(covered)), edition.DecoyOwn)
	}
}

// Build makes the panel the edge edition when it is given the edge's node link: Me then names the edition and lists the
// features it lacks. Without a link it is the VPS panel.
func TestBuildEditionDecidesMe(t *testing.T) {
	owner := store.Admin{ID: "adm_owner", Role: store.RoleOwner}
	ctx := auth.WithAdmin(context.Background(), owner)
	me := func(a *auth.Service) *adminv1.MeResponse {
		r, err := a.Me(ctx, connect.NewRequest(&adminv1.MeRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		return r.Msg
	}

	_, edgeAuth := buildPanel(t, &edgeTickRemote{})
	if m := me(edgeAuth); m.Edition != edition.Edge || !slices.Equal(m.Unavailable, edition.Unavailable(true)) {
		t.Errorf("edge Me = %q %v", m.Edition, m.Unavailable)
	}
	if m := me(edgeAuth); slices.Contains(m.Unavailable, "provision") {
		t.Errorf("edge Me lists SSH installs as unavailable: %v", m.Unavailable)
	}
	_, vpsAuth := buildPanel(t, nil)
	if m := me(vpsAuth); m.Edition != edition.VPS || len(m.Unavailable) != 0 {
		t.Errorf("VPS Me = %q %v", m.Edition, m.Unavailable)
	}
}

// The wiring: through the panel's own handlers (an owner behind the session middleware), each RPC that starts work the edge
// lacks is refused with the coded reason on the edge panel, and is not refused as an edition limit on the VPS panel.
func TestBuildEditionWiresTheRefusals(t *testing.T) {
	owner := store.Admin{ID: "adm_owner", Role: store.RoleOwner, DisplayName: "alice"}
	// serve mounts a service handler the way the admin does, with the owner already signed in.
	serve := func(_ string, h http.Handler) (string, *http.Client) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h.ServeHTTP(w, r.WithContext(auth.WithAdmin(r.Context(), owner)))
		}))
		t.Cleanup(srv.Close)
		return srv.URL, srv.Client()
	}
	ctx := context.Background()
	calls := map[string]struct {
		feature string
		call    func(p *Panel, a *auth.Service) error
	}{
		"SendTelegramTest": {edition.Telegram, func(p *Panel, _ *auth.Service) error {
			base, hc := serve(p.Telegram.Handler())
			_, err := adminv1connect.NewTelegramServiceClient(hc, base).SendTelegramTest(ctx, connect.NewRequest(&adminv1.SendTelegramTestRequest{}))
			return err
		}},
		"CreateBackup": {edition.Backups, func(p *Panel, _ *auth.Service) error {
			base, hc := serve(p.Backup.Handler())
			_, err := adminv1connect.NewBackupServiceClient(hc, base).CreateBackup(ctx, connect.NewRequest(&adminv1.CreateBackupRequest{}))
			return err
		}},
		"StartRollout": {edition.AgentUpdates, func(p *Panel, _ *auth.Service) error {
			base, hc := serve(p.Update.Handler())
			_, err := adminv1connect.NewUpdateServiceClient(hc, base).StartRollout(ctx, connect.NewRequest(&adminv1.StartRolloutRequest{}))
			return err
		}},
		"InstallPanelUpdate": {edition.PanelUpdate, func(p *Panel, _ *auth.Service) error {
			base, hc := serve(p.Update.Handler())
			_, err := adminv1connect.NewUpdateServiceClient(hc, base).InstallPanelUpdate(ctx, connect.NewRequest(&adminv1.InstallPanelUpdateRequest{}))
			return err
		}},
		"CreateApiToken": {edition.MCP, func(_ *Panel, a *auth.Service) error {
			base, hc := serve(a.TokenHandler())
			_, err := adminv1connect.NewApiTokenServiceClient(hc, base).CreateApiToken(ctx, connect.NewRequest(&adminv1.CreateApiTokenRequest{Name: "ops"}))
			return err
		}},
		"UpdateTspuProbe": {edition.TSPUProbe, func(p *Panel, _ *auth.Service) error {
			enabled := true
			base, hc := serve(p.CountryProbe.Handler())
			_, err := adminv1connect.NewTspuProbeServiceClient(hc, base).UpdateTspuProbe(ctx, connect.NewRequest(&adminv1.UpdateTspuProbeRequest{Enabled: &enabled}))
			return err
		}},
		"TestTspuProbe": {edition.TSPUProbe, func(p *Panel, _ *auth.Service) error {
			base, hc := serve(p.CountryProbe.Handler())
			_, err := adminv1connect.NewTspuProbeServiceClient(hc, base).TestTspuProbe(ctx, connect.NewRequest(&adminv1.TestTspuProbeRequest{}))
			return err
		}},
		"UploadNodeDecoy": {edition.DecoyOwn, func(p *Panel, _ *auth.Service) error {
			base, hc := serve(p.Fleet.NodeHandler())
			_, err := adminv1connect.NewNodeServiceClient(hc, base).UploadNodeDecoy(ctx, connect.NewRequest(&adminv1.UploadNodeDecoyRequest{}))
			return err
		}},
	}
	for name, c := range calls {
		pe, ae := buildPanel(t, &edgeTickRemote{})
		err := c.call(pe, ae)
		want := "unavailable_in_edition: feature=" + c.feature
		if err == nil || connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.Contains(err.Error(), want) {
			t.Errorf("edge %s = %v, want %q", name, err, want)
		}
		pv, av := buildPanel(t, nil)
		if err := c.call(pv, av); err != nil && strings.Contains(err.Error(), edition.Code) {
			t.Errorf("VPS %s = %v, must not be an edition refusal", name, err)
		}
	}
}

// The TSPU probe is not built for the edge: Get says so (available=false, reason "edition") and the VPS panel offers it. The
// scheduler is a background job of the VPS only, so nothing of it runs on the edge (TestEdgeBackgroundJobParity).
func TestBuildEditionSaysTheTSPUProbeIsUnavailableOnTheEdge(t *testing.T) {
	owner := store.Admin{ID: "adm_owner", Role: store.RoleOwner, DisplayName: "alice"}
	get := func(p *Panel) *adminv1.TspuProbeSettings {
		_, h := p.CountryProbe.Handler()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h.ServeHTTP(w, r.WithContext(auth.WithAdmin(r.Context(), owner)))
		}))
		t.Cleanup(srv.Close)
		r, err := adminv1connect.NewTspuProbeServiceClient(srv.Client(), srv.URL).GetTspuProbe(context.Background(), connect.NewRequest(&adminv1.GetTspuProbeRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		return r.Msg.Settings
	}
	pe, _ := buildPanel(t, &edgeTickRemote{})
	if s := get(pe); s.Available || s.UnavailableReason != "edition" || s.Enabled {
		t.Errorf("edge Get = %+v", s)
	}
	pv, _ := buildPanel(t, nil)
	if s := get(pv); !s.Available || s.UnavailableReason != "" || s.Enabled {
		t.Errorf("VPS Get = %+v", s)
	}
}
