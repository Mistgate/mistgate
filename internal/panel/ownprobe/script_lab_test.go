package ownprobe

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The lab runs the embedded probe script for real, under busybox ash and dash, with a fake curl in the PATH the script puts
// first when MGP_ROOT is set (testdata/fakecurl.sh). Nothing leaves the temporary directory of the test.

//go:embed testdata/fakecurl.sh
var fakeCurl []byte

type shellSpec struct {
	name string
	argv []string // the shell and its leading arguments
	path string
}

// scriptShells returns the shells the script tests run under. busybox ash is the shell of the routers the script is made for;
// without busybox the tests run under dash only, and say so. In CI MGP_REQUIRE_BUSYBOX=1 turns a missing busybox into a failure.
func scriptShells(t testing.TB) []shellSpec {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skipf("SKIP: the probe script tests need busybox ash or dash on Linux; they run under WSL and in CI, not on %s", runtime.GOOS)
	}
	var out []shellSpec
	if p, err := exec.LookPath("busybox"); err == nil && exec.Command(p, "ash", "-c", "exit 0").Run() == nil {
		out = append(out, shellSpec{"busybox-ash", []string{p, "ash"}, p})
	} else if os.Getenv("MGP_REQUIRE_BUSYBOX") != "" {
		t.Fatal("MGP_REQUIRE_BUSYBOX is set and there is no busybox with ash")
	}
	if p, err := exec.LookPath("dash"); err == nil {
		out = append(out, shellSpec{"dash", []string{p}, p})
	}
	if len(out) == 0 {
		t.Skip("SKIP: neither busybox nor dash is installed")
	}
	if len(out) == 1 && out[0].name != "busybox-ash" {
		t.Log("busybox is not installed: this run covers dash only")
	}
	return out
}

func forShells(t *testing.T, fn func(t *testing.T, sh shellSpec)) {
	t.Helper()
	for _, sh := range scriptShells(t) {
		t.Run(sh.name, func(t *testing.T) { fn(t, sh) })
	}
}

type labOptions struct {
	realClock    bool   // real date and sleep (a supervisor that runs for real needs them)
	curlVersion  string // what the fake curl reports; default 8.5.0
	noConf       bool
	notInstalled bool   // the script is not at its install path: the test installs it
	mode         string // entware (default) or systemd
	scriptBytes  []byte // the script under test; default the embedded one
	root         string // the directory of the lab; default a temporary directory of the test
}

type lab struct {
	t      testing.TB
	sh     shellSpec
	root   string
	fake   string
	script string
	state  string
	conf   string
	opts   labOptions
	env    []string
}

const (
	labBase   = "https://panel.example.com/abcdefghijklmnopqrstuvwx"
	labID     = "prb_0123456789abcdef01234567"
	labSecret = "mgps_0123456789012345678901234567890123456789012"
)

func newLab(t testing.TB, sh shellSpec, opts labOptions) *lab {
	t.Helper()
	if opts.mode == "" {
		opts.mode = "entware"
	}
	if opts.scriptBytes == nil {
		opts.scriptBytes = probeScript
	}
	root := opts.root
	if root == "" {
		root = t.TempDir()
	}
	l := &lab{t: t, sh: sh, root: root, fake: filepath.Join(root, "fake"), opts: opts}
	for _, d := range []string{"bin", "fake/t", "tmp", "run"} {
		must(t, os.MkdirAll(filepath.Join(root, d), 0o755))
	}
	if opts.mode == "entware" {
		l.script = filepath.Join(root, "opt/sbin/mistgate-probe")
		l.conf = filepath.Join(root, "opt/etc/mistgate-probe/conf")
		l.state = filepath.Join(root, "tmp/mistgate-probe")
	} else {
		l.script = filepath.Join(root, "usr/local/sbin/mistgate-probe")
		l.conf = filepath.Join(root, "etc/mistgate-probe/conf")
		l.state = filepath.Join(root, "run/mistgate-probe")
	}
	must(t, os.MkdirAll(filepath.Dir(l.script), 0o755))
	if !opts.notInstalled {
		must(t, os.WriteFile(l.script, opts.scriptBytes, 0o755))
	}
	bin := filepath.Join(root, "bin")
	must(t, os.WriteFile(filepath.Join(bin, "curl"), fakeCurl, 0o755))
	if sh.name == "busybox-ash" && !opts.realClock {
		must(t, os.WriteFile(filepath.Join(bin, "sh"), []byte(busyboxSh(sh.path)), 0o755))
	} else {
		must(t, os.Symlink(sh.path, filepath.Join(bin, "sh")))
	}
	for _, tool := range []string{"opkg", "systemctl"} {
		must(t, os.WriteFile(filepath.Join(bin, tool), []byte("#!/bin/sh\necho \"$0 $*\" >>\"$FAKE_DIR/tools.log\"\nexit 0\n"), 0o755))
	}
	must(t, os.WriteFile(filepath.Join(bin, "logger"), []byte("#!/bin/sh\necho \"$*\" >>\"$FAKE_DIR/logger.log\"\n"), 0o755))
	if !opts.realClock {
		must(t, os.WriteFile(filepath.Join(bin, "date"), []byte("#!/bin/sh\ncase ${1:-} in +%s) read -r t <\"$FAKE_DIR/clock\"; echo \"$t\" ;; *) exec /bin/date \"$@\" ;; esac\n"), 0o755))
		must(t, os.WriteFile(filepath.Join(bin, "sleep"), []byte("#!/bin/sh\n[ -z \"${FAKE_SLEEP_BROKEN:-}\" ] || exit 1\nread -r t <\"$FAKE_DIR/clock\"; echo $((t + $1)) >\"$FAKE_DIR/clock\"\n"), 0o755))
	}
	l.setClock(1_700_000_000)
	l.env = []string{"MGP_ROOT=" + root, "FAKE_DIR=" + l.fake, "PATH=" + os.Getenv("PATH"), "HOME=" + root, "LAB_SH=" + strings.Join(l.shellArgv(), " ")}
	if opts.curlVersion != "" {
		l.env = append(l.env, "FAKE_CURL_VERSION="+opts.curlVersion)
	}
	if opts.mode != "entware" {
		l.env = append(l.env, "MGP_MODE="+opts.mode)
	}
	if !opts.noConf {
		l.writeConf("url=" + labBase + "\nfallback=\nid=" + labID + "\nsecret=" + labSecret + "\n")
	}
	l.setAnswer(0, goodAnswer("t 1 203.0.113.12 443 tcp - -", "t 2 203.0.113.12 8443 tls target.example.test /", "t 3 203.0.113.21 443 fetch target.example.test /"), 200, "")
	return l
}

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func (l *lab) writeConf(text string) {
	l.t.Helper()
	must(l.t, os.MkdirAll(filepath.Dir(l.conf), 0o700))
	must(l.t, os.WriteFile(l.conf, []byte(text), 0o600))
}

func (l *lab) setClock(sec int64) {
	l.t.Helper()
	must(l.t, os.WriteFile(filepath.Join(l.fake, "clock"), []byte(strconv.FormatInt(sec, 10)+"\n"), 0o644))
}

func (l *lab) clock() int64 {
	b, _ := os.ReadFile(filepath.Join(l.fake, "clock"))
	n, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return n
}

// setAnswer sets the body, status and extra header lines of the k-th sync (0: every sync that has no answer of its own).
func (l *lab) setAnswer(k int, body string, code int, header string) {
	l.t.Helper()
	name := "ans.default"
	if k > 0 {
		name = "ans." + strconv.Itoa(k)
	}
	must(l.t, os.WriteFile(filepath.Join(l.fake, name), []byte(body), 0o644))
	must(l.t, os.WriteFile(filepath.Join(l.fake, name+".code"), []byte(strconv.Itoa(code)+"\n"), 0o644))
	must(l.t, os.WriteFile(filepath.Join(l.fake, name+".hdr"), []byte(header), 0o644))
}

// resetSyncs makes the next sync the first again for the scripted answers.
func (l *lab) resetSyncs() { _ = os.Remove(filepath.Join(l.fake, "syncn")) }

func (l *lab) setTarget(ip string, port int, line string) {
	l.t.Helper()
	must(l.t, os.WriteFile(filepath.Join(l.fake, "t", fmt.Sprintf("%s_%d", ip, port)), []byte(line+"\n"), 0o644))
}

// busyboxSh is the "sh" of a busybox lab that has a fake clock. This busybox runs its own applets for date, sleep and logger whatever
// the PATH says (it is built with FEATURE_SH_STANDALONE), so the fakes of the lab's bin would never run and the script would see
// the real clock. Handed a script file, this sh runs it under ash with them as shell functions that read and move the lab's
// clock and write its logger.log; a function is looked up before an applet. Anything else (sh -n, sh -c) is plain ash.
func busyboxSh(busybox string) string {
	const prologue = `date() { case ${1:-} in +%s) read -r fk_t <"$FAKE_DIR/clock"; echo "$fk_t" ;; *) command date "$@" ;; esac; }
sleep() { [ -z "${FAKE_SLEEP_BROKEN:-}" ] || return 1; read -r fk_t <"$FAKE_DIR/clock"; echo $((fk_t + $1)) >"$FAKE_DIR/clock"; }
logger() { echo "$*" >>"$FAKE_DIR/logger.log"; }
. "$0"`
	return "#!/bin/sh\ncase ${1:-} in -* | \"\") exec '" + busybox + "' ash \"$@\" ;; esac\nexec '" + busybox + "' ash -c '" + prologue + "' \"$@\"\n"
}

// shellArgv is the shell, and its leading arguments, that runs the lab's script.
func (l *lab) shellArgv() []string {
	if l.sh.name == "busybox-ash" && !l.opts.realClock {
		return []string{filepath.Join(l.root, "bin", "sh")}
	}
	return l.sh.argv
}

func (l *lab) cmd(extraEnv []string, args ...string) *exec.Cmd {
	sh := l.shellArgv()
	argv := append(append([]string{}, sh[1:]...), l.script)
	argv = append(argv, args...)
	c := exec.Command(sh[0], argv...)
	c.Dir = l.root
	c.Env = append(append([]string{}, l.env...), extraEnv...)
	return c
}

// run runs one command of the script and returns its output and exit status.
func (l *lab) run(args ...string) (string, int) {
	l.t.Helper()
	return l.runEnv(nil, args...)
}

func (l *lab) runEnv(extraEnv []string, args ...string) (string, int) {
	l.t.Helper()
	var out bytes.Buffer
	c := l.cmd(extraEnv, args...)
	c.Stdout, c.Stderr = &out, &out
	err := c.Run()
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			l.t.Fatalf("running the script: %v", err)
		}
		code = ee.ExitCode()
	}
	return out.String(), code
}

// shellScript runs a small sh program in the lab's environment, under the shell under test.
func (l *lab) shellScript(program string, args ...string) (string, int) {
	l.t.Helper()
	argv := append(append([]string{}, l.sh.argv[1:]...), "-c", program, "sh")
	c := exec.Command(l.sh.argv[0], append(argv, args...)...)
	c.Dir = l.root
	c.Env = append(append([]string{}, l.env...), "PATH="+filepath.Join(l.root, "bin")+":"+os.Getenv("PATH"))
	var out bytes.Buffer
	c.Stdout, c.Stderr = &out, &out
	err := c.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		l.t.Fatal(err)
	}
	return out.String(), code
}

type fakeCall struct {
	N    int
	Args []string
	At   int64
}

func (c fakeCall) url() string {
	for _, a := range c.Args {
		if strings.HasPrefix(a, "https://") {
			return a
		}
	}
	return ""
}

func (c fakeCall) isSync() bool   { return strings.HasSuffix(c.url(), "/sync") }
func (c fakeCall) isEnroll() bool { return strings.HasSuffix(c.url(), "/enroll") }
func (c fakeCall) isCheck() bool {
	return c.url() != "" && !c.isSync() && !c.isEnroll() && !strings.HasSuffix(c.url(), "/probe.sh")
}

// calls returns every curl call of the lab so far, in order.
func (l *lab) calls() []fakeCall {
	l.t.Helper()
	files, _ := filepath.Glob(filepath.Join(l.fake, "calls", "*"))
	var out []fakeCall
	for _, f := range files {
		if strings.HasSuffix(f, ".t") {
			continue
		}
		n, err := strconv.Atoi(filepath.Base(f))
		if err != nil {
			continue
		}
		raw, _ := os.ReadFile(f)
		args := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
		if len(raw) == 0 {
			args = nil
		}
		at, _ := os.ReadFile(f + ".t")
		sec, _ := strconv.ParseInt(strings.TrimSpace(string(at)), 10, 64)
		out = append(out, fakeCall{N: n, Args: args, At: sec})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].N < out[j].N })
	return out
}

func (l *lab) checks() []fakeCall {
	var out []fakeCall
	for _, c := range l.calls() {
		if c.isCheck() {
			out = append(out, c)
		}
	}
	return out
}

func (l *lab) syncBodies() []string {
	files, _ := filepath.Glob(filepath.Join(l.fake, "body.*"))
	sort.Slice(files, func(i, j int) bool {
		a, _ := strconv.Atoi(strings.TrimPrefix(filepath.Base(files[i]), "body."))
		b, _ := strconv.Atoi(strings.TrimPrefix(filepath.Base(files[j]), "body."))
		return a < b
	})
	var out []string
	for _, f := range files {
		raw, _ := os.ReadFile(f)
		out = append(out, string(raw))
	}
	return out
}

func (l *lab) stateFile(name string) string {
	b, _ := os.ReadFile(filepath.Join(l.state, name))
	return string(b)
}

func (l *lab) stateHas(name string) bool {
	_, err := os.Stat(filepath.Join(l.state, name))
	return err == nil
}

// forget clears the record of curl calls and the state of the script, keeping the configuration.
func (l *lab) forget() {
	l.t.Helper()
	for _, d := range []string{"calls"} {
		_ = os.RemoveAll(filepath.Join(l.fake, d))
	}
	for _, f := range []string{"seq", "syncn", "events"} {
		_ = os.Remove(filepath.Join(l.fake, f))
	}
	for _, pat := range []string{"body.*", "auth.*"} {
		m, _ := filepath.Glob(filepath.Join(l.fake, pat))
		for _, f := range m {
			_ = os.Remove(f)
		}
	}
}

// goodAnswer is a valid answer to a sync with the given target lines.
func goodAnswer(targets ...string) string {
	var b strings.Builder
	b.WriteString("mgp 1\nnext 912\nstate active\njob 00112233445566ff\nctl 192.0.2.1 443 control.example.test\nref 198.51.100.1 443\n")
	for _, t := range targets {
		b.WriteString(t + "\n")
	}
	b.WriteString("end\n")
	return b.String()
}

func scriptSHA() string {
	h := sha256.Sum256(probeScript)
	return hex.EncodeToString(h[:])
}

// tree lists every file and directory under dir, with a hash of the contents of files and the mode, skipping the listed
// subtrees. It is how the tests prove that the script touched nothing it should not.
func tree(t testing.TB, dir string, skip ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		for _, s := range skip {
			if p == s || strings.HasPrefix(p, s+string(filepath.Separator)) {
				return nil
			}
		}
		rel, _ := filepath.Rel(dir, p)
		if info.IsDir() {
			out[rel] = "dir " + info.Mode().String()
			return nil
		}
		raw, _ := os.ReadFile(p)
		h := sha256.Sum256(raw)
		out[rel] = info.Mode().String() + " " + hex.EncodeToString(h[:8])
		return nil
	})
	must(t, err)
	return out
}

func diffTree(a, b map[string]string) []string {
	var d []string
	for k, v := range b {
		if a[k] != v {
			d = append(d, "changed or new: "+k)
		}
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			d = append(d, "removed: "+k)
		}
	}
	sort.Strings(d)
	return d
}
