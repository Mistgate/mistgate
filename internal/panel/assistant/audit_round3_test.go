//go:build !js

package assistant

import "testing"

func TestFollowUpTelegramRequestPhrases(t *testing.T) {
	cases := []struct {
		message string
		want    bool
	}{
		{"Which nodes serve the Telegram proxy?", false},
		{"Which nodes serve Telegram US users?", false},
		{"Tell me how many users in Telegram groups are blocked", false},
		{"Send me the list of users on Telegram", false},
		{"Show me which of us use telegram", false},
		{"ping my Telegram when it is done", false},
		{"Ping me on Telegram when node no.3 is back", true},
		{"Let me know on Telegram if there is no traffic", true},
		{"notify me on telegram, not by email", true},
		{"Do not ping me on Telegram", false},
		{"not by email, notify me on Telegram", true},
		{"Напиши, сколько пользователей в телеграм-прокси", false},
		{"Напиши в отчёт про отгрузку", false},
		{"напиши мне в тг когда закончится", true},
		{"сообщи в телеграм, когда нода поднимется", true},
		{"не пиши мне в телеграм", false},
		{"сообщи результат через телеграм", true},
		{"我们的 Telegram 通知机器人坏了吗", false},
		{"完成后在 Telegram 上通知我", true},
		{"完成后用电报通知我", true},
		{"完成后不要在 Telegram 上通知我", false},
	}
	for _, test := range cases {
		if got := requestsFollowUpTelegram(test.message); got != test.want {
			t.Errorf("requestsFollowUpTelegram(%q) = %v, want %v", test.message, got, test.want)
		}
	}
}

func TestCompletePlanTextChecksJoinersAfterRejoining(t *testing.T) {
	if got, err := completePlanText("summary", "check \U0001f469\u200d\U0001f4bb later", 100); err != nil || got != "check \U0001f469\u200d\U0001f4bb later" {
		t.Fatalf("visible emoji joiner was refused: %q, %v", got, err)
	}
	for _, value := range []string{
		"\u200dleading",
		"trailing\u200c",
		"a\u200c\u200d b",
		"tk1_abcdefgh\u200dijklmnopqrstuv",
	} {
		if _, err := completePlanText("summary", value, 100); err == nil {
			t.Errorf("invalid or credential-splitting joiners were accepted: %q", value)
		}
	}
}
