//go:build js && wasm

package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"errors"
	"strings"
	"syscall/js"
	"testing"
	"time"
)

//go:embed testdata/schema.golden
var d1SchemaGolden embed.FS

func TestD1MigrationParity(t *testing.T) {
	binding := js.Global().Get("__d1")
	binding.Call("__reset")
	st, err := OpenD1(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	got, err := normalizedSchema(context.Background(), st.R)
	if err != nil {
		t.Fatal(err)
	}
	want, err := d1SchemaGolden.ReadFile("testdata/schema.golden")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(got), bytes.TrimSpace(want)) {
		t.Fatalf("D1 schema differs from the SQLite golden")
	}

	wantMigrations, err := d1Migrations()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := st.R.QueryContext(context.Background(), `SELECT version_id FROM goose_db_version
		WHERE is_applied = TRUE AND version_id > 0 ORDER BY version_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var gotVersions []int64
	for rows.Next() {
		var version int64
		if err := rows.Scan(&version); err != nil {
			t.Fatal(err)
		}
		gotVersions = append(gotVersions, version)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(gotVersions) != len(wantMigrations) {
		t.Fatalf("applied %d migration versions, want %d", len(gotVersions), len(wantMigrations))
	}
	for i, migration := range wantMigrations {
		if gotVersions[i] != migration.version {
			t.Errorf("migration version %d = %d, want %d", i, gotVersions[i], migration.version)
		}
	}
}

// D1 rejects a batch whose statement ends in a comment ("SQL code did not contain a statement"), which a SQLite-backed
// fake does not notice, so the shape is checked here.
func TestD1MigrationStatementsEndInCode(t *testing.T) {
	migrations, err := d1Migrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range migrations {
		statements, err := migrationStatements(migration.name)
		if err != nil {
			t.Fatal(err)
		}
		for _, statement := range statements {
			lines := strings.Split(statement.Query, "\n")
			last := lines[len(lines)-1]
			if strings.TrimSpace(stripLineComment(last)) != strings.TrimSpace(last) {
				t.Errorf("%s: a statement ends in a comment: %q", migration.name, last)
			}
		}
	}
}

func TestD1StoreSmoke(t *testing.T) {
	binding := js.Global().Get("__d1")
	binding.Call("__reset")
	st, err := OpenD1(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()

	// This smoke covers Setting, SetSettings, Audit, ListAudit, and NodeHello's missing-node error mapping.
	if err := st.SetSettings(ctx, map[string]string{"edge.smoke": "ready"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.NodeHello(ctx, "nod_missing", HelloInfo{Instance: "instance"}, time.Unix(123, 0).UTC()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("NodeHello(missing) = %v, want ErrNotFound", err)
	}
	if value, err := st.Setting(ctx, "edge.smoke"); err != nil || value != "ready" {
		t.Fatalf("Setting() = %q, %v", value, err)
	}
	if err := st.Audit(ctx, time.Unix(123, 0).UTC(), AuditEntry{Actor: "anonymous", Action: "edge.smoke", Result: "ok"}); err != nil {
		t.Fatal(err)
	}
	rows, err := st.ListAudit(ctx, "", 0, 10)
	if err != nil || len(rows) != 1 || rows[0].Action != "edge.smoke" {
		t.Fatalf("ListAudit() = %+v, %v", rows, err)
	}
}

func TestD1FleetBatchSmoke(t *testing.T) {
	st := openD1Store(t)
	ctx := context.Background()
	now := time.Unix(1_700_000_000, 0).UTC()
	if err := st.InsertCA(ctx, CARow{ID: "cas_d1", CertPEM: "ca", Fingerprint: "fingerprint", KeyEnc: []byte{1},
		NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(1, 0, 0)}, now); err != nil {
		t.Fatal(err)
	}
	node, err := st.CreateEnrollment(ctx, &NodeRow{ID: "nod_d1_stats", Name: "d1-stats", Address: "example.com"}, "", []byte("d1-stats-token"), "adm_test", now, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	binding := js.Global().Get("__d1")
	var stats FleetStatsOut
	var calls d1CallCount
	calls = countD1Queries(t, binding, "IngestStats", func() {
		stats, err = st.IngestStats(ctx, FleetStatsIn{NodeID: node.ID, Instance: "d1-instance", Seq: 1, Now: now, HourStart: now.Unix() / 3600 * 3600})
	})
	if err != nil || stats.Duplicate {
		t.Fatalf("IngestStats() = %+v, %v", stats, err)
	}
	if calls.queries != 2 || calls.batches != 2 {
		t.Fatalf("IngestStats used %d D1 calls in %d batches, want 2 calls in 2 batches", calls.queries, calls.batches)
	}
	var duplicate bool
	calls = countD1Queries(t, binding, "IngestEvent", func() {
		duplicate, err = st.IngestEvent(ctx, node.ID, "d1-instance", 2, now, EventRow{Time: now, Severity: 1, Code: "edge.smoke.event"})
	})
	if err != nil || duplicate {
		t.Fatalf("IngestEvent() = duplicate %v, %v", duplicate, err)
	}
	if calls.queries != 1 || calls.batches != 1 {
		t.Fatalf("IngestEvent used %d D1 calls in %d batches, want 1 call in 1 batch", calls.queries, calls.batches)
	}
	calls = countD1Queries(t, binding, "NodeHello", func() {
		_, acked, helloErr := st.NodeHello(ctx, node.ID, HelloInfo{AgentVersion: "test", Instance: "d1-instance"}, now)
		if helloErr != nil {
			err = helloErr
			return
		}
		if acked != 2 {
			err = errors.New("NodeHello did not return the committed sequence")
		}
	})
	if err != nil {
		t.Fatalf("NodeHello() = %v", err)
	}
	if calls.queries != 1 || calls.batches != 1 {
		t.Fatalf("NodeHello used %d D1 calls in %d batches, want 1 call in 1 batch", calls.queries, calls.batches)
	}

	enrolled, err := st.CreateEnrollment(ctx, &NodeRow{ID: "nod_d1_enroll", Name: "d1-enroll", Address: "example.com"}, "", []byte("d1-enroll-token"), "adm_test", now, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	result, err := st.Enroll(ctx, []byte("d1-enroll-token"), []byte("d1-key"), now, func(nodeID string) (CertRow, error) {
		return CertRow{Serial: "serial-d1", NodeID: nodeID, CAID: "cas_d1", PEM: "certificate", NotBefore: now,
			NotAfter: now.Add(24 * time.Hour), IssuedAt: now}, nil
	})
	if err != nil || result.NodeID != enrolled.ID || result.Replay {
		t.Fatalf("Enroll() = %+v, %v", result, err)
	}
	if err := st.NodeApplied(ctx, enrolled.ID, 1, "hash", nil, now); err != nil {
		t.Fatalf("write-only NodeApplied transaction on D1: %v", err)
	}
	if err := st.SkipSeq(ctx, enrolled.ID, "d1-instance", 3, now); err != nil {
		t.Fatalf("write-only SkipSeq transaction on D1: %v", err)
	}
	if err := st.RenewCert(ctx, CertRow{Serial: "serial-renewed", NodeID: enrolled.ID, CAID: "cas_d1", PEM: "renewed",
		NotBefore: now, NotAfter: now.Add(48 * time.Hour), IssuedAt: now}, now, time.Minute); err != nil {
		t.Fatalf("write-only RenewCert transaction on D1: %v", err)
	}
	calls = countD1Queries(t, binding, "RetireNode", func() { err = st.RetireNode(ctx, enrolled.ID, now) })
	if err != nil {
		t.Fatalf("RetireNode() = %v", err)
	}
	if calls.queries != 1 || calls.batches != 1 {
		t.Fatalf("RetireNode used %d D1 calls in %d batches, want 1 call in 1 batch", calls.queries, calls.batches)
	}
}

type d1CallCount struct{ queries, batches int }

func countD1Queries(t *testing.T, binding js.Value, label string, run func()) d1CallCount {
	t.Helper()
	binding.Call("__beginQueryCount", label)
	run()
	counts := binding.Call("__endQueryCount")
	return d1CallCount{queries: counts.Get("sequentialQueries").Int(), batches: counts.Get("batchCalls").Int()}
}

func openD1Store(t *testing.T) *Store {
	t.Helper()
	binding := js.Global().Get("__d1")
	binding.Call("__reset")
	st, err := OpenD1(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestD1BatchGuard(t *testing.T) {
	ctx := context.Background()
	st := openD1Store(t)
	if _, err := st.W.ExecContext(ctx, `CREATE TABLE batch_guard_values (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.batch(ctx,
		Stmt{Query: `INSERT INTO batch_guard_values (id) VALUES (1)`},
		guard(`0`),
	); !errors.Is(err, errGuard) {
		t.Fatalf("false guard error = %v, want errGuard", err)
	}
	var count int
	if err := st.R.QueryRowContext(ctx, `SELECT count(*) FROM batch_guard_values`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("failed guard left %d earlier writes", count)
	}

	if _, err := st.batch(ctx, guard(`NULL`)); !errors.Is(err, errGuard) {
		t.Fatalf("NULL guard error = %v, want errGuard", err)
	}
	if err := st.R.QueryRowContext(ctx, `SELECT count(*) FROM batch_guard_values`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("NULL guard left %d writes", count)
	}

	if _, err := st.batch(ctx,
		guard(`1`),
		Stmt{Query: `INSERT INTO batch_guard_values (id) VALUES (2)`},
	); err != nil {
		t.Fatalf("true guard failed: %v", err)
	}
	if err := st.R.QueryRowContext(ctx, `SELECT count(*) FROM batch_guard_values`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("true guard left %d writes, want one", count)
	}
}

func TestD1RewrittenAuthStoreMethods(t *testing.T) {
	ctx := context.Background()
	st := openD1Store(t)
	now := time.Unix(1_800_000_000, 0).UTC()
	tokenHash := []byte("setup-auth")
	t.Run("PutSetupToken", func(t *testing.T) {
		if err := st.PutSetupToken(ctx, tokenHash, now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("AuthCeremonyCAS", func(t *testing.T) {
		row := AuthCeremony{ID: "ceremony_edge_cas", Kind: "setup-password", SessionData: "{}", ExpiresAt: now.Add(time.Minute)}
		if err := st.PutAuthCeremony(ctx, row, now, 8, 100); err != nil {
			t.Fatal(err)
		}
		got, err := st.GetAuthCeremony(ctx, row.ID, now)
		if err != nil || got.Tries != 0 {
			t.Fatalf("get: %+v %v", got, err)
		}
		if changed, err := st.FailAuthCeremony(ctx, row.ID, 3); err != nil || !changed {
			t.Fatalf("fail: changed=%v err=%v", changed, err)
		}
		got, err = st.GetAuthCeremony(ctx, row.ID, now)
		if err != nil || got.Tries != 1 {
			t.Fatalf("get after failure: %+v %v", got, err)
		}
		if consumed, err := st.ConsumeAuthCeremony(ctx, row.ID); err != nil || !consumed {
			t.Fatalf("consume after a wrong code: consumed=%v err=%v", consumed, err)
		}
		row.ID = "ceremony_edge_max"
		if err := st.PutAuthCeremony(ctx, row, now, 8, 100); err != nil {
			t.Fatal(err)
		}
		if changed, err := st.FailAuthCeremony(ctx, row.ID, 1); err != nil || !changed {
			t.Fatalf("maximum failure: changed=%v err=%v", changed, err)
		}
		if _, err := st.GetAuthCeremony(ctx, row.ID, now); !errors.Is(err, ErrAuthCeremonyNotFound) {
			t.Fatalf("ceremony after maximum failure: %v", err)
		}
	})
	a := Admin{ID: NewID("adm_"), DisplayName: "Owner", Role: RoleOwner, UserHandle: []byte("owner-handle")}
	pk := Passkey{ID: NewID("pk_"), AdminID: a.ID, CredentialID: []byte("owner-credential"), PublicKey: []byte("key"), Name: "Primary"}
	t.Run("CreateFirstAdmin", func(t *testing.T) {
		if err := st.CreateFirstAdmin(ctx, tokenHash, now, a, pk); err != nil {
			t.Fatalf("create: %v", err)
		}
		if err := st.CreateFirstAdmin(ctx, tokenHash, now, Admin{ID: "adm_second", DisplayName: "Second", Role: RoleOwner, UserHandle: []byte("other")}, Passkey{ID: "pk_second", AdminID: "adm_second", CredentialID: []byte("other-cred"), PublicKey: []byte("key")}); !errors.Is(err, ErrSetupClosed) {
			t.Fatalf("reused setup token: %v", err)
		}
	})
	t.Run("AddPassword", func(t *testing.T) {
		if err := st.AddPassword(ctx, PasswordCred{AdminID: a.ID, Login: "owner", Hash: "hash-one", TOTPSecret: []byte("sealed"), TOTPStep: 7}, now); err != nil {
			t.Fatalf("add: %v", err)
		}
		if err := st.AddPassword(ctx, PasswordCred{AdminID: a.ID, Login: "owner", Hash: "hash-two", TOTPSecret: []byte("sealed"), TOTPStep: 8}, now); !errors.Is(err, ErrAccessExists) {
			t.Fatalf("existing credential: %v", err)
		}
	})
	t.Run("RecordLoginFailure", func(t *testing.T) {
		if _, err := st.RecordLoginFailure(ctx, "owner", now, 3, time.Hour, time.Minute); err != nil {
			t.Fatal(err)
		}
	})
	if _, err := st.W.ExecContext(ctx, `INSERT INTO session (token_hash, admin_id, created_at, last_seen_at, expires_at, ip, user_agent) VALUES (?, ?, ?, ?, ?, '', '')`, []byte("session-one"), a.ID, now.Unix(), now.Unix(), now.Add(time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	t.Run("ResetPasswordLogin", func(t *testing.T) {
		ended, err := st.ResetPasswordLogin(ctx, PasswordCred{AdminID: a.ID, Login: "owner", Hash: "hash-reset", TOTPSecret: []byte("sealed-new"), TOTPStep: 3}, false,
			[]string{"owner"}, now.Add(time.Minute), AuditEntry{Actor: a.ID, Action: "reset_login", Result: "ok"})
		if err != nil || ended != 1 {
			t.Fatalf("update: ended=%d err=%v", ended, err)
		}
		if got, err := st.PasswordByAdmin(ctx, a.ID); err != nil || got.Hash != "hash-reset" || got.TOTPStep != 7 {
			t.Fatalf("credential: %+v %v", got, err)
		}
		if failure, err := st.LoginFailures(ctx, "owner", now.Add(time.Minute), time.Hour); err != nil || failure.Failures != 0 {
			t.Fatalf("lock clear: %+v %v", failure, err)
		}
		if rows, err := st.ListAudit(ctx, "", 0, 10); err != nil || len(rows) != 1 || rows[0].Action != "reset_login" {
			t.Fatalf("audit: %+v %v", rows, err)
		}
	})
	t.Run("DeletePasskey", func(t *testing.T) {
		if err := st.DeletePasskey(ctx, a.ID, pk.ID); err != nil {
			t.Fatalf("with password fallback: %v", err)
		}
	})

	if _, err := st.W.ExecContext(ctx, `INSERT INTO admin (id, display_name, role, user_handle, created_at) VALUES (?, ?, ?, ?, ?)`, "adm_helper", "Helper", RoleHelper, []byte("helper-handle"), now.Unix()); err != nil {
		t.Fatal(err)
	}
	t.Run("ResetPasswordLoginCreate", func(t *testing.T) {
		ended, err := st.ResetPasswordLogin(ctx, PasswordCred{AdminID: "adm_helper", Login: "helper", Hash: "helper-hash", TOTPSecret: []byte("helper-secret")}, true,
			nil, now.Add(2*time.Minute), AuditEntry{Actor: "cli", Action: "reset_login", Result: "ok"})
		if err != nil || ended != 0 {
			t.Fatalf("create: ended=%d err=%v", ended, err)
		}
		if got, err := st.PasswordByLogin(ctx, "helper"); err != nil || got.AdminID != "adm_helper" || got.Hash != "helper-hash" {
			t.Fatalf("created credential: %+v %v", got, err)
		}
	})

	t.Run("CreateFirstAdminPassword", func(t *testing.T) {
		fresh := openD1Store(t)
		if err := fresh.PutSetupToken(ctx, []byte("setup-password"), now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		admin := Admin{ID: NewID("adm_"), DisplayName: "Password owner", Role: RoleOwner, UserHandle: []byte("password-handle")}
		if err := fresh.CreateFirstAdminPassword(ctx, []byte("setup-password"), now, admin, PasswordCred{Login: "first", Hash: "first-hash", TOTPSecret: []byte("secret")}); err != nil {
			t.Fatal(err)
		}
		if cred, err := fresh.PasswordByLogin(ctx, "first"); err != nil || cred.AdminID != admin.ID {
			t.Fatalf("CreateFirstAdminPassword: %+v %v", cred, err)
		}
	})
}

func TestD1RewrittenTokenAndMCPPlanMethods(t *testing.T) {
	ctx := context.Background()
	st := openD1Store(t)
	now := time.Unix(1_800_000_000, 0).UTC()
	if _, err := st.W.ExecContext(ctx, `INSERT INTO admin (id, display_name, role, user_handle, created_at) VALUES ('adm_owner', 'Owner', 'owner', x'01', ?)`, now.Unix()); err != nil {
		t.Fatal(err)
	}
	newToken := func(id, name string) APIToken {
		t.Helper()
		return APIToken{ID: id, Name: name, Profile: ProfileAdmin, Hint: "wxyz", RatePerMin: 60, CreatedBy: "adm_owner", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	}
	token := newToken("tok_one", "one")
	t.Run("CreateAPIToken", func(t *testing.T) {
		if err := st.CreateAPIToken(ctx, token, []byte("secret-hash-one")); err != nil {
			t.Fatalf("create: %v", err)
		}
		if err := st.CreateAPIToken(ctx, newToken("tok_dup", "one"), []byte("secret-hash-dup")); !errors.Is(err, ErrNameTaken) {
			t.Fatalf("duplicate name: %v", err)
		}
	})
	plan := MCPPlan{ID: "pln_decide", TokenID: token.ID, Tool: "approval_tool", ParamsJSON: "{}", ConfirmHash: []byte("confirm-decide"),
		NeedsApproval: true, Status: PlanAwaiting, CreatedAt: now}
	t.Run("CreateMCPPlan", func(t *testing.T) {
		if err := st.CreateMCPPlan(ctx, plan, 5, 5); err != nil {
			t.Fatalf("create: %v", err)
		}
		gotPlan, err := st.GetMCPPlan(ctx, plan.ID)
		if err != nil || gotPlan.Status != PlanAwaiting || gotPlan.TokenName != token.Name {
			t.Fatalf("created plan: %+v %v", gotPlan, err)
		}
	})
	t.Run("DecideMCPPlan", func(t *testing.T) {
		decided, err := st.DecideMCPPlan(ctx, plan.ID, "adm_owner", false, now.Add(time.Minute))
		if err != nil || decided.Status != PlanRejected || decided.DecidedByName != "Owner" {
			t.Fatalf("decide: %+v %v", decided, err)
		}
	})

	paramsHash := sha256.Sum256([]byte("{}"))
	confirmHash := sha256.Sum256([]byte("confirm-secret"))
	secretPlan := MCPPlan{ID: "pln_secret", TokenID: token.ID, Tool: "node_install", ParamsJSON: "{}", ParamsHash: paramsHash[:], ConfirmHash: confirmHash[:],
		NeedsApproval: true, Status: PlanAwaiting, CreatedAt: now}
	t.Run("CreateMCPPlanAwaiting", func(t *testing.T) {
		if err := st.CreateMCPPlan(ctx, secretPlan, 5, 5); err != nil {
			t.Fatalf("create awaiting: %v", err)
		}
	})
	t.Run("ApproveMCPPlanWithSecret", func(t *testing.T) {
		approved, err := st.ApproveMCPPlanWithSecret(ctx, secretPlan.ID, "adm_owner", []byte("sealed-owner-secret"), now.Add(time.Minute))
		if err != nil || approved.Status != PlanApproved {
			t.Fatalf("approve: %+v %v", approved, err)
		}
	})
	t.Run("BeginApply", func(t *testing.T) {
		started, err := st.BeginApply(ctx, secretPlan.ID, token.ID, secretPlan.Tool, paramsHash[:], confirmHash[:], now.Add(2*time.Minute))
		if err != nil || started.Status != PlanApplying {
			t.Fatalf("begin apply: %+v %v", started, err)
		}
	})
	t.Run("TakeMCPPlanOwnerSecret", func(t *testing.T) {
		secret, err := st.TakeMCPPlanOwnerSecret(ctx, secretPlan.ID, token.ID, secretPlan.Tool)
		if err != nil || string(secret) != "sealed-owner-secret" {
			t.Fatalf("take: %q %v", secret, err)
		}
		if _, err := st.TakeMCPPlanOwnerSecret(ctx, secretPlan.ID, token.ID, secretPlan.Tool); !errors.Is(err, ErrNotFound) {
			t.Fatalf("take twice: %v", err)
		}
	})

	t.Run("RevokeAPIToken", func(t *testing.T) {
		revoked, err := st.RevokeAPIToken(ctx, token.ID, "adm_owner", now.Add(3*time.Minute))
		if err != nil || !revoked.Revoked() || revoked.RevokedBy != "adm_owner" {
			t.Fatalf("revoke: %+v %v", revoked, err)
		}
	})
}

func TestD1RewrittenTelegramMethods(t *testing.T) {
	ctx := context.Background()
	st := openD1Store(t)
	now := time.Unix(1_800_000_000, 0).UTC()
	for _, id := range []string{"adm_one", "adm_two"} {
		if _, err := st.W.ExecContext(ctx, `INSERT INTO admin (id, display_name, role, user_handle, created_at) VALUES (?, ?, 'owner', ?, ?)`, id, id, []byte(id), now.Unix()); err != nil {
			t.Fatal(err)
		}
	}
	bot := TelegramBotRow{Token: []byte("sealed-token"), BotID: 10, Username: "first"}
	t.Run("SetTelegramBot", func(t *testing.T) {
		if dropped, err := st.SetTelegramBot(ctx, bot, now); err != nil || dropped != 0 {
			t.Fatalf("initial: dropped=%d err=%v", dropped, err)
		}
	})
	t.Run("BindTelegramChat", func(t *testing.T) {
		if err := st.BindTelegramChat(ctx, "adm_one", 100, now); err != nil {
			t.Fatalf("first owner: %v", err)
		}
		if err := st.BindTelegramChat(ctx, "adm_two", 100, now.Add(time.Minute)); err != nil {
			t.Fatalf("replace chat owner: %v", err)
		}
	})
	if _, err := st.TelegramLink(ctx, "adm_one"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old chat owner still linked: %v", err)
	}
	t.Run("BindTelegramChatReplacement", func(t *testing.T) {
		if err := st.BindTelegramChat(ctx, "adm_one", 101, now.Add(2*time.Minute)); err != nil {
			t.Fatalf("second link: %v", err)
		}
	})
	t.Run("SetTelegramBotReplacement", func(t *testing.T) {
		if dropped, err := st.SetTelegramBot(ctx, TelegramBotRow{Token: []byte("sealed-token-2"), BotID: 20, Username: "second"}, now.Add(3*time.Minute)); err != nil || dropped != 2 {
			t.Fatalf("switch: dropped=%d err=%v", dropped, err)
		}
	})
	t.Run("BindTelegramChatMissingAdmin", func(t *testing.T) {
		if err := st.BindTelegramChat(ctx, "missing", 102, now); !errors.Is(err, ErrNotFound) {
			t.Fatalf("missing admin: %v", err)
		}
		if err := st.BindTelegramChat(ctx, "adm_one", 103, now); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("ClearTelegramBot", func(t *testing.T) {
		if dropped, err := st.ClearTelegramBot(ctx); err != nil || dropped != 1 {
			t.Fatalf("clear: dropped=%d err=%v", dropped, err)
		}
	})
	if _, err := st.TelegramBot(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("TelegramBot after clear: %v", err)
	}
}

func TestD1RewrittenAccessMethods(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	newUser := func(id, name, groupID string) AccessUser {
		return AccessUser{ID: id, Name: name, GroupID: groupID, Status: "active", AppHapp: true, AppAmnezia: true,
			AllNodes: true, QuotaReset: "month", PeriodStart: now, DeviceLimit: 5, SubTokenHash: []byte(id + "-hash"),
			SubTokenEnc: []byte("sealed"), CreatedAt: now}
	}
	createGroup := func(t *testing.T, st *Store, id string, profileIDs []string) {
		t.Helper()
		if err := st.Access().CreateGroup(ctx, AccessGroup{ID: id, Name: id, ProfileIDs: profileIDs, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	createUser := func(t *testing.T, st *Store, id, name, groupID string) {
		t.Helper()
		if err := st.Access().CreateUser(ctx, newUser(id, name, groupID), AccessDevice{}, nil); err != nil {
			t.Fatal(err)
		}
	}
	createProfile := func(t *testing.T, st *Store, id string) {
		t.Helper()
		if err := st.Access().CreateProfile(ctx, AccessProfile{ID: id, Protocol: "hysteria2", Name: id, SettingsJSON: "{}", CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	createNode := func(t *testing.T, st *Store, id string) {
		t.Helper()
		if _, err := st.W.ExecContext(ctx, `INSERT INTO node (id, name, address, state, created_at) VALUES (?, ?, '203.0.113.10', 'active', ?)`, id, id, unix(now)); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("CreateUser", func(t *testing.T) {
		st := openD1Store(t)
		createGroup(t, st, "grp_user", nil)
		createNode(t, st, "nod_user")
		u := newUser("usr_user", "User", "grp_user")
		u.AllNodes, u.NodeIDs = false, []string{"nod_user"}
		dev := AccessDevice{ID: "dev_user", UserID: u.ID, Implicit: true, CreatedAt: now}
		cred := AccessCred{ID: "crd_user", DeviceID: dev.ID, UserID: u.ID, Protocol: "hysteria2", SecretEnc: []byte("sealed"), DataJSON: `{}`, CreatedAt: now}
		if err := st.Access().CreateUser(ctx, u, dev, []AccessCred{cred}); err != nil {
			t.Fatal(err)
		}
		if got, err := st.Access().User(ctx, "usr_user"); err != nil || got.Name != "User" || len(got.NodeIDs) != 1 || got.NodeIDs[0] != "nod_user" {
			t.Fatalf("user = %+v, %v", got, err)
		}
		data, err := st.Access().SubscriptionData(ctx, u.ID, "grp_user", now, true)
		if err != nil || data.ImplicitDevice.ID != dev.ID || len(data.ImplicitCreds) != 1 || data.ImplicitCreds[0].ID != cred.ID {
			t.Fatalf("user implicit credentials = %+v, %+v", data, err)
		}
	})
	t.Run("UpdateUser", func(t *testing.T) {
		st := openD1Store(t)
		createGroup(t, st, "grp_update_user", nil)
		createNode(t, st, "nod_update_user")
		createUser(t, st, "usr_update_user", "Before", "grp_update_user")
		u, err := st.Access().User(ctx, "usr_update_user")
		if err != nil {
			t.Fatal(err)
		}
		u.Name, u.AllNodes, u.NodeIDs = "After", false, []string{"nod_update_user"}
		if err := st.Access().UpdateUser(ctx, u, true); err != nil {
			t.Fatal(err)
		}
		if got, err := st.Access().User(ctx, u.ID); err != nil || got.Name != "After" || len(got.NodeIDs) != 1 || got.NodeIDs[0] != "nod_update_user" {
			t.Fatalf("user = %+v, %v", got, err)
		}
	})
	t.Run("EnsureImplicitDeviceWithoutCredentials", func(t *testing.T) {
		st := openD1Store(t)
		createGroup(t, st, "grp_add_device", nil)
		createUser(t, st, "usr_add_device", "Device user", "grp_add_device")
		dev := AccessDevice{ID: "dev_add_device", UserID: "usr_add_device", Implicit: true, CreatedAt: now}
		got, live, created, err := st.Access().EnsureImplicitDevice(ctx, dev, nil)
		if err != nil || created || got.ID != "" || len(live) != 0 {
			t.Fatalf("EnsureImplicitDevice without credentials = %+v, %+v, %v, %v", got, live, created, err)
		}
		if _, err := st.Access().ImplicitDevice(ctx, dev.UserID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("implicit device after empty ensure = %v, want not found", err)
		}
	})
	t.Run("EnsureImplicitDeviceAddsCredentials", func(t *testing.T) {
		st := openD1Store(t)
		createGroup(t, st, "grp_add_creds", nil)
		createUser(t, st, "usr_add_creds", "Credential user", "grp_add_creds")
		dev := AccessDevice{ID: "dev_add_creds", UserID: "usr_add_creds", Implicit: true, CreatedAt: now}
		cred := AccessCred{ID: "crd_add_creds", UserID: dev.UserID, Protocol: "hysteria2", SecretEnc: []byte("sealed"), DataJSON: `{}`, CreatedAt: now}
		got, live, created, err := st.Access().EnsureImplicitDevice(ctx, dev, []AccessCred{cred})
		if err != nil || !created || got.ID != dev.ID || len(live) != 1 || live[0].ID != cred.ID {
			t.Fatalf("EnsureImplicitDevice = %+v, %+v, %v, %v", got, live, created, err)
		}
		data, err := st.Access().SubscriptionData(ctx, dev.UserID, "grp_add_creds", now, false)
		if err != nil || len(data.Devices) != 1 || data.Devices[0].ID != dev.ID || len(data.Devices[0].Protocols) != 1 || data.Devices[0].Protocols[0] != cred.Protocol {
			t.Fatalf("subscription devices after add credentials = %+v, %v", data.Devices, err)
		}
	})
	t.Run("ReadBatchAndEnsureImplicitDevice", func(t *testing.T) {
		st := openD1Store(t)
		createGroup(t, st, "grp_implicit_device", nil)
		createUser(t, st, "usr_implicit_device", "Implicit device", "grp_implicit_device")
		var value string
		r := reads{}
		r.add(func(rows [][]any) error {
			if len(rows) != 1 {
				return errors.New("read batch returned no row")
			}
			return batchRow(rows[0]).Scan(&value)
		}, `SELECT 'd1' AS value`)
		if err := r.run(ctx, st); err != nil || value != "d1" {
			t.Fatalf("read batch = %q, %v", value, err)
		}

		dev := AccessDevice{ID: "dev_implicit_device_a", UserID: "usr_implicit_device", Implicit: true, CreatedAt: now, NoInitialSeen: true}
		cred := AccessCred{ID: "crd_implicit_device_a", UserID: dev.UserID, Protocol: "hysteria2", SecretEnc: []byte("sealed"), DataJSON: `{}`, CreatedAt: now}
		got, live, created, err := st.Access().EnsureImplicitDevice(ctx, dev, []AccessCred{cred})
		if err != nil || !created || got.ID != dev.ID || len(live) != 1 {
			t.Fatalf("EnsureImplicitDevice = %+v, %+v, %v, %v", got, live, created, err)
		}
		dev.ID, cred.ID = "dev_implicit_device_b", "crd_implicit_device_b"
		got, live, created, err = st.Access().EnsureImplicitDevice(ctx, dev, []AccessCred{cred})
		if err != nil || created || got.ID != "dev_implicit_device_a" || len(live) != 1 || live[0].ID != "crd_implicit_device_a" {
			t.Fatalf("repeat EnsureImplicitDevice = %+v, %+v, %v, %v", got, live, created, err)
		}
	})
	t.Run("EnsureImplicitAWGCreds", func(t *testing.T) {
		st := openD1Store(t)
		createGroup(t, st, "grp_implicit_awg", nil)
		createUser(t, st, "usr_implicit_awg", "Implicit AWG", "grp_implicit_awg")
		profileID := "prf_implicit_awg"
		if err := st.Access().CreateProfile(ctx, AccessProfile{ID: profileID, Protocol: "awg", Name: profileID, SettingsJSON: "{}", CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
		dev := AccessDevice{ID: "dev_implicit_awg", UserID: "usr_implicit_awg", Implicit: true, CreatedAt: now}
		want := []AWGImplicitWant{{ProfileID: profileID, MaxIdx: 8, Issue: func(int) (AccessCred, string, error) {
			return AccessCred{ID: "crd_implicit_awg", Protocol: "awg", SecretEnc: []byte("sealed"), DataJSON: "{}"}, "pub_implicit_awg", nil
		}}}
		result, err := st.Access().EnsureImplicitAWGCreds(ctx, dev.UserID, dev, now, want)
		if err != nil || result.Device.ID == "" || len(result.Added) != 1 || len(result.Creds) != 1 {
			t.Fatalf("EnsureImplicitAWGCreds = %+v, %v; want one added and live credential", result, err)
		}
		deviceID := result.Device.ID
		result, err = st.Access().EnsureImplicitAWGCreds(ctx, dev.UserID, dev, now, want)
		if err != nil || result.Device.ID != deviceID || len(result.Added) != 0 || len(result.Creds) != 1 {
			t.Fatalf("repeat EnsureImplicitAWGCreds = %+v, %v; want one live credential and no additions", result, err)
		}
	})
	t.Run("RevokeDevice", func(t *testing.T) {
		st := openD1Store(t)
		createGroup(t, st, "grp_revoke_device", nil)
		createUser(t, st, "usr_revoke_device", "Revoke user", "grp_revoke_device")
		dev := AccessDevice{ID: "dev_revoke_device", UserID: "usr_revoke_device", Implicit: true, CreatedAt: now}
		cred := AccessCred{ID: "crd_revoke_device", DeviceID: dev.ID, UserID: dev.UserID, Protocol: "hysteria2", SecretEnc: []byte("sealed"), DataJSON: `{}`, CreatedAt: now}
		if _, _, created, err := st.Access().EnsureImplicitDevice(ctx, dev, []AccessCred{cred}); err != nil || !created {
			t.Fatalf("create implicit device for revoke = created %v, %v", created, err)
		}
		if userID, err := st.Access().RevokeDevice(ctx, dev.ID, now); err != nil || userID != dev.UserID {
			t.Fatalf("revoke = %q, %v", userID, err)
		}
		data, err := st.Access().SubscriptionData(ctx, dev.UserID, "grp_revoke_device", now, false)
		if err != nil || len(data.Devices) != 0 {
			t.Fatalf("subscription devices after revoke = %+v, %v", data.Devices, err)
		}
	})
	t.Run("UpdateProfile", func(t *testing.T) {
		st := openD1Store(t)
		createProfile(t, st, "prf_update_profile")
		p := AccessProfile{ID: "prf_update_profile", Name: "Renamed", SettingsJSON: `{"updated":true}`}
		got, err := st.Access().UpdateProfile(ctx, p, 1, false, true, now.Add(time.Minute))
		if err != nil || got.Name != "Renamed" || got.Version != 2 {
			t.Fatalf("updated profile = %+v, %v", got, err)
		}
	})
	t.Run("DeleteProfile", func(t *testing.T) {
		st := openD1Store(t)
		createProfile(t, st, "prf_delete_profile")
		if err := st.Access().DeleteProfile(ctx, "prf_delete_profile", now); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Access().Profile(ctx, "prf_delete_profile"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("profile after delete: %v", err)
		}
	})
	t.Run("CreateInbound", func(t *testing.T) {
		st := openD1Store(t)
		createProfile(t, st, "prf_create_inbound")
		createNode(t, st, "nod_create_inbound")
		in := AccessInbound{ID: "inb_create_inbound", ProfileID: "prf_create_inbound", NodeID: "nod_create_inbound", Enabled: true, CreatedAt: now}
		if err := st.Access().CreateInbound(ctx, in); err != nil {
			t.Fatal(err)
		}
		if got, err := st.Access().Inbound(ctx, in.ID); err != nil || got.ProfileID != in.ProfileID || got.NodeID != in.NodeID {
			t.Fatalf("inbound = %+v, %v", got, err)
		}
	})
	t.Run("DeleteInbound", func(t *testing.T) {
		st := openD1Store(t)
		createProfile(t, st, "prf_delete_inbound")
		createNode(t, st, "nod_delete_inbound")
		in := AccessInbound{ID: "inb_delete_inbound", ProfileID: "prf_delete_inbound", NodeID: "nod_delete_inbound", Enabled: true, CreatedAt: now, PluginStateEnc: []byte{1, 2}, PluginPublicJSON: `{"public_key":"test"}`}
		if err := st.Access().CreateInbound(ctx, in); err != nil {
			t.Fatal(err)
		}
		if err := st.Access().DeleteInbound(ctx, in.ID, now, func(enc []byte, _, _ string) ([]byte, error) { return append([]byte("retained:"), enc...), nil }); err != nil {
			t.Fatal(err)
		}
		if got, err := st.Access().RetainedKey(ctx, in.ProfileID, in.NodeID); err != nil || string(got.StateEnc) != "retained:\x01\x02" {
			t.Fatalf("retained key = %+v, %v", got, err)
		}
	})
	t.Run("CreateGroup", func(t *testing.T) {
		st := openD1Store(t)
		createProfile(t, st, "prf_group_profile")
		createGroup(t, st, "grp_create", []string{"prf_group_profile"})
		got, err := st.Access().Group(ctx, "grp_create")
		if err != nil || len(got.ProfileIDs) != 1 || got.ProfileIDs[0] != "prf_group_profile" || got.Color != GroupTones[0] {
			t.Fatalf("group = %+v, %v", got, err)
		}
	})
	t.Run("UpdateGroup", func(t *testing.T) {
		st := openD1Store(t)
		createProfile(t, st, "prf_update_group")
		createGroup(t, st, "grp_update", nil)
		name := "Updated group"
		profileIDs := []string{"prf_update_group"}
		if err := st.Access().UpdateGroup(ctx, "grp_update", &name, &profileIDs, nil, nil); err != nil {
			t.Fatal(err)
		}
		got, err := st.Access().Group(ctx, "grp_update")
		if err != nil || got.Name != name || len(got.ProfileIDs) != 1 || got.ProfileIDs[0] != profileIDs[0] {
			t.Fatalf("group = %+v, %v", got, err)
		}
	})
	t.Run("DeleteGroup", func(t *testing.T) {
		st := openD1Store(t)
		createGroup(t, st, "grp_delete", nil)
		if err := st.Access().DeleteGroup(ctx, "grp_delete", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Access().Group(ctx, "grp_delete"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("group after delete: %v", err)
		}
	})
}

func TestD1RewrittenDNSAndSettingsMethods(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	t.Run("DNSDelete", func(t *testing.T) {
		st := openD1Store(t)
		id := "dns_delete"
		if err := st.DNS().Create(ctx, DNSPreset{ID: id, Name: id, ServersJSON: `[]`, SplitJSON: `[]`, Transport: "plain", CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		if _, err := st.W.ExecContext(ctx, `INSERT INTO user_group (id, name, created_at, dns_preset_id) VALUES ('grp_dns_delete', 'dns group', ?, ?)`, unix(now), id); err != nil {
			t.Fatal(err)
		}
		if err := st.DNS().Delete(ctx, id); err != nil {
			t.Fatal(err)
		}
		var dns sql.NullString
		if err := st.R.QueryRowContext(ctx, `SELECT dns_preset_id FROM user_group WHERE id = 'grp_dns_delete'`).Scan(&dns); err != nil || dns.Valid {
			t.Fatalf("group DNS reference = %v, %v", dns, err)
		}
	})
	t.Run("SetNodeOptions", func(t *testing.T) {
		st := openD1Store(t)
		nodeID := "nod_options"
		if _, err := st.W.ExecContext(ctx, `INSERT INTO node (id, name, address, state, created_at) VALUES (?, ?, '203.0.113.11', 'active', ?)`, nodeID, nodeID, unix(now)); err != nil {
			t.Fatal(err)
		}
		presetID := "dns_options"
		if err := st.DNS().Create(ctx, DNSPreset{ID: presetID, Name: presetID, ServersJSON: `[]`, SplitJSON: `[]`, Transport: "plain", CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err := st.DNS().SetNodeOptions(ctx, nodeID, []string{presetID}, presetID); err != nil {
			t.Fatal(err)
		}
		got, err := st.DNS().NodeOptions(ctx)
		if err != nil || len(got[nodeID]) != 1 || got[nodeID][0].PresetID != presetID || !got[nodeID][0].Default {
			t.Fatalf("node options = %+v, %v", got[nodeID], err)
		}
	})
	t.Run("SetSettings", func(t *testing.T) {
		st := openD1Store(t)
		if err := st.SetSettings(ctx, map[string]string{"edge.rewritten": "ready"}); err != nil {
			t.Fatal(err)
		}
		if err := st.SetSettings(ctx, map[string]string{"edge.rewritten": "updated"}); err != nil {
			t.Fatal(err)
		}
		if err := st.SetSettings(ctx, nil); err != nil {
			t.Fatal(err)
		}
		if got, err := st.Setting(ctx, "edge.rewritten"); err != nil || got != "updated" {
			t.Fatalf("setting = %q, %v", got, err)
		}
	})
}

func TestD1RewrittenHealthProvisionUpdateMethods(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	newStore := func(t *testing.T) *Store {
		t.Helper()
		return openD1Store(t)
	}
	addNode := func(t *testing.T, st *Store, id string) {
		t.Helper()
		if _, err := st.W.ExecContext(ctx, `INSERT INTO node (id, name, address, state, created_at) VALUES (?, ?, '203.0.113.10', 'active', ?)`, id, id, now.Unix()); err != nil {
			t.Fatal(err)
		}
	}
	newJob := func(id, nodeID, name string, created time.Time) NodeProvisionJob {
		return NodeProvisionJob{ID: id, NodeID: nodeID, Name: name, Address: "203.0.113.10", SSHHost: "203.0.113.10", SSHPort: 22,
			HostFingerprint: "SHA256:example", Secret: []byte("sealed"), CreatedBy: "admin", CreatedAt: created, UpdatedAt: created}
	}
	seedJob := func(t *testing.T, st *Store, id, nodeID, name, state string, created time.Time) {
		t.Helper()
		job := newJob(id, nodeID, name, created)
		if _, err := st.W.ExecContext(ctx, `INSERT INTO node_provision_job (
			id, node_id, name, address, ssh_host, ssh_port, host_fingerprint, secret, state, phase, created_by, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, job.ID, job.NodeID, job.Name, job.Address, job.SSHHost, job.SSHPort,
			job.HostFingerprint, job.Secret, state, state, job.CreatedBy, job.CreatedAt.Unix(), job.UpdatedAt.Unix()); err != nil {
			t.Fatal(err)
		}
	}
	countEvents := func(t *testing.T, st *Store, jobID string) int {
		t.Helper()
		var count int
		if err := st.R.QueryRowContext(ctx, `SELECT count(*) FROM node_provision_event WHERE job_id = ?`, jobID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	newRollout := func(id string) RolloutRow {
		return RolloutRow{ID: id, Status: RolloutRunning, ToVersion: "v2", ToBuilt: 20, Manifest: []byte("manifest"), Signature: []byte("signature"),
			BatchSize: 1, CreatedAt: now}
	}

	t.Run("OpenAlert", func(t *testing.T) {
		st := newStore(t)
		a := HealthAlert{Kind: "node_down", Severity: 2, NodeID: "node_de", Subject: "agent", TitleKey: "title", WhyKey: "why"}
		first, reopened, err := st.OpenAlert(ctx, a, time.Hour, now)
		if err != nil || reopened {
			t.Fatalf("open: %+v reopened=%v err=%v", first, reopened, err)
		}
		if ok, err := st.ResolveAlert(ctx, first.ID, "cleared", now.Add(time.Minute)); err != nil || !ok {
			t.Fatalf("resolve: %v %v", ok, err)
		}
		again, reopened, err := st.OpenAlert(ctx, a, time.Hour, now.Add(30*time.Minute))
		if err != nil || !reopened || again.ID != first.ID || !again.FirstSeen.Equal(first.FirstSeen) {
			t.Fatalf("reopen: %+v reopened=%v err=%v", again, reopened, err)
		}
	})
	t.Run("PutDoctor", func(t *testing.T) {
		st := newStore(t)
		addNode(t, st, "node_doctor")
		rows := []DoctorRow{{CheckID: "resolver", Status: 2, Detail: "old"}, {CheckID: "disk", Status: 1}}
		if err := st.PutDoctor(ctx, "node_doctor", rows, true, now); err != nil {
			t.Fatal(err)
		}
		if err := st.PutDoctor(ctx, "node_doctor", []DoctorRow{{CheckID: "resolver", Status: 1, Detail: "new"}}, false, now.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		got, err := st.DoctorResults(ctx, "node_doctor")
		if err != nil || len(got) != 2 || got[0].CheckID != "disk" || got[1].Status != 1 || got[1].Detail != "new" {
			t.Fatalf("doctor rows: %+v %v", got, err)
		}
	})
	t.Run("RollupDaily", func(t *testing.T) {
		st := newStore(t)
		addNode(t, st, "node_health")
		if _, err := st.W.ExecContext(ctx, `INSERT INTO profile (id, protocol, name, settings_json, created_at, updated_at) VALUES ('prf_health', 'hysteria2', 'health', '{}', ?, ?)`, now.Unix(), now.Unix()); err != nil {
			t.Fatal(err)
		}
		if _, err := st.W.ExecContext(ctx, `INSERT INTO inbound (id, profile_id, node_id, created_at, updated_at) VALUES ('inb_health', 'prf_health', 'node_health', ?, ?)`, now.Unix(), now.Unix()); err != nil {
			t.Fatal(err)
		}
		at := now.Unix() - 2*86400 + 100
		if err := st.InsertSample(ctx, CheckSample{InboundID: "inb_health", At: time.Unix(at, 0), Status: 1, LatencyMS: 50}); err != nil {
			t.Fatal(err)
		}
		if err := st.InsertSample(ctx, CheckSample{InboundID: "inb_health", At: time.Unix(at+1, 0), Status: 2, LatencyMS: 100}); err != nil {
			t.Fatal(err)
		}
		if err := st.RollupDaily(ctx, now); err != nil {
			t.Fatal(err)
		}
		rows, err := st.DailyRows(ctx, "inb_health")
		if err != nil || len(rows) != 1 || rows[0].OK != 1 || rows[0].Degraded != 1 || rows[0].P50MS != 50 || rows[0].P95MS != 100 {
			t.Fatalf("daily rows: %+v %v", rows, err)
		}
	})
	t.Run("createNodeProvisionJob", func(t *testing.T) {
		st := newStore(t)
		job := newJob("job_create", "node_create", "node-create", now)
		if err := st.createNodeProvisionJob(ctx, job, "created"); err != nil {
			t.Fatal(err)
		}
		got, err := st.NodeProvisionJob(ctx, job.ID)
		if err != nil || got.State != "queued" || countEvents(t, st, job.ID) != 1 {
			t.Fatalf("created job: %+v events=%d err=%v", got, countEvents(t, st, job.ID), err)
		}
		duplicate := newJob("job_create_2", "node_create", "node-create-2", now)
		if err := st.createNodeProvisionJob(ctx, duplicate, "created"); !errors.Is(err, ErrConflict) {
			t.Fatalf("duplicate node job: %v", err)
		}
	})
	t.Run("RequeueNodeProvisionJobs", func(t *testing.T) {
		st := newStore(t)
		seedJob(t, st, "job_running", "node_run", "node-run", "running", now)
		seedJob(t, st, "job_cancel", "node_cancel", "node-cancel", "cancel_requested", now)
		if err := st.RequeueNodeProvisionJobs(ctx, now.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		if err := st.RequeueNodeProvisionJobs(ctx, now.Add(2*time.Minute)); err != nil {
			t.Fatal(err)
		}
		state1, _ := st.NodeProvisionJobState(ctx, "job_running")
		state2, _ := st.NodeProvisionJobState(ctx, "job_cancel")
		if state1 != "queued" || state2 != "cancelled" || countEvents(t, st, "job_running") != 1 || countEvents(t, st, "job_cancel") != 1 {
			t.Fatalf("states=%q,%q events=%d,%d", state1, state2, countEvents(t, st, "job_running"), countEvents(t, st, "job_cancel"))
		}
	})
	t.Run("RequestCancelNodeProvisionJob", func(t *testing.T) {
		st := newStore(t)
		seedJob(t, st, "job_cancel", "node_cancel", "node-cancel", "queued", now)
		state, changed, err := st.RequestCancelNodeProvisionJob(ctx, "job_cancel", now.Add(time.Minute))
		if err != nil || !changed || state != "cancelled" {
			t.Fatalf("cancel queued: %q %v %v", state, changed, err)
		}
		state, changed, err = st.RequestCancelNodeProvisionJob(ctx, "job_cancel", now.Add(2*time.Minute))
		if err != nil || changed || state != "cancelled" || countEvents(t, st, "job_cancel") != 1 {
			t.Fatalf("cancel again: %q %v %v events=%d", state, changed, err, countEvents(t, st, "job_cancel"))
		}
		seedJob(t, st, "job_cancel_running", "node_cancel_running", "node-cancel-running", "running", now)
		state, changed, err = st.RequestCancelNodeProvisionJob(ctx, "job_cancel_running", now.Add(time.Minute))
		if err != nil || !changed || state != "cancel_requested" || countEvents(t, st, "job_cancel_running") != 1 {
			t.Fatalf("cancel running: %q %v %v events=%d", state, changed, err, countEvents(t, st, "job_cancel_running"))
		}
	})
	t.Run("FinishCancelledNodeProvisionJob", func(t *testing.T) {
		st := newStore(t)
		seedJob(t, st, "job_finish", "node_finish", "node-finish", "cancel_requested", now)
		finished, err := st.FinishCancelledNodeProvisionJob(ctx, "job_finish", now.Add(time.Minute))
		if err != nil || !finished {
			t.Fatalf("finish cancellation: %v %v", finished, err)
		}
		finished, err = st.FinishCancelledNodeProvisionJob(ctx, "job_finish", now.Add(2*time.Minute))
		if err != nil || finished || countEvents(t, st, "job_finish") != 1 {
			t.Fatalf("finish twice: %v %v events=%d", finished, err, countEvents(t, st, "job_finish"))
		}
	})
	t.Run("ClaimNodeProvisionJob", func(t *testing.T) {
		st := newStore(t)
		seedJob(t, st, "job_first", "node_first", "node-first", "queued", now)
		seedJob(t, st, "job_next", "node_next", "node-next", "queued", now.Add(time.Second))
		first, ok, err := st.ClaimNodeProvisionJob(ctx, now.Add(time.Minute))
		if err != nil || !ok || first.ID != "job_first" || first.State != "running" || countEvents(t, st, first.ID) != 1 {
			t.Fatalf("first claim: %+v %v %v", first, ok, err)
		}
		second, ok, err := st.ClaimNodeProvisionJob(ctx, now.Add(2*time.Minute))
		if err != nil || !ok || second.ID != "job_next" {
			t.Fatalf("second claim: %+v %v %v", second, ok, err)
		}
		if _, ok, err := st.ClaimNodeProvisionJob(ctx, now.Add(3*time.Minute)); err != nil || ok {
			t.Fatalf("empty claim: ok=%v err=%v", ok, err)
		}
	})
	t.Run("updateNodeProvisionJobFromState", func(t *testing.T) {
		st := newStore(t)
		seedJob(t, st, "job_update", "node_update", "node-update", "running", now)
		if err := st.updateNodeProvisionJobFromState(ctx, "job_update", "running", "failed", "failed", "install_failed", []byte("sealed"), "failed", now.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		got, err := st.NodeProvisionJob(ctx, "job_update")
		if err != nil || got.State != "failed" || got.ErrorCode != "install_failed" || countEvents(t, st, "job_update") != 1 {
			t.Fatalf("updated job: %+v events=%d err=%v", got, countEvents(t, st, "job_update"), err)
		}
		if err := st.updateNodeProvisionJobFromState(ctx, "job_update", "running", "failed", "failed", "again", nil, "", now); !errors.Is(err, ErrConflict) {
			t.Fatalf("stale state: %v", err)
		}
	})
	t.Run("CompleteNodeProvisionJob", func(t *testing.T) {
		st := newStore(t)
		addNode(t, st, "node_complete")
		seedJob(t, st, "job_complete", "node_complete", "node-complete", "running", now)
		access := NodeServerAccess{NodeID: "node_complete", NodeName: "node-complete", SSHHost: "203.0.113.10", SSHPort: 22,
			SSHUser: "user1", HostFingerprint: "SHA256:example", Password: []byte("sealed-password")}
		if err := st.CompleteNodeProvisionJob(ctx, "job_complete", access, now.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		got, err := st.NodeServerAccess(ctx, "node_complete")
		if err != nil || string(got.Password) != "sealed-password" || countEvents(t, st, "job_complete") != 1 {
			t.Fatalf("completed access: %+v events=%d err=%v", got, countEvents(t, st, "job_complete"), err)
		}
		if err := st.CompleteNodeProvisionJob(ctx, "job_complete", access, now.Add(2*time.Minute)); !errors.Is(err, ErrNotFound) {
			t.Fatalf("complete twice: %v", err)
		}
	})
	t.Run("AddNodeToRunningRollout", func(t *testing.T) {
		st := newStore(t)
		rollout := newRollout("rol_join")
		if err := st.CreateRollout(ctx, rollout, []StepRow{{NodeID: "node_old", NodeName: "old", Stage: 0, State: StepPending}}); err != nil {
			t.Fatal(err)
		}
		if err := st.AddNodeToRunningRollout(ctx, rollout.ID, rollout.ToVersion, rollout.ToBuilt, 0, StepRow{NodeID: "node_new", NodeName: "new"}); err != nil {
			t.Fatal(err)
		}
		steps, err := st.RolloutSteps(ctx, rollout.ID)
		if err != nil || len(steps) != 2 || steps[0].NodeID != "node_new" || steps[0].Stage != 0 || steps[1].Stage != 1 {
			t.Fatalf("rollout steps: %+v %v", steps, err)
		}
	})
	t.Run("createRollout", func(t *testing.T) {
		st := newStore(t)
		rollout := newRollout("rol_create")
		addNode(t, st, "node_schedule")
		schedule := NodeUpdateScheduleRow{NodeID: "node_schedule", ToVersion: rollout.ToVersion, ToBuilt: rollout.ToBuilt,
			ScheduledAt: now.Add(time.Hour).Unix(), CreatedAt: now}
		if err := st.SetNodeUpdateSchedule(ctx, schedule); err != nil {
			t.Fatal(err)
		}
		if err := st.createRollout(ctx, rollout, []StepRow{{NodeID: "node_rollout", NodeName: "rollout", State: StepPending}}, &schedule, nil); err != nil {
			t.Fatal(err)
		}
		if got, err := st.ActiveRollout(ctx); err != nil || got.ID != rollout.ID {
			t.Fatalf("active rollout: %+v %v", got, err)
		}
		if schedules, err := st.NodeUpdateSchedules(ctx); err != nil || len(schedules) != 0 {
			t.Fatalf("consumed schedule: %+v %v", schedules, err)
		}
		if err := st.createRollout(ctx, newRollout("rol_second"), nil, nil, nil); !errors.Is(err, ErrConflict) {
			t.Fatalf("second active rollout: %v", err)
		}
	})
	t.Run("ReplaceWarpAccount", func(t *testing.T) {
		st := newStore(t)
		addNode(t, st, "node_warp")
		old := WarpAccountRow{NodeID: "node_warp", Source: WarpImported, SecretEnc: []byte("old"), PeerPublicKey: "old-key", CreatedAt: now, UpdatedAt: now}
		if err := st.CreateWarpAccount(ctx, old); err != nil {
			t.Fatal(err)
		}
		invalid := old
		invalid.Source = "invalid"
		if err := st.ReplaceWarpAccount(ctx, invalid); err == nil {
			t.Fatal("invalid replacement was accepted")
		}
		preserved, err := st.WarpAccount(ctx, "node_warp")
		if err != nil || string(preserved.SecretEnc) != "old" {
			t.Fatalf("failed replacement lost the old account: %+v %v", preserved, err)
		}
		next := old
		next.SecretEnc, next.PeerPublicKey, next.UpdatedAt = []byte("new"), "new-key", now.Add(time.Minute)
		if err := st.ReplaceWarpAccount(ctx, next); err != nil {
			t.Fatal(err)
		}
		got, err := st.WarpAccount(ctx, "node_warp")
		if err != nil || got.PeerPublicKey != "new-key" || string(got.SecretEnc) != "new" {
			t.Fatalf("replacement: %+v %v", got, err)
		}
	})
	t.Run("awgPrepareTx", func(t *testing.T) {
		st := newStore(t)
		addNode(t, st, "node_awg")
		if err := st.SetAwgPrepare(ctx, "node_awg", AwgPrepareRow{Want: true}); err != nil {
			t.Fatal(err)
		}
		if err := st.AwgPrepareStarted(ctx, "node_awg", now.Unix()); err != nil {
			t.Fatal(err)
		}
		if switched, err := st.AwgPrepareFinish(ctx, "node_awg", true, now.Add(time.Minute).Unix(), "", ""); err != nil || !switched {
			t.Fatalf("finish: switched=%v err=%v", switched, err)
		}
		var raw, backend string
		if err := st.R.QueryRowContext(ctx, `SELECT awg_prepare_json, awg_backend FROM node WHERE id = 'node_awg'`).Scan(&raw, &backend); err != nil {
			t.Fatal(err)
		}
		if prepared := parseAwgPrepare(raw); prepared.State != AwgPrepareDone || backend != "kernel" {
			t.Fatalf("AWG state: %+v backend=%q", prepared, backend)
		}
	})
}
