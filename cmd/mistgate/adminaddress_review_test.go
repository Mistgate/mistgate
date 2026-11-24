package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/store"
)

func TestAdminAddressBindAndOverrides(t *testing.T) {
	t.Setenv("MISTGATE_ADMIN_LISTEN", "")
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "data")
	if err := setup(ctx, dir, setupOpts{publicURL: "https://example.com"}, io.Discard, time.Now()); err != nil {
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
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	addr := ln.Addr().String()
	args := []string{"--data-dir", dir, "--admin-listen", addr}
	if err := runAdminAddress(args, io.Discard); err == nil || !strings.Contains(err.Error(), "cannot be bound") {
		t.Fatalf("occupied listener: %v", err)
	}
	if after := load(); !reflect.DeepEqual(before, after) {
		t.Fatal("bind refusal changed settings")
	}
	t.Setenv("MISTGATE_ADMIN_LISTEN", "127.0.0.1:1")
	// The environment conflict must win even over the occupied port: no bind first.
	if err := runAdminAddress(args, io.Discard); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("environment conflict: %v", err)
	}
	if after := load(); !reflect.DeepEqual(before, after) {
		t.Fatal("override refusal changed settings")
	}
	t.Setenv("MISTGATE_ADMIN_LISTEN", addr)
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runAdminAddress(args, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "cannot inspect another process") {
		t.Fatalf("missing service guidance: %s", &out)
	}
	// A successful probe releases its socket after saving.
	ln2, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer ln2.Close()
	saved := load()
	// An unchanged listener can be owned by the running panel; do not probe it.
	if err := runAdminAddress(args, io.Discard); err != nil {
		t.Fatalf("unchanged occupied listener: %v", err)
	}
	if after := load(); !reflect.DeepEqual(saved, after) {
		t.Fatal("unchanged listener changed settings")
	}
	for _, tc := range []struct{ name, env, flag string }{
		{"environment", "127.0.0.1:1", ""},
		{"flag", "", "127.0.0.1:1"},
		{"flag beats environment", addr, "127.0.0.1:1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MISTGATE_ADMIN_LISTEN", tc.env)
			args := []string{"--data-dir", dir}
			if tc.flag != "" {
				args = append(args, "--admin-listen", tc.flag)
			}
			if err := runServe(args); err == nil || !strings.Contains(err.Error(), "conflicts") {
				t.Fatalf("serve: %v", err)
			}
		})
	}
	t.Run("matching flag overrides conflicting environment", func(t *testing.T) {
		t.Setenv("MISTGATE_ADMIN_LISTEN", "127.0.0.1:1")
		// A missing certificate stops startup after override resolution, before
		// any listener starts; the environment must not cause a conflict.
		err := runServe([]string{"--data-dir", dir, "--admin-listen", addr,
			"--tls-cert", filepath.Join(dir, "missing.pem"), "--tls-key", filepath.Join(dir, "missing.key")})
		if err == nil || !strings.Contains(err.Error(), "tls certificate") {
			t.Fatalf("serve: %v", err)
		}
	})
}

func TestAdminListenPort80Origin(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:80", "[::1]:80", "localhost:80"} {
		in, err := newInstance(setupOpts{adminListen: addr})
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(in.RPOrigins, []string{"http://localhost"}) || in.adminURL() != "http://localhost/" {
			t.Fatalf("%s: %+v", addr, in)
		}
	}
	for _, tc := range []struct {
		override, stored string
		wantErr          bool
	}{
		{"", "127.0.0.1:80", false},
		{"127.0.0.1:80", "127.0.0.1:80", false},
		{"127.0.0.1:81", "127.0.0.1:80", true},
	} {
		if err := checkAdminOverride(tc.override, tc.stored); (err != nil) != tc.wantErr {
			t.Errorf("%+v: %v", tc, err)
		}
	}
}

func TestAdminAddressLegacySettingsStayUntouched(t *testing.T) {
	t.Setenv("MISTGATE_ADMIN_LISTEN", "")
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "data")
	if err := setup(ctx, dir, setupOpts{publicURL: "https://example.com"}, io.Discard, time.Now()); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, dbPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.DeleteSettings(ctx, "agent_sni", "sub_prefix", "link_prefix", "probe_prefix"); err != nil {
		t.Fatal(err)
	}
	// Leave migration 154 genuinely pending: remove both its seed and its
	// applied-version record. A migrating open would recreate them on refusal.
	if _, err := st.W.ExecContext(ctx, "DELETE FROM dns_preset WHERE id = 'dns_builtin_ru_geo'"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.W.ExecContext(ctx, "DELETE FROM goose_db_version WHERE version_id >= 154"); err != nil {
		t.Fatal(err)
	}
	checkPending := func() {
		t.Helper()
		var versions, seeds int
		if err := st.R.QueryRowContext(ctx, "SELECT count(*) FROM goose_db_version WHERE version_id >= 154").Scan(&versions); err != nil {
			t.Fatal(err)
		}
		if err := st.R.QueryRowContext(ctx, "SELECT count(*) FROM dns_preset WHERE id = 'dns_builtin_ru_geo'").Scan(&seeds); err != nil {
			t.Fatal(err)
		}
		if versions != 0 || seeds != 0 {
			t.Fatalf("pending migration ran: version rows=%d, seeded presets=%d", versions, seeds)
		}
	}
	checkPending()
	snapshot := func() map[string]string {
		t.Helper()
		rows, err := st.R.QueryContext(ctx, "SELECT k, v FROM setting")
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		values := map[string]string{}
		for rows.Next() {
			var key, value string
			if err := rows.Scan(&key, &value); err != nil {
				t.Fatal(err)
			}
			values[key] = value
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return values
	}
	before := snapshot()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	addr := ln.Addr().String()
	for _, tc := range []struct{ name, listen, env, want string }{
		{"validation", "127.0.0.1:abc", "", "port"},
		{"bind", addr, "", "cannot be bound"},
		{"override", addr, "127.0.0.1:1", "conflicts"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MISTGATE_ADMIN_LISTEN", tc.env)
			err := runAdminAddress([]string{"--data-dir", dir, "--admin-listen", tc.listen}, io.Discard)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("refusal: %v", err)
			}
			if !reflect.DeepEqual(snapshot(), before) {
				t.Fatal("refusal modified legacy settings")
			}
			checkPending()
		})
	}
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	// Force a write failure inside the batch; no subset of the five fields
	// may be committed, regardless of map iteration order.
	if _, err := st.W.ExecContext(ctx, `CREATE TRIGGER refuse_admin_origin BEFORE UPDATE OF v ON setting
		WHEN NEW.k = 'rp_origins' BEGIN SELECT RAISE(ABORT, 'test refuses origin'); END`); err != nil {
		t.Fatal(err)
	}
	if err := runAdminAddress([]string{"--data-dir", dir, "--admin-listen", addr}, io.Discard); err == nil || !strings.Contains(err.Error(), "test refuses origin") {
		t.Fatalf("expected atomic settings write to fail: %v", err)
	}
	if !reflect.DeepEqual(snapshot(), before) {
		t.Fatal("failed settings transaction partially modified legacy settings")
	}
	if _, err := st.W.ExecContext(ctx, "DROP TRIGGER refuse_admin_origin"); err != nil {
		t.Fatal(err)
	}
	if err := runAdminAddress([]string{"--data-dir", dir, "--admin-listen", addr}, io.Discard); err != nil {
		t.Fatal(err)
	}
	want := before
	var in instance
	if err := in.listenAdmin(addr); err != nil {
		t.Fatal(err)
	}
	want["admin_listen"], want["admin_host"], want["admin_prefix"] = addr, "", "/"
	want["rp_id"], want["rp_origins"] = in.RPID, in.RPOrigins[0]
	if !reflect.DeepEqual(snapshot(), want) {
		t.Fatal("successful move wrote more than the five admin/WebAuthn fields")
	}
	checkPending()
}
