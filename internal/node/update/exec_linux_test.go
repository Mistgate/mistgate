//go:build linux

package update

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/release"
)

// fileSrc serves one file from disk, like the panel's FetchUpdate would.
type fileSrc struct{ path string }

func (fileSrc) Now() time.Time   { return time.Now() }
func (fileSrc) Failed() []string { return nil }
func (f fileSrc) Fetch(_ context.Context, _ string, off uint64, w io.Writer) (uint64, error) {
	b, err := os.ReadFile(f.path)
	if err != nil {
		return 0, err
	}
	_, err = w.Write(b[off:])
	return uint64(len(b)), err
}

// TestHelperApplyAndExec is the child of TestRealExecKeepsThePID: it is the "agent process", and Finish really calls
// syscall.Exec. It does nothing unless the parent test started it.
func TestHelperApplyAndExec(t *testing.T) {
	if os.Getenv("MGU_HELPER") != "1" {
		t.Skip("helper process of TestRealExecKeepsThePID")
	}
	pub, _ := base64.StdEncoding.DecodeString(os.Getenv("MGU_PUB"))
	man, _ := os.ReadFile(os.Getenv("MGU_MANIFEST"))
	sig, _ := os.ReadFile(os.Getenv("MGU_SIG"))
	exe := os.Getenv("MGU_EXE")
	u := New(Config{
		StateDir: os.Getenv("MGU_STATE"), ExePath: exe, Version: "old", Built: 1000, Key: pub, UnitGen: 2,
		Exec: syscall.Exec, Args: []string{exe, "run", "--state-dir", "x y"}, Env: append(os.Environ(), "MGU_PHASE=after-exec"),
	})
	res, fin := u.Apply(context.Background(), &pb.UpdateAgent{RequestId: "r", Manifest: man, Signature: sig}, fileSrc{os.Getenv("MGU_NEW")})
	if !res.Ok || fin == nil {
		os.Stderr.WriteString("apply: " + res.String() + "\n")
		os.Exit(2)
	}
	err := fin() // replaces this process; only a failure returns
	os.Stderr.WriteString("exec returned: " + err.Error() + "\n")
	os.Exit(3)
}

// TestRealExecKeepsThePID runs a real syscall.Exec: after the swap the process image is the new file, the PID is the same
// (so systemd sees nothing), and the arguments and environment given to Exec arrive intact.
func TestRealExecKeepsThePID(t *testing.T) {
	dir, state := t.TempDir(), t.TempDir()
	exe, out := filepath.Join(dir, "mistgate-node"), filepath.Join(dir, "out")
	newFile := filepath.Join(t.TempDir(), "new-build")
	script := "#!/bin/sh\necho \"$$|$1|$2|$3|$MGU_PHASE\" > \"$MGU_OUT\"\n"
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nexit 9\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newFile, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := release.GenerateKey()
	sum := sha256.Sum256([]byte(script))
	m := &release.Manifest{Schema: 1, Version: "new", Built: 2000, Expires: time.Now().Unix() + 600,
		Files: []release.File{{OS: "linux", Arch: goarch, Name: "mistgate-node-linux-" + goarch, Size: int64(len(script)), SHA256: hex.EncodeToString(sum[:])}}}
	man, err := m.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	manPath, sigPath := filepath.Join(dir, "m.json"), filepath.Join(dir, "m.sig")
	_ = os.WriteFile(manPath, man, 0o600)
	_ = os.WriteFile(sigPath, release.Sign(priv, man), 0o600)

	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperApplyAndExec$", "-test.v")
	cmd.Env = append(os.Environ(), "MGU_HELPER=1", "MGU_PUB="+release.EncodePublicKey(pub), "MGU_MANIFEST="+manPath, "MGU_SIG="+sigPath,
		"MGU_EXE="+exe, "MGU_STATE="+state, "MGU_NEW="+newFile, "MGU_OUT="+out)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("helper: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("the new build never ran: %v", err)
	}
	f := strings.Split(strings.TrimSpace(string(got)), "|")
	if len(f) != 5 {
		t.Fatalf("output = %q", got)
	}
	if pid, _ := strconv.Atoi(f[0]); pid != cmd.Process.Pid {
		t.Errorf("pid after exec = %s, want %d (exec must keep the PID)", f[0], cmd.Process.Pid)
	}
	if f[1] != "run" || f[2] != "--state-dir" || f[3] != "x y" || f[4] != "after-exec" {
		t.Errorf("args/env after exec = %v", f[1:])
	}
	if b, _ := os.ReadFile(exe); string(b) != script {
		t.Error("executable on disk is not the new build")
	}
	if b, _ := os.ReadFile(exe + ".prev"); string(b) != "#!/bin/sh\nexit 9\n" {
		t.Error(".prev is not the old build")
	}
}
