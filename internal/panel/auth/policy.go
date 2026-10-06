package auth

import (
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// level is how much of the admin API a procedure needs.
type level int

const (
	// levelRead: any signed-in admin, readonly included. Reads, and the admin's own account (passkeys,
	// sessions, step-up): nothing that changes the fleet, the users or the settings of the installation.
	levelRead level = iota
	// levelWrite: owner or helper. Changes users, groups and what nodes run day to day.
	levelWrite
	// levelOwner: the owner only. Infrastructure (enrolling and retiring nodes, profiles and inbounds, logs),
	// the installation's identity and security, the audit log.
	levelOwner
)

// procedureLevels is the role policy, one entry per admin procedure. RequireSession enforces it on every
// request; a procedure that is not listed here needs the owner (fail closed), and TestEveryProcedureHasAPolicy
// fails until a new RPC is given a level on purpose. The public procedures (publicProcedures) bypass it.
var procedureLevels = map[string]level{
	// AuthService: own account.
	adminv1connect.AuthServiceMeProcedure:               levelRead,
	adminv1connect.AuthServiceListPasskeysProcedure:     levelRead,
	adminv1connect.AuthServiceBeginAddPasskeyProcedure:  levelRead, // also needs a step-up
	adminv1connect.AuthServiceFinishAddPasskeyProcedure: levelRead,
	adminv1connect.AuthServiceRemovePasskeyProcedure:    levelRead, // also needs a step-up
	adminv1connect.AuthServiceListSessionsProcedure:     levelRead,
	adminv1connect.AuthServiceEndSessionProcedure:       levelRead, // another session: also needs a step-up
	adminv1connect.AuthServiceEndOtherSessionsProcedure: levelRead, // also needs a step-up
	adminv1connect.AuthServiceBeginStepUpProcedure:      levelRead,
	adminv1connect.AuthServiceFinishStepUpProcedure:     levelRead,

	// AuthService: own account, the password + authenticator-code login (credentials.go).
	adminv1connect.AuthServiceGetPasswordLoginProcedure:     levelRead,
	adminv1connect.AuthServiceChangePasswordProcedure:       levelRead, // also needs a step-up and the current password
	adminv1connect.AuthServiceBeginTotpEnrollmentProcedure:  levelRead, // also needs a step-up
	adminv1connect.AuthServiceFinishTotpEnrollmentProcedure: levelRead,

	// AuthService: installation.
	adminv1connect.AuthServiceListAuditProcedure:              levelOwner,
	adminv1connect.AuthServiceGetSecuritySettingsProcedure:    levelOwner,
	adminv1connect.AuthServiceUpdateSecuritySettingsProcedure: levelOwner,

	adminv1connect.InstanceServiceGetInstanceProcedure:    levelRead,
	adminv1connect.InstanceServiceUpdateInstanceProcedure: levelOwner,

	adminv1connect.FleetServiceOverviewProcedure:   levelRead,
	adminv1connect.FleetServiceListEventsProcedure: levelRead,

	adminv1connect.NodeServiceListNodesProcedure:        levelRead,
	adminv1connect.NodeServiceGetNodeProcedure:          levelRead,
	adminv1connect.NodeServiceUpdateNodeProcedure:       levelWrite, // the handler refuses a helper that sets client_ipv6 (where people seem to come from): owner only
	adminv1connect.NodeServiceRestartInboundsProcedure:  levelWrite,
	adminv1connect.NodeServicePrepareAwgKernelProcedure: levelOwner, // installs packages on the node as root, like ApplyFix; closed to tokens (policy_tokens.go)
	adminv1connect.NodeServiceMeasureBandwidthProcedure: levelOwner, // makes the node push up to 1 GB through a public server; closed to tokens (policy_tokens.go)
	adminv1connect.NodeServiceCreateEnrollmentProcedure: levelOwner, // hands out the means to run an agent as a node
	adminv1connect.NodeServiceRetireNodeProcedure:       levelOwner,
	adminv1connect.NodeServiceStreamLogsProcedure:       levelOwner, // node logs carry client addresses

	// ProvisioningService can install a root agent over SSH; every operation is owner-only.
	adminv1connect.ProvisioningServiceGetSSHFingerprintProcedure:        levelOwner,
	adminv1connect.ProvisioningServiceCheckSSHProcedure:                 levelOwner, // sends a root password after host-key confirmation; also needs step-up
	adminv1connect.ProvisioningServiceStartNodeProvisionProcedure:       levelOwner, // installs a root service; also needs step-up
	adminv1connect.ProvisioningServiceRetryNodeProvisionProcedure:       levelOwner, // resumes a root installation; also needs step-up
	adminv1connect.ProvisioningServiceGetNodeProvisionProcedure:         levelOwner,
	adminv1connect.ProvisioningServiceListNodeProvisionsProcedure:       levelOwner,
	adminv1connect.ProvisioningServiceListNodeProvisionEventsProcedure:  levelOwner,
	adminv1connect.ProvisioningServiceListNodeServerAccessProcedure:     levelOwner,
	adminv1connect.ProvisioningServiceRotateNodeServerPasswordProcedure: levelOwner, // SSH credentials need approval and step-up
	adminv1connect.ProvisioningServiceRevealNodeServerPasswordProcedure: levelOwner, // credential reveal is owner-only and needs step-up
	adminv1connect.ProvisioningServiceForgetNodeServerAccessProcedure:   levelOwner, // deletes a saved credential; needs step-up

	adminv1connect.ProfileServiceListProtocolsProcedure:  levelRead,
	adminv1connect.ProfileServiceListProfilesProcedure:   levelRead,
	adminv1connect.ProfileServiceGetProfileProcedure:     levelRead, // secrets come back masked
	adminv1connect.ProfileServicePreviewProfileProcedure: levelRead, // masked too
	adminv1connect.ProfileServiceCreateProfileProcedure:  levelOwner,
	adminv1connect.ProfileServiceUpdateProfileProcedure:  levelOwner,
	adminv1connect.ProfileServiceDeleteProfileProcedure:  levelOwner,
	adminv1connect.ProfileServiceCreateInboundProcedure:  levelOwner,
	adminv1connect.ProfileServiceUpdateInboundProcedure:  levelOwner,
	adminv1connect.ProfileServiceDeleteInboundProcedure:  levelOwner,
	adminv1connect.ProfileServiceTwinProfileProcedure:    levelOwner, // CreateProfile + CreateInbound + UpdateGroup in one

	adminv1connect.GroupServiceListGroupsProcedure:  levelRead,
	adminv1connect.GroupServiceCreateGroupProcedure: levelWrite,
	adminv1connect.GroupServiceUpdateGroupProcedure: levelWrite,
	adminv1connect.GroupServiceDeleteGroupProcedure: levelWrite,

	// DnsService: presets decide which resolvers a user's traffic trusts, so editing them is owner-only;
	// choosing one for a user or group is a user/group edit (levelWrite above).
	adminv1connect.DnsServiceListDnsPresetsProcedure:  levelRead,
	adminv1connect.DnsServiceCreateDnsPresetProcedure: levelOwner,
	adminv1connect.DnsServiceUpdateDnsPresetProcedure: levelOwner,
	adminv1connect.DnsServiceDeleteDnsPresetProcedure: levelOwner,
	// DNS per server for the person (what the user page offers on a node, and what people picked): which resolvers a node
	// offers is the owner's call like the presets themselves; reading is for everyone, and removing a person's picks is a
	// user edit (levelWrite). None of it is open to tokens (policy_tokens.go).
	adminv1connect.DnsServiceListNodeDnsOptionsProcedure:  levelRead,
	adminv1connect.DnsServiceSetNodeDnsOptionsProcedure:   levelOwner,
	adminv1connect.DnsServiceGetUserDnsChoicesProcedure:   levelRead,
	adminv1connect.DnsServiceResetUserDnsChoicesProcedure: levelWrite,

	// SubscriptionService: readonly may read; owner and helper change how subscriptions look.
	adminv1connect.SubscriptionServiceGetSubscriptionSettingsProcedure:    levelRead,
	adminv1connect.SubscriptionServiceUpdateSubscriptionSettingsProcedure: levelWrite,
	adminv1connect.SubscriptionServiceListClientsProcedure:                levelRead,
	adminv1connect.SubscriptionServiceTestUserAgentProcedure:              levelRead,

	// HealthService: reads for every role; mute, "check now" and "run doctor" are day-to-day work; a fix
	// changes host configuration or drops sessions, so the owner (restart_inbound may later be relaxed
	// for helpers with an in-handler check).
	adminv1connect.HealthServiceListAlertsProcedure:   levelRead,
	adminv1connect.HealthServiceGetChecksProcedure:    levelRead,
	adminv1connect.HealthServiceGetDoctorProcedure:    levelRead,
	adminv1connect.HealthServiceMuteAlertProcedure:    levelWrite,
	adminv1connect.HealthServiceRunChecksNowProcedure: levelWrite,
	adminv1connect.HealthServiceRunDoctorProcedure:    levelWrite,
	adminv1connect.HealthServiceApplyFixProcedure:     levelOwner,
	// "This is normal for this node" quiets a warning like a mute does, only for longer: day-to-day work too.
	adminv1connect.HealthServiceAcceptDoctorItemProcedure:   levelWrite,
	adminv1connect.HealthServiceUnacceptDoctorItemProcedure: levelWrite,

	// UpdateService: the page is readable by everyone; every change decides what code runs as root on all
	// nodes (or on the panel itself), so it is owner-only and the handler also requires a fresh step-up
	// (auth.Service.RequireStepUp, like passkey removal). Not relaxed for helpers.
	adminv1connect.UpdateServiceGetUpdatesProcedure:               levelRead,
	adminv1connect.UpdateServiceCheckPanelUpdateProcedure:         levelRead,
	adminv1connect.UpdateServiceInstallPanelUpdateProcedure:       levelOwner, // also needs a step-up
	adminv1connect.UpdateServiceStartRolloutProcedure:             levelOwner, // also needs a step-up
	adminv1connect.UpdateServicePauseRolloutProcedure:             levelOwner, // also needs a step-up
	adminv1connect.UpdateServiceResumeRolloutProcedure:            levelOwner, // also needs a step-up
	adminv1connect.UpdateServiceCancelRolloutProcedure:            levelOwner, // also needs a step-up
	adminv1connect.UpdateServiceRollbackNodeProcedure:             levelOwner, // also needs a step-up
	adminv1connect.UpdateServiceRescanBundleProcedure:             levelOwner, // also needs a step-up (reads disk, but it is the trigger of what gets rolled out)
	adminv1connect.UpdateServiceScheduleNodeUpdateProcedure:       levelOwner, // also needs a step-up
	adminv1connect.UpdateServiceCancelNodeUpdateScheduleProcedure: levelOwner, // also needs a step-up
	adminv1connect.UpdateServiceSetUpdateTimezoneProcedure:        levelOwner, // also needs a step-up

	// DeviceService: devices that hold their own keys. All of it is levelWrite, like the subscription link
	// (it is a user credential), and every call that returns a private key writes an audit row in the handler;
	// none of it may ever be offered to a bot or MCP scope.
	adminv1connect.DeviceServiceCreateAwgDeviceProcedure:  levelWrite,
	adminv1connect.DeviceServiceGetDeviceConfigsProcedure: levelWrite,
	adminv1connect.DeviceServiceRotateDeviceKeysProcedure: levelWrite,
	adminv1connect.DeviceServiceRenameDeviceProcedure:     levelWrite,

	// AwgService: stateless editor helpers, nothing is stored.
	adminv1connect.AwgServiceListMimicryPresetsProcedure:  levelRead,
	adminv1connect.AwgServiceGenerateObfuscationProcedure: levelRead,

	// WarpService: the owner only, all of it. Registering creates an account at Cloudflare and accepts its
	// terms on the owner's behalf; import carries a private key; delete destroys the account. Those three also
	// require a fresh step-up in the handler (auth.Service.RequireStepUp).
	adminv1connect.WarpServiceGetWarpProcedure:                      levelOwner,
	adminv1connect.WarpServiceRegisterWarpProcedure:                 levelOwner, // also needs a step-up
	adminv1connect.WarpServiceImportWarpProcedure:                   levelOwner, // also needs a step-up
	adminv1connect.WarpServiceSetWarpEnabledProcedure:               levelOwner,
	adminv1connect.WarpServiceRestartWarpProcedure:                  levelOwner,
	adminv1connect.WarpServiceRefreshWarpProcedure:                  levelOwner,
	adminv1connect.WarpServiceDeleteWarpProcedure:                   levelOwner, // also needs a step-up
	adminv1connect.WarpServiceGetWarpRegistrationParamsProcedure:    levelOwner,
	adminv1connect.WarpServiceUpdateWarpRegistrationParamsProcedure: levelOwner,

	// ApiTokenService and ApprovalService: the owner only, in the signed-in UI, never with a
	// token. Creating and revoking a token and approving a change decide what an agent may do, so the handlers
	// also require a fresh step-up (auth.Service.RequireStepUp). Rejecting is safe and needs none.
	adminv1connect.ApiTokenServiceCreateApiTokenProcedure: levelOwner, // also needs a step-up
	adminv1connect.ApiTokenServiceListApiTokensProcedure:  levelOwner,
	adminv1connect.ApiTokenServiceRevokeApiTokenProcedure: levelOwner, // also needs a step-up
	adminv1connect.ApprovalServiceListApprovalsProcedure:  levelOwner,
	adminv1connect.ApprovalServiceApproveProcedure:        levelOwner, // also needs a step-up
	adminv1connect.ApprovalServiceRejectProcedure:         levelOwner,

	// Backups hold the panel database and encrypted server credentials. Only the owner can manage them.
	adminv1connect.BackupServiceGetBackupSettingsProcedure:    levelOwner,
	adminv1connect.BackupServiceUpdateBackupSettingsProcedure: levelOwner, // also needs a step-up
	adminv1connect.BackupServiceTestBackupStorageProcedure:    levelOwner, // writes and deletes a probe object; also needs a step-up
	adminv1connect.BackupServiceCreateBackupProcedure:         levelOwner, // exports sensitive installation state; also needs a step-up
	adminv1connect.BackupServiceListBackupsProcedure:          levelOwner,

	// TelegramService: setting the bot is the owner's (it decides where alerts go; also needs a step-up); linking, unlinking and
	// switching alerts is each admin's own account, any role, like passkeys (linking also needs a step-up). Not open to tokens.
	adminv1connect.TelegramServiceGetTelegramProcedure:       levelRead,
	adminv1connect.TelegramServiceSetTelegramBotProcedure:    levelOwner,
	adminv1connect.TelegramServiceBeginTelegramLinkProcedure: levelRead,
	adminv1connect.TelegramServiceUnlinkTelegramProcedure:    levelRead,
	adminv1connect.TelegramServiceSetTelegramAlertsProcedure: levelRead,
	adminv1connect.TelegramServiceSendTelegramTestProcedure:  levelRead,

	adminv1connect.UserServiceListUsersProcedure:           levelRead,
	adminv1connect.UserServiceGetUserProcedure:             levelRead,
	adminv1connect.UserServiceCreateUserProcedure:          levelWrite,
	adminv1connect.UserServiceUpdateUserProcedure:          levelWrite,
	adminv1connect.UserServiceSetUsersEnabledProcedure:     levelWrite,
	adminv1connect.UserServiceExtendUsersProcedure:         levelWrite,
	adminv1connect.UserServiceResetUserTrafficProcedure:    levelWrite,
	adminv1connect.UserServiceRevokeDeviceProcedure:        levelWrite,
	adminv1connect.UserServiceDeleteUsersProcedure:         levelWrite,
	adminv1connect.UserServiceGetSubscriptionLinkProcedure: levelWrite, // the link is the user's credential
}

// levelOf is the level a procedure path needs; unknown paths need the owner.
func levelOf(path string) level {
	if l, ok := procedureLevels[path]; ok {
		return l
	}
	return levelOwner
}

func levelForRole(role string) level {
	switch role {
	case store.RoleReadonly:
		return levelRead
	case store.RoleHelper:
		return levelWrite
	default:
		return levelOwner
	}
}

// roleAllows reports whether an admin with this role may call a procedure of this level. An unknown role
// may call nothing above read.
func roleAllows(role string, l level) bool {
	switch l {
	case levelRead:
		return true
	case levelWrite:
		return role == store.RoleOwner || role == store.RoleHelper
	}
	return role == store.RoleOwner
}
