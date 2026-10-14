//go:build js && wasm

// Command mistgate-edge-admin is the wasm of the admin Worker of the edge edition: the admin SPA and API, sign-in and setup. It alone creates the instance.
package main

import (
	"github.com/mistgate/mistgate/edge/bridge"
	"github.com/mistgate/mistgate/internal/panel/app"
)

func main() {
	bridge.Run(bridge.Role{
		Name: "admin", Build: app.BuildAdmin, Auth: true, CreatesInstance: true,
		Ops: []bridge.Op{bridge.OpFetch},
	})
}
