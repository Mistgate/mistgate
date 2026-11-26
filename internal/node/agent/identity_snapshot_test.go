package agent

import (
	"bytes"
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestLoadIdentityForcedEnrollmentSnapshot(t *testing.T) {
	oldPanel := newFakePanel(t)
	dir := t.TempDir()
	if _, err := Enroll(context.Background(), EnrollConfig{
		StateDir: dir, Panel: oldPanel.addr(), SNI: testSNI,
		CASHA256: oldPanel.fingerprint(), Token: oldPanel.token,
	}); err != nil {
		t.Fatal(err)
	}

	// Stage a real forced enrollment separately so its publication can be
	// paused deterministically while loadIdentity waits on the destination lock.
	staged := t.TempDir()
	files := []string{fileCA, fileMeta, fileIdentity}
	for _, name := range files {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(staged, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	newPanel := newFakePanel(t)
	if _, err := Enroll(context.Background(), EnrollConfig{
		StateDir: staged, Panel: newPanel.addr(), SNI: testSNI,
		CASHA256: newPanel.fingerprint(), Token: newPanel.token, Force: true,
	}); err != nil {
		t.Fatal(err)
	}
	want, err := loadIdentity(staged)
	if err != nil {
		t.Fatal(err)
	}
	publication := make([][]byte, len(files))
	for i, name := range files {
		publication[i], err = os.ReadFile(filepath.Join(staged, name))
		if err != nil {
			t.Fatal(err)
		}
	}

	type result struct {
		id  *identity
		err error
	}
	loaded := make(chan result, 1)
	err = withMetaLock(dir, func() error {
		go func() {
			id, err := loadIdentity(dir)
			loaded <- result{id, err}
		}()
		// Observe the actual reader inside withMetaLock, not merely a ready
		// signal before the call. The pre-fix reader has already read the old
		// identity and CA at this point; the fixed reader has read neither.
		stack := make([]byte, 1<<20)
		deadline := time.Now().Add(10 * time.Second)
		for {
			n := runtime.Stack(stack, true)
			waiting := false
			for _, goroutine := range bytes.Split(stack[:n], []byte("\n\n")) {
				if bytes.Contains(goroutine, []byte(".loadIdentity(")) &&
					bytes.Contains(goroutine, []byte(".withMetaLock(")) {
					waiting = true
					break
				}
			}
			if waiting {
				break
			}
			select {
			case r := <-loaded:
				return fmt.Errorf("loadIdentity bypassed publication lock: %v", r.err)
			default:
			}
			if time.Now().After(deadline) {
				return errors.New("loadIdentity did not reach publication lock")
			}
			runtime.Gosched()
		}
		// Match Enroll's CA -> metadata -> identity publication order, using
		// the exact files produced by the forced enrollment above.
		for i, name := range files {
			if err := writeFileAtomic(filepath.Join(dir, name), publication[i], 0o600); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-loaded:
		if r.err != nil {
			t.Fatal(r.err)
		}
		if r.id.meta != want.meta || !r.id.cert.Leaf.Equal(want.cert.Leaf) || !r.id.caCert.Equal(want.caCert) {
			t.Fatal("loadIdentity mixed snapshots across forced enrollment")
		}
		if _, err := r.id.cert.Leaf.Verify(x509.VerifyOptions{
			Roots: r.id.ca, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		}); err != nil {
			t.Fatalf("loaded certificate does not chain to loaded CA: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("loadIdentity did not finish after publication unlocked")
	}
}
