//go:build js && wasm && !edgeentrytest

package main

import (
	"net/http"

	"github.com/mistgate/mistgate/internal/panel/fleet"
)

func withEdgeTestHooks(handler http.Handler, _ *fleet.Fleet) http.Handler { return handler }
