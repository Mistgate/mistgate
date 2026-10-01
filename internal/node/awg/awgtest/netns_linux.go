//go:build linux

package awgtest

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Prefix is the name prefix of every namespace this package creates.
const Prefix = "mg3-awg-"

// Require skips the test unless root tests are enabled, and removes leftovers of a crashed earlier run.
func Require(t testing.TB) {
	t.Helper()
	if os.Getenv("MG_ROOT_TESTS") != "1" {
		t.Skip("set MG_ROOT_TESTS=1 to run root network tests (Linux in WSL or a throwaway VM, never a fleet host)")
	}
	if os.Geteuid() != 0 {
		t.Skip("root is needed for network namespaces")
	}
	if _, err := exec.LookPath("ip"); err != nil {
		t.Skip("ip (iproute2) is not installed")
	}
	CleanupStale()
}

// CleanupStale deletes namespaces named Prefix* left by a crashed run, after killing what runs in them. It
// touches nothing else.
func CleanupStale() {
	out, err := exec.Command("ip", "netns", "list").Output()
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(out), "\n") {
		name, _, _ := strings.Cut(strings.TrimSpace(line), " ")
		if strings.HasPrefix(name, Prefix) {
			destroyNS(name)
		}
	}
}

func destroyNS(name string) {
	if !strings.HasPrefix(name, Prefix) {
		return // never anything that is not ours
	}
	for i := 0; i < 3; i++ {
		if pids, err := exec.Command("ip", "netns", "pids", name).Output(); err == nil {
			for _, p := range strings.Fields(string(pids)) {
				if n, err := strconv.Atoi(p); err == nil && n > 1 {
					_ = syscall.Kill(n, syscall.SIGKILL)
				}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = exec.Command("ip", "netns", "del", name).Run()
}

// NS is a network namespace created for one test.
type NS struct{ Name string }

// NewNS creates "mg3-awg-<suffix>" with lo up and deletes it (and kills its processes) when the test ends.
func NewNS(t testing.TB, suffix string) NS {
	t.Helper()
	ns := NS{Name: Prefix + suffix}
	destroyNS(ns.Name)
	if out, err := exec.Command("ip", "netns", "add", ns.Name).CombinedOutput(); err != nil {
		t.Fatalf("ip netns add %s: %v: %s", ns.Name, err, out)
	}
	t.Cleanup(func() { destroyNS(ns.Name) })
	ns.Must(t, "ip", "link", "set", "lo", "up")
	return ns
}

// Cmd builds a command that runs inside the namespace.
func (n NS) Cmd(args ...string) *exec.Cmd {
	return exec.Command("ip", append([]string{"netns", "exec", n.Name}, args...)...)
}

// Run runs a command inside the namespace and returns its combined output.
func (n NS) Run(args ...string) (string, error) {
	out, err := n.Cmd(args...).CombinedOutput()
	return string(out), err
}

// Must is Run that fails the test on error.
func (n NS) Must(t testing.TB, args ...string) string {
	t.Helper()
	out, err := n.Run(args...)
	if err != nil {
		t.Fatalf("[%s] %s: %v: %s", n.Name, strings.Join(args, " "), err, out)
	}
	return out
}

// Veth creates a veth pair between a and b with the given names and CIDR addresses on each end, all up.
func Veth(t testing.TB, a NS, aName string, aAddrs []string, b NS, bName string, bAddrs []string) {
	t.Helper()
	// Both ends are created straight in their namespaces: no name ever exists in the root namespace.
	if out, err := exec.Command("ip", "link", "add", aName, "netns", a.Name, "type", "veth", "peer", "name", bName, "netns", b.Name).CombinedOutput(); err != nil {
		t.Fatalf("veth %s/%s: %v: %s", aName, bName, err, out)
	}
	for _, e := range []struct {
		ns    NS
		name  string
		addrs []string
	}{{a, aName, aAddrs}, {b, bName, bAddrs}} {
		for _, ad := range e.addrs {
			args := []string{"ip", "addr", "add", ad, "dev", e.name}
			if strings.Contains(ad, ":") {
				args = append(args, "nodad")
			}
			e.ns.Must(t, args...)
		}
		e.ns.Must(t, "ip", "link", "set", e.name, "up")
	}
}

// Proc is a process started in a namespace; it is killed at the end of the test.
type Proc struct {
	cmd   *exec.Cmd
	out   *syncBuf
	done  chan struct{}
	Stdin io.WriteCloser // closing it is the polite way to ask a helper to stop
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// Start runs a long-lived command in the namespace (its own process group, killed on cleanup).
func (n NS) Start(t testing.TB, env []string, args ...string) *Proc {
	t.Helper()
	p := &Proc{cmd: n.Cmd(args...), out: &syncBuf{}, done: make(chan struct{})}
	p.cmd.Env = append(os.Environ(), env...)
	p.cmd.Stdout, p.cmd.Stderr = p.out, p.out
	var err error
	if p.Stdin, err = p.cmd.StdinPipe(); err != nil {
		t.Fatal(err)
	}
	p.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	if err := p.cmd.Start(); err != nil {
		t.Fatalf("start %v: %v", args, err)
	}
	go func() { _ = p.cmd.Wait(); close(p.done) }()
	t.Cleanup(p.Kill)
	return p
}

// Output is everything the process printed so far.
func (p *Proc) Output() string { return p.out.String() }

// Exited waits up to d for the process to end on its own.
func (p *Proc) Exited(d time.Duration) bool {
	select {
	case <-p.done:
		return true
	case <-time.After(d):
		return false
	}
}

// Kill ends the process group and waits for it.
func (p *Proc) Kill() {
	_ = p.Stdin.Close()
	select {
	case <-p.done:
		return
	default:
	}
	_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	<-p.done
}

// Wait polls until ok returns true or the timeout passes.
func Wait(timeout time.Duration, ok func() bool) bool {
	deadline := time.Now().Add(timeout)
	for {
		if ok() {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Ping sends count echo requests from ns to target and returns the packet loss in percent and the tool output.
// size is the ICMP payload (0 = default); df sets "do not fragment". interval 0 = default 1 s; extra are more ping
// options, for example "-I", "<interface>".
func Ping(ns NS, target string, count, size int, df bool, interval time.Duration, extra ...string) (loss int, out string) {
	bin := "ping"
	if strings.Contains(target, ":") {
		bin = "ping6"
		if _, err := exec.LookPath("ping6"); err != nil {
			bin = "ping"
		}
	}
	args := []string{bin, "-c", strconv.Itoa(count), "-W", "1"}
	if strings.Contains(target, ":") && bin == "ping" {
		args = append(args, "-6")
	}
	if size > 0 {
		args = append(args, "-s", strconv.Itoa(size))
	}
	if df {
		args = append(args, "-M", "do")
	}
	if interval > 0 {
		args = append(args, "-i", fmt.Sprintf("%.2f", interval.Seconds()))
	}
	args = append(args, extra...)
	args = append(args, target)
	o, _ := ns.Run(args...)
	for _, f := range strings.Split(o, ",") {
		if f = strings.TrimSpace(f); strings.HasSuffix(f, "% packet loss") {
			loss, _ = strconv.Atoi(strings.TrimSuffix(f, "% packet loss"))
			return loss, o
		}
	}
	return 100, o
}

const whoamiPy = `
import socket, sys
host, port = sys.argv[1], int(sys.argv[2])
s = socket.socket(socket.AF_INET6 if ":" in host else socket.AF_INET)
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind((host, port)); s.listen(16)
while True:
    c, a = s.accept()
    c.sendall(("peer=%s\n" % a[0]).encode())
    c.close()
`

const dialPy = `
import socket, sys
host, port = sys.argv[1], int(sys.argv[2])
s = socket.socket(socket.AF_INET6 if ":" in host else socket.AF_INET)
s.settimeout(3)
s.connect((host, port))
sys.stdout.write(s.recv(200).decode())
`

// StartWhoami runs a TCP server in ns that answers "peer=<your address>" and closes: the way to see which source
// address (after masquerade) a connection arrived with.
func StartWhoami(t testing.TB, ns NS, host string, port int) *Proc {
	t.Helper()
	p := ns.Start(t, nil, "python3", "-c", whoamiPy, host, strconv.Itoa(port))
	ok := Wait(5*time.Second, func() bool { _, err := ns.Run("python3", "-c", dialPy, host, strconv.Itoa(port)); return err == nil })
	if !ok {
		t.Fatalf("whoami server on %s:%d did not start: %s", host, port, p.Output())
	}
	return p
}

// Whoami connects from ns to a StartWhoami server and returns the address it saw ("" on failure).
func Whoami(ns NS, host string, port int) string {
	out, err := ns.Run("python3", "-c", dialPy, host, strconv.Itoa(port))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(out), "peer="))
}
