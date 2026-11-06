//go:build !js

package app

import (
	"net/http"

	"github.com/mistgate/mistgate/internal/panel/assistant"
	"github.com/mistgate/mistgate/internal/panel/mcp"
)

func (k *core) newAssistantService() {
	service := assistant.New(assistant.Config{Store: k.st, Vault: k.vlt, Auth: k.authSvc, Now: k.c.Clock, Log: k.log})
	k.asst = service
	k.assistantRetention = service
}

func (k *core) assistantHandler() (string, http.Handler) {
	if k.asst == nil {
		return "", nil
	}
	return k.asst.Handler()
}

func (k *core) attachAssistantRuntime(api http.Handler) {
	if k.asst == nil {
		return
	}
	runtime, err := mcp.NewAssistant(mcpConfig(k.authSvc, k.st, k.log, k.tg.PlanWaiting, k.c.Clock, api))
	if err != nil {
		k.log.Error("assistant MCP tools are off", "err", err)
		return
	}
	service, ok := k.asst.(*assistant.Service)
	if ok {
		service.SetToolRuntime(runtime)
	}
}
