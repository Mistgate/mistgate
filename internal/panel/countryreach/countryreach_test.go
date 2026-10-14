package countryreach

import (
	"context"
	"path/filepath"
	"testing"

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
	return New(st, nil, nil), st
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
