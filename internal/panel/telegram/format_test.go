package telegram

import (
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/store"
)

func TestDoctorAlertLocalizesWARPWithoutInboundIDs(t *testing.T) {
	a := store.HealthAlert{
		Kind:     "doctor_fail",
		Severity: 3,
		Params: map[string]string{
			"check":       "warp_path",
			"detail_code": "warp_path.not_configured",
			"detail":      "inbound(s) with egress warp are not started, the node has no WARP account: inb_test",
			"inbounds":    "inb_test",
			"profiles":    "HY2 · WARP",
			"cred_id":     "cred_test",
		},
	}

	for _, tc := range []struct {
		lang L
		want string
	}{
		{lang: "en", want: "Profiles with WARP egress (“HY2 · WARP”) cannot start: the node has no WARP account"},
		{lang: "ru", want: "Профили с выходом через WARP («HY2 · WARP») не стартуют: у ноды нет аккаунта WARP"},
	} {
		got := alertOpened(tc.lang, a, "nl", "")
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s message %q lacks %q", tc.lang, got, tc.want)
		}
		for _, forbidden := range []string{"inb_test", "cred_test", "egress warp:", "not configured on this node"} {
			if strings.Contains(got, forbidden) {
				t.Errorf("%s message contains %q: %q", tc.lang, forbidden, got)
			}
		}
	}
}

func TestUnknownDoctorDetailUsesLocalizedGenericLine(t *testing.T) {
	a := store.HealthAlert{
		Kind:     "doctor_fail",
		Severity: 3,
		Params: map[string]string{
			"check":       "warp_path",
			"detail_code": "warp_path.future_failure",
			"detail":      "new English detail containing inb_test",
			"inbound_id":  "inb_test",
		},
	}

	for _, tc := range []struct {
		lang L
		want string
	}{
		{lang: "en", want: "Node check: WARP is down"},
		{lang: "ru", want: "Проверка узла: WARP не работает"},
	} {
		got := alertOpened(tc.lang, a, "nl", "")
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s message %q lacks generic line %q", tc.lang, got, tc.want)
		}
		for _, forbidden := range []string{"inb_test", "new English detail"} {
			if strings.Contains(got, forbidden) {
				t.Errorf("%s message contains %q: %q", tc.lang, forbidden, got)
			}
		}
	}
}
