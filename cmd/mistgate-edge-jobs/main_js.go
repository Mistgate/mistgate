//go:build js && wasm

// Command mistgate-edge-jobs is the wasm of the jobs Worker of the edge edition: the scheduled work (EdgeTick).
package main

import (
	"github.com/mistgate/mistgate/edge/bridge"
	"github.com/mistgate/mistgate/internal/panel/app"
)

func main() {
	bridge.Run(bridge.Role{
		Name: "jobs", Build: app.BuildJobs, Auth: true,
		Ops: []bridge.Op{bridge.OpCron},
	})
}
