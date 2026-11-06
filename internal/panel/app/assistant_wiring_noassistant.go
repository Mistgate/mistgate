//go:build noassistant

package app

import "net/http"

// The split edge Workers that do not serve the admin surface omit the assistant backend and MCP client runtime.
func (k *core) newAssistantService() {}

func (k *core) assistantHandler() (string, http.Handler) { return "", nil }

func (k *core) attachAssistantRuntime(http.Handler) {}
