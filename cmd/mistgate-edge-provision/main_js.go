//go:build js && wasm

// Command mistgate-edge-provision is the wasm of the provision Worker of the edge edition: the Provisioner: SSH installs.
package main

import (
	"github.com/mistgate/mistgate/edge/bridge"
	"github.com/mistgate/mistgate/internal/panel/app"
)

func main() {
	bridge.Run(bridge.Role{
		Name: "provision", Build: app.BuildProvision, Auth: true,
		Ops: []bridge.Op{bridge.OpFetch, bridge.OpProvision},
	})
}
