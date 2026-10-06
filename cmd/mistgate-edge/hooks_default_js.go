//go:build js && wasm && !edgeentrytest

package main

import "net/http"

func withEdgeTestHooks(handler http.Handler) http.Handler { return handler }
