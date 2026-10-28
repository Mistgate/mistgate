//go:build !js

package app

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/update"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

func TestRepoStarsBackgroundJobIsVPSOnly(t *testing.T) {
	for _, tc := range []struct {
		name   string
		remote *edgeTickRemote
		want   bool
	}{
		{name: "vps", want: true},
		{name: "edge", remote: &edgeTickRemote{}, want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "panel.sqlite"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = st.Close() })
			key := make([]byte, vault.KeySize)
			for i := range key {
				key[i] = byte(i + 1)
			}
			vlt, err := vault.New(key)
			if err != nil {
				t.Fatal(err)
			}
			log := slog.New(slog.NewTextHandler(io.Discard, nil))
			authSvc, err := auth.New(st, auth.Config{
				RPID: "example.com", Origins: []string{"https://example.com"}, Vault: vlt,
				SourceURL: "https://github.com/Mistgate/mistgate",
			}, log)
			if err != nil {
				t.Fatal(err)
			}
			if authSvc.SourceURL() != "https://github.com/Mistgate/mistgate" || update.NewRepoStars(authSvc.SourceURL(), log) == nil {
				t.Fatalf("source URL %q did not produce a GitHub repository", authSvc.SourceURL())
			}
			cfg := Config{
				Store: st, Vault: vlt, Auth: authSvc, Clock: time.Now, Logger: log,
				Instance: InstanceConfig{
					PublicURL: "https://example.com", AdminPrefix: "/admin/", RPID: "example.com",
					RPOrigins: []string{"https://example.com"}, AgentSNI: "agent.example.com", SubPrefix: "/sub/",
				},
				DataDir: t.TempDir(), MasterKey: key,
				AfterResponse: func(work func()) { work() },
			}
			if tc.remote != nil {
				cfg.Remote = tc.remote
			}
			p, err := Build(cfg)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, job := range p.BackgroundJobs {
				if job.Name == "repo-stars" {
					found = true
					break
				}
			}
			if found != tc.want {
				t.Fatalf("repo-stars background job present = %t, want %t (remote set: %t)", found, tc.want, tc.remote != nil)
			}
		})
	}
}
