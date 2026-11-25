package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strings"

	"github.com/mistgate/mistgate/internal/panel/store"
)

const adminAddressUsage = `usage: mistgate admin-address --admin-listen host:port [--data-dir DIR]

  Move the admin to a separate listener (e.g. 127.0.0.1:8443, reached over an SSH tunnel), dropping the admin host
  and the secret admin prefix. The way back in when the admin's public address no longer works. Restart the panel
  afterwards: serve reads the stored address itself.
`

// runAdminAddress implements `mistgate admin-address`: it stores the separate admin listener the way `mistgate setup
// --admin-listen` does, keeping the public URL and the secret prefixes.
func runAdminAddress(args []string, out io.Writer) error {
	var listen string
	dir, pos, err := authFlags("admin-address", args, func(fs *flag.FlagSet) {
		fs.StringVar(&listen, "admin-listen", "", "host:port of the separate admin listener")
	})
	if err != nil {
		return err
	}
	if len(pos) != 0 || listen == "" {
		return errors.New(adminAddressUsage)
	}
	ctx := context.Background()
	st, err := store.OpenExisting(ctx, dbPath(dir))
	if err != nil {
		return fmt.Errorf("open existing panel database in %s (check --data-dir): %w", dir, err)
	}
	defer st.Close()
	// Do not call loadInstance here: it backfills missing legacy secrets.
	current, err := st.SettingValues(ctx, []string{"rp_id", "admin_listen"})
	if err != nil {
		return err
	}
	if _, configured := current["rp_id"]; !configured {
		return errNotConfigured
	}
	if err := checkAdminOverride(os.Getenv("MISTGATE_ADMIN_LISTEN"), listen); err != nil {
		return err
	}
	previous := current["admin_listen"]
	var in instance
	if err := in.listenAdmin(listen); err != nil {
		return err
	}
	// The running panel may already own the unchanged listener. Do not probe or
	// steal that socket; only a changed address needs a trial bind.
	if listen != previous {
		ln, err := net.Listen("tcp", listen)
		if err != nil {
			return fmt.Errorf("admin listener %q cannot be bound; settings unchanged: %w", listen, err)
		}
		defer ln.Close() // Hold the reservation until the settings have been saved.
	}
	if err := st.SetSettings(ctx, map[string]string{
		"admin_listen": in.AdminListen,
		"admin_host":   in.AdminHost,
		"admin_prefix": in.AdminPrefix,
		"rp_id":        in.RPID,
		"rp_origins":   strings.Join(in.RPOrigins, ","),
	}); err != nil {
		return err
	}
	fmt.Fprintf(out, "Admin URL: %s\n", in.adminURL())
	// From one loopback port to another the RP ID stays localhost and the passkeys keep working.
	if current["rp_id"] != in.RPID {
		fmt.Fprintln(out, "Passkeys were registered for the previous address and no longer work here. Sign in with your password and authenticator code, then add a new passkey.")
	}
	fmt.Fprintln(out, "Before restarting, remove --admin-listen from the service command and MISTGATE_ADMIN_LISTEN from its environment. This command cannot inspect another process's arguments or environment; serve refuses a conflicting override. The trial bind cannot reserve the address across a restart.")
	fmt.Fprintln(out, "Run this command while the panel is running: the trial bind then also sees the panel's own ports (443, 80). If the panel does not start afterwards, its log names the port; run this command again with another one.")
	fmt.Fprintln(out, "Restart the panel.")
	return nil
}
