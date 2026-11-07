//go:build !js

package store

import "strings"

// withRequestID adds "req":"<id>" to a JSON-object params string, so an audit row names the HTTP request that wrote it (the
// id the admin UI shows on a failed call and the logs carry). Params that are not an object, or already have a "req" key, stay.
func withRequestID(params, id string) string {
	if id == "" || len(params) < 2 || params[0] != '{' || params[len(params)-1] != '}' || strings.Contains(params, `"req":`) {
		return params
	}
	if strings.TrimSpace(params[1:len(params)-1]) == "" {
		return `{"req":"` + id + `"}`
	}
	return params[:len(params)-1] + `,"req":"` + id + `"}`
}
