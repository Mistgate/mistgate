package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/store"
)

var (
	prefixRe = regexp.MustCompile(`^/[a-z2-7]{24}/$`)
	linkRe   = regexp.MustCompile(`Setup link: (\S+)setup#([A-Za-z0-9_-]{43})\n`)
)

func TestNewInstanceModes(t *testing.T) {
	// (b) secret path prefix
	in, err := newInstance(setupOpts{publicURL: "https://example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	if !prefixRe.MatchString(in.AdminPrefix) || in.RPID != "example.com" || in.RPOrigins[0] != "https://example.com" ||
		in.adminURL() != "https://example.com"+in.AdminPrefix || in.muxPrefix() != in.AdminPrefix {
		t.Errorf("prefix mode: %+v", in)
	}
	in2, _ := newInstance(setupOpts{publicURL: "https://example.com"})
	if in.AdminPrefix == in2.AdminPrefix {
		t.Error("prefix is not random")
	}

	// (a) secret host
	in, err = newInstance(setupOpts{publicURL: "https://example.com", adminHost: "K7Q2X9.example.com"})
	if err != nil || in.AdminPrefix != "/" || in.muxPrefix() != "" || in.RPID != "k7q2x9.example.com" ||
		in.adminURL() != "https://k7q2x9.example.com/" {
		t.Errorf("host mode: %+v %v", in, err)
	}

	// (c) separate admin listener
	in, err = newInstance(setupOpts{adminListen: "127.0.0.1:8081"})
	if err != nil || in.AdminPrefix != "/" || in.RPID != "localhost" || in.adminURL() != "http://localhost:8081/" {
		t.Errorf("listener mode: %+v %v", in, err)
	}

	// overrides, and errors
	in, _ = newInstance(setupOpts{adminListen: "127.0.0.1:9000", rpID: "example.com", rpOrigins: "https://a.example.com/, https://b.example.com"})
	if in.RPID != "example.com" || len(in.RPOrigins) != 2 || in.RPOrigins[0] != "https://a.example.com" {
		t.Errorf("overrides: %+v", in)
	}
	for _, o := range []setupOpts{{}, {publicURL: "example.com"}, {publicURL: "ftp://x.y"}, {adminListen: "8081"}} {
		if _, err := newInstance(o); err == nil {
			t.Errorf("%+v accepted", o)
		}
	}
}

func TestSetupLifecycle(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	ctx := context.Background()
	var out bytes.Buffer
	if err := setup(ctx, dir, setupOpts{publicURL: "https://example.com"}, &out, time.Now()); err != nil {
		t.Fatal(err)
	}
	m := linkRe.FindStringSubmatch(out.String())
	if m == nil {
		t.Fatalf("no setup link in output:\n%s", out.String())
	}
	adminURL := m[1]
	if !strings.HasPrefix(adminURL, "https://example.com/") || !prefixRe.MatchString(strings.TrimPrefix(adminURL, "https://example.com")) {
		t.Errorf("admin URL %q", adminURL)
	}
	if fi, err := os.Stat(filepath.Join(dir, "master.key")); err != nil || fi.Size() != 32 {
		t.Errorf("master key: %v", err)
	}

	// Settings were stored and load back identically.
	st, err := store.Open(ctx, dbPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	in, err := loadInstance(ctx, st)
	if err != nil || in.adminURL() != adminURL || in.RPID != "example.com" {
		t.Fatalf("loadInstance: %+v %v", in, err)
	}

	// Running setup again keeps the same admin URL and issues a new link (the old one dies).
	out.Reset()
	if err := setup(ctx, dir, setupOpts{publicURL: "https://other.example"}, &out, time.Now()); err != nil {
		t.Fatal(err)
	}
	m2 := linkRe.FindStringSubmatch(out.String())
	if m2 == nil || m2[1] != adminURL || m2[2] == m[2] {
		t.Fatalf("second run:\n%s", out.String())
	}

	// Once an admin exists there is no link any more.
	a := store.Admin{ID: store.NewID("adm_"), DisplayName: "A", Role: store.RoleOwner, UserHandle: []byte("h")}
	if err := st.PutSetupToken(ctx, []byte("t"), time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateFirstAdmin(ctx, []byte("t"), time.Now(), a, store.Passkey{ID: "pk_1", AdminID: a.ID, CredentialID: []byte("c"), PublicKey: []byte("k")}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	out.Reset()
	if err := setup(ctx, dir, setupOpts{}, &out, time.Now()); err != nil {
		t.Fatal(err)
	}
	if linkRe.MatchString(out.String()) || !strings.Contains(out.String(), "admin already exists") {
		t.Fatalf("link issued for an installation with an admin:\n%s", out.String())
	}
}

// A lost master.key next to a database: setup must not make a new key, and both setup and serve send the owner to the
// backup instead of to setup.
func TestMissingKeyOnExistingDatabase(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	ctx := context.Background()
	if err := setup(ctx, dir, setupOpts{publicURL: "https://example.com"}, &bytes.Buffer{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "master.key")
	if err := os.Remove(keyPath); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	err := setup(ctx, dir, setupOpts{}, &bytes.Buffer{}, time.Now())
	if err == nil || !strings.Contains(err.Error(), "restore master.key from your backup") || strings.Contains(err.Error(), "run `mistgate setup`") {
		t.Fatalf("setup: %v", err)
	}
	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Fatalf("setup made a new key over an existing database: %v", err)
	}
	if err := keyErr(dir, os.ErrNotExist); !strings.Contains(err.Error(), "restore master.key") {
		t.Errorf("serve, existing database: %v", err)
	}
	if err := keyErr(t.TempDir(), os.ErrNotExist); !strings.Contains(err.Error(), "run `mistgate setup`") {
		t.Errorf("serve, no database: %v", err)
	}
}

func TestLoadInstanceNotConfigured(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if _, err := loadInstance(context.Background(), st); err != errNotConfigured {
		t.Fatalf("got %v", err)
	}
}
