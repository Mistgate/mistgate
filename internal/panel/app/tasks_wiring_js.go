//go:build js

package app

import (
	"errors"
	"net/http"

	"connectrpc.com/connect"

	"github.com/mistgate/mistgate/internal/panel/httpserver"
)

// The scheduled tasks are the VPS edition's (proto/mistgate/tasks/v1): the Workers keep none of their code, only this stub that
// answers every procedure of the service with UNIMPLEMENTED. It names the service by its path so no generated code of it is
// linked.
const tasksServicePath = "/mistgate.tasks.v1.TasksService/"

func (k *core) newTasksService() {}

func (k *core) addTasks(cfg *httpserver.Config) {
	cfg.AdminHandlers = append(cfg.AdminHandlers, httpserver.AdminHandler{Path: tasksServicePath, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		err := connect.NewError(connect.CodeUnimplemented, errors.New("not available on this edition"))
		_ = connect.NewErrorWriter().Write(w, r, err)
	})})
}
