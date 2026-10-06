//go:build js && wasm && edgeentrytest

package main

import "net/http"

func withEdgeTestHooks(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__edge_bridge_test__/cookies" {
			w.Header().Add("Set-Cookie", "first=one; Path=/")
			w.Header().Add("Set-Cookie", "second=two; Path=/")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte("cookie-test"))
			return
		}
		if r.URL.Path == "/__edge_bridge_test__/headers" {
			for _, value := range r.Header.Values("X-Multi") {
				w.Header().Add("X-Multi-Response", value)
			}
			_, _ = w.Write([]byte("header-test"))
			return
		}
		if r.URL.Path == "/__edge_bridge_test__/stream" {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("stream-test"))
			w.(http.Flusher).Flush()
			return
		}
		handler.ServeHTTP(w, r)
	})
}
