package ownprobe

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/countryreach"
)

// These tests run the embedded probe script under busybox ash and dash with a fake curl, and cover the nine points of
// ru-probes-plan §8 ("what we do not repeat") and the pid-file rule. The names say which point they are.

func TestScriptIsNotTheStubAndMatchesTheInstallCommand(t *testing.T) {
	if len(probeScript) < 8000 || strings.Contains(string(probeScript), "not implemented") || !strings.HasPrefix(string(probeScript), "#!/bin/sh\n") {
		t.Fatalf("the embedded script looks like a stub (%d bytes)", len(probeScript))
	}
	disk, err := os.ReadFile("mistgate-probe.sh")
	if err != nil || string(disk) != string(probeScript) {
		t.Fatalf("the embedded script is not the file on disk: %v", err)
	}
	command, err := BuildInstallCommand("https://panel.example.com", "/abcdefghijklmnopqrstuvwx/", "mgp_"+strings.Repeat("a", 52), ScriptHash())
	if err != nil || !strings.Contains(command, scriptSHA()+"  mgp.sh") || ScriptVersion() != scriptSHA()[:12] {
		t.Fatalf("the install command does not check the sha256 of this script: %v %s", err, command)
	}
}

// forbidden are constructs the plan bans (§8); comments are not looked at.
var forbidden = map[string]*regexp.Regexp{
	"eval":              regexp.MustCompile(`\beval\b`),
	"sourcing a file":   regexp.MustCompile(`(^|[;&|(]|\bthen|\bdo|\belse)\s*(\.|source)\s`),
	"pkill or killall":  regexp.MustCompile(`\b(pkill|killall)\b`),
	"curl piped to sh":  regexp.MustCompile(`\bcurl\b[^\n]*\|\s*(ba|da|a)?sh\b`),
	"following redirs":  regexp.MustCompile(`\bcurl\b[^\n]*\s-[A-Za-z]*L\b|--location`),
	"xargs":             regexp.MustCompile(`\bxargs\b`),
	"bash-only syntax":  regexp.MustCompile(`\[\[|\$RANDOM|<<<|(^|[\s;{(])local\s+[A-Za-z_]|\bfunction\b|\$'|\$\{[a-zA-Z_]+:[0-9]|&>`),
	"echo flags":        regexp.MustCompile(`\becho\s+-[ne]`),
	"fractional sleep":  regexp.MustCompile(`\bsleep\s+[0-9]*\.[0-9]`),
	"nanosecond date":   regexp.MustCompile(`date\s+\+%[a-zA-Z]*N`),
	"secret in a flag":  regexp.MustCompile(`Bearer[^\n]*(--header|-H)|(--header|-H)[^\n]*Bearer`),
	"an unchecked temp": regexp.MustCompile(`\bmktemp\b`),
}

var trailingComment = regexp.MustCompile(`\s#\s.*$`)

func TestScriptStaticChecks(t *testing.T) {
	text := string(probeScript)
	if len(probeScript) > 32<<10 {
		t.Errorf("the script is %d bytes; the limit is 32768", len(probeScript))
	}
	if strings.ContainsAny(text, "\r\x00") {
		t.Error("the script has CR or NUL")
	}
	var code []string
	for _, line := range strings.Split(text, "\n") {
		if s := strings.TrimSpace(line); s != "" && !strings.HasPrefix(s, "#") {
			code = append(code, trailingComment.ReplaceAllString(line, ""))
		}
	}
	body := strings.Join(code, "\n")
	for name, re := range forbidden {
		if loc := re.FindString(body); loc != "" {
			t.Errorf("forbidden construct (%s): %q", name, loc)
		}
	}
	for _, want := range []string{"set -u\n", "umask 077\n", "set -f\n", "--proto '=https'", "--noproxy '*'", "curl -q ", "PATH=/opt/bin:/opt/sbin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"} {
		if !strings.Contains(body, want) {
			t.Errorf("the script lacks %q", want)
		}
	}
	// every curl command is the one wrapper or the version probe: nothing builds a curl command line anywhere else
	if got := regexp.MustCompile(`\bcurl\s+-`).FindAllString(body, -1); len(got) != 2 {
		t.Errorf("curl is run from %d places, want 2 (net_curl and curl --version): %q", len(got), got)
	}
	if !strings.Contains(body, "net_curl() { curl -q -s --proto '=https' --noproxy '*' -4 --http1.1 \"$@\"; }") {
		t.Error("the curl wrapper is not the expected one")
	}
	// the secret is read from a file by curl (-K) and never put in an argument
	for _, line := range code {
		if strings.Contains(line, "${SEC}") && !strings.Contains(line, "SEC=") && !strings.Contains(line, `printf 'header = "Authorization: Bearer %s"\n' "${SEC}" >"${S}/auth.cfg"`) && !strings.Contains(line, `-n "${SEC}"`) {
			t.Errorf("the secret is used where it should not be: %s", line)
		}
	}
	for _, sh := range scriptShells(t) {
		args := append(append([]string{}, sh.argv[1:]...), "-n", "mistgate-probe.sh")
		if out, err := combinedOutput(exec.Command(sh.argv[0], args...)); err != nil {
			t.Errorf("%s -n: %v\n%s", sh.name, err, out)
		}
	}
	if p, err := exec.LookPath("shellcheck"); err == nil {
		if out, err := combinedOutput(exec.Command(p, "-s", "sh", "-o", "all", "mistgate-probe.sh")); err != nil {
			t.Errorf("shellcheck -s sh -o all:\n%s", out)
		}
	} else {
		t.Log("shellcheck is not installed here; CI runs it")
	}
}

// argvProblem checks one curl call of the script, argument by argument, against what the script is allowed to give curl.
func (l *lab) argvProblem(c fakeCall) string {
	if len(c.Args) == 1 && c.Args[0] == "--version" {
		return ""
	}
	a := c.Args
	if len(a) == 0 || a[0] != "-q" {
		return "the first argument is not -q"
	}
	digits := regexp.MustCompile(`^[0-9]{1,6}$`)
	formats := map[string]bool{
		"%{time_connect} %{time_appconnect} %{size_download} %{time_total} %{exitcode} %{errormsg}": true,
		"%{time_connect} %{time_appconnect} %{size_download} %{time_total}":                         true,
		"%{http_code}": true,
	}
	var url string
	sawProto, sawNoProxy, maxTime, connectTime := false, false, 0, 0
	for i := 0; i < len(a); i++ {
		arg := a[i]
		next := func() string {
			i++
			if i >= len(a) {
				return "\x00missing"
			}
			return a[i]
		}
		switch arg {
		case "-q", "-s", "-4", "-k", "--http1.1":
		case "--proto":
			sawProto = next() == "=https"
			if !sawProto {
				return "--proto is not =https"
			}
		case "--noproxy":
			sawNoProxy = next() == "*"
		case "--connect-timeout", "-m", "--max-filesize", "--speed-limit", "--speed-time":
			v := next()
			if !digits.MatchString(v) {
				return arg + " " + v
			}
			n, _ := strconv.Atoi(v)
			switch arg {
			case "-m":
				maxTime = n
			case "--connect-timeout":
				connectTime = n
			}
		case "--limit-rate":
			if v := next(); !regexp.MustCompile(`^[0-9]{1,4}k$`).MatchString(v) {
				return arg + " " + v
			}
		case "-w":
			if v := next(); !formats[v] {
				return "-w " + v
			}
		case "-o":
			if v := next(); v != "/dev/null" && v != filepath.Join(l.state, "resp") {
				return "-o " + v
			}
		case "-K":
			if v := next(); v != filepath.Join(l.state, "auth.cfg") {
				return "-K " + v
			}
		case "-X":
			if v := next(); v != "POST" {
				return "-X " + v
			}
		case "-H":
			if v := next(); v != "Content-Type: text/plain" {
				return "-H " + v
			}
		case "--data-binary":
			if v := next(); v != "@"+filepath.Join(l.state, "out") && v != "@-" {
				return "--data-binary " + v
			}
		case "-D":
			if v := next(); v != filepath.Join(l.state, "hdr") {
				return "-D " + v
			}
		case "--resolve":
			v := next()
			p := strings.Split(v, ":")
			if len(p) != 3 || !sniOK(p[0]) || !portOK(p[1]) || !countryreach.ProbeIPv4(p[2]) {
				return "--resolve " + v
			}
		default:
			if !strings.HasPrefix(arg, "https://") || url != "" {
				return "unexpected argument " + strconv.Quote(arg)
			}
			url = arg
			if !regexp.MustCompile(`^https://[A-Za-z0-9.:-]+/[A-Za-z0-9._~/-]*$`).MatchString(arg) {
				return "bad URL " + strconv.Quote(arg)
			}
		}
	}
	if !sawProto || !sawNoProxy || url == "" {
		return "no --proto =https, --noproxy * or URL"
	}
	// no curl of the script can hang: every call has a hard limit on the whole transfer and one on the connection
	if maxTime < 1 || maxTime > 30 || connectTime < 1 || connectTime > 10 {
		return fmt.Sprintf("no hard time limit: -m %d --connect-timeout %d", maxTime, connectTime)
	}
	return ""
}

// assertArgv fails when any recorded curl call has anything the script may not pass, or has the secret in it.
func (l *lab) assertArgv() {
	l.t.Helper()
	for _, c := range l.calls() {
		if p := l.argvProblem(c); p != "" {
			l.t.Errorf("curl call %d: %s\n%q", c.N, p, c.Args)
		}
		if strings.Contains(strings.Join(c.Args, "\n"), labSecret[5:20]) || strings.Contains(strings.Join(c.Args, "\n"), "Bearer") {
			l.t.Errorf("curl call %d has the secret on its command line: %q", c.N, c.Args)
		}
	}
}

func (l *lab) exists(rel string) bool {
	_, err := os.Lstat(filepath.Join(l.root, rel))
	return err == nil
}

func (l *lab) forgetJob() {
	for _, f := range []string{"job", "last_job", "last_exec", "mute", "n404", "fails", "via", "report"} {
		_ = os.Remove(filepath.Join(l.state, f))
	}
}

// §8 point 1: the self-update of the other agent. Here a line "upd ..." is an unknown line: the whole answer is refused, and the
// script file is the same afterwards.
func TestNoSelfUpdate(t *testing.T) {
	t.Parallel()
	forShells(t, func(t *testing.T, sh shellSpec) {
		t.Parallel()
		l := newLab(t, sh, labOptions{})
		before := tree(t, l.root, l.fake, l.state)
		base := goodAnswer("t 1 203.0.113.12 443 tcp - -")
		upd := "upd https://panel.example.com/abcdefghijklmnopqrstuvwx/probe.sh 0123456789ab\n"
		for name, body := range map[string]string{
			"before end": strings.Replace(base, "end\n", upd+"end\n", 1), "after end": base + upd, "instead of end": strings.Replace(base, "end\n", upd, 1),
			"first": upd + base, "alone": "mgp 1\nnext 900\nstate active\njob 0000000000000000\nctl 192.0.2.1 443 control.example.test\nref 198.51.100.1 443\n" + upd + "end\n",
			"with a url": strings.Replace(base, "end\n", "upd https://evil.example.com/x.sh\nend\n", 1),
		} {
			l.forget()
			l.forgetJob()
			l.setAnswer(0, body, 200, "")
			out, code := l.run("run")
			if code != 0 || l.stateHas("job") || len(l.checks()) != 0 || len(l.calls()) > 2 {
				t.Errorf("%s: the answer was not refused whole (exit %d, %d checks): %s", name, code, len(l.checks()), out)
			}
			if got, _ := os.ReadFile(l.script); string(got) != string(probeScript) {
				t.Fatalf("%s: the script file changed", name)
			}
		}
		if d := diffTree(before, tree(t, l.root, l.fake, l.state)); len(d) != 0 {
			t.Errorf("files changed: %v", d)
		}
		l.assertArgv()
	})
}

// §8 point 2: no curl | sh as root without a check. The command downloads to a file, checks the sha256, and only then runs it; the
// script itself refuses to install itself when it does not parse.
func TestTamperedScriptStopsTheInstall(t *testing.T) {
	t.Parallel()
	forShells(t, func(t *testing.T, sh shellSpec) {
		t.Parallel()
		token := "mgp_" + strings.Repeat("a", 52)
		command, err := BuildInstallCommand("https://panel.example.com", "/abcdefghijklmnopqrstuvwx/", token, ScriptHash())
		must(t, err)
		serve := func(l *lab, script []byte) (string, int) {
			must(t, os.WriteFile(filepath.Join(l.fake, "probe.sh"), script, 0o644))
			must(t, os.WriteFile(filepath.Join(l.fake, "enroll.resp"), []byte("mgp 1\nid "+labID+"\nsecret "+labSecret+"\nend\n"), 0o644))
			dl := filepath.Join(l.root, "download")
			must(t, os.MkdirAll(dl, 0o755))
			return l.shellScript(strings.Replace(command, "cd /tmp", "cd "+dl, 1))
		}
		// the real script: the line works (this is also the end to end install of the entware layout)
		good := newLab(t, sh, labOptions{noConf: true, notInstalled: true, realClock: true})
		t.Cleanup(func() { pids := good.probePids(); good.run("uninstall"); waitGone(pids) })
		if out, code := serve(good, probeScript); code != 0 || !strings.Contains(out, "OK: probe "+labID+" registered and running") {
			t.Fatalf("the untouched script did not install (exit %d): %s", code, out)
		}
		// a script with one byte changed: sha256sum -c stops the line before anything runs
		for name, bad := range map[string][]byte{
			"appended": append(append([]byte{}, probeScript...), []byte("\ntouch pwned\n")...),
			"edited":   []byte(strings.Replace(string(probeScript), "upd", "udp", 1)),
		} {
			l := newLab(t, sh, labOptions{noConf: true, notInstalled: true})
			out, code := serve(l, bad)
			if code == 0 || !strings.Contains(out, "FAILED") && !strings.Contains(out, "did NOT match") {
				t.Errorf("%s: the line did not stop (exit %d): %s", name, code, out)
			}
			if l.exists("opt/sbin/mistgate-probe") || l.exists("opt/etc/mistgate-probe") || l.exists("opt/etc/init.d/S99mistgate-probe") || l.exists("download/pwned") || l.exists("pwned") {
				t.Errorf("%s: something was installed or run: %v", name, tree(t, l.root, l.fake, filepath.Join(l.root, "bin")))
			}
			if _, err := os.Stat(filepath.Join(l.fake, "enroll.token")); err == nil {
				t.Errorf("%s: the script reached the panel", name)
			}
		}
		// and a script that does not parse, run by hand: install refuses before it changes anything
		broken := newLab(t, sh, labOptions{noConf: true, notInstalled: true})
		must(t, writeExe(filepath.Join(broken.root, "mgp.sh"), append(append([]byte{}, probeScript...), []byte("\nfi\n")...)))
		out, code := broken.runAt(filepath.Join(broken.root, "mgp.sh"), "install", labBase, token)
		if code == 0 || broken.exists("opt/sbin/mistgate-probe") || broken.exists("opt/etc/mistgate-probe") {
			t.Errorf("a script that does not parse was installed (exit %d): %s", code, out)
		}
	})
}

// §8 point 3: the fields of the job on the curl command line. Every dirty answer must be refused whole, the trap file must never
// appear, and every curl call must be one the script is allowed to make (checked argument by argument).
func TestJobFieldsNeverReachTheShell(t *testing.T) {
	t.Parallel()
	forShells(t, func(t *testing.T, sh shellSpec) {
		t.Parallel()
		l := newLab(t, sh, labOptions{})
		pwn := filepath.Join(l.root, "pwned")
		mutants := dirtyAnswers(goodAnswer("t 1 203.0.113.12 443 tcp - -", "t 2 203.0.113.12 8443 tls target.example.test /", "t 3 203.0.113.21 443 fetch target.example.test /"), pwn)
		if len(mutants) < 200 {
			t.Fatalf("only %d dirty answers", len(mutants))
		}
		accepted, refused := 0, 0
		for _, m := range mutants {
			l.forget()
			l.forgetJob()
			l.setAnswer(0, string(m.body), 200, "")
			out, code := l.run("run")
			if code != 0 {
				t.Fatalf("%s: exit %d: %s", m.name, code, out)
			}
			if l.exists("pwned") {
				t.Fatalf("%s: the trap file appeared", m.name)
			}
			want := answerOracle(m.body)
			got := l.stateHas("job")
			if got != want {
				t.Errorf("%s: the script %s the answer, the grammar says %s:\n%q", m.name, verdict(got), verdict(want), m.body)
			}
			if !got && len(l.checks()) != 0 {
				t.Errorf("%s: a refused answer started checks", m.name)
			}
			if got {
				accepted++
			} else {
				refused++
			}
			l.assertArgv()
			l.setClock(l.clock() + 2000)
		}
		t.Logf("%d dirty answers: %d refused, %d accepted (valid but odd)", len(mutants), refused, accepted)
		if refused < 150 || accepted < 10 {
			t.Errorf("the set is lopsided: %d refused, %d accepted", refused, accepted)
		}
	})
}

func verdict(b bool) string {
	if b {
		return "accepts"
	}
	return "refuses"
}

// FuzzScriptAnswer lets the Go fuzzer propose answers; the script and the grammar must agree on each, and no trap may appear.
func FuzzScriptAnswer(f *testing.F) {
	base := goodAnswer("t 1 203.0.113.12 443 tcp - -", "t 2 203.0.113.12 8443 tls target.example.test /")
	for _, m := range dirtyAnswers(base, "/nonexistent/pwned")[:40] {
		f.Add(m.body)
	}
	f.Add([]byte(base))
	shells := scriptShells(f)
	l := newLab(f, shells[len(shells)-1], labOptions{root: f.TempDir()}) // one lab for all inputs
	f.Fuzz(func(t *testing.T, body []byte) {
		l.t = t
		l.forget()
		l.forgetJob()
		l.setAnswer(0, string(body), 200, "")
		if _, code := l.run("run"); code != 0 {
			t.Fatalf("exit %d", code)
		}
		if got, want := l.stateHas("job"), answerOracle(body); got != want {
			t.Fatalf("the script %s, the grammar says %s: %q", verdict(got), verdict(want), body)
		}
		l.assertArgv()
		l.setClock(l.clock() + 2000)
	})
}

// §8 point 4: the configuration is text. A value that is a command is never run, and the probe refuses to work.
func TestConfIsReadNotRun(t *testing.T) {
	t.Parallel()
	forShells(t, func(t *testing.T, sh shellSpec) {
		t.Parallel()
		l := newLab(t, sh, labOptions{})
		pwn := filepath.Join(l.root, "pwned")
		ok := "url=" + labBase + "\nfallback=\nid=" + labID + "\nsecret=" + labSecret + "\n"
		bad := map[string]string{
			"command substitution":  strings.Replace(ok, "secret="+labSecret, "secret=$(touch "+pwn+")", 1),
			"backticks":             strings.Replace(ok, "secret="+labSecret, "secret=`touch "+pwn+"`", 1),
			"semicolon":             strings.Replace(ok, labBase, labBase+";touch "+pwn, 1),
			"id with a command":     strings.Replace(ok, labID, "$(touch "+pwn+")", 1),
			"unknown key":           ok + "debug=1\n",
			"export line":           ok + "export X=1\n",
			"source line":           ok + ". " + pwn + "\n",
			"short secret":          strings.Replace(ok, labSecret, labSecret[:20], 1),
			"url without prefix":    strings.Replace(ok, labBase, "https://panel.example.com/short", 1),
			"http url":              strings.Replace(ok, "https://", "http://", 1),
			"url with credentials":  strings.Replace(ok, "https://", "https://u:p@", 1),
			"no secret":             "url=" + labBase + "\nid=" + labID + "\n",
			"ref with a value":      ok + "ref=maybe\n",
			"cr in a line":          strings.Replace(ok, "fallback=\n", "fallback=\r\n", 1),
			"huge":                  ok + strings.Repeat("# padding\n", 200),
			"fallback with a quote": strings.Replace(ok, "fallback=", "fallback=https://f.example.com/'x", 1),
		}
		for name, text := range bad {
			l.forget()
			l.writeConf(text)
			out, code := l.run("run")
			if code == 0 || !strings.Contains(out, "refusing") || l.exists("pwned") || len(l.calls()) > 1 {
				t.Errorf("%s: exit %d, %d calls: %s", name, code, len(l.calls()), out)
			}
		}
		// a conf that is a link is not read; a good conf with comments and ref=off is
		l.forget()
		must(t, os.Remove(l.conf))
		must(t, os.WriteFile(filepath.Join(l.root, "other-conf"), []byte(ok), 0o600))
		must(t, os.Symlink(filepath.Join(l.root, "other-conf"), l.conf))
		if out, code := l.run("run"); code == 0 || !strings.Contains(out, "refusing") {
			t.Errorf("a linked conf was read: %d %s", code, out)
		}
		must(t, os.Remove(l.conf))
		l.writeConf("# a comment\n\n" + ok + "ref=off\n")
		l.forget()
		l.forgetJob()
		if out, code := l.run("run"); code != 0 || !strings.Contains(out, "http 200") {
			t.Errorf("a good conf was refused (%d): %s", code, out)
		}
		if bodies := l.syncBodies(); len(bodies) < 2 || !strings.Contains(bodies[len(bodies)-1], "\nref fail\n") {
			t.Errorf("ref=off still measured the reference: %q", bodies)
		}
		for _, c := range l.checks() {
			if strings.Contains(c.url(), "198.51.100.1") {
				t.Errorf("ref=off connected to the reference address")
			}
		}
		l.assertArgv()
	})
}

// §8 point 5: not trusted with more than it needs. Files are private, the PATH is fixed, nothing is written outside the state
// directory, and a hostile PATH in the environment does not reach the script's commands.
func TestPrivilegeHygiene(t *testing.T) {
	t.Parallel()
	forShells(t, func(t *testing.T, sh shellSpec) {
		t.Parallel()
		l := newLab(t, sh, labOptions{})
		before := tree(t, l.root, l.fake, l.state)
		if out, code := l.run("run"); code != 0 {
			t.Fatalf("run: %d %s", code, out)
		}
		must(t, filepath.Walk(l.state, func(p string, info os.FileInfo, err error) error {
			if err == nil && info.Mode().Perm()&0o077 != 0 {
				t.Errorf("%s has mode %v: the state is private", p, info.Mode())
			}
			return nil
		}))
		for _, f := range []string{"auth.cfg", "out", "hdr", "resp.tmp"} {
			if l.stateHas(f) && f == "auth.cfg" {
				t.Errorf("the curl config with the secret stayed in the state directory")
			}
		}
		if d := diffTree(before, tree(t, l.root, l.fake, l.state)); len(d) != 0 {
			t.Errorf("the script wrote outside its state directory: %v", d)
		}
		// the script's own PATH is fixed: a program of the same name earlier in the caller's PATH is not used. No MGP_ROOT here,
		// so this is the production PATH; status only reads.
		evil := t.TempDir()
		trap := filepath.Join(evil, "pwned")
		for _, tool := range []string{"sha256sum", "curl", "id", "tr", "wc", "date", "logger", "systemctl", "sh"} {
			must(t, writeExe(filepath.Join(evil, tool), []byte("#!/bin/sh\ntouch "+trap+"\nexit 1\n")))
		}
		argv := append(append([]string{}, sh.argv[1:]...), l.script, "status")
		c := exec.Command(sh.argv[0], argv...)
		c.Env = []string{"PATH=" + evil + ":" + os.Getenv("PATH"), "HOME=" + evil, "FAKE_DIR=" + l.fake}
		l.own(c)
		out, _ := combinedOutput(c)
		if _, err := os.Stat(trap); err == nil {
			t.Errorf("a program from the caller's PATH was run: %s", out)
		}
		// set -u: a typo would be an error, not an empty string
		if !strings.Contains(string(probeScript), "\nset -u\n") {
			t.Error("no set -u")
		}
	})
}

// §8 point 7: not a proxy and not a scanner. The limits are in the script and no answer loosens them.
func TestLimitsHold(t *testing.T) {
	t.Parallel()
	forShells(t, func(t *testing.T, sh shellSpec) {
		t.Parallel()
		l := newLab(t, sh, labOptions{})
		target := func(i int) string { return fmt.Sprintf("t %d 203.0.113.%d 443 tcp - -", i, i) }
		head := "mgp 1\nnext 912\nstate active\njob 00112233445566ff\nctl 192.0.2.1 443 control.example.test\nref 198.51.100.1 443\n"
		// 100 targets, 33 targets: refused as a whole
		for _, n := range []int{33, 100} {
			var b strings.Builder
			b.WriteString(head)
			for i := 1; i <= n; i++ {
				b.WriteString(target(i%200 + 1)[:0] + fmt.Sprintf("t %d 203.0.113.%d 443 tcp - -\n", i, i%200+1))
			}
			b.WriteString("end\n")
			l.forget()
			l.forgetJob()
			l.setAnswer(0, b.String(), 200, "")
			l.run("run")
			if l.stateHas("job") || len(l.checks()) != 0 {
				t.Errorf("%d targets were accepted", n)
			}
		}
		// private, reserved and malformed addresses: each refused whole, never connected to
		for _, ip := range badIPs[:] {
			if strings.ContainsAny(ip, "$`;\v") || ip == "" {
				continue
			}
			l.forget()
			l.forgetJob()
			l.setAnswer(0, strings.Replace(goodAnswer("t 1 203.0.113.12 443 tcp - -"), "203.0.113.12", ip, 1), 200, "")
			l.run("run")
			if l.stateHas("job") || len(l.checks()) != 0 {
				t.Errorf("address %q was accepted", ip)
			}
		}
		// next is clamped into [600, 3600]; an answer asking for less never makes the probe poll faster
		for next, want := range map[string]string{"1": "600", "0": "600", "599": "600", "600": "600", "912": "912", "3600": "3600", "3601": "3600", "9999": "3600"} {
			l.forget()
			l.forgetJob()
			l.setAnswer(0, strings.Replace(goodAnswer("t 1 203.0.113.12 443 tcp - -"), "next 912", "next "+next, 1), 200, "")
			if out, _ := l.run("run"); !strings.Contains(out, "next "+want+"\n") {
				t.Errorf("next %s: %s", next, out)
			}
		}
		// 32 valid targets run, two at a time, never faster than 20 connections a minute
		l.forget()
		l.forgetJob()
		var many []string
		for i := 1; i <= 32; i++ {
			many = append(many, fmt.Sprintf("t %d 203.0.113.%d 443 tcp - -", i, i))
		}
		l.setAnswer(0, goodAnswer(many...), 200, "")
		l.env = append(l.env, "FAKE_DELAY=0.03")
		start := l.clock()
		l.run("run")
		l.env = l.env[:len(l.env)-1]
		if got := len(l.checks()); got != 34 {
			t.Errorf("a job of 32 targets made %d connections to targets, want 34 (32, the control and the reference)", got)
		}
		if peak := peakConcurrency(l); peak != 2 {
			t.Errorf("%d curl calls at the same time, want 2", peak)
		}
		if elapsed := l.clock() - start; elapsed < 96 {
			t.Errorf("34 connections took %d s of the clock: that is more than 20 a minute", elapsed)
		}
		assertRate(t, l.checks(), 20, 60)
		// a day of cycles: at most 600 connections and 64 different addresses in any 24 hours
		l2 := newLab(t, sh, labOptions{})
		for cycle := 0; cycle < 60; cycle++ {
			var targets []string
			for i := 1; i <= 32; i++ {
				targets = append(targets, fmt.Sprintf("t %d 203.0.113.%d 443 tcp - -", i, (cycle*7+i)%250+1))
			}
			l2.setAnswer(0, strings.Replace(goodAnswer(targets...), "00112233445566ff", fmt.Sprintf("%016x", cycle+1), 1), 200, "")
			l2.run("run")
			l2.setClock(l2.clock() + 1800)
		}
		checks := l2.checks()
		assertRate(t, checks, 600, 86400)
		assertDistinct(t, checks, 64, 86400)
		l2.assertArgv()
		// a cycle is no more often than every 30 minutes, whatever the panel hands out
		l3 := newLab(t, sh, labOptions{})
		for i := 0; i < 5; i++ {
			l3.setAnswer(0, strings.Replace(goodAnswer("t 1 203.0.113.12 443 tcp - -"), "00112233445566ff", fmt.Sprintf("%016x", i+1), 1), 200, "")
			l3.run("run")
			l3.setClock(l3.clock() + 300)
		}
		if got := len(l3.checks()); got != 3 {
			t.Errorf("jobs 5 minutes apart ran %d times (%d connections): once is 3 connections", got/3, got)
		}
		l.assertArgv()
	})
}

func peakConcurrency(l *lab) int {
	raw, _ := os.ReadFile(filepath.Join(l.fake, "events"))
	cur, peak := 0, 0
	for _, line := range strings.Split(string(raw), "\n") {
		switch {
		case strings.HasPrefix(line, "S "):
			cur++
			peak = max(peak, cur)
		case strings.HasPrefix(line, "E "):
			cur--
		}
	}
	return peak
}

func assertRate(t *testing.T, calls []fakeCall, limit int, window int64) {
	t.Helper()
	for i := range calls {
		n := 0
		for j := i; j < len(calls) && calls[j].At-calls[i].At < window; j++ {
			n++
		}
		if n > limit {
			t.Fatalf("%d connections within %d s from call %d, the limit is %d", n, window, calls[i].N, limit)
		}
	}
}

func assertDistinct(t *testing.T, calls []fakeCall, limit int, window int64) {
	t.Helper()
	ip := func(c fakeCall) string {
		for i, a := range c.Args {
			if a == "--resolve" && i+1 < len(c.Args) {
				p := strings.Split(c.Args[i+1], ":")
				return p[len(p)-1]
			}
		}
		return strings.Split(strings.TrimPrefix(c.url(), "https://"), ":")[0]
	}
	for i := range calls {
		seen := map[string]bool{}
		for j := i; j < len(calls) && calls[j].At-calls[i].At < window; j++ {
			seen[ip(calls[j])] = true
		}
		if len(seen) > limit {
			t.Fatalf("%d different addresses within %d s from call %d, the limit is %d", len(seen), window, calls[i].N, limit)
		}
	}
}

// §8 point 8: no log files on the flash. Many cycles later nothing outside the state directory changed, the state is small, and
// there is no file that looks like a log anywhere.
func TestManyCyclesLeaveNothingBehind(t *testing.T) {
	t.Parallel()
	forShells(t, func(t *testing.T, sh shellSpec) {
		t.Parallel()
		cycles := 1000
		if testing.Short() {
			cycles = 100
		}
		l := newLab(t, sh, labOptions{})
		before := tree(t, l.root, l.fake, l.state)
		program := `i=0
while [ "$i" -lt "$1" ]; do
	i=$((i + 1))
	printf 'mgp 1\nnext 912\nstate active\njob %016x\nctl 192.0.2.1 443 control.example.test\nref 198.51.100.1 443\nt 1 203.0.113.12 443 tcp - -\nt 2 203.0.113.12 8443 tls target.example.test /\nt 3 203.0.113.21 443 fetch target.example.test /\nend\n' "$i" >"$FAKE_DIR/ans.default"
	$LAB_SH "$2" run >/dev/null || exit 1
	read -r t <"$FAKE_DIR/clock"
	echo $((t + 2000)) >"$FAKE_DIR/clock"
	rm -rf "$FAKE_DIR/calls" "$FAKE_DIR"/body.* "$FAKE_DIR"/auth.*
done`
		if out, code := l.shellScript(program, strconv.Itoa(cycles), l.script); code != 0 {
			t.Fatalf("the cycles failed: %d %s", code, out)
		}
		if d := diffTree(before, tree(t, l.root, l.fake, l.state)); len(d) != 0 {
			t.Errorf("after %d cycles files outside the state directory changed: %v", cycles, d)
		}
		var total int64
		files := 0
		must(t, filepath.Walk(l.state, func(p string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() {
				total += info.Size()
				files++
			}
			return nil
		}))
		if total > 64<<10 || files > 20 {
			t.Errorf("the state grew to %d files and %d bytes", files, total)
		}
		if n := strings.Count(l.stateFile("budget"), "\n"); n > 600 {
			t.Errorf("the connection ledger holds %d lines", n)
		}
		raw, _ := os.ReadFile(filepath.Join(l.fake, "logger.log"))
		if len(raw) == 0 {
			t.Log("nothing was logged")
		}
		for p := range tree(t, l.root, l.fake, filepath.Join(l.root, "bin")) {
			if strings.Contains(strings.ToLower(p), "log") && p != "" {
				t.Errorf("a log-like file: %s", p)
			}
		}
		if strings.Contains(string(raw), labSecret[5:20]) {
			t.Errorf("the secret is in the log")
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if len(line) > 80 {
				t.Errorf("a long log line: %q", line)
			}
		}
	})
}

// §8 point 9: what the provider sees and what stays on the router. The report has no address in it; the probe connects to the
// panel and to the hosts of the job and to nothing else; the state is the job and counters.
func TestWhatTheProviderSeesAndWhatStays(t *testing.T) {
	t.Parallel()
	forShells(t, func(t *testing.T, sh shellSpec) {
		t.Parallel()
		l := newLab(t, sh, labOptions{})
		if _, code := l.run("run"); code != 0 {
			t.Fatal("run failed")
		}
		hosts := map[string]bool{"panel.example.com": true, "control.example.test": true, "target.example.test": true, "198.51.100.1": true, "203.0.113.12": true, "203.0.113.21": true}
		for _, c := range l.calls() {
			if c.url() == "" {
				continue
			}
			host := strings.Split(strings.TrimPrefix(c.url(), "https://"), "/")[0]
			if !hosts[strings.Split(host, ":")[0]] {
				t.Errorf("connection to %s, which is not in the job or the panel's address", host)
			}
		}
		bodies := l.syncBodies()
		last := bodies[len(bodies)-1]
		for _, secret := range []string{"203.0.113", "198.51.100", "192.0.2", "example.test", "example.com", labID, labSecret} {
			if strings.Contains(last, secret) {
				t.Errorf("the report carries %q", secret)
			}
		}
		if _, err := ParseSync([]byte(last)); err != nil {
			t.Errorf("the report is not a report: %v", err)
		}
		// on the router: the script, the configuration, and in the state directory only these names
		allowed := map[string]bool{"budget": true, "job": true, "last_exec": true, "last_job": true, "synced": true, "res": true, "resp": true}
		entries, _ := os.ReadDir(l.state)
		for _, e := range entries {
			if !allowed[e.Name()] {
				t.Errorf("unexpected state file %s", e.Name())
			}
		}
		if strings.Contains(l.stateFile("job"), "prb_") || strings.Contains(l.stateFile("job"), "mgps_") {
			t.Error("the job file holds an identity")
		}
		if l.stateHas("auth.cfg") {
			t.Error("the secret stayed in a file in the state directory")
		}
	})
}

// the pid-file rule: stop kills what the pid file names only when /proc/<pid>/cmdline says it is ours.
func TestStopTouchesOnlyItsOwnProcesses(t *testing.T) {
	t.Parallel()
	forShells(t, func(t *testing.T, sh shellSpec) {
		t.Parallel()
		l := newLab(t, sh, labOptions{realClock: true})
		must(t, os.MkdirAll(l.state, 0o700))
		foreign := exec.Command("sleep", "300")
		must(t, startCmd(foreign))
		t.Cleanup(func() { _ = foreign.Process.Kill(); _ = foreign.Wait() })
		for _, name := range []string{"supervisor.pid", "worker.pid"} {
			must(t, os.WriteFile(filepath.Join(l.state, name), []byte(strconv.Itoa(foreign.Process.Pid)+"\n"), 0o600))
		}
		if out, code := l.run("stop"); code != 0 {
			t.Fatalf("stop: %d %s", code, out)
		}
		time.Sleep(200 * time.Millisecond)
		if !procAlive(foreign.Process.Pid) {
			t.Fatal("stop killed a process that is not the probe's")
		}
		if l.stateHas("supervisor.pid") || l.stateHas("worker.pid") {
			t.Error("a stale pid file was left")
		}
		// a pid file with garbage, an empty one and one with a number that is no process do not hurt either
		for _, garbage := range []string{"", "abc\n", "1; kill -9 $$\n", "99999999\n", "-1\n", "0\n"} {
			must(t, os.WriteFile(filepath.Join(l.state, "supervisor.pid"), []byte(garbage), 0o600))
			if out, code := l.run("stop"); code != 0 {
				t.Errorf("stop with pid file %q: %d %s", garbage, code, out)
			}
		}
		if !procAlive(foreign.Process.Pid) {
			t.Fatal("garbage pid files made stop kill something")
		}
	})
}

func procAlive(pid int) bool {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	s := string(raw)
	i := strings.LastIndex(s, ")")
	return i >= 0 && i+2 < len(s) && s[i+2] != 'Z'
}

func waitFor(t *testing.T, what string, within time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (l *lab) pidOf(name string) int {
	n, _ := strconv.Atoi(strings.TrimSpace(l.stateFile(name)))
	return n
}

// stopAndWait is "stop" that returns when the supervisor and the worker it names are gone. The script's stop only signals them and
// removes the pid files; a worker in the middle of a cycle ends, and drops its lock, only when the command it runs returns (under
// load, much later). A test that takes a snapshot of the tree, or runs a cycle itself, right after stop must wait for the pids.
func (l *lab) stopAndWait() (string, int) {
	l.t.Helper()
	pids := l.probePids()
	out, code := l.run("stop")
	waitGone(pids)
	return out, code
}

// probePids are the supervisor and the worker the pid files name now (read them before a stop removes the files).
func (l *lab) probePids() []int {
	return []int{l.pidOf("supervisor.pid"), l.pidOf("worker.pid")}
}

// waitGone waits (by pid, not by a sleep of a guessed length) until none of the processes lives; false when one still does.
func waitGone(pids []int) bool {
	deadline := time.Now().Add(30 * time.Second)
	for _, p := range pids {
		for p > 0 && procAlive(p) {
			if time.Now().After(deadline) {
				return false
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	return true
}

// the supervisor and its worker really run, answer the fake panel, and stop on stop; the install, upgrade and uninstall of the
// entware layout and the systemd unit are written as they must be.
func TestInstallRunUpgradeUninstall(t *testing.T) {
	t.Parallel()
	forShells(t, func(t *testing.T, sh shellSpec) {
		t.Parallel()
		l := newLab(t, sh, labOptions{noConf: true, notInstalled: true, realClock: true})
		t.Cleanup(func() { l.stopAndWait() })
		token := "mgp_" + strings.Repeat("b", 52)
		down := filepath.Join(l.root, "mgp.sh")
		must(t, writeExe(down, probeScript))
		must(t, os.WriteFile(filepath.Join(l.fake, "enroll.resp"), []byte("mgp 1\nid "+labID+"\nsecret "+labSecret+"\nend\n"), 0o644))
		// bad inputs change nothing
		for _, args := range [][]string{{"install"}, {"install", labBase}, {"install", "http://panel.example.com/abcdefghijklmnopqrstuvwx", token}, {"install", labBase, "mgp_short"}, {"install", labBase + ";touch x", token}} {
			if out, code := l.runAt(down, args...); code == 0 || l.exists("opt/etc") || l.exists("opt/sbin/mistgate-probe") {
				t.Errorf("%q was accepted: %s", args, out)
			}
		}
		out, code := l.runAt(down, "install", labBase, token)
		if code != 0 || !strings.HasPrefix(out, "OK: probe "+labID+" registered and running") {
			t.Fatalf("install: %d %s", code, out)
		}
		if strings.Contains(out, labSecret) || strings.Contains(out, token) {
			t.Errorf("install printed a secret: %s", out)
		}
		if raw, _ := os.ReadFile(filepath.Join(l.fake, "enroll.token")); string(raw) != token {
			t.Errorf("the panel got %q instead of the token", raw)
		}
		confInfo, err := os.Stat(l.conf)
		must(t, err)
		dirInfo, _ := os.Stat(filepath.Dir(l.conf))
		if confInfo.Mode().Perm() != 0o600 || dirInfo.Mode().Perm() != 0o700 {
			t.Errorf("conf %v, dir %v: want 0600 and 0700", confInfo.Mode().Perm(), dirInfo.Mode().Perm())
		}
		if got, _ := os.ReadFile(l.conf); string(got) != "url="+labBase+"\nfallback=\nid="+labID+"\nsecret="+labSecret+"\n" {
			t.Errorf("conf: %q", got)
		}
		if got, _ := os.ReadFile(l.script); string(got) != string(probeScript) || func() bool { i, _ := os.Stat(l.script); return i.Mode().Perm() != 0o755 }() {
			t.Error("the installed script is not the downloaded one, mode 0755")
		}
		s99 := filepath.Join(l.root, "opt/etc/init.d/S99mistgate-probe")
		if i, err := os.Stat(s99); err != nil || i.Mode().Perm() != 0o755 {
			t.Fatalf("S99: %v", err)
		}
		raw, _ := os.ReadFile(s99)
		if !strings.Contains(string(raw), "exec sh "+l.script+` "$1"`) || strings.Contains(string(raw), labSecret) {
			t.Errorf("S99:\n%s", raw)
		}
		sup, work := l.pidOf("supervisor.pid"), l.pidOf("worker.pid")
		waitFor(t, "the worker", 5*time.Second, func() bool { work = l.pidOf("worker.pid"); return work > 0 && procAlive(work) })
		if !procAlive(sup) {
			t.Fatal("no supervisor")
		}
		if cmdline, _ := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", work)); !strings.Contains(string(cmdline), "mistgate-probe\x00worker") {
			t.Errorf("worker command line %q", cmdline)
		}
		// the S99 script, as the router runs it at boot, reports and stops it
		if st, _ := l.shellScript(s99 + " status"); !strings.Contains(st, "supervisor: running") || strings.Contains(st, labSecret) {
			t.Errorf("status: %s", st)
		}
		// a second install with the same (now spent) token refreshes and keeps the registration
		must(t, os.WriteFile(filepath.Join(l.fake, "enroll.code"), []byte("404\n"), 0o644))
		if out, code := l.runAt(down, "install", labBase, token); code != 0 || !strings.Contains(out, "already registered") {
			t.Errorf("second install: %d %s", code, out)
		}
		waitFor(t, "the restarted worker", 5*time.Second, func() bool { w := l.pidOf("worker.pid"); return w > 0 && procAlive(w) })
		// upgrade: a changed script replaces the installed one, the registration stays
		newer := append(append([]byte{}, probeScript...), []byte("# a newer build\n")...)
		must(t, writeExe(down, newer))
		if out, code := l.runAt(down, "upgrade"); code != 0 || !strings.HasPrefix(out, "OK: upgraded to ") {
			t.Fatalf("upgrade: %d %s", code, out)
		}
		if got, _ := os.ReadFile(l.script); string(got) != string(newer) {
			t.Error("upgrade did not replace the script")
		}
		if got, _ := os.ReadFile(l.conf); !strings.Contains(string(got), labID) {
			t.Error("upgrade lost the registration")
		}
		waitFor(t, "the worker of the new script", 5*time.Second, func() bool { w := l.pidOf("worker.pid"); return w > 0 && procAlive(w) })
		sup, work = l.pidOf("supervisor.pid"), l.pidOf("worker.pid")
		l.assertArgv()
		// uninstall: nothing is left, the processes are gone
		if out, code := l.run("uninstall"); code != 0 || !strings.Contains(out, "removed") {
			t.Fatalf("uninstall: %d %s", code, out)
		}
		waitFor(t, "the processes to end", 5*time.Second, func() bool { return !procAlive(sup) && !procAlive(work) })
		for _, rel := range []string{"opt/sbin/mistgate-probe", "opt/etc/mistgate-probe", "opt/etc/init.d/S99mistgate-probe", "tmp/mistgate-probe"} {
			if l.exists(rel) {
				t.Errorf("%s is left after uninstall", rel)
			}
		}
		left := tree(t, l.root, l.fake, filepath.Join(l.root, "bin"))
		for p := range left {
			if strings.Contains(p, "mistgate") {
				t.Errorf("left after uninstall: %s", p)
			}
		}
	})
}

// The lab ends what a test started in it before the directory of the test is removed: here a loop that keeps writing there,
// left in the background the way the script leaves a worker's curls. The loop holds a pipe of this test; the pipe ends when
// the loop is gone, and after the lab that must be at once.
func TestLabEndsWhatItStarted(t *testing.T) {
	t.Parallel()
	forShells(t, func(t *testing.T, sh shellSpec) {
		t.Parallel()
		r, w, err := os.Pipe()
		must(t, err)
		defer func() { _ = r.Close() }()
		t.Run("lab", func(t *testing.T) {
			l := newLab(t, sh, labOptions{})
			argv := append(append([]string{}, sh.argv[1:]...), "-c", `( while :; do mkdir -p "$FAKE_DIR/calls/x"; rmdir "$FAKE_DIR/calls/x"; done ) >/dev/null 2>&1 &`)
			c := exec.Command(sh.argv[0], argv...)
			c.Env = l.env
			l.own(c)
			c.ExtraFiles = append([]*os.File{w}, c.ExtraFiles...) // before the lab's own pipe: a process that ends closes this one first
			must(t, runCmd(c))
		})
		must(t, w.Close())
		_ = r.SetReadDeadline(time.Now().Add(10 * time.Second)) // reached only when the loop still runs
		if _, err := r.Read(make([]byte, 1)); err != io.EOF {
			t.Errorf("the loop outlived its lab: %v", err)
		}
	})
}

func (l *lab) runAt(script string, args ...string) (string, int) {
	l.t.Helper()
	saved := l.script
	l.script = script
	defer func() { l.script = saved }()
	return l.run(args...)
}

func TestSystemdUnit(t *testing.T) {
	t.Parallel()
	forShells(t, func(t *testing.T, sh shellSpec) {
		t.Parallel()
		l := newLab(t, sh, labOptions{noConf: true, notInstalled: true, mode: "systemd"})
		down := filepath.Join(l.root, "mgp.sh")
		must(t, writeExe(down, probeScript))
		must(t, os.WriteFile(filepath.Join(l.fake, "enroll.resp"), []byte("mgp 1\nid "+labID+"\nsecret "+labSecret+"\nend\n"), 0o644))
		out, code := l.runAt(down, "install", labBase, "mgp_"+strings.Repeat("c", 52))
		if code != 0 || !strings.Contains(out, "WARNING: probe "+labID) {
			t.Fatalf("install: %d %s", code, out)
		}
		unit, err := os.ReadFile(filepath.Join(l.root, "etc/systemd/system/mistgate-probe.service"))
		must(t, err)
		lines := strings.Split(string(unit), "\n")
		have := map[string]bool{}
		for _, line := range lines {
			have[line] = true
		}
		for _, want := range []string{"ExecStart=/bin/sh /usr/local/sbin/mistgate-probe supervise", "Restart=always", "DynamicUser=yes", "LoadCredential=conf:/etc/mistgate-probe/conf",
			"NoNewPrivileges=yes", "ProtectSystem=strict", "ProtectHome=yes", "PrivateTmp=yes", "PrivateDevices=yes", "CapabilityBoundingSet=",
			"RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX", "MemoryMax=64M", "TasksMax=16", "RuntimeDirectory=mistgate-probe", "UMask=0077"} {
			if !have[want] {
				t.Errorf("the unit has no line %q:\n%s", want, unit)
			}
		}
		if strings.Contains(string(unit), labSecret) || strings.Contains(string(unit), l.root) {
			t.Errorf("the unit has a secret or a test path:\n%s", unit)
		}
		if i, _ := os.Stat(l.conf); i == nil || i.Mode().Perm() != 0o600 {
			t.Errorf("conf mode")
		}
		tools, _ := os.ReadFile(filepath.Join(l.fake, "tools.log"))
		for _, want := range []string{"systemctl stop mistgate-probe", "systemctl daemon-reload", "systemctl enable mistgate-probe", "systemctl start mistgate-probe"} {
			_ = want // the production-only calls are skipped under MGP_ROOT; start and stop are made
		}
		if !strings.Contains(string(tools), "systemctl start mistgate-probe") {
			t.Errorf("the service was not started: %s", tools)
		}
		if out, code := l.run("uninstall"); code != 0 || l.exists("etc/systemd/system/mistgate-probe.service") || l.exists("etc/mistgate-probe") {
			t.Errorf("uninstall: %d %s", code, out)
		}
	})
}

// the answers of the panel by status: what the script does with each (ru-probes-plan §11.1)
func TestPanelStatusCodes(t *testing.T) {
	t.Parallel()
	forShells(t, func(t *testing.T, sh shellSpec) {
		t.Parallel()
		l := newLab(t, sh, labOptions{})
		next := func(out string) string {
			m := regexp.MustCompile(`next (\d+)`).FindStringSubmatch(out)
			if m == nil {
				return out
			}
			return m[1]
		}
		// 500s back off 60, 120, 240 ... 3600
		l.setAnswer(0, "oops", 500, "")
		var got []string
		for i := 0; i < 8; i++ {
			out, _ := l.run("run")
			got = append(got, next(out))
		}
		if strings.Join(got, " ") != "60 120 240 480 960 1920 3600 3600" {
			t.Errorf("backoff: %v", got)
		}
		l.forgetJob()
		for name, c := range map[string]struct {
			code int
			hdr  string
			want string
		}{
			"429 asks to wait":  {429, "Retry-After: 1200\r\n", "1200"},
			"429 too short":     {429, "Retry-After: 5\r\n", "900"},
			"429 too long":      {429, "Retry-After: 99999\r\n", "3600"},
			"429 without":       {429, "", "900"},
			"503 Retry-After":   {503, "retry-after: 900\r\n", "900"},
			"400 report refuse": {400, "", "600"},
		} {
			l.forgetJob()
			l.setAnswer(0, "x", c.code, c.hdr)
			if out, _ := l.run("run"); next(out) != c.want {
				t.Errorf("%s: %s", name, out)
			}
		}
		// 404 is the decoy site: the first is a failure, the second silences the probe for six hours, then it tries again
		l.forgetJob()
		l.forget()
		l.setAnswer(0, "<html>nothing</html>", 404, "")
		if out, _ := l.run("run"); next(out) != "60" {
			t.Errorf("first 404: %s", out)
		}
		if out, _ := l.run("run"); next(out) != "3600" || !l.stateHas("mute") {
			t.Errorf("second 404: %s", out)
		}
		syncs := len(l.calls())
		for i := 0; i < 5; i++ {
			l.run("run")
		}
		if len(l.calls()) != syncs+0 && len(l.calls()) > syncs+5 { // each run only asks curl for its version
			t.Errorf("a silenced probe kept talking: %d calls", len(l.calls())-syncs)
		}
		for _, c := range l.calls()[syncs:] {
			if c.isSync() {
				t.Errorf("a silenced probe made a sync")
			}
		}
		l.setClock(l.clock() + 6*3600 + 10)
		l.setAnswer(0, goodAnswer("t 1 203.0.113.12 443 tcp - -"), 200, "")
		if out, _ := l.run("run"); !strings.Contains(out, "http 200") {
			t.Errorf("after the silence: %s", out)
		}
		// an answer that is not a protocol answer: no job, no checks
		l.forgetJob()
		l.forget()
		l.setAnswer(0, "<html>Welcome</html>\n", 200, "")
		l.run("run")
		if len(l.checks()) != 0 || l.stateHas("job") {
			t.Error("a page of a site was taken for an answer")
		}
		// the panel's address is the one in conf; a fallback is used for an hour after two failures
		l.forgetJob()
		l.forget()
		l.writeConf("url=" + labBase + "\nfallback=https://cdn.example.com/zyxwvutsrqponmlkjihgfedc\nid=" + labID + "\nsecret=" + labSecret + "\n")
		l.setAnswer(0, "oops", 500, "")
		l.run("run")
		l.run("run")
		l.run("run")
		var urls []string
		for _, c := range l.calls() {
			if c.isSync() {
				urls = append(urls, strings.SplitN(strings.TrimPrefix(c.url(), "https://"), "/", 2)[0])
			}
		}
		if strings.Join(urls, " ") != "panel.example.com panel.example.com cdn.example.com" {
			t.Errorf("the fallback was used as %v", urls)
		}
		l.assertArgv()
	})
}

// measure runs a one-target job and returns the "r" line the script reported, and the whole report.
func measure(t *testing.T, l *lab, kind, curlLine string) (string, string) {
	t.Helper()
	l.forget()
	l.forgetJob()
	target := "t 1 203.0.113.10 443 tcp - -"
	if kind != "tcp" {
		target = "t 1 203.0.113.10 443 " + kind + " a.example.test /"
	}
	l.setAnswer(0, goodAnswer(target), 200, "")
	l.setTarget("203.0.113.10", 443, curlLine)
	l.run("run")
	bodies := l.syncBodies()
	if len(bodies) < 2 {
		t.Fatalf("no report was sent for %q", curlLine)
	}
	report := bodies[len(bodies)-1]
	if _, err := ParseSync([]byte(report)); err != nil {
		t.Fatalf("the panel would refuse the report: %v\n%s", err, report)
	}
	for _, line := range strings.Split(report, "\n") {
		if strings.HasPrefix(line, "r 1 ") {
			l.setClock(l.clock() + 2000)
			return line, report
		}
	}
	t.Fatalf("no result line in:\n%s", report)
	return "", ""
}

func TestMeasurementsAreClassifiedLikeThePlan(t *testing.T) {
	t.Parallel()
	forShells(t, func(t *testing.T, sh shellSpec) {
		t.Parallel()
		rich := newLab(t, sh, labOptions{})
		cases := []struct{ kind, line, want string }{
			{"tcp", "0.041000 0.000000 0 0.150000 0 ", "r 1 ok 41 - 0 - 0"},
			{"tcp", "0.000000 0.000000 0 5.001000 28 Connection timed out after 5001 milliseconds", "r 1 timeout 5001 - 0 - 0"},
			{"tcp", "0.000000 0.000000 0 0.002000 7 Failed to connect to 203.0.113.10 port 443 after 2 ms: Couldn't connect to server", "r 1 refused 2 - 0 - 0"},
			{"tcp", "0.000000 0.000000 0 0.001000 7 Failed to connect: Network is unreachable", "r 1 unreach 1 - 0 - 0"},
			{"tls", "0.040000 0.110000 5000 0.200000 0 ", "r 1 ok 40 ok 110 - 0"},
			{"tls", "0.040000 0.000000 0 0.100000 35 OpenSSL SSL_connect: Connection reset by peer in connection to a.example.test:443", "r 1 ok 40 reset 100 - 0"},
			{"tls", "0.040000 0.000000 0 0.100000 35 OpenSSL/3.0.13: error:0A000438:SSL routines::tlsv1 alert internal error", "r 1 ok 40 alert 100 - 0"},
			{"tls", "0.040000 0.000000 0 0.100000 35 OpenSSL SSL_connect: SSL_ERROR_SYSCALL in connection to a.example.test:443", "r 1 ok 40 reset 100 - 0"},
			{"tls", "0.040000 0.000000 0 8.001000 28 Operation timed out after 8001 milliseconds with 0 bytes received", "r 1 ok 40 timeout 8001 - 0"},
			{"tls", "0.040000 0.000000 0 0.100000 35 something else", "r 1 ok 40 fail 100 - 0"},
			{"tls", "0.000000 0.000000 0 5.001000 28 Connection timed out", "r 1 timeout 5001 - 0 - 0"},
			{"fetch", "0.040000 0.110000 65536 0.300000 63 Maximum file size exceeded", "r 1 ok 40 ok 110 full 65536"},
			{"fetch", "0.040000 0.110000 40000 0.300000 0 ", "r 1 ok 40 ok 110 full 40000"},
			{"fetch", "0.040000 0.110000 1500 0.200000 0 ", "r 1 ok 40 ok 110 short 1500"},
			{"fetch", "0.040000 0.110000 16384 6.200000 28 Operation too slow. Less than 1 bytes/sec transferred the last 6 seconds", "r 1 ok 40 ok 110 freeze 16384"},
			{"fetch", "0.040000 0.110000 100 1.000000 56 Recv failure: Connection reset by peer", "r 1 ok 40 ok 110 freeze 100"},
			{"fetch", "0.040000 0.000000 0 0.100000 35 OpenSSL SSL_connect: Connection reset by peer", "r 1 ok 40 reset 100 - 0"},
		}
		for _, c := range cases {
			if got, _ := measure(t, rich, c.kind, c.line); got != c.want {
				t.Errorf("%s %q:\n got %s\nwant %s", c.kind, c.line, got, c.want)
			}
		}
		// the control and the reference
		l := newLab(t, sh, labOptions{})
		l.setTarget("192.0.2.1", 443, "0.040000 0.000000 0 8.000000 28 timed out")
		l.setTarget("198.51.100.1", 443, "0.000000 0.000000 0 5.001000 28 Connection timed out")
		if _, report := measure(t, l, "tcp", "0.041000 0.000000 0 0.150000 0 "); !strings.Contains(report, "\nctl fail 8000\nref blocked\n") {
			t.Errorf("control failed, reference blocked:\n%s", report)
		}
		l.setTarget("192.0.2.1", 443, "0.040000 0.110000 10 0.200000 0 ")
		l.setTarget("198.51.100.1", 443, "0.020000 0.000000 0 0.100000 0 ")
		if _, report := measure(t, l, "tcp", "0.041000 0.000000 0 0.150000 0 "); !strings.Contains(report, "\nctl ok 110\nref open\n") {
			t.Errorf("control ok, reference open:\n%s", report)
		}
		l.setTarget("198.51.100.1", 443, "0.000000 0.000000 0 0.001000 7 Network is unreachable")
		if _, report := measure(t, l, "tcp", "0.041000 0.000000 0 0.150000 0 "); !strings.Contains(report, "\nref fail\n") {
			t.Errorf("reference unreachable is no evidence of filtering:\n%s", report)
		}
		rich.assertArgv()
	})
}

// an old curl (before 7.75: no %{exitcode}, no %{errormsg}) falls back to coarse results: the exit status only, and no freeze.
func TestOldCurlGivesCoarseResultsAndNoFreeze(t *testing.T) {
	t.Parallel()
	forShells(t, func(t *testing.T, sh shellSpec) {
		t.Parallel()
		for _, version := range []string{"7.74.0", "7.68.0", "6.0.0", "garbage"} {
			l := newLab(t, sh, labOptions{curlVersion: version})
			cases := []struct{ kind, line, want string }{
				{"tcp", "0.000000 0.000000 0 5.001000 28", "r 1 timeout 5001 - 0 - 0"},
				{"tcp", "0.000000 0.000000 0 0.002000 7", "r 1 refused 2 - 0 - 0"},
				{"tls", "0.040000 0.110000 5000 0.200000 0", "r 1 ok 40 ok 110 - 0"},
				{"tls", "0.040000 0.000000 0 0.100000 35", "r 1 ok 40 fail 100 - 0"},
				{"tls", "0.040000 0.000000 0 0.100000 56", "r 1 ok 40 reset 100 - 0"},
				{"tls", "0.040000 0.000000 0 8.000000 28", "r 1 ok 40 timeout 8000 - 0"},
				{"fetch", "0.040000 0.110000 16384 6.200000 28", "r 1 ok 40 ok 110 - 0"}, // a fetch is a tls check: freeze is not claimed
			}
			for _, c := range cases {
				if got, _ := measure(t, l, c.kind, c.line); got != c.want {
					t.Errorf("curl %s, %s %q:\n got %s\nwant %s", version, c.kind, c.line, got, c.want)
				}
			}
			for _, call := range l.calls() {
				for _, a := range call.Args {
					if strings.Contains(a, "exitcode") || strings.Contains(a, "errormsg") || a == "--max-filesize" && !call.isSync() {
						t.Errorf("curl %s was given %q", version, a)
					}
				}
			}
			l.assertArgv()
		}
		l := newLab(t, sh, labOptions{curlVersion: "7.75.0"})
		var rich bool
		measure(t, l, "tcp", "0.041000 0.000000 0 0.150000 0 0 ")
		for _, call := range l.calls() {
			for _, a := range call.Args {
				rich = rich || strings.Contains(a, "%{exitcode}")
			}
		}
		if !rich {
			t.Error("curl 7.75 has %{exitcode}: the rich format must be used")
		}
	})
}

// the secret and the token are never arguments of any command: the secret goes to curl in a 0600 file given with -K, the token on stdin.
func TestSecretsAreNotOnCommandLines(t *testing.T) {
	t.Parallel()
	forShells(t, func(t *testing.T, sh shellSpec) {
		t.Parallel()
		l := newLab(t, sh, labOptions{})
		l.run("run")
		auths, _ := filepath.Glob(filepath.Join(l.fake, "auth.*"))
		if len(auths) == 0 {
			t.Fatal("no -K file was given to curl")
		}
		for _, a := range auths {
			if raw, _ := os.ReadFile(a); !strings.Contains(string(raw), `header = "Authorization: Bearer `+labSecret+`"`) {
				t.Errorf("the -K file: %q", raw)
			}
		}
		l.assertArgv()
		// the secret never appears in what the script itself logs or prints
		out, _ := l.run("status")
		raw, _ := os.ReadFile(filepath.Join(l.fake, "logger.log"))
		if strings.Contains(out, labSecret) || strings.Contains(string(raw), labSecret) {
			t.Error("the secret was printed or logged")
		}
	})
}
