package agent

import (
	"context"
	"testing"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/osupdate"
)

func TestOsStatusReaderStaysSilentUntilNodeSetting(t *testing.T) {
	commands := 0
	reader := &osupdate.Reader{Run: func(context.Context, string, ...string) ([]byte, error) {
		commands++
		return nil, nil
	}}
	a := &Agent{osUpdateMonitor: osupdate.NewMonitor(reader), osStatusWake: make(chan struct{}, 1)}
	a.settings.Store(&pb.NodeSettings{})
	status, read := a.pollOsStatus(context.Background())
	if status != nil || read || commands != 0 {
		t.Fatalf("unrequested read returned status=%v read=%v commands=%d", status, read, commands)
	}
}
