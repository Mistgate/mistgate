//go:build !js

package auth

// The TasksService (proto/mistgate/tasks/v1) is a package of its own so the Cloudflare edition does not link its messages; its
// procedures are named here by path. Reading the tasks, their runs and the tools a check may run, and starting a run now, are
// day-to-day work of the owner and the helpers (operator-profile tokens through the MCP tools tasks_list, task_get, task_runs
// and task_run_now; a run is bounded: one at a time per task, a manual one at most once a minute or five). Creating, changing
// and deleting a task are the owner's: a task runs tools or the assistant again and again on its own, so a token reaches them
// only through the owner's approved plan (task_create_plan, task_update_plan, task_delete_plan and their _apply).
const (
	TasksListTasksProcedure      = "/mistgate.tasks.v1.TasksService/ListTasks"
	TasksGetTaskProcedure        = "/mistgate.tasks.v1.TasksService/GetTask"
	TasksCreateTaskProcedure     = "/mistgate.tasks.v1.TasksService/CreateTask"
	TasksUpdateTaskProcedure     = "/mistgate.tasks.v1.TasksService/UpdateTask"
	TasksDeleteTaskProcedure     = "/mistgate.tasks.v1.TasksService/DeleteTask"
	TasksRunTaskNowProcedure     = "/mistgate.tasks.v1.TasksService/RunTaskNow"
	TasksListTaskRunsProcedure   = "/mistgate.tasks.v1.TasksService/ListTaskRuns"
	TasksListCheckToolsProcedure = "/mistgate.tasks.v1.TasksService/ListCheckTools"
)

func init() {
	for _, p := range []string{TasksListTasksProcedure, TasksGetTaskProcedure, TasksRunTaskNowProcedure, TasksListTaskRunsProcedure, TasksListCheckToolsProcedure} {
		procedureLevels[p] = levelWrite
		tokenProcedures[p] = TokenAccessDirect
	}
	for p, tool := range map[string]string{TasksCreateTaskProcedure: "task_create", TasksUpdateTaskProcedure: "task_update", TasksDeleteTaskProcedure: "task_delete"} {
		procedureLevels[p] = levelOwner
		tokenProcedures[p] = TokenAccessApproved
		grantProcedures[tool] = []string{p}
	}
}
