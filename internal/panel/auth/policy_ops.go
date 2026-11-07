//go:build !js

package auth

// The OpsService (proto/mistgate/ops/v1) is a package of its own so the Cloudflare edition does not link its messages; its
// procedures are named here by path. Every one is owner-only. Tokens (the admin profile) read the log level, the panel logs and the
// retention settings, may change the log level (it resets on restart and removes nothing), and change retention only through
// the owner's approved plan with a step-up, since a shorter retention deletes rows for good. The export and the metrics
// switch are the owner's in the admin UI and closed to tokens.
const (
	OpsGetLogSettingsProcedure     = "/mistgate.ops.v1.OpsService/GetLogSettings"
	OpsSetLogLevelProcedure        = "/mistgate.ops.v1.OpsService/SetLogLevel"
	OpsGetPanelLogsProcedure       = "/mistgate.ops.v1.OpsService/GetPanelLogs"
	OpsGetRetentionProcedure       = "/mistgate.ops.v1.OpsService/GetRetention"
	OpsSetRetentionProcedure       = "/mistgate.ops.v1.OpsService/SetRetention"
	OpsExportDataProcedure         = "/mistgate.ops.v1.OpsService/ExportData"
	OpsGetMetricsSettingsProcedure = "/mistgate.ops.v1.OpsService/GetMetricsSettings"
	OpsSetMetricsSettingsProcedure = "/mistgate.ops.v1.OpsService/SetMetricsSettings"
	// The on-demand debug capture of a node. Starting one raises a node's log level for at most 15 minutes and removes nothing, like
	// SetLogLevel: an admin-profile token may start one (the MCP tools node_debug_capture_plan / _apply put a plan in front of it)
	// and read the records back, which are scrubbed on the node, on intake and on the way out. Stopping one is the owner's in the
	// admin UI (a capture ends by itself), closed to tokens like the export.
	OpsStartDebugCaptureProcedure = "/mistgate.ops.v1.OpsService/StartDebugCapture"
	OpsStopDebugCaptureProcedure  = "/mistgate.ops.v1.OpsService/StopDebugCapture"
	OpsListDebugCapturesProcedure = "/mistgate.ops.v1.OpsService/ListDebugCaptures"
	OpsGetDebugCaptureProcedure   = "/mistgate.ops.v1.OpsService/GetDebugCapture"
)

func init() {
	for _, p := range []string{
		OpsGetLogSettingsProcedure, OpsSetLogLevelProcedure, OpsGetPanelLogsProcedure, OpsGetRetentionProcedure,
		OpsSetRetentionProcedure, OpsExportDataProcedure, OpsGetMetricsSettingsProcedure, OpsSetMetricsSettingsProcedure,
		OpsStartDebugCaptureProcedure, OpsStopDebugCaptureProcedure, OpsListDebugCapturesProcedure, OpsGetDebugCaptureProcedure,
	} {
		procedureLevels[p] = levelOwner
	}
	for _, p := range []string{OpsGetLogSettingsProcedure, OpsSetLogLevelProcedure, OpsGetPanelLogsProcedure, OpsGetRetentionProcedure} {
		tokenProcedures[p] = TokenAccessDirect
	}
	tokenProcedures[OpsSetRetentionProcedure] = TokenAccessApproved
	tokenProcedures[OpsStartDebugCaptureProcedure] = TokenAccessDirect
	tokenProcedures[OpsListDebugCapturesProcedure] = TokenAccessDirect
	tokenProcedures[OpsGetDebugCaptureProcedure] = TokenAccessDirect
	for _, p := range []string{OpsSetRetentionProcedure, OpsExportDataProcedure, OpsSetMetricsSettingsProcedure} {
		stepUpProcedures[p] = true
	}
	grantProcedures["retention"] = []string{OpsSetRetentionProcedure}
}
