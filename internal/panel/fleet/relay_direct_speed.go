//go:build !js

package fleet

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"connectrpc.com/connect"

	"github.com/mistgate/mistgate/internal/panel/store"
)

const relayDirectSpeedMB = 8

// directSpeed is the panel's own proof that the DIRECT way carries a real flow before a reverse relay goes back to it: the
// entry connects to the exit's node and takes 8 MB (the existing path speed test, the entry the one that connects, the exit
// sends). The leg of a relay is only packets and passes a way that freezes after the first 16 KB; this flow does not.
//
// The controller starts it and never the owner, so it holds the same limits as any speed test (one test per node at a time, 6 a
// day per node, 12 a day for the panel as the caller): a limit reached is "skipped, busy" and the controller asks again later.
// It connects to a port the exit's agent opens for the test, not to the relay's port, so it proves the route of the two nodes and
// not the filter's view of that one port.
func (r relayRun) directSpeed(ctx context.Context) store.RelayStage {
	stage := store.RelayStage{Sender: r.entry.ID, At: r.f.now().UTC().Unix()}
	for _, id := range []string{r.entry.ID, r.exit.ID} {
		if _, code := r.f.probeLive(r.live, id, capSpeed); code != "" {
			switch {
			case code == "agent_too_old":
				stage.Status, stage.Code = "skipped", "unsupported"
			case id == r.exit.ID:
				stage.Status, stage.Code = "fail", "exit_offline"
			default:
				stage.Status, stage.Code = "fail", "entry_offline"
			}
			return stage
		}
	}
	release, err := r.f.startSpeedAs(ctx, "system", r.entry.ID, r.exit.ID)
	if err != nil {
		if connect.CodeOf(err) == connect.CodeResourceExhausted {
			stage.Status, stage.Code = "skipped", "busy"
		} else {
			stage.Status, stage.Code = "fail", "failed"
		}
		return stage
	}
	defer release()

	result := r.f.runPathSpeed(ctx, r.entry, r.exit, "down", relayDirectSpeedMB)
	stage.At, stage.MB = r.f.now().UTC().Unix(), result.MB
	if result.Error != "" {
		stage.Status, stage.Code = "fail", result.Error
		switch result.Error {
		case "agent_too_old":
			stage.Status, stage.Code = "skipped", "unsupported"
		case "busy":
			stage.Status, stage.Code = "skipped", "busy"
		}
		return stage
	}
	stage.Status, stage.MbitS, stage.Bytes = "ok", result.MbitS, result.Bytes
	stage.ConnectMS, stage.FirstByteMS, stage.DurationMS = result.ConnectMS, result.FirstByteMS, result.DurationMS
	if b, err := json.Marshal(result); err == nil {
		if err := r.f.st.AddPathSpeed(ctx, r.entry.ID, r.exit.ID, "down", time.Unix(stage.At, 0), string(b)); err != nil {
			r.f.log.Warn("store direct relay path speed", "code", "store_failed", "relay", r.link.ID, "err", err)
		}
	}
	r.f.audit(context.WithoutCancel(ctx), "node.path_speed", map[string]string{
		"from": r.entry.Name, "to": r.exit.Name, "direction": "down", "mb": strconv.Itoa(relayDirectSpeedMB), "error": "", "by": "relay_controller",
	})
	return stage
}
