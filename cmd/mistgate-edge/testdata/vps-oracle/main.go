package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/mistgate/mistgate/internal/panel/app"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: vps-oracle <database> <listen-address>")
		os.Exit(2)
	}
	ctx := context.Background()
	st, err := store.Open(ctx, os.Args[1])
	if err != nil {
		fatal(err)
	}
	defer st.Close()
	key := make([]byte, vault.KeySize)
	if _, err := io.ReadFull(os.Stdin, key); err != nil {
		fatal(err)
	}
	vlt, err := vault.New(key)
	if err != nil {
		fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	in := app.InstanceConfig{
		PublicURL: "https://example.com", AdminPrefix: "/test-admin/", RPID: "example.com",
		RPOrigins: []string{"https://example.com"}, AgentSNI: "agent.example.com", SubPrefix: "/test-sub/",
	}
	authSvc, err := auth.New(st, auth.Config{RPID: in.RPID, RPName: "Mistgate", Origins: in.RPOrigins, Vault: vlt, SourceURL: "https://github.com/Mistgate/mistgate"}, log)
	if err != nil {
		fatal(err)
	}
	built, err := app.Build(app.Config{
		Store: st, Vault: vlt, Auth: authSvc, MasterKey: key, Clock: time.Now, Logger: log,
		Instance: in, Title: "Mistgate", DataDir: filepath.Join(filepath.Dir(os.Args[1]), "backup-unavailable"),
	})
	if err != nil {
		fatal(err)
	}
	listener, err := net.Listen("tcp", os.Args[2])
	if err != nil {
		fatal(err)
	}
	fmt.Println("READY")
	if err := http.Serve(listener, built.Handler); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
