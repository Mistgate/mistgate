package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/store"
)

func TestAdminModesAreExclusive(t *testing.T) {
	for name, o := range map[string]setupOpts{
		"listener and host":         {adminListen: "127.0.0.1:8081", adminHost: "k7q2x9.example.com"},
		"listener and host and url": {adminListen: "127.0.0.1:8081", adminHost: "k7q2x9.example.com", publicURL: "https://example.com"},
	} {
		if _, err := newInstance(o); err == nil || !strings.Contains(err.Error(), "mutually exclusive") {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A listener with a public URL (for the decoy and subscription links) is one mode, not two.
	in, err := newInstance(setupOpts{adminListen: "127.0.0.1:8081", publicURL: "https://example.com"})
	if err != nil || in.AdminPrefix != "/" || in.AdminHost != "" || in.AdminListen == "" {
		t.Errorf("listener with public URL: %+v %v", in, err)
	}
	// Setup refuses it end to end, without creating an installation.
	dir := filepath.Join(t.TempDir(), "data")
	var out bytes.Buffer
	err = setup(context.Background(), dir, setupOpts{adminListen: "127.0.0.1:8081", adminHost: "k7q2x9.example.com"}, &out, time.Now())
	if err == nil || strings.Contains(out.String(), "Setup link") {
		t.Fatalf("setup accepted both modes: %v\n%s", err, out.String())
	}
}

func TestInstanceSecrets(t *testing.T) {
	// The agent SNI borrows the panel's own domain (and never the recognisable .invalid): see newAgentSNI.
	sniRe := regexp.MustCompile(`^[a-z2-7]{16}\.example\.com$`)
	genericRe := regexp.MustCompile(`^[a-z2-7]{16}\.com$`)
	prefixRe := regexp.MustCompile(`^/[a-z2-7]{24}/$`)
	a, _ := newInstance(setupOpts{publicURL: "https://example.com"})
	b, _ := newInstance(setupOpts{publicURL: "https://example.com"})
	if !sniRe.MatchString(a.AgentSNI) || !prefixRe.MatchString(a.SubPrefix) || a.AgentSNI == b.AgentSNI || a.SubPrefix == b.SubPrefix || a.SubPrefix == a.AdminPrefix {
		t.Fatalf("secrets: %+v / %+v", a, b)
	}
	in, err := newInstance(setupOpts{adminListen: "127.0.0.1:8081"})
	if err != nil || !genericRe.MatchString(in.AgentSNI) || !prefixRe.MatchString(in.SubPrefix) {
		t.Fatalf("listener mode secrets: %+v %v", in, err)
	}
	ah, err := newInstance(setupOpts{adminHost: "K7Q2X9.Admin.Example.org", publicURL: "https://198.51.100.7"})
	if err != nil || !regexp.MustCompile(`^[a-z2-7]{16}\.k7q2x9\.admin\.example\.org$`).MatchString(ah.AgentSNI) {
		t.Fatalf("admin-host mode (public URL is an IP): %q %v", ah.AgentSNI, err)
	}
	if strings.HasSuffix(a.AgentSNI, ".invalid") || strings.HasSuffix(in.AgentSNI, ".invalid") {
		t.Fatal("the agent SNI must not carry the .invalid marker")
	}

	// Setup stores them, and load returns the same values every time.
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "data")
	var out bytes.Buffer
	if err := setup(ctx, dir, setupOpts{publicURL: "https://example.com"}, &out, time.Now()); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, dbPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	first, err := loadInstance(ctx, st)
	if err != nil || !sniRe.MatchString(first.AgentSNI) || !prefixRe.MatchString(first.SubPrefix) {
		t.Fatalf("load: %+v %v", first, err)
	}
	if out.Reset(); setup(ctx, dir, setupOpts{}, &out, time.Now()) != nil {
		t.Fatal("second setup")
	}
	if again, _ := loadInstance(ctx, st); again.AgentSNI != first.AgentSNI || again.SubPrefix != first.SubPrefix {
		t.Error("secrets changed between runs")
	}

	// An installation from before these settings existed gets them once, then keeps them.
	if _, err := st.W.ExecContext(ctx, `DELETE FROM setting WHERE k IN ('agent_sni', 'sub_prefix')`); err != nil {
		t.Fatal(err)
	}
	legacy, err := loadInstance(ctx, st)
	if err != nil || !sniRe.MatchString(legacy.AgentSNI) || !prefixRe.MatchString(legacy.SubPrefix) {
		t.Fatalf("legacy install: %+v %v", legacy, err)
	}
	if again, _ := loadInstance(ctx, st); again.AgentSNI != legacy.AgentSNI || again.SubPrefix != legacy.SubPrefix {
		t.Error("legacy secrets not stored")
	}
	if legacy.adminURL() != first.adminURL() {
		t.Error("the admin address changed")
	}

	// --dev stores no instance settings but keeps the secrets in the same place.
	dev := devInstance()
	if err := ensureSecrets(ctx, st, &dev); err != nil || dev.AgentSNI != legacy.AgentSNI {
		t.Errorf("ensureSecrets: %+v %v", dev, err)
	}
}

func TestLoadInstanceRejectsMixedModes(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	in, _ := newInstance(setupOpts{publicURL: "https://example.com"})
	in.AdminListen = "127.0.0.1:8081" // prefix mode plus a listener: what an old binary could have stored
	if err := st.SetSettings(ctx, in.settings()); err != nil {
		t.Fatal(err)
	}
	if _, err := loadInstance(ctx, st); err == nil || !strings.Contains(err.Error(), "separate admin listener") {
		t.Errorf("mixed stored modes: %v", err)
	}
}

func TestDataDirIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no POSIX mode bits on Windows")
	}
	ctx := context.Background()
	check := func(path string, want os.FileMode) {
		t.Helper()
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != want {
			t.Errorf("%s: mode %04o, want %04o", filepath.Base(path), fi.Mode().Perm(), want)
		}
	}
	privateUmask()
	// An existing world-readable data dir is tightened by setup.
	dir := filepath.Join(t.TempDir(), "data")
	os.Mkdir(dir, 0o755)
	os.Chmod(dir, 0o755)
	var out bytes.Buffer
	if err := setup(ctx, dir, setupOpts{publicURL: "https://example.com"}, &out, time.Now()); err != nil {
		t.Fatal(err)
	}
	check(dir, 0o700)
	check(filepath.Join(dir, "master.key"), 0o600)
	check(dbPath(dir), 0o600)
	// A new one is created private, and serve tightens an existing one too.
	fresh := filepath.Join(t.TempDir(), "a", "b")
	if err := secureDataDir(fresh); err != nil {
		t.Fatal(err)
	}
	check(fresh, 0o700)
	os.Chmod(fresh, 0o755)
	if err := secureDataDir(fresh); err != nil {
		t.Fatal(err)
	}
	check(fresh, 0o700)
}

// runServe fails before it listens on anything for these: bad flags, a stored admin
// address that does not match the flags, unusable TLS/ACME settings.
func TestServeRefusesBadConfiguration(t *testing.T) {
	ctx := context.Background()
	prefixDir := filepath.Join(t.TempDir(), "prefix")
	var out bytes.Buffer
	if err := setup(ctx, prefixDir, setupOpts{publicURL: "https://example.com"}, &out, time.Now()); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		args []string
		want string
	}{
		"admin listener over a prefix install": {[]string{"--data-dir", prefixDir, "--admin-listen", "127.0.0.1:8099"}, "conflicts"},
		"cert without key":                     {[]string{"--data-dir", prefixDir, "--tls-cert", "c.pem"}, "go together"},
		"bad trusted proxy":                    {[]string{"--data-dir", prefixDir, "--trusted-proxy", "not-an-ip"}, "trusted proxy"},
		"bad trusted proxy in a list":          {[]string{"--data-dir", prefixDir, "--trusted-proxy", "10.0.0.0/8,300.0.0.0/8"}, "trusted proxy"},
		"bad ACME domain":                      {[]string{"--data-dir", prefixDir, "--listen", "127.0.0.1:0", "--acme-domain", "a/b"}, "acme domain"},
		"ACME wildcard":                        {[]string{"--data-dir", prefixDir, "--listen", "127.0.0.1:0", "--acme-domain", "*.example.com"}, "acme domain"},
		"missing certificate":                  {[]string{"--data-dir", prefixDir, "--listen", "127.0.0.1:0", "--tls-cert", "nope.pem", "--tls-key", "nope.key"}, "tls certificate"},
		"data dir that does not exist":         {[]string{"--data-dir", filepath.Join(t.TempDir(), "nope")}, "run `mistgate setup`"},
	} {
		err := runServe(tc.args)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want an error mentioning %q", name, err, tc.want)
		}
	}
	// A key file other users can read stops the panel.
	if runtime.GOOS != "windows" {
		os.Chmod(filepath.Join(prefixDir, "master.key"), 0o644)
		if err := runServe([]string{"--data-dir", prefixDir}); err == nil || !strings.Contains(err.Error(), "chmod 600") {
			t.Errorf("world-readable master key: %v", err)
		}
	}
}

func TestListFlag(t *testing.T) {
	var l listFlag
	for _, v := range []string{"a.example.com", "b.example.com, c.example.com", "", " d.example.com/ "} {
		l.Set(v)
	}
	if got := l.String(); got != "a.example.com,b.example.com,c.example.com,d.example.com" {
		t.Errorf("%q", got)
	}
}
