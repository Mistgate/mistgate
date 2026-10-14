//go:build js && wasm

// Command mistgate-edge is the wasm of the single edge Worker: every role of the panel in one program. The released bundle is
// still this one. The Workers of the split edition have a wasm each, cmd/mistgate-edge-{public,link,jobs,admin,provision}.
package main

import (
	"github.com/mistgate/mistgate/edge/bridge"
	"github.com/mistgate/mistgate/internal/panel/app"
)

func main() {
	bridge.Run(bridge.Role{
		Name: "all", Build: app.Build, Auth: true, CreatesInstance: true,
		Ops: []bridge.Op{bridge.OpFetch, bridge.OpCron, bridge.OpLink, bridge.OpProvision},
	})
}
