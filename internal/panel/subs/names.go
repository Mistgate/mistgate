package subs

import (
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"

	"github.com/mistgate/mistgate/internal/panel/access"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
)

const happRemarkMaxUTF16 = 30

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
// is already taken gets a number, from 2 on ("🇩🇪 DE · Hysteria2", "🇩🇪 DE · Hysteria2 2"). The profile
// name can distinguish protocols and WARP exits. {country} is the two-letter code (a localised name only for a code
// that is not one). These are the Mihomo names: no load percentage, because a select group remembers the chosen server
// by its name and a name that follows the load would reset the choice on every refresh.
// web/src/screens/subscriptions/model.ts serverNames draws the admin's (Happ) preview by the same rules.
func remarks(servers []access.SubServer, template, lang string) []string {
	return renderRemarks(servers, template, lang, false, false)
}

// happRemarks keeps URI-list names within Happ's 30-character title limit. Happ counts supplementary
// Unicode characters as two UTF-16 units, so use that stricter limit and compact protocol labels when needed.
// load adds the node's load percentage (" · 64%"): only for Happ, every other app of the list keeps stable names.
func happRemarks(servers []access.SubServer, template, lang string, load bool) []string {
	return renderRemarks(servers, template, lang, true, load)
}

func renderRemarks(servers []access.SubServer, template, lang string, limitHapp, load bool) []string {
	if template == "" {
		template = subsettings.DefaultNameTemplate
	}
	names := make([]string, len(servers))
	taken := map[string]bool{}
	made := make([]int, len(servers)) // numbers go out in creation order (SubServer.Made), not in list order
	for i := range made {
		made[i] = i
	}
	slices.SortStableFunc(made, func(a, b int) int { return servers[a].Made - servers[b].Made })
	for _, i := range made {
		s := servers[i]
		country := access.CountryName(s.CountryCode, lang)
		if flagEmoji(s.CountryCode) != "" {
			country = strings.ToUpper(strings.TrimSpace(s.CountryCode))
		}
		rep := strings.NewReplacer("{flag}", flagEmoji(s.CountryCode), "{country}", country,
			"{node}", s.Node, "{profile}", s.Profile)
		n := clean(rep.Replace(template))
		if n == "" {
			n = clean(s.Node)
		}
		if n == "" {
			n = "server"
		}
		if limitHapp {
			n = strings.ReplaceAll(n, "%", "％") // U+FF05, see the load suffix below
		}
		suffix := ""
		if load && s.LoadPercent != nil {
			// U+FF05, not "%": Happ decodes the name a second time, and a bare "%" makes that fail, so Happ drops the
			// whole name and lists the server by its address. The fullwidth sign looks the same and survives it.
			suffix = " · " + strconv.Itoa(*s.LoadPercent) + "％"
		}
		name := namedRemark(n, suffix, s.Profile, limitHapp)
		for k := 2; taken[name]; k++ {
			name = namedRemark(n, suffix+" "+strconv.Itoa(k), s.Profile, limitHapp)
		}
		taken[name] = true
		names[i] = name
	}
	return names
}

func namedRemark(base, suffix, profile string, limitHapp bool) string {
	if !limitHapp {
		return base + suffix
	}
	return fitHappRemark(base, suffix, profile)
}

func fitHappRemark(base, suffix, profile string) string {
	if utf16Length(suffix) > happRemarkMaxUTF16 {
		suffix = truncateUTF16(suffix, happRemarkMaxUTF16)
	}
	if utf16Length(base)+utf16Length(suffix) <= happRemarkMaxUTF16 {
		return base + suffix
	}
	if compact := compactProfile(profile); compact != profile {
		base = strings.ReplaceAll(base, profile, compact)
		if utf16Length(base)+utf16Length(suffix) <= happRemarkMaxUTF16 {
			return base + suffix
		}
	}
	room := max(0, happRemarkMaxUTF16-utf16Length(suffix))
	base = truncateUTF16(base, room)
	if base == "" {
		base = truncateUTF16("server", room)
	}
	return base + suffix
}

func compactProfile(profile string) string {
	for _, item := range []struct{ full, short string }{
		{"Hysteria2", "HY2"}, {"AmneziaWG", "AWG"}, {"WireGuard", "WG"},
	} {
		if len(profile) >= len(item.full) && strings.EqualFold(profile[:len(item.full)], item.full) {
			return item.short + profile[len(item.full):]
		}
	}
	return profile
}

func utf16Length(s string) int { return len(utf16.Encode([]rune(s))) }

func truncateUTF16(s string, maxUnits int) string {
	var out strings.Builder
	units := 0
	for _, r := range s {
		width := utf16.RuneLen(r)
		if width < 0 || units+width > maxUnits {
			break
		}
		out.WriteRune(r)
		units += width
	}
	return strings.TrimRight(out.String(), " ·")
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

// mihomoOnlyNote is what the link-list apps (Happ and the rest) show a person who has servers, none of which they can use:
// every one is Gecko, which only the Mihomo apps speak. It is the entry that says so, instead of an empty list; "" when
// anything else is the reason the list is empty.
func mihomoOnlyNote(v access.SubView, lang string) string {
	if v.Status != access.StatusActive || len(v.Lines) > 0 || !slices.ContainsFunc(v.Nodes, func(n access.SubNode) bool {
		return slices.ContainsFunc(n.Conns, func(c access.SubConn) bool { return c.MihomoOnly })
	}) {
		return ""
	}
	if lang == "ru" {
		return "Эти серверы работают только в kl!ck и других Mihomo-приложениях"
	}
	return "These servers work only in kl!ck and other Mihomo apps"
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
