package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mistgate/mistgate/internal/buildinfo"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/httpserver"
	settings "github.com/mistgate/mistgate/internal/panel/instance"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

func dbPath(dataDir string) string { return filepath.Join(dataDir, "mistgate.db") }

// secureDataDir makes sure the data directory exists and is private (0700): it holds the
// master key and the database.
func secureDataDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700)
}

// listFlag is a flag that may be repeated and also takes comma-separated values.
type listFlag []string

func (l *listFlag) String() string { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error {
	*l = append(*l, splitList(v)...)
	return nil
}

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	dev := fs.Bool("dev", envBool("MISTGATE_DEV"), "development mode: ./.data, plain HTTP on 127.0.0.1:8080 (decoy) and :8081 (admin), agent endpoint on 127.0.0.1:8082, RP ID localhost, prints a setup link")
	dataDir := fs.String("data-dir", os.Getenv("MISTGATE_DATA_DIR"), "data directory (default /var/lib/mistgate, ./.data with --dev)")
	listen := fs.String("listen", envOr("MISTGATE_LISTEN", "127.0.0.1:8080"), "public listener address")
	adminListen := fs.String("admin-listen", os.Getenv("MISTGATE_ADMIN_LISTEN"), "separate admin listener address; only for an installation set up with --admin-listen")
	agentListen := fs.String("agent-listen", os.Getenv("MISTGATE_AGENT_LISTEN"), "separate TLS listener for the node agent endpoint (default 127.0.0.1:8082 with --dev; otherwise agents use the public listener with the secret SNI name)")
	agentAddr := fs.String("agent-addr", os.Getenv("MISTGATE_AGENT_ADDR"), "host:port agents dial, put into the install command (default: --agent-listen, else the public URL's host and port)")
	decoyDir := fs.String("decoy-dir", os.Getenv("MISTGATE_DECOY_DIR"), "directory with the decoy site (default: built-in page)")
	updateService := fs.String("update-service", os.Getenv("MISTGATE_UPDATE_SERVICE"), "systemd unit to restart after a GitHub panel update (default mistgate.service)")
	tlsCert := fs.String("tls-cert", os.Getenv("MISTGATE_TLS_CERT"), "TLS certificate for the public listener (reloaded when the file changes)")
	tlsKey := fs.String("tls-key", os.Getenv("MISTGATE_TLS_KEY"), "TLS private key for the public listener")
	acmeEmail := fs.String("acme-email", os.Getenv("MISTGATE_ACME_EMAIL"), "contact address for Let's Encrypt (optional)")
	acmeHTTP := fs.String("acme-http", envOr("MISTGATE_ACME_HTTP", ":80"), "listener for ACME HTTP-01 and the http -> https redirect, used with --acme-domain (empty disables it)")
	sourceURL := fs.String("source-url", sourceURLDefault(), "where the source code of this build is published, linked next to the version in the admin only (AGPL-3.0 section 13); a fork points it at its own repository, empty hides the link")
	var acmeDomains, trustedProxies listFlag
	fs.Var(&acmeDomains, "acme-domain", "get a Let's Encrypt certificate for this host name (repeatable; comma-separated env MISTGATE_ACME_DOMAIN); the public listener must be reachable on port 443; using it accepts the CA's terms of service")
	fs.Var(&trustedProxies, "trusted-proxy", "CIDR or IP of a reverse proxy whose X-Forwarded-For / Forwarded headers are believed (repeatable; env MISTGATE_TRUSTED_PROXY). Without it the TCP peer is the client")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(acmeDomains) == 0 {
		acmeDomains = splitList(os.Getenv("MISTGATE_ACME_DOMAIN"))
	}
	if len(trustedProxies) == 0 {
		trustedProxies = splitList(os.Getenv("MISTGATE_TRUSTED_PROXY"))
	}
	if (*tlsCert == "") != (*tlsKey == "") {
		return errors.New("--tls-cert and --tls-key go together")
	}
	proxies, err := auth.ParseProxies(trustedProxies)
	if err != nil {
		return err
	}
	if *dataDir == "" {
		*dataDir = "/var/lib/mistgate"
		if *dev {
			*dataDir = "./.data"
		}
	}
	if *updateService == "" && !*dev {
		*updateService = "mistgate.service"
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *dev {
		if err := secureDataDir(*dataDir); err != nil {
			return err
		}
	} else if _, err := os.Stat(*dataDir); err != nil {
		return fmt.Errorf("data dir: %w (run `mistgate setup`)", err)
	} else if err := os.Chmod(*dataDir, 0o700); err != nil {
		return err
	}
	// The master key must exist before serving: it seals the TOTP secrets (and later the
	// nodes' secrets). Dev creates it on the fly.
	key, err := vault.LoadKey(*dataDir, *dev)
	if err != nil {
		return fmt.Errorf("master key: %w (run `mistgate setup`)", err)
	}
	vlt, err := vault.New(key)
	if err != nil {
		return err
	}
	st, err := store.Open(ctx, dbPath(*dataDir))
	if err != nil {
		return err
	}
	defer st.Close()
	in := devInstance()
	if *dev {
		if err := ensureSecrets(ctx, st, &in); err != nil {
			return err
		}
		if *adminListen == "" {
			*adminListen = in.AdminListen
		}
	} else {
		if in, err = loadInstance(ctx, st); err != nil {
			return err
		}
		if *adminListen != "" && in.AdminListen == "" {
			// Host and prefix modes have the admin on the public listener and their WebAuthn
			// origins derived from it; a second way in would be reachable but broken.
			return errors.New("--admin-listen conflicts with the stored admin address (a secret host or path prefix); run setup with --admin-listen instead")
		}
		if *adminListen == "" {
			*adminListen = in.AdminListen
		}
	}
	brand, err := settings.Load(ctx, st)
	if err != nil {
		return err
	}
	// The brand name is read once here (authenticator label in the passkey prompt);
	// a rename reaches the prompt after a restart. The TOTP issuer is read per setup.
	authSvc, err := auth.New(st, auth.Config{
		RPID: in.RPID, RPName: brand.BrandName(), Origins: in.RPOrigins, Vault: vlt, TrustedProxies: proxies, SourceURL: *sourceURL,
	}, log)
	if err != nil {
		return err
	}

	if *dev {
		// Nothing is stored in dev: the subscription links point at the dev decoy listener.
		if _, port, err := net.SplitHostPort(*listen); err == nil {
			in.PublicURL = "http://localhost:" + port
		}
		if *agentListen == "" {
			*agentListen = "127.0.0.1:8082"
		}
	}
	p, err := newPanel(st, vlt, authSvc, panelOpts{
		in: in, decoyDir: *decoyDir, title: brand.BrandName(), dataDir: *dataDir,
		panelAddr: agentAddress(*agentAddr, *agentListen, in.PublicURL), updateService: *updateService, masterKey: key,
	}, log)
	if err != nil {
		return err
	}
	log.Info("mistgate starting", "version", buildinfo.Version, "dev", *dev, "data_dir", *dataDir)
	if n, err := st.AdminCount(ctx); err != nil {
		return err
	} else if n == 0 {
		if *dev {
			tok, err := auth.IssueSetupToken(ctx, st, time.Now())
			if err != nil {
				return err
			}
			// Printed to stdout on purpose: this is the one-time link the developer opens.
			fmt.Printf("\nNo admin yet. Create one (link works once, 30 minutes):\n  http://localhost:8081/setup#%s\n\n", tok)
		} else {
			log.Warn("no admin exists yet: run `mistgate setup` to get a one-time setup link")
		}
	}
	opts := httpserver.ServeOptions{
		Public: *listen, Admin: *adminListen, AgentListen: *agentListen, TLSCert: *tlsCert, TLSKey: *tlsKey,
		ACMEDomains: acmeDomains, ACMEEmail: *acmeEmail,
	}
	if len(acmeDomains) > 0 {
		opts.ACMEDir = filepath.Join(*dataDir, "acme")
		opts.ACMEHTTP = *acmeHTTP
	}
	return p.run(ctx, opts)
}
