package auth

import (
	"testing"

	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// TelegramService: setting the bot is the owner's and needs a step-up; linking a chat (step-up too), unlinking, switching
// the alerts and a test message are the admin's own account, any role. No token or MCP agent may call any of it.
func TestTelegramServicePolicy(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		path                  string
		owner, helper, readon bool
		stepUp                bool
	}{
		{"GetTelegram", adminv1connect.TelegramServiceGetTelegramProcedure, true, true, true, false},
		{"SetTelegramBot", adminv1connect.TelegramServiceSetTelegramBotProcedure, true, false, false, true},
		{"BeginTelegramLink", adminv1connect.TelegramServiceBeginTelegramLinkProcedure, true, true, true, true},
		{"UnlinkTelegram", adminv1connect.TelegramServiceUnlinkTelegramProcedure, true, true, true, false},
		{"SetTelegramAlerts", adminv1connect.TelegramServiceSetTelegramAlertsProcedure, true, true, true, false},
		{"SendTelegramTest", adminv1connect.TelegramServiceSendTelegramTestProcedure, true, true, true, false},
	} {
		if _, listed := procedureLevels[tc.path]; !listed {
			t.Errorf("%s has no explicit level", tc.name)
		}
		for role, want := range map[string]bool{store.RoleOwner: tc.owner, store.RoleHelper: tc.helper, store.RoleReadonly: tc.readon} {
			if got := roleAllows(role, levelOf(tc.path)); got != want {
				t.Errorf("%s as %s: allowed = %v, want %v", tc.name, role, got, want)
			}
		}
		if got := NeedsStepUp(tc.path); got != tc.stepUp {
			t.Errorf("%s needs step-up = %v, want %v", tc.name, got, tc.stepUp)
		}
		if _, allowed := TokenAccess(tc.path); allowed {
			t.Errorf("%s must stay closed to API tokens and MCP", tc.name)
		}
	}
}
