package ownprobe

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/mistgate/mistgate/internal/panel/countryreach"
)

// answerOracle is the grammar the script must apply to an answer of the panel (ru-probes-plan §2.3 and §11.1), written again
// here from the plan and the panel's own validators, not from the script: the tests run both on the same bytes and compare.
func answerOracle(body []byte) bool {
	s := string(body)
	if len(s) == 0 || len(s) > 8192 || strings.ContainsAny(s, "\x00\r\t") || !strings.HasSuffix(s, "\n") {
		return false
	}
	lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
	if len(lines) < 7 {
		return false
	}
	f := func(i int, want ...string) []string {
		parts := strings.Split(lines[i], " ")
		if len(parts) != len(want) {
			return nil
		}
		for j, w := range want {
			if w != "" && parts[j] != w {
				return nil
			}
		}
		return parts
	}
	if lines[0] != "mgp 1" || lines[len(lines)-1] != "end" {
		return false
	}
	next := f(1, "next", "")
	state := f(2, "state", "")
	job := f(3, "job", "")
	ctl := f(4, "ctl", "", "", "")
	ref := f(5, "ref", "", "")
	if next == nil || state == nil || job == nil || ctl == nil || ref == nil {
		return false
	}
	if !uintDigits(next[1], 4) || (state[1] != "active" && state[1] != "paused") || !lowerHex(job[1], 16) {
		return false
	}
	if !countryreach.ProbeIPv4(ctl[1]) || !portOK(ctl[2]) || !sniOK(ctl[3]) || !countryreach.ProbeIPv4(ref[1]) || !portOK(ref[2]) {
		return false
	}
	count := 0
	for _, line := range lines[6 : len(lines)-1] {
		p := strings.Split(line, " ")
		if len(p) != 7 || p[0] != "t" || p[1] != strconv.Itoa(count+1) || count+1 > 32 || !uintDigits(p[1], 2) {
			return false
		}
		if !countryreach.ProbeIPv4(p[2]) || !portOK(p[3]) {
			return false
		}
		switch p[4] {
		case "tcp":
			if p[5] != "-" || p[6] != "-" {
				return false
			}
		case "tls", "fetch":
			if !sniOK(p[5]) || (p[6] != "-" && !countryreach.ProbePath(p[6])) {
				return false
			}
		default:
			return false
		}
		count++
	}
	zero := job[1] == noJobID
	if zero || state[1] == "paused" {
		return zero && count == 0
	}
	return count >= 1
}

func uintDigits(s string, max int) bool {
	if s == "" || len(s) > max || (len(s) > 1 && s[0] == '0') {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func portOK(s string) bool {
	if !uintDigits(s, 5) {
		return false
	}
	n, _ := strconv.Atoi(s)
	return n >= 1 && n <= 65535
}

// sniOK is the panel's host-name rule, and no name made only of digits and dots (an address in disguise).
func sniOK(s string) bool {
	if !countryreach.ProbeSNI(s) {
		return false
	}
	return strings.Trim(s, "0123456789.") != ""
}

// dirty is one hostile or broken answer.
type dirty struct {
	name string
	body []byte
}

var (
	badIPs = []string{"", "1.2.3", "1.2.3.4.5", "01.2.3.4", "256.1.1.1", "1.2.3.256", "1.2.3.04", "a.b.c.d", "1.2.3.4;id", "$(touch PWN)", "`touch PWN`",
		"10.0.0.1", "10.255.255.255", "172.16.0.1", "172.31.255.255", "192.168.0.1", "127.0.0.1", "127.255.255.254", "169.254.169.254", "100.64.0.1", "100.127.255.254",
		"0.0.0.0", "0.1.2.3", "224.0.0.1", "239.255.255.255", "255.255.255.255", "240.0.0.1", "192.0.0.5", "198.18.0.1", "198.19.255.254", "1.2.3.4/8",
		"1.2.3.4:80", "::1", "0x7f.0.0.1", "1.2.3.-4", "1.2.3.4\vx", "1..3.4", ".1.2.3", "1.2.3.", "-o", "--output=PWN"}
	goodIPs  = []string{"1.2.3.4", "172.15.255.255", "172.32.0.1", "100.63.255.255", "100.128.0.0", "198.17.255.255", "198.20.0.1", "223.255.255.255", "192.0.1.1", "169.253.255.255", "11.0.0.1", "126.255.255.255", "192.0.2.77", "203.0.113.5"}
	badPorts = []string{"", "0", "65536", "99999", "070", "-1", "1e3", "80;id", "+80", "123456", "0x50", "$(touch PWN)", "4 43"}
	badSNIs  = []string{"", "-", ".", "a..b", "-a.com", "a-.com", "a.com.", "a_b.com", "a b.com", "$(touch PWN).com", "a.com;touch PWN", "`touch PWN`", "1.2.3.4", "123",
		"a/b", "a:b", "--output=PWN", "-o", "x.com\nPWN", strings.Repeat("a", 64) + ".com", strings.Repeat("a.", 127) + "a", "a*.com", "a,b.com", "a@b.com", "ü.com"}
	badPaths = []string{"", "x", "/a b", "/a;touch PWN", "/$(touch PWN)", "/`touch PWN`", "/a?b", "/a#b", "/a%20b", "/" + strings.Repeat("a", 129), "/\\x", "/--output=PWN", "/a|b",
		"/a&b", "/a\"b", "/a'b", "/a>b", "/a<b", "/a*b", "/a[b]", "/ü", "-o"}
	badKinds  = []string{"", "TCP", "quic", "awg", "tcp;id", "ftp", "tls;touch PWN", "$(touch PWN)", "udp", "http"}
	badIdx    = []string{"0", "2", "01", "33", "-1", "x", "1;id", ""}
	badStates = []string{"", "ACTIVE", "running", "active;id", "$(touch PWN)"}
	badJobs   = []string{"", "abc", "00112233445566FF", "00112233445566ff0", "00112233445566f", "gggggggggggggggg", "0011223344556 6f", "$(touch PWN)00", "`touch PWN`0000000"}
	badNexts  = []string{"", "-1", "01", "1.5", "99999", "abc", "1e3", "9 9", "1;id", "0x10", "+5"}
)

// dirtyAnswers builds more than two hundred answers that are wrong in one way each, from the valid answer base. Every one of them
// must be refused as a whole; a few are only odd and the oracle says which.
func dirtyAnswers(base string, pwn string) []dirty {
	var out []dirty
	add := func(name string, body string) {
		out = append(out, dirty{name, []byte(strings.ReplaceAll(body, "PWN", pwn))})
	}
	lines := strings.Split(strings.TrimSuffix(base, "\n"), "\n")
	join := func(ls []string) string { return strings.Join(ls, "\n") + "\n" }
	with := func(i int, v string) string {
		ls := append([]string{}, lines...)
		ls[i] = v
		return join(ls)
	}
	// the answer as a whole
	add("empty", "")
	add("only a newline", "\n")
	add("no final newline", strings.TrimSuffix(base, "\n"))
	add("crlf", strings.ReplaceAll(base, "\n", "\r\n"))
	add("big", base+strings.Repeat("t 1 1.2.3.4 443 tcp - -\n", 400))
	add("nul in the middle", base[:20]+"\x00"+base[20:])
	add("nul as a line", strings.Replace(base, "next", "\x00next", 1))
	add("tab", strings.Replace(base, "state active", "state\tactive", 1))
	add("lone cr", strings.Replace(base, "state active", "state active\r", 1))
	for i := range lines {
		add(fmt.Sprintf("drop line %d", i), join(append(append([]string{}, lines[:i]...), lines[i+1:]...)))
		add(fmt.Sprintf("double line %d", i), join(append(append(append([]string{}, lines[:i+1]...), lines[i]), lines[i+1:]...)))
		add(fmt.Sprintf("leading space line %d", i), with(i, " "+lines[i]))
		add(fmt.Sprintf("trailing space line %d", i), with(i, lines[i]+" "))
		add(fmt.Sprintf("double space line %d", i), with(i, strings.Replace(lines[i], " ", "  ", 1)))
		add(fmt.Sprintf("upper case line %d", i), with(i, strings.ToUpper(lines[i])))
		add(fmt.Sprintf("blank line before %d", i), join(append(append(append([]string{}, lines[:i]...), ""), lines[i:]...)))
		add(fmt.Sprintf("unknown line before %d", i), join(append(append(append([]string{}, lines[:i]...), "upd https://panel.example.com/x.sh 0123456789ab"), lines[i:]...)))
		add(fmt.Sprintf("comment line before %d", i), join(append(append(append([]string{}, lines[:i]...), "# note"), lines[i:]...)))
		add(fmt.Sprintf("shell line before %d", i), join(append(append(append([]string{}, lines[:i]...), "$(touch PWN)"), lines[i:]...)))
		if i+1 < len(lines) {
			ls := append([]string{}, lines...)
			ls[i], ls[i+1] = ls[i+1], ls[i]
			add(fmt.Sprintf("swap %d %d", i, i+1), join(ls))
		}
	}
	for _, tail := range []string{"\n", "upd https://panel.example.com/x.sh 0123456789ab\n", "t 9 1.2.3.4 443 tcp - -\n", "end\n", " \n", "$(touch PWN)\n", "mgp 1\n"} {
		add("after end: "+strings.TrimSpace(tail), base+tail)
	}
	add("version 2", strings.Replace(base, "mgp 1", "mgp 2", 1))
	add("version 1.0", strings.Replace(base, "mgp 1", "mgp 1.0", 1))
	// one field wrong at a time
	field := func(name string, lineIdx int, fieldIdx int, values []string) {
		parts := strings.Split(lines[lineIdx], " ")
		for _, v := range values {
			p := append([]string{}, parts...)
			p[fieldIdx] = v
			add(fmt.Sprintf("%s=%q", name, v), with(lineIdx, strings.Join(p, " ")))
		}
	}
	field("next", 1, 1, badNexts)
	field("state", 2, 1, badStates)
	field("job", 3, 1, badJobs)
	field("ctl ip", 4, 1, badIPs)
	field("ctl port", 4, 2, badPorts)
	field("ctl sni", 4, 3, badSNIs)
	field("ref ip", 5, 1, badIPs)
	field("ref port", 5, 2, badPorts)
	first := true
	for i, line := range lines {
		p := strings.Split(line, " ")
		if p[0] != "t" {
			continue
		}
		if first {
			first = false
			field("t index", i, 1, badIdx)
			field("t ip", i, 2, badIPs)
			field("t port", i, 3, badPorts)
			field("t kind", i, 4, badKinds)
		}
		if p[4] != "tcp" {
			field("t sni", i, 5, badSNIs)
			field("t path", i, 6, badPaths)
		} else {
			field("tcp sni", i, 5, []string{"target.example.test", "$(touch PWN)"})
			field("tcp path", i, 6, []string{"/", "/a;touch PWN"})
		}
	}
	// too many targets, and targets that are a scan
	var many []string
	many = append(many, lines[:6]...)
	for i := 1; i <= 33; i++ {
		many = append(many, fmt.Sprintf("t %d 203.0.113.%d 443 tcp - -", i, i))
	}
	add("33 targets", join(append(many, "end")))
	add("32 targets", join(append(append([]string{}, many[:6+32]...), "end")))
	var hundred []string
	hundred = append(hundred, lines[:6]...)
	for i := 1; i <= 100; i++ {
		hundred = append(hundred, fmt.Sprintf("t %d 203.0.113.%d 443 tcp - -", i, i%250+1))
	}
	add("100 targets", join(append(hundred, "end")))
	// valid ones that are only odd: the script must accept exactly what the oracle accepts
	for _, ip := range goodIPs {
		add("good ip "+ip, with(6, "t 1 "+ip+" 443 tcp - -"))
	}
	add("path dash", with(7, "t 2 203.0.113.12 8443 tls target.example.test -"))
	add("path deep", with(7, "t 2 203.0.113.12 8443 tls target.example.test /a/b-c_d.e~f"))
	add("path option-like", with(7, "t 2 203.0.113.12 8443 tls target.example.test /-o"))
	add("sni option-like inside", with(7, "t 2 203.0.113.12 8443 tls a--output.example.test /"))
	add("paused with a job", strings.Replace(base, "state active", "state paused", 1))
	add("zero job with targets", strings.Replace(base, "job 00112233445566ff", "job 0000000000000000", 1))
	add("next 1", strings.Replace(base, "next 912", "next 1", 1))
	add("next 0", strings.Replace(base, "next 912", "next 0", 1))
	add("next 9999", strings.Replace(base, "next 912", "next 9999", 1))
	add("port 65535", with(6, "t 1 203.0.113.12 65535 tcp - -"))
	add("port 1", with(6, "t 1 203.0.113.12 1 tcp - -"))
	return out
}
