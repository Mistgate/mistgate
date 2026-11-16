//go:build js

package fleet

import (
	"context"

	"github.com/mistgate/mistgate/internal/panel/store"
)

func (r relayRun) directSpeed(context.Context) store.RelayStage {
	return r.stage("skipped", "unsupported", r.entry.ID)
}
