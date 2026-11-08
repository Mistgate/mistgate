//go:build !js

package app

import (
	"github.com/mistgate/mistgate/internal/panel/assistant"
	"github.com/mistgate/mistgate/internal/panel/httpserver"
	"github.com/mistgate/mistgate/internal/panel/mcp"
	"github.com/mistgate/mistgate/internal/panel/tasks"
)

// newTasksService builds the owner's scheduled tasks (mistgate.tasks.v1): the TasksService, the scheduler (a background job)
// and the alert conditions the health evaluator reads. An assistant task runs on the assistant service; the tools of both
// kinds come with the assistant's in-process MCP runtime once the admin API is assembled (attachTasksRuntime).
func (k *core) newTasksService() {
	var runner tasks.Assistant
	if svc, ok := k.asst.(*assistant.Service); ok {
		runner = svc
	}
	svc := tasks.New(tasks.Config{Store: k.st, Vault: k.vlt, Assistant: runner, Now: k.c.Clock, Log: k.log})
	k.sched = svc
	k.hl.AddConditionSource(svc.Conditions)
}

// addTasks mounts the TasksService behind the session.
func (k *core) addTasks(cfg *httpserver.Config) {
	if k.sched == nil {
		return
	}
	path, handler := k.sched.Handler()
	cfg.AdminHandlers = append(cfg.AdminHandlers, httpserver.AdminHandler{Path: path, Handler: handler})
}

// attachTasksRuntime hands the in-process MCP runtime to the scheduled tasks.
func (k *core) attachTasksRuntime(runtime mcp.AssistantRuntime) {
	if svc, ok := k.sched.(*tasks.Service); ok {
		svc.SetToolRuntime(runtime)
	}
}
