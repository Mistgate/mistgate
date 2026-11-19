//go:build !js

package fleet

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"connectrpc.com/connect"

	"github.com/mistgate/mistgate/internal/agentcap"
	"github.com/mistgate/mistgate/internal/panel/store"
)

const relayReverseSpeedMB = 8

// reverseSpeed is an explicit proof that the exit can send a full 8 MB flow to the entry.
func (r relayRun) reverseSpeed(ctx context.Context) store.RelayStage {
	stage := store.RelayStage{Sender: r.exit.ID, At: r.f.now().UTC().Unix()}
	for _, id := range []string{r.exit.ID, r.entry.ID} {
		if _, code := r.f.probeLive(r.live, id, agentcap.Speed); code != "" {
			if code == "agent_too_old" {
				stage.Status, stage.Code = "skipped", "unsupported"
			} else if id == r.entry.ID {
				stage.Status, stage.Code = "fail", "entry_offline"
			} else {
				stage.Status, stage.Code = "fail", "exit_offline"
			}
			return stage
		}
	}
	release, err := r.f.startSpeed(ctx, r.exit.ID, r.entry.ID)
	if err != nil {
		if connect.CodeOf(err) == connect.CodeResourceExhausted {
			stage.Status, stage.Code = "skipped", "busy"
		} else {
			stage.Status, stage.Code = "fail", "failed"
		}
		return stage
	}
	defer release()

	result := r.f.runPathSpeed(ctx, r.exit, r.entry, "up", relayReverseSpeedMB)
	stage.At, stage.MB = r.f.now().UTC().Unix(), result.MB
	if result.Error != "" {
		stage.Status, stage.Code = "fail", result.Error
		if result.Error == "agent_too_old" {
			stage.Status, stage.Code = "skipped", "unsupported"
		}
		if result.Error == "busy" {
			stage.Status, stage.Code = "skipped", "busy"
		}
		return stage
	}
	stage.Status, stage.MbitS, stage.Bytes = "ok", result.MbitS, result.Bytes
	stage.ConnectMS, stage.FirstByteMS, stage.DurationMS = result.ConnectMS, result.FirstByteMS, result.DurationMS
	if b, err := json.Marshal(result); err == nil {
		if err := r.f.st.AddPathSpeed(ctx, r.exit.ID, r.entry.ID, "up", time.Unix(stage.At, 0), string(b)); err != nil {
			r.f.log.Warn("store reverse relay path speed", "code", "store_failed", "relay", r.link.ID, "err", err)
		}
	}
	r.f.audit(context.WithoutCancel(ctx), "node.path_speed", map[string]string{
		"from": r.exit.Name, "to": r.entry.Name, "direction": "up", "mb": strconv.Itoa(relayReverseSpeedMB), "error": "",
	})
	return stage
}
