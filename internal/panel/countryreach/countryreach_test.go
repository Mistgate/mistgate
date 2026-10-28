package countryreach

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func newService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st, nil, nil, nil), st
}

func ownerCtx() context.Context {
	return auth.WithAdmin(context.Background(), store.Admin{ID: "adm_owner", Role: store.RoleOwner})
}

func TestTheCountryIsRussiaUntilTheOwnerSaysOtherwise(t *testing.T) {
	s, _ := newService(t)
	if got := s.Country(context.Background()); got != "RU" {
		t.Fatalf("default country = %q", got)
	}
	r, err := rpc{s}.GetCountryReachability(ownerCtx(), connect.NewRequest(&adminv1.GetCountryReachabilityRequest{}))
	if err != nil || r.Msg.Settings.Country != "RU" {
		t.Fatalf("Get = %+v, %v", r, err)
	}
}

func TestUpdateStoresAnUpperCaseCountryAndRefusesTheRest(t *testing.T) {
	s, st := newService(t)
	u, err := rpc{s}.UpdateCountryReachability(ownerCtx(), connect.NewRequest(&adminv1.UpdateCountryReachabilityRequest{Country: " kz "}))
	if err != nil || u.Msg.Settings.Country != "KZ" || s.Country(context.Background()) != "KZ" {
		t.Fatalf("Update = %+v, %v", u, err)
	}
	for _, bad := range []string{"", "K", "KAZ", "1a", "k z"} {
		if _, err := (rpc{s}).UpdateCountryReachability(ownerCtx(), connect.NewRequest(&adminv1.UpdateCountryReachabilityRequest{Country: bad})); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("country %q: err = %v", bad, err)
		}
	}
	if s.Country(context.Background()) != "KZ" {
		t.Fatal("a refused update changed the country")
	}
	rows, err := st.ListAudit(context.Background(), "", 0, 10)
	if err != nil || len(rows) != 1 || rows[0].Action != "country_reachability_update" {
		t.Fatalf("audit = %+v, %v", rows, err)
	}
	// a damaged stored value falls back to the default
	if err := st.SetSettings(context.Background(), map[string]string{settingCountry: "xyz"}); err != nil {
		t.Fatal(err)
	}
	if got := s.Country(context.Background()); got != DefaultCountry {
		t.Fatalf("country from a bad value = %q", got)
	}
}

func TestOnlyTheOwnerMayCallIt(t *testing.T) {
	s, _ := newService(t)
	for name, ctx := range map[string]context.Context{
		"helper":    auth.WithAdmin(context.Background(), store.Admin{ID: "adm_h", Role: store.RoleHelper}),
		"token":     auth.WithAdmin(context.Background(), store.Admin{ID: "token:tok_1", Role: store.RoleOwner}),
		"mcp":       auth.WithAdmin(context.Background(), store.Admin{ID: "mcp:tok_1", Role: store.RoleOwner}),
		"anonymous": context.Background(),
	} {
		if _, err := (rpc{s}).GetCountryReachability(ctx, connect.NewRequest(&adminv1.GetCountryReachabilityRequest{})); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Errorf("%s: Get err = %v", name, err)
		}
		if _, err := (rpc{s}).UpdateCountryReachability(ctx, connect.NewRequest(&adminv1.UpdateCountryReachabilityRequest{Country: "BY"})); connect.CodeOf(err) != connect.CodePermissionDenied {
			t.Errorf("%s: Update err = %v", name, err)
		}
	}
	if s.Country(context.Background()) != "RU" {
		t.Fatal("a refused call changed the country")
	}
}

// The history: days 1..7 back from now (0 and more than 7 are 7), the country, and what the health module says of the sources.
// It is a read like GetChecks, so no role and no token is refused here (the policy table lets every role and tokens in).
func TestGetReachabilityHistory(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	now := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	var asked time.Time
	s := New(st, nil, func() time.Time { return now }, func(_ context.Context, since time.Time, _ int) ([]string, []*adminv1.NodeReachabilityHistory, error) {
		asked = since
		return []string{"lists"}, []*adminv1.NodeReachabilityHistory{{NodeId: "nod_se1"}}, nil
	})
	for _, tc := range []struct{ days, want uint32 }{{0, 7}, {1, 1}, {7, 7}, {30, 7}} {
		r, err := rpc{s}.GetReachabilityHistory(ownerCtx(), connect.NewRequest(&adminv1.GetReachabilityHistoryRequest{Days: tc.days}))
		since := now.Add(-time.Duration(tc.want) * 24 * time.Hour)
		if err != nil || !asked.Equal(since) || r.Msg.SinceUnix != since.Unix() || r.Msg.NowUnix != now.Unix() || r.Msg.Country != "RU" ||
			len(r.Msg.Sources) != 1 || len(r.Msg.Nodes) != 1 {
			t.Errorf("days %d: %+v, %v, asked since %v", tc.days, r, err, asked)
		}
	}
	for name, ctx := range map[string]context.Context{
		"helper":   auth.WithAdmin(context.Background(), store.Admin{ID: "adm_h", Role: store.RoleHelper}),
		"readonly": auth.WithAdmin(context.Background(), store.Admin{ID: "adm_r", Role: store.RoleReadonly}),
		"token":    auth.WithAdmin(context.Background(), store.Admin{ID: "token:tok_1", Role: store.RoleReadonly}),
	} {
		if _, err := (rpc{s}).GetReachabilityHistory(ctx, connect.NewRequest(&adminv1.GetReachabilityHistoryRequest{})); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	// without the health module there are no sources, only the window and the country
	r, err := rpc{New(st, nil, nil, nil)}.GetReachabilityHistory(ownerCtx(), connect.NewRequest(&adminv1.GetReachabilityHistoryRequest{}))
	if err != nil || len(r.Msg.Sources) != 0 || len(r.Msg.Nodes) != 0 || r.Msg.Country != "RU" {
		t.Fatalf("no history: %+v, %v", r, err)
	}
}

// The family of the history: 0 is IPv4, 4 and 6 are passed to the health module and echoed, anything else is refused.
func TestGetReachabilityHistoryFamily(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	var asked int
	s := New(st, nil, nil, func(_ context.Context, _ time.Time, family int) ([]string, []*adminv1.NodeReachabilityHistory, error) {
		asked = family
		return nil, nil, nil
	})
	for _, tc := range []struct{ in, want uint32 }{{0, 4}, {4, 4}, {6, 6}} {
		r, err := rpc{s}.GetReachabilityHistory(ownerCtx(), connect.NewRequest(&adminv1.GetReachabilityHistoryRequest{Family: tc.in}))
		if err != nil || asked != int(tc.want) || r.Msg.Family != tc.want {
			t.Errorf("family %d: asked %d, answered %+v, %v", tc.in, asked, r, err)
		}
	}
	for _, bad := range []uint32{1, 5, 46} {
		if _, err := (rpc{s}).GetReachabilityHistory(ownerCtx(), connect.NewRequest(&adminv1.GetReachabilityHistoryRequest{Family: bad})); connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("family %d: %v", bad, err)
		}
	}
}
