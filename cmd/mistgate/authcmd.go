package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
	"github.com/mistgate/mistgate/internal/qr"
)

const authUsage = `usage: mistgate auth turnstile off [--data-dir DIR]
       mistgate auth reset-login [<login>] [--admin ID] [--qr-invert] [--data-dir DIR]

  turnstile off   switch the Cloudflare captcha off on the sign-in and setup pages (the keys stay);
                  the way back in when Cloudflare is unreachable or the keys are wrong.
  reset-login     the way back in after a lost phone: a new password and a new authenticator app
                  for <login>, its lockouts lifted, every session of that admin ended. Passkeys stay.
                  An admin with passkeys only gets <login> as a new password login (--admin picks
                  which one when the panel has several). Without <login> it lists the admins.
                  The authenticator key is also drawn as a QR code in the terminal, for a dark
                  background; --qr-invert draws it for a light one.
  Both work with the panel stopped or running (it reads these settings on every request).
`

// runAuth implements `mistgate auth ...`, the commands an operator runs on the panel's own server.
func runAuth(args []string, out io.Writer) error {
	switch {
	case len(args) >= 2 && args[0] == "turnstile" && args[1] == "off":
		dir, _, err := authFlags("auth turnstile off", args[2:], nil)
		if err != nil {
			return err
		}
		return turnstileOff(context.Background(), dir, out, time.Now())
	case len(args) >= 1 && args[0] == "reset-login":
		var adminID string
		var qrInvert bool
		dir, pos, err := authFlags("auth reset-login", args[1:], func(fs *flag.FlagSet) {
			fs.StringVar(&adminID, "admin", "", "the admin id (see the list) whose new password login this is")
			fs.BoolVar(&qrInvert, "qr-invert", false, "draw the QR code for a light terminal background")
		})
		if err != nil {
			return err
		}
		if len(pos) > 1 {
			return errors.New(authUsage)
		}
		login := ""
		if len(pos) == 1 {
			login = pos[0]
		}
		return resetLogin(context.Background(), dir, login, adminID, qrInvert, out, time.Now())
	}
	return errors.New(authUsage)
}

// authFlags parses the shared flags (and extra ones) wherever they stand, before or after the positional arguments.
func authFlags(name string, args []string, extra func(*flag.FlagSet)) (dataDir string, positional []string, err error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	dev := fs.Bool("dev", envBool("MISTGATE_DEV"), "development installation: data in ./.data")
	dir := fs.String("data-dir", os.Getenv("MISTGATE_DATA_DIR"), "data directory (default /var/lib/mistgate, ./.data with --dev)")
	if extra != nil {
		extra(fs)
	}
	for {
		if err := fs.Parse(args); err != nil {
			return "", nil, err
		}
		if fs.NArg() == 0 {
			break
		}
		positional = append(positional, fs.Arg(0))
		args = fs.Args()[1:]
	}
	dataDir = *dir
	if dataDir == "" {
		dataDir = "/var/lib/mistgate"
		if *dev {
			dataDir = "./.data"
		}
	}
	return dataDir, positional, nil
}

// openPanelDB opens the panel database of dataDir. store.Open would create an empty database in a wrong directory and
// report success: insist on a real one.
func openPanelDB(ctx context.Context, dataDir string) (*store.Store, error) {
	if _, err := os.Stat(dbPath(dataDir)); err != nil {
		return nil, fmt.Errorf("no panel database in %s (wrong --data-dir?): %w", dataDir, err)
	}
	return store.Open(ctx, dbPath(dataDir))
}

func turnstileOff(ctx context.Context, dataDir string, out io.Writer, now time.Time) error {
	st, err := openPanelDB(ctx, dataDir)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := auth.DisableTurnstile(ctx, st, now); err != nil {
		return err
	}
	fmt.Fprintln(out, "Turnstile is off. The sign-in and setup pages no longer ask for a captcha; switch it on again in Settings > Security.")
	return nil
}

// resetLogin is `mistgate auth reset-login`: without a login it lists the admins, with one it resets (or adds) that
// password login and prints the new password and authenticator secret, once. The secret comes as a QR code, as text
// and as an otpauth:// link; qrInvert draws the QR code for a light terminal.
func resetLogin(ctx context.Context, dataDir, login, adminID string, qrInvert bool, out io.Writer, now time.Time) error {
	st, err := openPanelDB(ctx, dataDir)
	if err != nil {
		return err
	}
	defer st.Close()
	if login == "" {
		admins, err := st.AdminLogins(ctx)
		if err != nil {
			return err
		}
		if len(admins) == 0 {
			fmt.Fprintln(out, "No admin yet: run `mistgate setup` and open its link.")
			return nil
		}
		fmt.Fprintln(out, "Admins of this panel:")
		for _, a := range admins {
			how := "passkeys only"
			if a.Login != "" {
				how = fmt.Sprintf("login %q", a.Login)
			}
			fmt.Fprintf(out, "  %s  %-20s %-8s %s, %d passkey(s)\n", a.ID, a.DisplayName, a.Role, how, a.Passkeys)
		}
		fmt.Fprintln(out, "\nReset one: mistgate auth reset-login <login>   (an admin with passkeys only: <new login> --admin <id>)")
		return nil
	}
	key, err := vault.LoadKey(dataDir, false)
	if err != nil {
		return fmt.Errorf("the master key: %w", err)
	}
	v, err := vault.New(key)
	if err != nil {
		return err
	}
	r, err := auth.ResetLogin(ctx, st, v, auth.ResetLoginInput{Login: login, AdminID: adminID}, now)
	if err != nil {
		return err
	}
	what := "New password and authenticator app"
	if r.Created {
		what = "A password login was added"
	}
	fmt.Fprintf(out, "%s for %s (%s, %s).\n\n", what, r.Admin.DisplayName, r.Admin.Role, r.Admin.ID)
	fmt.Fprintf(out, "  Login:     %s\n", r.Login)
	fmt.Fprintf(out, "  Password:  %s\n\n", r.Password)
	fmt.Fprintln(out, "  Authenticator app: scan this QR code, or add an account with \"Enter a setup key\" (time-based, 6 digits):")
	if code, err := qr.Encode([]byte(r.TOTPURI), qr.M); err == nil { // the text below stays the fallback
		fmt.Fprintln(out)
		fmt.Fprint(out, code.Terminal(qrInvert))
		fmt.Fprintln(out)
	}
	fmt.Fprintf(out, "    Key:     %s\n", groupKey(r.TOTPSecret))
	fmt.Fprintf(out, "    or open this on the phone: %s\n\n", r.TOTPURI)
	fmt.Fprintf(out, "Every session of this admin was ended (%d). Sign in with the login, the password and a code,\n", r.SessionsEnded)
	fmt.Fprintln(out, "then set your own password in Settings > Security. A lost passkey is removed there too.")
	fmt.Fprintln(out, "The password is shown only now and is not stored anywhere in plain text.")
	return nil
}

// groupKey writes a base32 secret in groups of four, the way the panel shows it.
func groupKey(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 && i%4 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return b.String()
}
