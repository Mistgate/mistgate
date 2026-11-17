package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
)

const adminAddressUsage = `usage: mistgate admin-address --admin-listen host:port [--data-dir DIR]

  Move the admin to a separate listener (e.g. 127.0.0.1:8443, reached over an SSH tunnel), dropping the admin host
  and the secret admin prefix. The way back in when the admin's public address no longer works. Restart the panel
  with the same --admin-listen afterwards.
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
	st, err := openPanelDB(ctx, dir)
	if err != nil {
		return err
	}
	defer st.Close()
	in, err := loadInstance(ctx, st)
	if err != nil {
		return err
	}
	if err := in.listenAdmin(listen); err != nil {
		return err
	}
	if err := st.SetSettings(ctx, in.settings()); err != nil {
		return err
	}
	fmt.Fprintf(out, "Admin URL: %s\n", in.adminURL())
	fmt.Fprintln(out, "Passkeys were registered for the previous address and no longer work here. Sign in with your password and authenticator code, then add a new passkey.")
	fmt.Fprintf(out, "Restart the panel with --admin-listen %s.\n", listen)
	return nil
}
