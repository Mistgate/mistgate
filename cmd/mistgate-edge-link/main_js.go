//go:build js && wasm

// Command mistgate-edge-link is the wasm of the link Worker of the edge edition: the Go side of the agent link (challenge, accept, step) and link-only enrolment.
package main

import (
	"github.com/mistgate/mistgate/edge/bridge"
	"github.com/mistgate/mistgate/internal/panel/app"
)

func main() {
	bridge.Run(bridge.Role{
		Name: "link", Build: app.BuildLink, Auth: true,
		Ops: []bridge.Op{bridge.OpFetch, bridge.OpLink},
	})
}
