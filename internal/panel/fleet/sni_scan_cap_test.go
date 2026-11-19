package fleet

import (
	"testing"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

// SniScan (agent.proto "NEIGHBOR SITES") goes only to an agent that listed sni-scan/1, and the session core carries it as a command.
func TestSniScanFrameNeedsItsCapability(t *testing.T) {
	frame := &agentv1.ConnectResponse{Message: &agentv1.ConnectResponse_SniScan{SniScan: &agentv1.SniScan{RequestId: "scan", Address: "192.0.2.10"}}}
	if capability, message := requestCapability(frame, false); capability != capSniScan || message == "" {
		t.Errorf("SniScan needs %q (%q), want %q", capability, message, capSniScan)
	}
	if requestKind(frame) != PendingCommand {
		t.Errorf("SniScan is request kind %v, want a command", requestKind(frame))
	}
}
