package sitegen

import (
	"strings"
	"unicode"
)

// hoursParts is the days and the hours of a line of opening hours (see hrs): the days from..to (1 is Monday), then the opening
// and the closing time as "08:00", or "" for a closed day. name is the function's, for the messages.
func (c *ctx) hoursParts(name string, a []any) (days, when string) {
	if len(a) != 4 {
		fail("%s takes the first day, the last day, the opening and the closing time", name)
	}
	from, ok1 := number(a[0])
	to, ok2 := number(a[1])
	if !ok1 || !ok2 || from < 1 || to > 7 || to < from {
		fail("%s: the days are %v to %v", name, a[0], a[1])
	}
	long, short := c.s.textList("weekdays"), c.s.textList("weekdays_short")
	days = nameAt(long, int(from)-1)
	if to > from {
		days = nameAt(short, int(from)-1) + "–" + nameAt(short, int(to)-1)
	}
	when = str(c.s.L["closed"])
	if open, shut := str(a[2]), str(a[3]); open != "" && shut != "" {
		cl := c.s.country
		when = cl.clock(open, str(c.s.L["am"]), str(c.s.L["pm"])) + "–" + cl.clock(shut, str(c.s.L["am"]), str(c.s.L["pm"]))
	}
	return days, when
}

// lastWord cuts a line into what comes before its last word (with the space), the last word, and the marks the line ends with
// (. ! ? … and a closing quote or bracket), which a template may keep with the word or colour on their own. A line of one word has
// an empty head, an empty line has three empty parts, and head + last + end is always the line without its outer white space.
func lastWord(s string) (head, last, end string) {
	r := []rune(strings.TrimSpace(s))
	n := len(r)
	for n > 0 && strings.ContainsRune(`.!?…»”’)"`, r[n-1]) {
		n--
	}
	w := n
	for w > 0 && !unicode.IsSpace(r[w-1]) {
		w--
	}
	if w == n { // nothing but marks, or nothing: no word to pick out
		return "", string(r), ""
	}
	return string(r[:w]), string(r[w:n]), string(r[n:])
}
