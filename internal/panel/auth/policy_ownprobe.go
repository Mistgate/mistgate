//go:build !js

package auth

// The OwnProbeService (proto/mistgate/ownprobe/v1) is a package of its own so the Cloudflare edition does not link its messages;
// its procedures are named here by path, and a test compares them with the generated names. All of them are the owner's in the
// admin UI and closed to API tokens and the MCP (no tokenProcedures line): a probe's token and secret are credentials of a
// device in someone's home, and what the probes see is the owner's to read. Creating, revoking, switching and disconnecting
// probes also need a fresh sign-in; the update command (no token in it) does not.
const (
	OwnProbeGetProcedure            = "/mistgate.ownprobe.v1.OwnProbeService/Get"
	OwnProbeUpdateProcedure         = "/mistgate.ownprobe.v1.OwnProbeService/Update"
	OwnProbeCreateProcedure         = "/mistgate.ownprobe.v1.OwnProbeService/Create"
	OwnProbeUpdateCommandProcedure  = "/mistgate.ownprobe.v1.OwnProbeService/UpdateCommand"
	OwnProbeRevokeProcedure         = "/mistgate.ownprobe.v1.OwnProbeService/Revoke"
	OwnProbeConfirmNetworkProcedure = "/mistgate.ownprobe.v1.OwnProbeService/ConfirmNetwork"
	OwnProbeCheckNowProcedure       = "/mistgate.ownprobe.v1.OwnProbeService/CheckNow"
	OwnProbeDisconnectProcedure     = "/mistgate.ownprobe.v1.OwnProbeService/Disconnect"
)

func init() {
	for _, p := range []string{OwnProbeGetProcedure, OwnProbeUpdateProcedure, OwnProbeCreateProcedure, OwnProbeUpdateCommandProcedure, OwnProbeRevokeProcedure,
		OwnProbeConfirmNetworkProcedure, OwnProbeCheckNowProcedure, OwnProbeDisconnectProcedure} {
		procedureLevels[p] = levelOwner
	}
	for _, p := range []string{OwnProbeUpdateProcedure, OwnProbeCreateProcedure, OwnProbeRevokeProcedure, OwnProbeDisconnectProcedure} {
		stepUpProcedures[p] = true
	}
}
