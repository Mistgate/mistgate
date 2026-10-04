package subs

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/mistgate/mistgate/internal/panel/access"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
)

// flagEmoji turns a two-letter country code into its flag (two regional indicator symbols); anything else
// gives "". Happ shows a flag as the server icon only when the remark starts with one.
func flagEmoji(cc string) string {
	cc = strings.ToUpper(strings.TrimSpace(cc))
	if len(cc) != 2 || cc[0] < 'A' || cc[0] > 'Z' || cc[1] < 'A' || cc[1] > 'Z' {
		return ""
	}
	return string([]rune{0x1F1E6 + rune(cc[0]-'A'), 0x1F1E6 + rune(cc[1]-'A')})
}

// remarks renders the server name of every server of one user from the template, then makes them unique: a name that
// is already taken gets a number, from 2 on ("🇩🇪 Germany · Hysteria2", "🇩🇪 Germany · Hysteria2 2"). The profile
// name can distinguish protocols and WARP exits. lang only matters for {country}.
// web/src/screens/subscriptions/model.ts serverNames draws the admin's preview by the same rules.
func remarks(servers []access.SubServer, template, lang string) []string {
	if template == "" {
		template = subsettings.DefaultNameTemplate
	}
	names := make([]string, len(servers))
	taken := map[string]bool{}
	for i, s := range servers {
		rep := strings.NewReplacer("{flag}", flagEmoji(s.CountryCode), "{country}", access.CountryName(s.CountryCode, lang),
			"{node}", s.Node, "{profile}", s.Profile)
		n := clean(rep.Replace(template))
		if n == "" {
			n = clean(s.Node)
		}
		if n == "" {
			n = "server"
		}
		if s.LoadPercent != nil {
			n += " · " + strconv.Itoa(*s.LoadPercent) + "%"
		} else if !s.MetricsAt.IsZero() {
			n += " · ↓" + rateMbps(s.NetworkRxBps) + " ↑" + rateMbps(s.NetworkTxBps) + " Mbps"
		}
		name := n
		for k := 2; taken[name]; k++ {
			name = n + " " + strconv.Itoa(k)
		}
		taken[name] = true
		names[i] = name
	}
	return names
}

func rateMbps(bps uint64) string {
	n := strconv.FormatFloat(float64(bps)/1_000_000, 'f', 1, 64)
	n = strings.TrimSuffix(strings.TrimSuffix(n, "0"), ".")
	return n
}

// clean drops control characters and collapses runs of whitespace, so a node name cannot break the line.
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	// A template often puts separators between optional pieces. If {country} or {profile} is empty, remove a
	// dangling middle dot as well as the surrounding space.
	return strings.Trim(strings.Join(strings.Fields(s), " "), " ·")
}

// withRemark replaces the #fragment of a share link with the given remark, percent-encoded (UTF-8 included).
func withRemark(uri, remark string) string {
	if i := strings.IndexByte(uri, '#'); i >= 0 {
		uri = uri[:i]
	}
	return uri + "#" + (&url.URL{Fragment: remark}).EscapedFragment()
}

// ---- a subscription that does not work: the reason, right in the app ----

// The one entry a person without access gets instead of the servers, named after the reason: nothing listens on 0.0.0.0
// port 1, so it never connects, and the list says why. Mihomo gets the same entry as a proxy (writeMihomo renames it).
const (
	placeholderURI   = "hysteria2://off@0.0.0.0:1/"
	placeholderProxy = "- name: \"off\"\n  type: hysteria2\n  server: \"0.0.0.0\"\n  port: 1\n  password: \"off\"\n"
)

var ruMonths = [...]string{"января", "февраля", "марта", "апреля", "мая", "июня", "июля", "августа", "сентября", "октября", "ноября", "декабря"}

// dayText is a date the way the user page writes it: "29 сентября", "29 September"; the year only when it is not this one.
func dayText(t, now time.Time, lang string) string {
	m := t.Month().String()
	if lang == "ru" {
		m = ruMonths[t.Month()-1]
	}
	s := strconv.Itoa(t.Day()) + " " + m
	if t.Year() != now.Year() {
		s += " " + strconv.Itoa(t.Year())
	}
	return s
}

// stateNote is what the subscription apps show a person whose subscription does not work, in the words of the user page and
// the instance language; "" while it works.
func stateNote(v access.SubView, lang string, now time.Time) string {
	pick := func(ru, en string) string {
		if lang == "ru" {
			return ru
		}
		return en
	}
	switch v.Status {
	case access.StatusExpired:
		if v.Expires.IsZero() {
			return pick("Подписка закончилась. Напишите — продлим", "Subscription ended. Message us to renew")
		}
		return fmt.Sprintf(pick("Подписка закончилась %s. Напишите — продлим", "Subscription ended on %s. Message us to renew"), dayText(v.Expires, now, lang))
	case access.StatusLimited:
		if v.NextReset.IsZero() || v.QuotaReset == "" || v.QuotaReset == access.ResetNone {
			return pick("Трафик закончился. Напишите — увеличим", "Traffic used up. Message us to raise the limit")
		}
		return fmt.Sprintf(pick("Трафик закончился, обнулится %s", "Traffic used up, resets on %s"), dayText(v.NextReset, now, lang))
	case access.StatusDisabled:
		return pick("Доступ приостановлен", "Access paused")
	}
	return ""
}

// cutAnnounce shortens an announcement to the max runes Happ shows: at the last word boundary in the second half when
// the cut lands inside a word, and with "…" at the end. web/src/screens/subscriptions/model.ts cutAnnounce draws the
// admin's counter and preview by the same rule.
func cutAnnounce(s string, max int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= max {
		return string(r)
	}
	cut := r[:max-1]
	if !unicode.IsSpace(r[max-1]) {
		for i := len(cut) - 1; i >= max/2; i-- {
			if unicode.IsSpace(cut[i]) {
				cut = cut[:i]
				break
			}
		}
	}
	return strings.TrimRightFunc(string(cut), func(c rune) bool { return unicode.IsSpace(c) || strings.ContainsRune(",;:—–-", c) }) + "…"
}
