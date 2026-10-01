package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

// runSetup prepares an installation and prints how to reach the admin. It is safe to run
// again: an existing configuration is kept, and a fresh setup link is issued while no
// admin exists.
func runSetup(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	dataDir := fs.String("data-dir", envOr("MISTGATE_DATA_DIR", "/var/lib/mistgate"), "data directory (database, master key)")
	var o setupOpts
	fs.StringVar(&o.publicURL, "public-url", os.Getenv("MISTGATE_PUBLIC_URL"), "URL of the public (decoy) site, e.g. https://example.com")
	fs.StringVar(&o.adminHost, "admin-host", os.Getenv("MISTGATE_ADMIN_HOST"), "serve the admin on this secret host instead of a secret path prefix")
	fs.StringVar(&o.adminListen, "admin-listen", os.Getenv("MISTGATE_ADMIN_LISTEN"), "serve the admin on a separate listener, e.g. 127.0.0.1:8081 (exclusive with --admin-host)")
	fs.StringVar(&o.rpID, "rp-id", os.Getenv("MISTGATE_RP_ID"), "WebAuthn RP ID (default derived from the admin address)")
	fs.StringVar(&o.rpOrigins, "rp-origins", os.Getenv("MISTGATE_RP_ORIGINS"), "comma-separated allowed WebAuthn origins (default derived)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return setup(context.Background(), *dataDir, o, out, time.Now())
}

func setup(ctx context.Context, dataDir string, o setupOpts, out io.Writer, now time.Time) error {
	if err := secureDataDir(dataDir); err != nil {
		return err
	}
	if _, err := vault.LoadKey(dataDir, true); err != nil {
		return err
	}
	st, err := store.Open(ctx, dbPath(dataDir))
	if err != nil {
		return err
	}
	defer st.Close()

	in, err := loadInstance(ctx, st)
	switch {
	case errors.Is(err, errNotConfigured):
		if in, err = newInstance(o); err != nil {
			return err
		}
		if err := st.SetSettings(ctx, in.settings()); err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		fmt.Fprintln(out, "Already configured; keeping the existing settings.")
	}

	fmt.Fprintf(out, "Data dir:   %s\n", dataDir)
	fmt.Fprintf(out, "Admin URL:  %s\n", in.adminURL())
	tok, err := auth.IssueSetupToken(ctx, st, now)
	if errors.Is(err, auth.ErrAdminExists) {
		fmt.Fprintln(out, "An admin already exists; no setup link issued.")
		return nil
	}
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Setup link: %ssetup#%s\n", in.adminURL(), tok)
	fmt.Fprintf(out, "The link works once and expires in %d minutes. Open it in a browser and create your admin (a passkey, or a password with an authenticator code).\n", int(auth.SetupTokenTTL.Minutes()))
	return nil
}
