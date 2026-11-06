//go:build !js

package backup

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"

	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

type sealedValueQuery struct {
	description string
	table       string
	columns     []string
	aadParts    int
	aadFrom     func(...string) string
	query       string
}

var restoredSealedValueQueries = []sealedValueQuery{
	// admin_password.totp_secret uses auth.TOTPSecretAAD; see auth.Service.FinishSetup in auth/handler.go
	// and auth.Service.FinishTotpEnrollment in auth/credentials.go.
	{description: "admin password TOTP", table: "admin_password", columns: []string{"totp_secret", "admin_id"}, aadParts: 1,
		aadFrom: func(parts ...string) string { return auth.TOTPSecretAAD(parts[0]) },
		query:   "SELECT totp_secret, admin_id FROM admin_password WHERE length(totp_secret) > 0"},
	// panel_ca.key_enc is sealed with the CA row id; see fleet.newCARow in fleet/ca.go.
	{description: "panel CA private key", table: "panel_ca", columns: []string{"key_enc", "id"},
		query: "SELECT key_enc, id FROM panel_ca WHERE length(key_enc) > 0"},
	// profile.secrets_enc is sealed with the profile id; see access.Service.sealSettings in access/profile_rpc.go.
	{description: "profile secrets", table: "profile", columns: []string{"secrets_enc", "id"},
		query: "SELECT secrets_enc, id FROM profile WHERE secrets_enc IS NOT NULL AND length(secrets_enc) > 0"},
	// user.sub_token_enc is sealed with the user id; see access.Service.CreateUser and GetSubscriptionLink in access/user_rpc.go.
	{description: "user subscription token", table: "user", columns: []string{"sub_token_enc", "id"},
		query: "SELECT sub_token_enc, id FROM user WHERE length(sub_token_enc) > 0"},
	// device_credential.secret_enc is sealed with the credential id; see access.Service.newCreds in access/user_rpc.go
	// and access.Service.awgIssuer in access/device.go.
	{description: "device credential", table: "device_credential", columns: []string{"secret_enc", "id"},
		query: "SELECT secret_enc, id FROM device_credential WHERE length(secret_enc) > 0"},
	// inbound.plugin_state_enc is sealed with the inbound id; see access.Service.CreateInbound in access/profile_rpc.go.
	{description: "inbound plugin state", table: "inbound", columns: []string{"plugin_state_enc", "id"},
		query: "SELECT plugin_state_enc, id FROM inbound WHERE plugin_state_enc IS NOT NULL AND length(plugin_state_enc) > 0"},
	// The retained-key AAD is shared with the production access paths; see store.RetainedKeyAAD in store/access_profile.go.
	{description: "retained AmneziaWG key", table: "awg_retained_key", columns: []string{"state_enc", "profile_id", "node_id"}, aadParts: 2,
		aadFrom: func(parts ...string) string { return store.RetainedKeyAAD(parts[0], parts[1]) },
		query:   "SELECT state_enc, profile_id, node_id FROM awg_retained_key WHERE length(state_enc) > 0"},
	// health_probe_cred.secret_enc is sealed with cred_id; see health.Service.probeCred and insertPeerCred in health/targets.go.
	{description: "health probe credential", table: "health_probe_cred", columns: []string{"secret_enc", "cred_id"},
		query: "SELECT secret_enc, cred_id FROM health_probe_cred WHERE length(secret_enc) > 0"},
	// warp_account.secret_enc is sealed with node_id; see warp.Service.seal and open in warp/service.go.
	{description: "WARP account", table: "warp_account", columns: []string{"secret_enc", "node_id"},
		query: "SELECT secret_enc, node_id FROM warp_account WHERE length(secret_enc) > 0"},
	// node_provision_job.secret uses store.NodeProvisionCredentialsAAD; see provision.Service.sealCredentials in provision/service.go.
	{description: "node provisioning credentials", table: "node_provision_job", columns: []string{"secret", "id"}, aadParts: 1,
		aadFrom: func(parts ...string) string { return store.NodeProvisionCredentialsAAD(parts[0]) },
		query:   "SELECT secret, id FROM node_provision_job WHERE length(secret) > 0"},
	// node_server_access.password uses store.NodeServerAccessPasswordAAD; see provision.Service.sealAccessPassword
	// and the worker completion path in provision/service.go and provision/worker.go.
	{description: "node SSH password", table: "node_server_access", columns: []string{"password", "node_id"}, aadParts: 1,
		aadFrom: func(parts ...string) string { return store.NodeServerAccessPasswordAAD(parts[0]) },
		query:   "SELECT password, node_id FROM node_server_access WHERE length(password) > 0"},
	// node_server_access.pending_password uses store.PendingNodeServerPasswordAAD; see
	// provision.Service.RotateNodeServerPassword in provision/service.go.
	{description: "pending node SSH password", table: "node_server_access", columns: []string{"pending_password", "node_id"}, aadParts: 1,
		aadFrom: func(parts ...string) string { return store.PendingNodeServerPasswordAAD(parts[0]) },
		query:   "SELECT pending_password, node_id FROM node_server_access WHERE pending_password IS NOT NULL AND length(pending_password) > 0"},
	// mcp_plan.owner_secret uses auth.NodeInstallSecretAAD; see auth.SealNodeInstallSecret in auth/approvals_rpc.go.
	{description: "MCP plan owner secret", table: "mcp_plan", columns: []string{"owner_secret", "id"}, aadParts: 1,
		aadFrom: func(parts ...string) string { return auth.NodeInstallSecretAAD(parts[0]) },
		query:   "SELECT owner_secret, id FROM mcp_plan WHERE owner_secret IS NOT NULL AND length(owner_secret) > 0"},
	// telegram_bot.token is sealed with tokenRecordID; see telegram.rpc.SetTelegramBot in telegram/rpc.go.
	{description: "Telegram bot token", table: "telegram_bot", columns: []string{"token"},
		query: "SELECT token, 'telegram_bot.token' FROM telegram_bot WHERE length(token) > 0"},
	// auth_ceremony.totp_enc uses auth.AuthCeremonyTOTPRecordID; see auth.Service.ceremonyRow in auth/service.go.
	{description: "auth ceremony TOTP", table: "auth_ceremony", columns: []string{"totp_enc", "id"}, aadParts: 1,
		aadFrom: func(parts ...string) string { return auth.AuthCeremonyTOTPRecordID(parts[0]) },
		query:   "SELECT totp_enc, id FROM auth_ceremony WHERE length(totp_enc) > 0"},
	// panel_ssh_key.private_key is sealed with panelSSHKeyAAD; see provision.Service.ensurePanelSSHKey in provision/panel_key.go.
	{description: "panel SSH key", table: "panel_ssh_key", columns: []string{"private_key"},
		query: "SELECT private_key, 'panel-ssh-key' FROM panel_ssh_key WHERE length(private_key) > 0"},
	// panel_backup_settings.secret_access_key is sealed with secretRecordID; see backup.rpc.UpdateBackupSettings in backup/service.go.
	{description: "panel backup secret", table: "panel_backup_settings", columns: []string{"secret_access_key"},
		query: "SELECT secret_access_key, 'panel_backup_settings.secret_access_key' FROM panel_backup_settings WHERE length(secret_access_key) > 0"},
}

var restoredSealedSettingKeys = []struct {
	key string
	aad string
}{
	// setting.turnstile_secret uses turnstileSecretAAD; see auth.Service.UpdateSecuritySettings in auth/turnstile.go.
	{"turnstile_secret", "setting/turnstile_secret"},
	// setting.globalping.token uses tokenAAD; see globalping.rpc.UpdateGlobalping in globalping/service.go.
	{"globalping.token", "setting/globalping.token"},
	// setting.cheburcheck.token uses tokenAAD; see cheburcheck.rpc.UpdateCheburcheck in cheburcheck/service.go.
	{"cheburcheck.token", "setting/cheburcheck.token"},
	// setting.bsbord.key uses keyAAD; see bsbord.rpc.UpdateBsbord in bsbord/service.go.
	{"bsbord.key", "setting/bsbord.key"},
}

func validateSealedDatabaseValues(ctx context.Context, db *sql.DB, key []byte) error {
	v, err := vault.New(key)
	if err != nil {
		return errors.New("backup: master key is invalid")
	}
	schema := restoredDatabaseSchema{db: db, tables: make(map[string]map[string]struct{})}
	for _, item := range restoredSealedValueQueries {
		present, err := schema.hasColumns(ctx, item.table, item.columns...)
		if err != nil {
			return fmt.Errorf("backup: inspect sealed %s schema: %w", item.description, err)
		}
		if !present {
			continue
		}
		if err := validateSealedRows(ctx, db, v, item); err != nil {
			return err
		}
	}
	present, err := schema.hasColumns(ctx, "setting", "k", "v")
	if err != nil {
		return fmt.Errorf("backup: inspect sealed settings schema: %w", err)
	}
	if !present {
		return nil
	}
	for _, item := range restoredSealedSettingKeys {
		var encoded string
		err := db.QueryRowContext(ctx, "SELECT v FROM setting WHERE k = ?", item.key).Scan(&encoded)
		if errors.Is(err, sql.ErrNoRows) || err == nil && encoded == "" {
			continue
		}
		if err != nil {
			return fmt.Errorf("backup: read sealed %s setting: %w", item.key, err)
		}
		blob, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(blob) == 0 {
			return fmt.Errorf("backup: sealed %s setting is invalid", item.key)
		}
		if err := authenticateSealed(v, blob, item.aad); err != nil {
			return fmt.Errorf("backup: restored database contains a sealed value that does not match master.key (%s)", item.key)
		}
	}
	return nil
}

type restoredDatabaseSchema struct {
	db     *sql.DB
	tables map[string]map[string]struct{}
}

func (s *restoredDatabaseSchema) hasColumns(ctx context.Context, table string, columns ...string) (bool, error) {
	tableColumns, ok := s.tables[table]
	if !ok {
		var exists int
		if err := s.db.QueryRowContext(ctx,
			"SELECT EXISTS (SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?)", table,
		).Scan(&exists); err != nil {
			return false, err
		}
		if exists == 0 {
			s.tables[table] = nil
			return false, nil
		}
		tableColumns = make(map[string]struct{})
		rows, err := s.db.QueryContext(ctx, "SELECT name FROM pragma_table_info(?)", table)
		if err != nil {
			return false, err
		}
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				rows.Close()
				return false, err
			}
			tableColumns[name] = struct{}{}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return false, err
		}
		if err := rows.Close(); err != nil {
			return false, err
		}
		s.tables[table] = tableColumns
	}
	if tableColumns == nil {
		return false, nil
	}
	for _, column := range columns {
		if _, ok := tableColumns[column]; !ok {
			return false, nil
		}
	}
	return true, nil
}

func validateSealedRows(ctx context.Context, db *sql.DB, v *vault.Vault, item sealedValueQuery) error {
	rows, err := db.QueryContext(ctx, item.query)
	if err != nil {
		return fmt.Errorf("backup: read sealed %s values: %w", item.description, err)
	}
	defer rows.Close()
	for rows.Next() {
		var blob []byte
		aadParts := make([]string, item.aadParts)
		dest := make([]any, 1+len(aadParts))
		dest[0] = &blob
		for i := range aadParts {
			dest[i+1] = &aadParts[i]
		}
		var aad string
		if item.aadParts == 0 {
			dest = append(dest, &aad)
		}
		if err := rows.Scan(dest...); err != nil {
			return fmt.Errorf("backup: read sealed %s value: %w", item.description, err)
		}
		if item.aadFrom != nil {
			aad = item.aadFrom(aadParts...)
		}
		if len(blob) == 0 {
			continue
		}
		if err := authenticateSealed(v, blob, aad); err != nil {
			return fmt.Errorf("backup: restored database contains a sealed value that does not match master.key (%s)", item.description)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("backup: read sealed %s values: %w", item.description, err)
	}
	return nil
}

func authenticateSealed(v *vault.Vault, blob []byte, aad string) error {
	plain, err := v.Open(blob, aad)
	clear(plain)
	return err
}
