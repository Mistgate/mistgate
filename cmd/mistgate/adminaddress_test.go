package main

import (
	"bytes"
	"context"
	"net"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/store"
)

// freeLoopback is a loopback address nothing listens on now: the command binds the address it is given.
func freeLoopback(t *testing.T) (addr, port string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, port, _ = net.SplitHostPort(ln.Addr().String())
	return ln.Addr().String(), port
}

// `mistgate admin-address` moves the admin to a separate listener and keeps the public address and the secrets, from
// an install with a secret admin host and from one with only a public URL (the admin under a secret prefix).
func TestAdminAddressMovesTheAdminToAListener(t *testing.T) {
	for name, o := range map[string]setupOpts{
		"admin host":    {publicURL: "https://example.com", adminHost: "admin.example.com"},
		"secret prefix": {publicURL: "https://example.com"},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			dir := filepath.Join(t.TempDir(), "data")
			var out bytes.Buffer
			if err := setup(ctx, dir, o, &out, time.Now()); err != nil {
				t.Fatal(err)
			}
			load := func() instance {
				t.Helper()
				st, err := store.Open(ctx, dbPath(dir))
				if err != nil {
					t.Fatal(err)
				}
				defer st.Close()
				in, err := loadInstance(ctx, st)
				if err != nil {
					t.Fatal(err)
				}
				return in
			}
			before := load()
			if o.adminHost == "" && before.AdminPrefix == "/" {
				t.Fatalf("a public-URL install without a secret admin prefix: %+v", before)
			}

			if err := runAdminAddress([]string{"--admin-listen", "nope", "--data-dir", dir}, &out); err == nil {
				t.Fatal("accepted an address without a port")
			}
			if after := load(); !reflect.DeepEqual(after, before) {
				t.Fatalf("a refused address changed the settings: before %+v after %+v", before, after)
			}
			out.Reset()
			addr, port := freeLoopback(t)
			if err := runAdminAddress([]string{"--admin-listen", addr, "--data-dir", dir}, &out); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(out.String(), "Admin URL: http://localhost:"+port+"/\n") || !strings.HasSuffix(out.String(), "\nRestart the panel.\n") {
				t.Errorf("output: %q", out.String())
			}
			if !strings.Contains(out.String(), "Passkeys were registered") || !strings.Contains(out.String(), "while the panel is running") {
				t.Errorf("the first move says nothing of passkeys or of running beside the panel: %q", out.String())
			}
			want := before
			want.AdminListen, want.AdminHost, want.AdminPrefix = addr, "", "/"
			want.RPID, want.RPOrigins = "localhost", []string{"http://localhost:" + port}
			if after := load(); !reflect.DeepEqual(after, want) {
				t.Errorf("settings:\n got  %+v\n want %+v", after, want)
			}

			// From one loopback port to another the passkeys keep working, and the command does not say otherwise.
			out.Reset()
			next, _ := freeLoopback(t)
			if err := runAdminAddress([]string{"--admin-listen", next, "--data-dir", dir}, &out); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(out.String(), "Passkeys") {
				t.Errorf("a move between two loopback ports warns about passkeys: %q", out.String())
			}
		})
	}
}

// `setup --admin-listen` and `admin-address` refuse an admin listener `serve` could not bring up.
func TestAdminListenIsChecked(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "data")
	var out bytes.Buffer
	if err := setup(ctx, dir, setupOpts{publicURL: "https://example.com"}, &out, time.Now()); err != nil {
		t.Fatal(err)
	}
	for listen, want := range map[string]string{
		"127.0.0.1:":       "port",
		"127.0.0.1:abc":    "port",
		"127.0.0.1:99999":  "port",
		"127.0.0.1:0":      "port",
		"127.0.0.1:08443":  "port",
		":8443":            "host",
		"127.0.0.1":        "missing port",
		"0.0.0.0:8443":     "loopback",
		"[::]:8443":        "loopback",
		"192.0.2.1:8443":   "loopback",
		"example.com:8443": "loopback",
		"127.0.0.1:8443":   "",
		"[::1]:8443":       "",
		"localhost:8443":   "",
	} {
		_, setupErr := newInstance(setupOpts{publicURL: "https://example.com", adminListen: listen})
		errs := map[string]error{"setup": setupErr}
		if want != "" {
			// An accepted address is bound for real, which needs that port free and IPv6 on the machine: the move test binds
			// one, here the shared check (newInstance) answers for both commands.
			errs["admin-address"] = runAdminAddress([]string{"--admin-listen", listen, "--data-dir", dir}, &out)
		}
		for name, err := range errs {
			switch {
			case want == "" && err != nil:
				t.Errorf("%s %q: %v", name, listen, err)
			case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
				t.Errorf("%s %q: got %v, want an error about the %s", name, listen, err, want)
			}
		}
	}
}
