package agent

import (
	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/nftmissing"
)

// A server without the nft command (a cloud image of Debian 13 ships without the nftables package) fails every firewall
// step of the agent at every sweep: the SSH guard, port hopping, WARP routing, the UDP delivery check. The agent says it
// once when it finds out, in the log and as the node event nft_missing, and once when nft works again (nft_restored).
// It needs no restart for that: every call looks the command up again, and the sweep retries the steps that failed.

// nftNote records the failure of a firewall step. It returns err without the missing-nft part: nil when that was all,
// and the whole of err when nft was not the cause, so the caller still logs what is left.
func (a *Agent) nftNote(err error) error {
	if !nftmissing.Is(err) {
		return err
	}
	if a.nft.Appeared() {
		a.log.Warn("nft is not installed on this server: the SSH guard, port hopping, WARP routing and the UDP delivery check do not work until the nftables package is installed",
			"code", "nft_missing")
		a.event(pb.Severity_SEVERITY_WARNING, "nft_missing", "", nil)
	}
	return nftmissing.Rest(err)
}

// nftOK is called when a firewall step went through: it ends a run of nft_missing.
func (a *Agent) nftOK() {
	if a.nft.Gone() {
		a.log.Info("nft is available again; the firewall steps that were waiting for it are done", "code", "nft_restored")
		a.event(pb.Severity_SEVERITY_INFO, "nft_restored", "", nil)
	}
}
