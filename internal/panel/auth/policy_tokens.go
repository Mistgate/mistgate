package auth

import (
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// API tokens. A token's profile maps to a role, and the role is checked against
// procedureLevels exactly like a signed-in admin's. On top of that a token may call only the procedures listed
// in tokenProcedures: anything else is refused whatever its level (fail closed). A token never passes step-up.

// Token access kinds, the first result of TokenAccess.
const (
	// TokenAccessDirect: a token of a sufficient profile may call the procedure.
	TokenAccessDirect = 1
	// TokenAccessApproved: only with a grant in the context (WithPlanning for a call that changes nothing,
	// WithApprovedStepUp for the owner's approved plan), which only the MCP layer can create. Never over /api.
	TokenAccessApproved = 2
	// TokenAccessPlanning: only while the MCP layer makes a plan (WithPlanning on the MCP channel). The call changes
	// nothing but must not be a free tool for a script: GetSSHFingerprint would let any admin token probe the network
	// from the panel. Never over /api.
	TokenAccessPlanning = 3
)

// tokenProcedures is the allow-list for tokens. Adding a line is a deliberate act with a test
// (TestTokenAllowList): nothing here may return a secret (a subscription link, a device key, a credential); a response
// field that looks like one must be listed in TestTokenReachableResponsesCarryNoSecret with the reason it is safe.
var tokenProcedures = map[string]int{
	// Reads.
	adminv1connect.FleetServiceOverviewProcedure:                    TokenAccessDirect,
	adminv1connect.FleetServiceListEventsProcedure:                  TokenAccessDirect,
	adminv1connect.NodeServiceListNodesProcedure:                    TokenAccessDirect,
	adminv1connect.NodeServiceGetNodeProcedure:                      TokenAccessDirect,
	adminv1connect.HealthServiceListAlertsProcedure:                 TokenAccessDirect,
	adminv1connect.HealthServiceGetChecksProcedure:                  TokenAccessDirect,
	adminv1connect.HealthServiceGetDoctorProcedure:                  TokenAccessDirect,
	adminv1connect.UserServiceListUsersProcedure:                    TokenAccessDirect,
	adminv1connect.UserServiceGetUserProcedure:                      TokenAccessDirect,
	adminv1connect.GroupServiceListGroupsProcedure:                  TokenAccessDirect,
	adminv1connect.ProfileServiceListProfilesProcedure:              TokenAccessDirect,
	adminv1connect.ProfileServiceGetProfileProcedure:                TokenAccessDirect,
	adminv1connect.SubscriptionServiceListClientsProcedure:          TokenAccessDirect,
	adminv1connect.SubscriptionServiceTestUserAgentProcedure:        TokenAccessDirect,
	adminv1connect.UpdateServiceGetUpdatesProcedure:                 TokenAccessDirect,
	adminv1connect.ProvisioningServiceGetSSHFingerprintProcedure:    TokenAccessPlanning, // node_install_plan only
	adminv1connect.ProvisioningServiceListNodeServerAccessProcedure: TokenAccessDirect,
	adminv1connect.ProvisioningServiceStartNodeProvisionProcedure:   TokenAccessApproved,
	// Day-to-day changes (level write: the operator profile and up).
	adminv1connect.UserServiceCreateUserProcedure:       TokenAccessDirect,
	adminv1connect.UserServiceUpdateUserProcedure:       TokenAccessDirect,
	adminv1connect.UserServiceSetUsersEnabledProcedure:  TokenAccessDirect,
	adminv1connect.UserServiceExtendUsersProcedure:      TokenAccessDirect,
	adminv1connect.UserServiceResetUserTrafficProcedure: TokenAccessDirect,
	adminv1connect.UserServiceRevokeDeviceProcedure:     TokenAccessDirect,
	adminv1connect.HealthServiceMuteAlertProcedure:      TokenAccessDirect,
	adminv1connect.HealthServiceRunChecksNowProcedure:   TokenAccessDirect,
	adminv1connect.HealthServiceRunDoctorProcedure:      TokenAccessDirect,
	// Owner level (the admin profile): the audit log, which names admins, tokens and addresses.
	adminv1connect.AuthServiceListAuditProcedure: TokenAccessDirect,
	// Only through the owner's approval (or, for the fix's dry run, a planning grant): see Service.WithApprovedStepUp.
	adminv1connect.HealthServiceApplyFixProcedure:                       TokenAccessApproved,
	adminv1connect.UpdateServiceStartRolloutProcedure:                   TokenAccessApproved,
	adminv1connect.UpdateServicePauseRolloutProcedure:                   TokenAccessApproved,
	adminv1connect.UpdateServiceResumeRolloutProcedure:                  TokenAccessApproved,
	adminv1connect.UpdateServiceCancelRolloutProcedure:                  TokenAccessApproved,
	adminv1connect.UpdateServiceRollbackNodeProcedure:                   TokenAccessApproved,
	adminv1connect.UpdateServiceScheduleNodeUpdateProcedure:             TokenAccessApproved,
	adminv1connect.UpdateServiceCancelNodeUpdateScheduleProcedure:       TokenAccessApproved,
	adminv1connect.UpdateServiceSetUpdateTimezoneProcedure:              TokenAccessApproved,
	adminv1connect.ProvisioningServiceRotateNodeServerPasswordProcedure: TokenAccessApproved,
}

// stepUpProcedures lists exactly the procedures whose handlers call RequireStepUp. A token cannot call them
// directly (none is TokenAccessDirect); through MCP they run only inside an approved plan.
var stepUpProcedures = map[string]bool{
	adminv1connect.AuthServiceBeginAddPasskeyProcedure:                  true,
	adminv1connect.AuthServiceRemovePasskeyProcedure:                    true,
	adminv1connect.AuthServiceEndSessionProcedure:                       true, // another session
	adminv1connect.AuthServiceEndOtherSessionsProcedure:                 true,
	adminv1connect.UpdateServiceStartRolloutProcedure:                   true,
	adminv1connect.UpdateServicePauseRolloutProcedure:                   true,
	adminv1connect.UpdateServiceResumeRolloutProcedure:                  true,
	adminv1connect.UpdateServiceCancelRolloutProcedure:                  true,
	adminv1connect.UpdateServiceRollbackNodeProcedure:                   true,
	adminv1connect.UpdateServiceRescanBundleProcedure:                   true,
	adminv1connect.UpdateServiceInstallPanelUpdateProcedure:             true,
	adminv1connect.UpdateServiceScheduleNodeUpdateProcedure:             true,
	adminv1connect.UpdateServiceCancelNodeUpdateScheduleProcedure:       true,
	adminv1connect.UpdateServiceSetUpdateTimezoneProcedure:              true,
	adminv1connect.ProvisioningServiceCheckSSHProcedure:                 true,
	adminv1connect.ProvisioningServiceStartNodeProvisionProcedure:       true,
	adminv1connect.ProvisioningServiceRetryNodeProvisionProcedure:       true,
	adminv1connect.ProvisioningServiceRotateNodeServerPasswordProcedure: true,
	adminv1connect.ProvisioningServiceRevealNodeServerPasswordProcedure: true,
	adminv1connect.ProvisioningServiceForgetNodeServerAccessProcedure:   true,
	adminv1connect.WarpServiceRegisterWarpProcedure:                     true,
	adminv1connect.WarpServiceImportWarpProcedure:                       true,
	adminv1connect.WarpServiceDeleteWarpProcedure:                       true,
	adminv1connect.ApiTokenServiceCreateApiTokenProcedure:               true,
	adminv1connect.ApiTokenServiceRevokeApiTokenProcedure:               true,
	adminv1connect.ApprovalServiceApproveProcedure:                      true,
	adminv1connect.BackupServiceUpdateBackupSettingsProcedure:           true,
	adminv1connect.BackupServiceTestBackupStorageProcedure:              true,
	adminv1connect.BackupServiceCreateBackupProcedure:                   true,

	// the password + authenticator-code login (credentials.go)
	adminv1connect.AuthServiceChangePasswordProcedure:         true,
	adminv1connect.AuthServiceBeginTotpEnrollmentProcedure:    true,
	adminv1connect.AuthServiceUpdateSecuritySettingsProcedure: true, // the login captcha (turnstile.go)
}

// grantProcedures says which procedures the approved plan of an MCP tool may open with its grant: the grant of
// a node_fix plan cannot start a rollout. A tool that is not listed gets no grant at all. The keys are the tool
// names without the _plan / _apply suffix, as stored in mcp_plan.tool.
var grantProcedures = map[string]string{
	"node_fix":                    adminv1connect.HealthServiceApplyFixProcedure,
	"rollout_start":               adminv1connect.UpdateServiceStartRolloutProcedure,
	"rollout_pause":               adminv1connect.UpdateServicePauseRolloutProcedure,
	"rollout_resume":              adminv1connect.UpdateServiceResumeRolloutProcedure,
	"rollout_cancel":              adminv1connect.UpdateServiceCancelRolloutProcedure,
	"node_rollback":               adminv1connect.UpdateServiceRollbackNodeProcedure,
	"node_update_schedule":        adminv1connect.UpdateServiceScheduleNodeUpdateProcedure,
	"node_update_schedule_cancel": adminv1connect.UpdateServiceCancelNodeUpdateScheduleProcedure,
	"update_timezone":             adminv1connect.UpdateServiceSetUpdateTimezoneProcedure,
	"node_install":                adminv1connect.ProvisioningServiceStartNodeProvisionProcedure,
	"node_server_password_rotate": adminv1connect.ProvisioningServiceRotateNodeServerPasswordProcedure,
}

// ProcedureRole is the lowest role that may call a procedure: "readonly", "helper" or "owner". Unknown
// procedures need the owner.
func ProcedureRole(path string) string {
	switch levelOf(path) {
	case levelRead:
		return store.RoleReadonly
	case levelWrite:
		return store.RoleHelper
	}
	return store.RoleOwner
}

// NeedsStepUp reports whether the procedure's handler asks for a step-up.
func NeedsStepUp(path string) bool { return stepUpProcedures[path] }

// TokenAccess says how a token may reach a procedure: TokenAccessDirect, TokenAccessApproved or TokenAccessPlanning; ok
// is false when the procedure is closed to tokens.
func TokenAccess(path string) (access int, ok bool) {
	access, ok = tokenProcedures[path]
	return access, ok
}

// profileRole is the role a token profile acts with. An unknown profile acts as readonly.
func profileRole(profile string) string {
	switch profile {
	case store.ProfileAdmin:
		return store.RoleOwner
	case store.ProfileOperator:
		return store.RoleHelper
	}
	return store.RoleReadonly
}
