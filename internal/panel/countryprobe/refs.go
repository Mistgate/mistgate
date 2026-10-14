package countryprobe

import "strings"

// References are the two addresses of a country that make the probe work (agent.proto "COUNTRY PROBE"). Control is an address
// that is never blocked there: the probe finds the TSPU box with the references and checks that SYNs to the control still pass
// behind it, which is how it knows the method applies on the probe node's network. Blocked are addresses that the TSPU surely
// drops, best first: the probe node SYN-traces one of them and the last router that answers is the box's position. The panel
// sends them one at a time (see run); the owner does not set them.
//
// They are built into the code, not settings: a wrong reference silently breaks the method, and the list changes with the
// panel's releases when a service stops being blocked.
type References struct {
	Control    string
	ControlSNI string // reserved by the agent (the SYN method sends no hello); kept so a later hello-based method has it
	Blocked    []string
}

// referencesByCountry has Russia only: the method, the references and the block lists are Russian.
var referencesByCountry = map[string]References{
	"RU": {
		// Yandex (ya.ru, 77.88.55.88): a Russian service that is never blocked there. It is the control of both method sources
		// (porutchik/tspu-check, its default --control-target; cheburcheck's probe).
		Control:    "77.88.55.88",
		ControlSNI: "www.yandex.ru",
		Blocked: []string{
			// Facebook, 31.13.72.36: Meta is blocked in Russia since March 2022, by address. 31.13.72.0/24 is in antifilter's
			// ipsum.lst (the RKN registry summed to /24, list of 14 Oct 2026) and 31.13.64.0/18 in its community.lst.
			"31.13.72.36",
			// Telegram, 149.154.167.99: the default --blocked-ref of porutchik/tspu-check (README, "Уточнение позиции ТСПУ").
			// 149.154.160.0/20 is in antifilter's community.lst (addresses the community found cut); it is not in ipsum.lst.
			"149.154.167.99",
			// LinkedIn, 108.174.10.10: blocked in Russia since November 2016, by address. 108.174.10.0/23 is in antifilter's
			// community.lst.
			"108.174.10.10",
		},
	},
}

// ReferencesFor are the references of a country; ok is false when it has none (the probe does not apply there).
func ReferencesFor(country string) (References, bool) {
	r, ok := referencesByCountry[strings.ToUpper(country)]
	return r, ok
}
