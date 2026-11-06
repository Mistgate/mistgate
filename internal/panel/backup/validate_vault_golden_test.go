//go:build !js

package backup_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"filippo.io/age"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/app"
	"github.com/mistgate/mistgate/internal/panel/assistant"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/backup"
	"github.com/mistgate/mistgate/internal/panel/bsbord"
	"github.com/mistgate/mistgate/internal/panel/cheburcheck"
	"github.com/mistgate/mistgate/internal/panel/globalping"
	"github.com/mistgate/mistgate/internal/panel/health"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/telegram"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

// This fixture builds the panel and writes sealed rows through the same services that handle
// the corresponding admin operations. The backup archive then exercises the validator end to end.
func TestRestoreValidatesProductionSealedValues(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	st, err := store.Open(ctx, filepath.Join(dataDir, "panel.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	key := bytes.Repeat([]byte{0x7c}, vault.KeySize)
	vlt, err := vault.New(key)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	authSvc, err := auth.New(st, auth.Config{
		RPID: "example.com", Origins: []string{"https://example.com"}, Vault: vlt,
	}, log)
	if err != nil {
		t.Fatal(err)
	}
	panel, err := app.Build(app.Config{
		Store: st, Vault: vlt, Auth: authSvc, Clock: time.Now, Logger: log,
		Instance: app.InstanceConfig{
			PublicURL: "https://example.com", AdminPrefix: "/admin/", RPID: "example.com",
			RPOrigins: []string{"https://example.com"}, AgentSNI: "agent.example.com", SubPrefix: "/sub/",
		},
		DataDir: dataDir, MasterKey: key, AfterResponse: func(work func()) { work() },
	})
	if err != nil {
		t.Fatal(err)
	}

	// Finish one password-admin setup while leaving a second, realistic pending setup ceremony.
	setupToken, err := auth.IssueSetupToken(ctx, st, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	beginSetup := func() *adminv1.BeginSetupResponse {
		t.Helper()
		response, err := authSvc.BeginSetup(ctx, connect.NewRequest(&adminv1.BeginSetupRequest{
			SetupToken: setupToken, DisplayName: "Golden Owner",
			Method: adminv1.SetupMethod_SETUP_METHOD_PASSWORD, Login: "golden-owner",
			Password: "golden-test-password-long-enough",
		}))
		if err != nil {
			t.Fatal(err)
		}
		return response.Msg
	}
	firstSetup := beginSetup()
	beginSetup() // a second tab may leave a pending ceremony after the first tab completes setup
	totpSecret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(firstSetup.TotpSecret)
	if err != nil {
		t.Fatal(err)
	}
	created, err := authSvc.FinishSetup(ctx, connect.NewRequest(&adminv1.FinishSetupRequest{
		SetupToken: setupToken, CeremonyId: firstSetup.CeremonyId, TotpCode: goldenTOTP(totpSecret, time.Now()),
	}))
	if err != nil {
		t.Fatal(err)
	}
	owner := store.Admin{ID: created.Msg.Admin.Id, DisplayName: created.Msg.Admin.DisplayName, Role: store.RoleOwner}
	ctx = auth.WithAdmin(ctx, owner)

	// Assistant values use the shared record-specific AAD helpers consumed by restore validation.
	assistantNow := time.Now().UTC()
	assistantToken, err := st.EnsureAssistantToken(ctx, owner.ID, store.ProfileAdmin, assistantNow)
	if err != nil {
		t.Fatal(err)
	}
	provider := adminv1.AssistantProvider_ASSISTANT_PROVIDER_ANTHROPIC
	if _, err := st.W.ExecContext(ctx, `UPDATE assistant_provider SET key_enc=?,key_hint=?,added_at=?,model=?,budget_usd=? WHERE provider=?`,
		vlt.Seal([]byte("golden assistant provider key"), assistant.ProviderKeyAAD(provider)), "r-key", assistantNow.Unix(), "claude-opus-5-5", 20, int32(provider)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.W.ExecContext(ctx, `UPDATE assistant_settings SET panel_notes_enc=? WHERE id=1`, vlt.Seal([]byte("golden assistant panel notes"), assistant.PanelNotesAAD())); err != nil {
		t.Fatal(err)
	}
	conversationID := "cnv_golden_assistant"
	if _, err := st.W.ExecContext(ctx, `INSERT INTO assistant_conversation(id,admin_id,title_enc,summary_enc,created_at,updated_at,segment) VALUES(?,?,?,?,?,?,0)`,
		conversationID, owner.ID,
		vlt.Seal([]byte("Golden Assistant"), assistant.ConversationTitleAAD(conversationID)),
		vlt.Seal([]byte("Golden assistant summary"), assistant.ConversationSummaryAAD(conversationID)), assistantNow.Unix(), assistantNow.Unix()); err != nil {
		t.Fatal(err)
	}
	messageID := "msg_golden_assistant"
	if _, err := st.W.ExecContext(ctx, `INSERT INTO assistant_message(id,conversation_id,admin_id,role,segment,body_enc,created_at) VALUES(?,?,?,'assistant',0,?,?)`,
		messageID, conversationID, owner.ID, vlt.Seal([]byte(`{"text":"golden assistant transcript"}`), assistant.MessageAAD(messageID)), assistantNow.Unix()); err != nil {
		t.Fatal(err)
	}
	assistantPlanID := "pln_golden_assistant"
	if _, err := st.W.ExecContext(ctx, `INSERT INTO assistant_plan(id,conversation_id,admin_id,token_id,tool,confirm_enc,card_enc,inbox_only,state,created_at,expires_at,updated_at) VALUES(?,?,?,?,?,?,?,0,'proposed',?,?,?)`,
		assistantPlanID, conversationID, owner.ID, assistantToken.ID, "user_disable",
		vlt.Seal([]byte("golden assistant confirm"), assistant.PlanConfirmAAD(assistantPlanID)),
		vlt.Seal([]byte(`{"summary":"golden plan card"}`), assistant.PlanCardAAD(assistantPlanID)), assistantNow.Unix(), assistantNow.Add(10*time.Minute).Unix(), assistantNow.Unix()); err != nil {
		t.Fatal(err)
	}
	memoryID := "mem_golden_assistant"
	if _, err := st.W.ExecContext(ctx, `INSERT INTO assistant_memory(id,admin_id,text_enc,by_assistant,conversation_id,created_at,updated_at) VALUES(?,?,?,1,?,?,?)`,
		memoryID, owner.ID, vlt.Seal([]byte("golden assistant memory"), assistant.MemoryAAD(memoryID)), conversationID, assistantNow.Unix(), assistantNow.Unix()); err != nil {
		t.Fatal(err)
	}

	authPath, authHandler := authSvc.Handler()
	authMux := http.NewServeMux()
	authMux.Handle(authPath, authSvc.RequireSession(authHandler))
	authServer := httptest.NewServer(authMux)
	t.Cleanup(authServer.Close)
	authClient := adminv1connect.NewAuthServiceClient(authServer.Client(), authServer.URL)
	cookies := (&http.Response{Header: created.Header()}).Cookies()
	if len(cookies) == 0 {
		t.Fatal("password setup did not return a session cookie")
	}
	cookieHeader := cookies[0].Name + "=" + cookies[0].Value
	beginStepUpRequest := connect.NewRequest(&adminv1.BeginStepUpRequest{})
	beginStepUpRequest.Header().Set("Cookie", cookieHeader)
	if _, err := authClient.BeginStepUp(ctx, beginStepUpRequest); err != nil {
		t.Fatal(err)
	}
	finishStepUpRequest := connect.NewRequest(&adminv1.FinishStepUpRequest{
		TotpCode: goldenTOTP(totpSecret, time.Now().Add(30*time.Second)),
	})
	finishStepUpRequest.Header().Set("Cookie", cookieHeader)
	if _, err := authClient.FinishStepUp(ctx, finishStepUpRequest); err != nil {
		t.Fatal(err)
	}
	turnstileSecret := "golden-turnstile-secret"
	updateSecurityRequest := connect.NewRequest(&adminv1.UpdateSecuritySettingsRequest{
		TurnstileSecretKey: &turnstileSecret,
	})
	updateSecurityRequest.Header().Set("Cookie", cookieHeader)
	if _, err := authClient.UpdateSecuritySettings(ctx, updateSecurityRequest); err != nil {
		t.Fatal(err)
	}

	for _, node := range []struct{ id, name, address string }{
		{"nod_golden_a", "golden-a", "de1.example.com"},
		{"nod_golden_b", "golden-b", "nl1.example.com"},
	} {
		if _, err := st.W.ExecContext(ctx,
			"INSERT INTO node (id, name, address, state, created_at) VALUES (?, ?, ?, 'active', ?)",
			node.id, node.name, node.address, time.Now().Unix(),
		); err != nil {
			t.Fatal(err)
		}
	}

	hyProfile, err := panel.Access.CreateProfile(ctx, connect.NewRequest(&adminv1.CreateProfileRequest{
		Protocol: "hysteria2", Name: "golden hy2",
		SettingsJson: "{\"obfs\":{\"password\":\"golden-profile-secret\"},\"port\":443}",
	}))
	if err != nil {
		t.Fatal(err)
	}
	awgProfile, err := panel.Access.CreateProfile(ctx, connect.NewRequest(&adminv1.CreateProfileRequest{
		Protocol: "awg", Name: "golden awg",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := panel.Access.CreateInbound(ctx, connect.NewRequest(&adminv1.CreateInboundRequest{
		ProfileId: hyProfile.Msg.Profile.Id, NodeId: "nod_golden_a",
	})); err != nil {
		t.Fatal(err)
	}
	if _, err := panel.Access.CreateInbound(ctx, connect.NewRequest(&adminv1.CreateInboundRequest{
		ProfileId: awgProfile.Msg.Profile.Id, NodeId: "nod_golden_a",
	})); err != nil {
		t.Fatal(err)
	}
	awgInboundB, err := panel.Access.CreateInbound(ctx, connect.NewRequest(&adminv1.CreateInboundRequest{
		ProfileId: awgProfile.Msg.Profile.Id, NodeId: "nod_golden_b",
	}))
	if err != nil {
		t.Fatal(err)
	}
	group, err := panel.Access.CreateGroup(ctx, connect.NewRequest(&adminv1.CreateGroupRequest{
		Name: "golden group", ProfileIds: []string{hyProfile.Msg.Profile.Id, awgProfile.Msg.Profile.Id},
	}))
	if err != nil {
		t.Fatal(err)
	}
	user, err := panel.Access.CreateUser(ctx, connect.NewRequest(&adminv1.CreateUserRequest{
		Name: "golden user", GroupId: group.Msg.Group.Id,
		Apps: &adminv1.AppToggles{Happ: true, Amnezia: true},
	}))
	if err != nil {
		t.Fatal(err)
	}
	userRow, err := st.Access().User(ctx, user.Msg.User.Id)
	if err != nil {
		t.Fatal(err)
	}
	subToken, err := vlt.Open(userRow.SubTokenEnc, userRow.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := panel.Access.Subscription(ctx, string(subToken)); err != nil {
		clear(subToken)
		t.Fatal(err)
	}
	clear(subToken)
	if _, err := panel.Access.CreateAwgDevice(ctx, connect.NewRequest(&adminv1.CreateAwgDeviceRequest{
		UserId: user.Msg.User.Id, ProfileId: awgProfile.Msg.Profile.Id, Platform: "ios", Label: "golden phone",
	})); err != nil {
		t.Fatal(err)
	}
	if _, err := panel.Desired(ctx, "nod_golden_a"); err != nil {
		t.Fatal(err)
	}
	if _, err := panel.Desired(ctx, "nod_golden_b"); err != nil {
		t.Fatal(err)
	}
	if _, err := panel.Access.DeleteInbound(ctx, connect.NewRequest(&adminv1.DeleteInboundRequest{
		InboundId: awgInboundB.Msg.Inbound.Id,
	})); err != nil {
		t.Fatal(err)
	}

	// The assembled fleet creates its panel CA during app.Build. Exercise the production RPC
	// that lazily creates the panel SSH identity as well.
	if _, err := panel.Provision.GetPanelSSHKey(ctx, connect.NewRequest(&adminv1.GetPanelSSHKeyRequest{})); err != nil {
		t.Fatal(err)
	}

	// SSH credentials use shared production AAD helpers and production store operations.
	now := time.Now().UTC()
	nodeID := "nod_golden_a"
	if err := st.InsertNodeServerAccess(ctx, store.NodeServerAccess{
		NodeID: nodeID, SSHHost: "de1.example.com", SSHPort: 22, SSHUser: "root",
		HostFingerprint: "SHA256:golden-host-key",
		Password:        vlt.Seal([]byte("golden current ssh password"), store.NodeServerAccessPasswordAAD(nodeID)),
		ConfiguredAt:    now, PasswordLogin: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetPendingNodeServerPassword(ctx, nodeID,
		vlt.Seal([]byte("golden pending ssh password"), store.PendingNodeServerPasswordAAD(nodeID)), false, now.Add(time.Second),
	); err != nil {
		t.Fatal(err)
	}
	jobID := "prv_golden"
	if err := st.CreateNodeProvisionJob(ctx, store.NodeProvisionJob{
		ID: jobID, NodeID: "nod_golden_provision", Name: "golden-provision", Address: "198.51.100.20",
		SSHHost: "provision.example.com", SSHPort: 22, HostFingerprint: "SHA256:golden-provision-key",
		Secret:    vlt.Seal([]byte("golden provisioning credential fixture"), store.NodeProvisionCredentialsAAD(jobID)),
		CreatedBy: owner.ID, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	// The MCP approval storage path seals its owner-supplied node-install secret to the plan id.
	apiToken := store.APIToken{
		ID: "tok_golden", Name: "golden-mcp-token", Profile: store.ProfileAdmin, RatePerMin: 30,
		CreatedBy: owner.ID, CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	apiHash := sha256.Sum256([]byte("golden api token hash fixture"))
	if err := st.CreateAPIToken(ctx, apiToken, apiHash[:]); err != nil {
		t.Fatal(err)
	}
	plan := store.MCPPlan{
		ID: "pln_golden", TokenID: apiToken.ID, Tool: auth.NodeInstallTool, ParamsJSON: "{}",
		ConfirmHash: []byte("golden-confirm-hash"), FactsJSON: "[]", Summary: "golden approved install",
		NeedsApproval: true, Status: store.PlanAwaiting, CreatedAt: now, ExpiresAt: now.Add(store.PlanTTL),
	}
	if err := st.CreateMCPPlan(ctx, plan, 20, 50); err != nil {
		t.Fatal(err)
	}
	ownerSecret := auth.SealNodeInstallSecret(vlt, plan.ID, auth.NodeInstallSecret{
		Password: "golden MCP SSH password", Fingerprint: "SHA256:golden-mcp-host-key",
	})
	if _, err := st.ApproveMCPPlanWithSecret(ctx, plan.ID, owner.ID, ownerSecret, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	noStepUp := func(context.Context) error { return nil }
	serve := func(path string, handler http.Handler) *httptest.Server {
		t.Helper()
		mux := http.NewServeMux()
		mux.Handle(path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handler.ServeHTTP(w, r.WithContext(auth.WithAdmin(r.Context(), owner)))
		}))
		server := httptest.NewServer(mux)
		t.Cleanup(server.Close)
		return server
	}

	// Telegram verifies the bot with Telegram before its RPC seals the token.
	tgToken := "123456789:" + strings.Repeat("A", 35)
	tgAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/getMe"):
			_, _ = io.WriteString(w, "{\"ok\":true,\"result\":{\"id\":123456789,\"is_bot\":true,\"username\":\"golden_bot\"}}")
		case strings.HasSuffix(r.URL.Path, "/deleteWebhook"):
			_, _ = io.WriteString(w, "{\"ok\":true,\"result\":true}")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(tgAPI.Close)
	tgService, err := telegram.New(telegram.Config{Store: st, Vault: vlt, StepUp: noStepUp, APIBase: tgAPI.URL, HTTPClient: tgAPI.Client()})
	if err != nil {
		t.Fatal(err)
	}
	tgPath, tgHandler := tgService.Handler()
	tgServer := serve(tgPath, tgHandler)
	tgClient := adminv1connect.NewTelegramServiceClient(tgServer.Client(), tgServer.URL)
	if _, err := tgClient.SetTelegramBot(ctx, connect.NewRequest(&adminv1.SetTelegramBotRequest{Token: tgToken})); err != nil {
		t.Fatal(err)
	}

	backupService, err := backup.New(backup.Config{Store: st, Vault: vlt, DataDir: dataDir, MasterKey: key, StepUp: noStepUp})
	if err != nil {
		t.Fatal(err)
	}
	backupPath, backupHandler := backupService.Handler()
	backupServer := serve(backupPath, backupHandler)
	backupClient := adminv1connect.NewBackupServiceClient(backupServer.Client(), backupServer.URL)
	recipientIdentity, err := age.GenerateHybridIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backupClient.UpdateBackupSettings(ctx, connect.NewRequest(&adminv1.UpdateBackupSettingsRequest{
		AccountId: "00112233445566778899aabbccddeeff", Jurisdiction: "default", Bucket: "mistgate-golden",
		AccessKeyId: "golden-access-key", SecretAccessKey: "golden-backup-secret",
		AgeRecipient: recipientIdentity.Recipient().String(), Enabled: true, IntervalHours: 24, RetentionDays: 7,
	})); err != nil {
		t.Fatal(err)
	}

	globalpingService, err := globalping.New(globalping.Config{Store: st, Vault: vlt, StepUp: noStepUp})
	if err != nil {
		t.Fatal(err)
	}
	globalpingPath, globalpingHandler := globalpingService.Handler()
	globalpingServer := serve(globalpingPath, globalpingHandler)
	globalpingClient := adminv1connect.NewGlobalpingServiceClient(globalpingServer.Client(), globalpingServer.URL)
	globalpingEnabled, globalpingToken := false, "golden-globalping-token"
	if _, err := globalpingClient.UpdateGlobalping(ctx, connect.NewRequest(&adminv1.UpdateGlobalpingRequest{
		Enabled: &globalpingEnabled, Token: &globalpingToken,
	})); err != nil {
		t.Fatal(err)
	}

	cheburcheckService, err := cheburcheck.New(cheburcheck.Config{
		Store: st, Vault: vlt, StepUp: noStepUp,
		Address: func(context.Context, store.NodeRow) (netip.Addr, bool) { return netip.Addr{}, false },
	})
	if err != nil {
		t.Fatal(err)
	}
	cheburcheckPath, cheburcheckHandler := cheburcheckService.Handler()
	cheburcheckServer := serve(cheburcheckPath, cheburcheckHandler)
	cheburcheckClient := adminv1connect.NewCheburcheckServiceClient(cheburcheckServer.Client(), cheburcheckServer.URL)
	cheburcheckEnabled, cheburcheckPublic, cheburcheckToken := false, false, "golden-cheburcheck-token"
	if _, err := cheburcheckClient.UpdateCheburcheck(ctx, connect.NewRequest(&adminv1.UpdateCheburcheckRequest{
		Enabled: &cheburcheckEnabled, PublicEnabled: &cheburcheckPublic, Token: &cheburcheckToken,
	})); err != nil {
		t.Fatal(err)
	}

	bsbordService, err := bsbord.New(bsbord.Config{
		Store: st, Vault: vlt, StepUp: noStepUp,
		Targets: func(context.Context) ([]health.CountryTarget, error) { return nil, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	bsbordPath, bsbordHandler := bsbordService.Handler()
	bsbordServer := serve(bsbordPath, bsbordHandler)
	bsbordClient := adminv1connect.NewBsbordServiceClient(bsbordServer.Client(), bsbordServer.URL)
	bsbordKey := "bsk_live_example_golden_key"
	if _, err := bsbordClient.UpdateBsbord(ctx, connect.NewRequest(&adminv1.UpdateBsbordRequest{Key: &bsbordKey})); err != nil {
		t.Fatal(err)
	}

	// Fail with the registry label if any expected production row was not created.
	for name, query := range map[string]string{
		"admin_password TOTP":            "SELECT count(*) FROM admin_password WHERE length(totp_secret) > 0",
		"panel CA":                       "SELECT count(*) FROM panel_ca WHERE length(key_enc) > 0",
		"profile secrets":                "SELECT count(*) FROM profile WHERE secrets_enc IS NOT NULL AND length(secrets_enc) > 0",
		"user subscription token":        "SELECT count(*) FROM user WHERE length(sub_token_enc) > 0",
		"subscription link device":       "SELECT count(*) FROM device WHERE kind = 'link'",
		"AWG device":                     "SELECT count(*) FROM device WHERE kind = 'key'",
		"device credentials":             "SELECT count(*) FROM device_credential WHERE length(secret_enc) > 0",
		"AWG inbound plugin state":       "SELECT count(*) FROM inbound WHERE length(plugin_state_enc) > 0",
		"retained AWG key":               "SELECT count(*) FROM awg_retained_key WHERE length(state_enc) > 0",
		"health probe credential":        "SELECT count(*) FROM health_probe_cred WHERE length(secret_enc) > 0",
		"node provisioning credentials":  "SELECT count(*) FROM node_provision_job WHERE length(secret) > 0",
		"node SSH password":              "SELECT count(*) FROM node_server_access WHERE length(password) > 0",
		"pending node SSH password":      "SELECT count(*) FROM node_server_access WHERE pending_password IS NOT NULL AND length(pending_password) > 0",
		"MCP plan owner secret":          "SELECT count(*) FROM mcp_plan WHERE owner_secret IS NOT NULL AND length(owner_secret) > 0",
		"Telegram bot token":             "SELECT count(*) FROM telegram_bot WHERE length(token) > 0",
		"pending auth ceremony TOTP":     "SELECT count(*) FROM auth_ceremony WHERE length(totp_enc) > 0",
		"panel SSH key":                  "SELECT count(*) FROM panel_ssh_key WHERE length(private_key) > 0",
		"panel backup secret":            "SELECT count(*) FROM panel_backup_settings WHERE length(secret_access_key) > 0",
		"assistant provider key":         "SELECT count(*) FROM assistant_provider WHERE length(key_enc) > 0",
		"assistant conversation title":   "SELECT count(*) FROM assistant_conversation WHERE length(title_enc) > 0",
		"assistant conversation summary": "SELECT count(*) FROM assistant_conversation WHERE length(summary_enc) > 0",
		"assistant message transcript":   "SELECT count(*) FROM assistant_message WHERE length(body_enc) > 0",
		"assistant plan confirmation":    "SELECT count(*) FROM assistant_plan WHERE length(confirm_enc) > 0",
		"assistant plan card":            "SELECT count(*) FROM assistant_plan WHERE length(card_enc) > 0",
		"assistant memory note":          "SELECT count(*) FROM assistant_memory WHERE length(text_enc) > 0",
		"Turnstile secret setting":       "SELECT count(*) FROM setting WHERE k = 'turnstile_secret' AND length(v) > 0",
		"Globalping token setting":       "SELECT count(*) FROM setting WHERE k = 'globalping.token' AND length(v) > 0",
		"Cheburcheck token setting":      "SELECT count(*) FROM setting WHERE k = 'cheburcheck.token' AND length(v) > 0",
		"Bsbord key setting":             "SELECT count(*) FROM setting WHERE k = 'bsbord.key' AND length(v) > 0",
	} {
		var count int
		if err := st.R.QueryRowContext(ctx, query).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", name, err)
		}
		if count == 0 {
			t.Errorf("golden fixture did not create %s", name)
		}
	}

	snapshot := filepath.Join(t.TempDir(), "snapshot.sqlite")
	if err := st.SnapshotDatabase(ctx, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	archiveIdentity, err := age.GenerateHybridIdentity()
	if err != nil {
		t.Fatal(err)
	}
	var correctArchive bytes.Buffer
	if err := backup.CreateEncryptedArchiveWithRecipient(ctx, dataDir, snapshot, key, "golden-test", archiveIdentity.Recipient(), time.Now(), &correctArchive); err != nil {
		t.Fatal(err)
	}
	correctTarget := filepath.Join(t.TempDir(), "restored-correct-key")
	if err := backup.RestoreEncryptedArchive(ctx, bytes.NewReader(correctArchive.Bytes()), []age.Identity{archiveIdentity}, correctTarget); err != nil {
		t.Fatalf("restore of production-path archive with its master key: %v", err)
	}

	wrongKey := bytes.Repeat([]byte{0x31}, vault.KeySize)
	if bytes.Equal(wrongKey, key) {
		t.Fatal("test keys unexpectedly match")
	}
	var wrongArchive bytes.Buffer
	if err := backup.CreateEncryptedArchiveWithRecipient(ctx, dataDir, snapshot, wrongKey, "golden-test", archiveIdentity.Recipient(), time.Now(), &wrongArchive); err != nil {
		t.Fatal(err)
	}
	wrongTarget := filepath.Join(t.TempDir(), "restored-wrong-key")
	err = backup.RestoreEncryptedArchive(ctx, bytes.NewReader(wrongArchive.Bytes()), []age.Identity{archiveIdentity}, wrongTarget)
	if err == nil || !strings.Contains(err.Error(), "does not match master.key") {
		t.Fatalf("restore of production-path archive with a different master key = %v", err)
	}
	if _, err := os.Lstat(wrongTarget); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("refused restore left a destination behind: %v", err)
	}
}

func goldenTOTP(secret []byte, now time.Time) string {
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(now.Unix()/30))
	mac := hmac.New(sha1.New, secret)
	_, _ = mac.Write(counter[:])
	result := mac.Sum(nil)
	offset := result[len(result)-1] & 0x0f
	value := binary.BigEndian.Uint32(result[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", value%1_000_000)
}
