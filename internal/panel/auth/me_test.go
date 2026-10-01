package auth

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
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
