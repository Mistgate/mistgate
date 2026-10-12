package auth

import (
	"context"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/edition"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// Me hands the admin the source link set with serve --source-url, and none when it is empty.
func TestMeSourceURL(t *testing.T) {
	_, st, _ := newTestService(t)
	ctx := context.WithValue(context.Background(), adminKey{}, store.Admin{ID: "adm_1", Role: store.RoleOwner})
	for _, want := range []string{"https://example.com/fork/mistgate", ""} {
		s, err := New(st, Config{RPID: "localhost", Origins: []string{"http://localhost:8081"}, SourceURL: want}, nil)
		if err != nil {
			t.Fatal(err)
		}
		me, err := s.Me(ctx, connect.NewRequest(&adminv1.MeRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		if me.Msg.SourceUrl != want {
			t.Errorf("Me.source_url = %q, want %q", me.Msg.SourceUrl, want)
		}
	}
}

// Me names the edition and, on the edge, the features it lacks; the VPS lists none.
func TestMeEdition(t *testing.T) {
	_, st, _ := newTestService(t)
	ctx := context.WithValue(context.Background(), adminKey{}, store.Admin{ID: "adm_1", Role: store.RoleOwner})
	s, err := New(st, Config{RPID: "localhost", Origins: []string{"http://localhost:8081"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	me := func() *adminv1.MeResponse {
		r, err := s.Me(ctx, connect.NewRequest(&adminv1.MeRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		return r.Msg
	}
	if m := me(); m.Edition != "vps" || len(m.Unavailable) != 0 {
		t.Errorf("default Me = edition %q unavailable %v, want vps and none", m.Edition, m.Unavailable)
	}
	s.SetEdge(true)
	m := me()
	if m.Edition != "edge" || !slices.Equal(m.Unavailable, edition.Unavailable(true)) || len(m.Unavailable) == 0 {
		t.Errorf("edge Me = edition %q unavailable %v", m.Edition, m.Unavailable)
	}
}

// API tokens exist for the MCP endpoint, which the edge does not serve: creating one is refused there and works on the VPS.
func TestCreateApiTokenEdition(t *testing.T) {
	e := newRPCEnv(t)
	req := &adminv1.CreateApiTokenRequest{Name: "ops", Profile: adminv1.TokenProfile_TOKEN_PROFILE_READONLY}
	e.s.SetEdge(true)
	_, err := create(e, e.ctx, req)
	if connect.CodeOf(err) != connect.CodeFailedPrecondition || !strings.HasPrefix(err.Error(), "failed_precondition: unavailable_in_edition: feature=mcp") {
		t.Fatalf("edge CreateApiToken = %v, want the coded refusal", err)
	}
	var n int
	if err := e.st.W.QueryRow(`SELECT count(*) FROM api_token`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("a refused CreateApiToken left %d tokens (%v)", n, err)
	}
	e.s.SetEdge(false)
	if _, err := create(e, e.ctx, req); err != nil {
		t.Fatalf("VPS CreateApiToken = %v", err)
	}
}
