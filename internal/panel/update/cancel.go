package update

import (
	"context"
	"slices"

	"github.com/mistgate/mistgate/internal/panel/store"
)

const pauseCancelWait = "cancel_wait"

// Older OS rollout rows also carry cancel_requested; keeping that read lets an upgrade finish an existing cancellation.
func isCancelWait(pauseKey string, legacyOSParams map[string]string) bool {
	return pauseKey == pauseCancelWait || legacyOSParams["cancel_requested"] == "true"
}

// A cancellation waits only on work already handed to a node; this keeps the agent and OS rollouts aligned when no queue remains.
func cancelMustWaitForStep[T any](steps []T, inFlight func(T) bool) bool {
	return slices.ContainsFunc(steps, inFlight)
}

type pauseState struct {
	status string
	key    string
}

type pauseCAS[T any] interface {
	read(context.Context, string) (T, error)
	write(context.Context, string, string, string, string, map[string]string) (bool, error)
}

type rolloutPauseCAS struct{ st *store.Store }

func (p rolloutPauseCAS) read(ctx context.Context, id string) (store.RolloutRow, error) {
	return p.st.Rollout(ctx, id)
}

func (p rolloutPauseCAS) write(ctx context.Context, id, status, key, code string, params map[string]string) (bool, error) {
	return p.st.PauseRollout(ctx, id, status, key, code, params)
}

type osRolloutPauseCAS struct{ st *store.Store }

func (p osRolloutPauseCAS) read(ctx context.Context, id string) (store.OsRolloutRow, error) {
	return p.st.OsRollout(ctx, id)
}

func (p osRolloutPauseCAS) write(ctx context.Context, id, status, key, code string, params map[string]string) (bool, error) {
	return p.st.PauseOsRollout(ctx, id, status, key, code, params)
}

// Retrying only after a resume keeps a failure from replacing a pause that another writer deliberately kept.
func pauseOverRead[T any](ctx context.Context, id string, observed T, code string, params map[string]string, stateOf func(T) pauseState, io pauseCAS[T]) (T, bool, error) {
	current := observed
	for {
		state := stateOf(current)
		ok, err := io.write(ctx, id, state.status, state.key, code, params)
		if err != nil {
			return current, false, err
		}
		if ok {
			return current, true, nil
		}
		current, err = io.read(ctx, id)
		if err != nil {
			return current, false, err
		}
		if state.status == store.RolloutRunning || stateOf(current).status != store.RolloutRunning {
			return current, false, nil
		}
	}
}

func rolloutPauseState(ro store.RolloutRow) pauseState {
	return pauseState{status: ro.Status, key: ro.PauseKey}
}

func osRolloutPauseState(ro store.OsRolloutRow) pauseState {
	return pauseState{status: ro.Status, key: ro.PauseKey}
}
