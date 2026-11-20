package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/store"
)

// `mistgate admin-address` moves the admin to a separate listener and keeps the public address and the secrets.
func TestAdminAddressMovesTheAdminToAListener(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "data")
	var out bytes.Buffer
	if err := setup(ctx, dir, setupOpts{publicURL: "https://example.com", adminHost: "admin.example.com"}, &out, time.Now()); err != nil {
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

	if err := runAdminAddress([]string{"--admin-listen", "nope", "--data-dir", dir}, &out); err == nil {
		t.Fatal("accepted an address without a port")
	}
	out.Reset()
	if err := runAdminAddress([]string{"--admin-listen", "127.0.0.1:8443", "--data-dir", dir}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.String(), "Admin URL: http://localhost:8443/\n") || !strings.Contains(out.String(), "Restart the panel with --admin-listen 127.0.0.1:8443.") {
		t.Errorf("output: %q", out.String())
	}
	after := load()
	if after.AdminListen != "127.0.0.1:8443" || after.RPID != "localhost" || after.AdminPrefix != "/" || after.AdminHost != "" {
		t.Errorf("admin: %+v", after)
	}
	if after.PublicURL != before.PublicURL || after.SubPrefix != before.SubPrefix || after.LinkPrefix != before.LinkPrefix ||
		after.ProbePrefix != before.ProbePrefix || after.AgentSNI != before.AgentSNI {
		t.Errorf("changed beyond the admin: before %+v after %+v", before, after)
	}
}
