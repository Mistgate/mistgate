//go:build js

package app

import (
	"errors"
	"net/http"

	"connectrpc.com/connect"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
)

const assistantUnavailableMessage = "assistant is available on the VPS edition"

type assistantUnavailableRPC struct{}

func (assistantUnavailableRPC) Handler(...connect.HandlerOption) (string, http.Handler) {
	return "/" + adminv1connect.AssistantServiceName + "/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case adminv1connect.AssistantServiceGetAssistantAccessProcedure:
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Connect-Protocol-Version", "1")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"available":false,"reason":"edition"}`))
		case adminv1connect.AssistantServiceGetAssistantSettingsProcedure,
			adminv1connect.AssistantServiceUpdateAssistantSettingsProcedure,
			adminv1connect.AssistantServiceSetProviderKeyProcedure,
			adminv1connect.AssistantServiceRemoveProviderKeyProcedure,
			adminv1connect.AssistantServiceListProviderModelsProcedure,
			adminv1connect.AssistantServiceListConversationsProcedure,
			adminv1connect.AssistantServiceGetConversationProcedure,
			adminv1connect.AssistantServiceDeleteConversationProcedure,
			adminv1connect.AssistantServiceDeleteAllConversationsProcedure,
			adminv1connect.AssistantServiceChatProcedure,
			adminv1connect.AssistantServiceStopTurnProcedure,
			adminv1connect.AssistantServiceApplyPlanProcedure,
			adminv1connect.AssistantServiceDismissPlanProcedure,
			adminv1connect.AssistantServiceCancelFollowUpProcedure,
			adminv1connect.AssistantServiceListMemoryProcedure,
			adminv1connect.AssistantServiceSaveMemoryProcedure,
			adminv1connect.AssistantServiceDeleteMemoryProcedure,
			adminv1connect.AssistantServiceForgetAllProcedure:
			writeAssistantUnavailable(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}

func writeAssistantUnavailable(w http.ResponseWriter, r *http.Request) {
	err := connect.NewError(connect.CodeUnimplemented, errors.New(assistantUnavailableMessage))
	_ = connect.NewErrorWriter().Write(w, r, err)
}

func (k *core) newAssistantService() { k.asst = assistantUnavailableRPC{} }

func (k *core) assistantHandler() (string, http.Handler) {
	if k.asst == nil {
		return "", nil
	}
	return k.asst.Handler()
}

func (k *core) attachAssistantRuntime(http.Handler) {}
