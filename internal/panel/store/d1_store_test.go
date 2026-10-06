//go:build js && wasm

package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"errors"
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

func TestD1StoreSmoke(t *testing.T) {
	binding := js.Global().Get("__d1")
	binding.Call("__reset")
	st, err := OpenD1(context.Background(), binding)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()

	// This smoke covers Setting, SetSettings, Audit, and ListAudit. The following
	// store methods still read inside a transaction and remain deferred to later steps:
	// CreateEnrollment, Enroll, NodeHello, RetireNode, IngestStats, IngestEvent,
	// OpenAlert, InsertProbeCredIdx, RequeueNodeProvisionJobs,
	// RequestCancelNodeProvisionJob, ClaimNodeProvisionJob, AwgPrepareStarted,
	// AwgPrepareFinish, Access.UpdateProfile, Access.DeleteProfile, Access.DeleteInbound,
	// Access.CreateGroup, Access.UpdateGroup, Access.DeleteGroup, Access.RevokeDevice,
	// Access.AddAWGDevice, Access.RotateAWGDevice, and Access.EnsureImplicitAWGCreds.
	if err := st.SetSettings(ctx, map[string]string{"edge.smoke": "ready"}); err != nil {
		t.Fatal(err)
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
