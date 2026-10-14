//go:build js && wasm

// Command mistgate-edge-public is the wasm of the public Worker of the edge edition: subscriptions of every format, the person's page, devices, the brand logo and the decoy.
package main

import (
	"github.com/mistgate/mistgate/edge/bridge"
	"github.com/mistgate/mistgate/internal/panel/app"
)

func main() {
	bridge.Run(bridge.Role{
		Name: "public", Build: app.BuildPublic,
		Ops: []bridge.Op{bridge.OpFetch},
	})
}
