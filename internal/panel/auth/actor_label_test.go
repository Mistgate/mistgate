package auth

import (
	"context"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/store"
)

func TestActorLabel(t *testing.T) {
	with := func(a store.Admin) context.Context { return context.WithValue(context.Background(), adminKey{}, a) }
	for _, c := range []struct {
		name string
		ctx  context.Context
		want string
	}{
		{"display name", with(store.Admin{ID: "adm_1", DisplayName: "Ops"}), "Ops"},
		{"id when there is no name (an API token)", with(store.Admin{ID: "token:tok_1"}), "token:tok_1"},
		{"nobody", context.Background(), ""},
	} {
		if got := ActorLabel(c.ctx); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestActorID(t *testing.T) {
	ctx := context.WithValue(context.Background(), adminKey{}, store.Admin{ID: "adm_alice", DisplayName: "alice"})
	if got := ActorID(ctx); got != "adm_alice" {
		t.Fatalf("ActorID = %q, want stable admin id", got)
	}
	if got := ActorID(context.Background()); got != "anonymous" {
		t.Fatalf("ActorID without an admin = %q, want anonymous", got)
	}
}
