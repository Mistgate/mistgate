package doctor

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// t0 is the fake clock.
var t0 = time.Unix(1_800_000_000, 0)

// fake is a machine in a temp directory: files under root, commands answered from a table, sockets and
// stat results from maps. Nothing real is touched, so the same tests run on Windows and in WSL.
type fake struct {
	t    *testing.T
	root string

	mu    sync.Mutex
	cmds  map[string]cmdReply // key "name arg1 arg2"
	calls []string
	stat  map[string]FSStat
	links map[string]string
	bins  map[string]bool
	sleep func() // called by Sleep
}

type cmdReply struct {
	out string
	err error
}

func newFake(t *testing.T) *fake {
	t.Helper()
	return &fake{
		t: t, root: t.TempDir(),
		cmds: map[string]cmdReply{}, stat: map[string]FSStat{}, links: map[string]string{}, bins: map[string]bool{},
	}
}

// put writes a file under the fake root.
func (f *fake) put(path, content string) {
	f.t.Helper()
	p := filepath.Join(f.root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// putSized creates a sparse file of n bytes.
func (f *fake) putSized(path string, n int64) {
	f.t.Helper()
	f.put(path, "")
	if err := os.Truncate(filepath.Join(f.root, filepath.FromSlash(path)), n); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fake) remove(path string) { _ = os.RemoveAll(filepath.Join(f.root, filepath.FromSlash(path))) }

func (f *fake) bin(names ...string) {
	for _, n := range names {
		f.bins[n] = true
	}
}

// cmd registers the answer to a command; the key is the full command line.
func (f *fake) cmd(line, out string, err error) { f.cmds[line] = cmdReply{out, err} }

func (f *fake) called(prefix string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

func (f *fake) env() Env {
	e := Env{
		Root: f.root,
		Now:  func() time.Time { return t0 },
		Sleep: func(ctx context.Context, d time.Duration) {
			if f.sleep != nil {
				f.sleep()
			}
		},
		CPUs: 4, SelfPID: 100, Virt: "kvm",
		Statfs: func(p string) (FSStat, error) {
			if s, ok := f.stat[p]; ok {
				return s, nil
			}
			return FSStat{}, errors.New("no such mount")
		},
		Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			line := strings.Join(append([]string{name}, args...), " ")
			f.mu.Lock()
			f.calls = append(f.calls, line)
			r, ok := f.cmds[line]
			f.mu.Unlock()
			if !ok {
				return nil, errors.New("fake: command not found: " + line)
			}
			return []byte(r.out), r.err
		},
		LookPath: func(n string) (string, error) {
			if f.bins[n] {
				return "/usr/bin/" + n, nil
			}
			return "", errors.New("not found")
		},
		Readlink: func(p string) (string, error) {
			if l, ok := f.links[p]; ok {
				return l, nil
			}
			return "", errors.New("not a link")
		},
	}
	e.defaults()
	return e
}

// doctor returns a Doctor over the fake with env tweaks applied.
func (f *fake) doctor(tweak ...func(*Env)) *Doctor {
	e := f.env()
	for _, t := range tweak {
		t(&e)
	}
	return New(e)
}

// run runs one check by id through a Doctor and returns its result.
func run(t *testing.T, d *Doctor, id string) Result {
	t.Helper()
	rep, err := d.Run(context.Background(), []string{id})
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Results) != 1 || rep.Results[0].ID != id {
		t.Fatalf("results = %+v", rep.Results)
	}
	checkCode(t, rep.Results[0])
	return rep.Results[0]
}

// checkCode: every result of every scenario carries a registered detail code of its own check (or a skip.* one).
func checkCode(t *testing.T, r Result) {
	t.Helper()
	if !slices.Contains(Codes, r.Code) || !strings.HasPrefix(r.Code, r.ID+".") && !strings.HasPrefix(r.Code, "skip.") {
		t.Errorf("%s: code %q is not a registered code of this check (%s)", r.ID, r.Code, r.Detail)
	}
}

func want(t *testing.T, r Result, st Status, fix string) {
	t.Helper()
	if r.Status != st || r.FixID != fix {
		t.Fatalf("%s: got %s fix=%q (%s), want %s fix=%q", r.ID, r.Status, r.FixID, r.Detail, st, fix)
	}
}

func param(t *testing.T, r Result, k, v string) {
	t.Helper()
	if r.Params[k] != v {
		t.Errorf("%s params[%s] = %q, want %q (all: %v)", r.ID, k, r.Params[k], v, r.Params)
	}
}

// mkCert makes a self-signed certificate for names that expires at notAfter.
func mkCert(t *testing.T, notAfter time.Time, names ...string) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "t"},
		NotBefore: notAfter.Add(-90 * 24 * time.Hour), NotAfter: notAfter,
	}
	for _, n := range names {
		if ip := net.ParseIP(n); ip != nil {
			tpl.IPAddresses = append(tpl.IPAddresses, ip)
		} else {
			tpl.DNSNames = append(tpl.DNSNames, n)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// snapshot lists every file under dir with its size, for "a dry run changed nothing" assertions.
func snapshot(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	_ = filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil {
			rel, _ := filepath.Rel(dir, p)
			fmt.Fprintf(&b, "%s:%d:%s\n", rel, fi.Size(), fi.ModTime())
		}
		return nil
	})
	return b.String()
}
