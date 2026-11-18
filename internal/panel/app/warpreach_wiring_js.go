//go:build js

package app

import (
	"github.com/mistgate/mistgate/internal/panel/edition"
	"github.com/mistgate/mistgate/internal/panel/health"
	"github.com/mistgate/mistgate/internal/panel/httpserver"
)

// The WARP reach check is the VPS edition's (proto/mistgate/warpreach/v1): the Workers run no check, keep none of its code and
// link none of the generated code, only this stub, which names the service by its path and refuses every procedure the way the
// admin words a feature the edition lacks (the check is health work, edition.HealthChecks).
const warpReachServicePath = "/mistgate.warpreach.v1.WarpReachService/"

func (k *core) addWarpReachConditions(*health.Service) {}

func (k *core) addWarpReach(cfg *httpserver.Config) {
	refuseInEdition(cfg, warpReachServicePath, edition.HealthChecks)
}
