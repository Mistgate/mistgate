//go:build !js

package mcp

import (
	"context"
	"sync"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// The constructors build no server; a profile is built by the first request that needs it and only that profile, and the
// endpoint and the assistant that share a set share the built server.
func TestServersAreBuiltOnFirstUse(t *testing.T) {
	e := newTestEnv(t)
	set := new(ServerSet)
	cfg := Config{Plans: e.plans, Auth: e.auth, API: e.w.api(e.auth), Now: e.now, Servers: set}
	if _, err := New(cfg); err != nil {
		t.Fatal(err)
	}
	assistant, err := NewAssistant(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range profiles {
		if set.built(p) {
			t.Fatalf("%s is built before anything asked for it", p)
		}
	}

	// The test endpoint has its own set; only the assistant uses the shared one here.
	s, err := assistant.OpenAssistant(context.Background(), AssistantIdentity{TokenID: "tk_a", Profile: string(ProfileOperator), AdminID: "adm_a"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	tools, err := s.ListTools(context.Background())
	if err != nil || len(tools) == 0 {
		t.Fatalf("the first request got no tools: %d %v", len(tools), err)
	}
	if !set.built(ProfileOperator) || set.built(ProfileReadonly) || set.built(ProfileAdmin) {
		t.Fatal("exactly the operator server should be built")
	}
	first := set.get(assistant.e, ProfileOperator)
	if again := set.get(&env{cfg: cfg}, ProfileOperator); again != first {
		t.Fatal("a second use built the server again")
	}

	// The endpoint serves a request on its own, lazily built set too.
	_, secret := e.token(ProfileReadonly)
	if out := mustOK(t, e.session(secret), "protocols_list", map[string]any{}); out == "" {
		t.Fatal("the endpoint answered nothing")
	}
	if set.get(assistant.e, Profile("nobody")) != nil {
		t.Fatal("an unknown profile got a server")
	}
}

// Many first requests at once build each profile exactly once, and everybody gets that server.
func TestServersFirstRequestsRace(t *testing.T) {
	e := &env{cfg: Config{}}
	set := new(ServerSet)
	const n = 16
	for _, p := range profiles {
		got := make([]*sdk.Server, n)
		var wg sync.WaitGroup
		for i := range got {
			wg.Add(1)
			go func() {
				defer wg.Done()
				got[i] = set.get(e, p)
			}()
		}
		wg.Wait()
		for i, s := range got {
			if s == nil || s != got[0] {
				t.Fatalf("%s: request %d got a different server", p, i)
			}
		}
	}
}

// Building is lazy, so a registry that cannot be built (a tool type the schema generator rejects panics in AddTool) must be
// caught here instead of at the first request: every profile builds and lists as many tools as the registry says.
func TestEveryProfileBuilds(t *testing.T) {
	e := &env{cfg: Config{}}
	for _, p := range profiles {
		srv := e.newServer(p)
		ct, st := sdk.NewInMemoryTransports()
		ss, err := srv.Connect(context.Background(), st, nil)
		if err != nil {
			t.Fatal(err)
		}
		cs, err := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "1"}, nil).Connect(context.Background(), ct, nil)
		if err != nil {
			t.Fatal(err)
		}
		count, cursor := 0, ""
		for {
			page, err := cs.ListTools(context.Background(), &sdk.ListToolsParams{Cursor: cursor})
			if err != nil {
				t.Fatal(err)
			}
			count += len(page.Tools)
			if page.NextCursor == "" {
				break
			}
			cursor = page.NextCursor
		}
		if want := len(ToolNames(p)); count != want {
			t.Errorf("%s: %d tools listed, the registry has %d", p, count, want)
		}
		_ = cs.Close()
		_ = ss.Close()
	}
}
