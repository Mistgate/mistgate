package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	metadataOldLink  = "wss://old.example.com/old-secret-prefix-0123456789/"
	metadataNextLink = "wss://next.example.com/next-secret-prefix-0123456789/"
	metadataNewLink  = "wss://new.example.com/new-secret-prefix-0123456789/"
)

func readMetaFile(dir string) (Meta, error) {
	var m Meta
	err := withMetaLock(dir, func() error {
		var err error
		m, err = readMetaFileUnlocked(dir)
		return err
	})
	return m, err
}

func metadataFixture(t *testing.T) (string, Meta) {
	t.Helper()
	dir := t.TempDir()
	m := Meta{Link: metadataOldLink, NodeID: "node", NextLink: metadataNextLink}
	if err := saveMeta(dir, &m); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, fileIdentity), []byte("enrolled"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir, m
}

func TestMetadataStaleSnapshotsPreserveNextIntent(t *testing.T) {
	for _, tc := range []struct {
		name      string
		requested string
		want      string
	}{
		{"replace", metadataNewLink, metadataNewLink},
		{"clear", metadataOldLink, ""},
		{"reassert", metadataNextLink, metadataNextLink},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, operation := range []string{"save", "clear", "commit"} {
				t.Run(operation, func(t *testing.T) {
					dir, stale := metadataFixture(t)
					if err := SetPanelLink(dir, tc.requested); err != nil {
						t.Fatal(err)
					}
					latest, err := readMetaFile(dir)
					if err != nil {
						t.Fatal(err)
					}
					if latest.NextLinkRevision == stale.NextLinkRevision {
						t.Fatal("operator intent did not receive a new revision")
					}
					stale.LinkHealthy = true
					if operation != "save" {
						stale.NextLink = ""
					}
					if operation == "commit" {
						stale.PrevLink, stale.Link = stale.Link, metadataNextLink
					}
					if err := saveMeta(dir, &stale); err != nil {
						t.Fatal(err)
					}
					got, err := readMetaFile(dir)
					if err != nil {
						t.Fatal(err)
					}
					if got.NextLink != tc.want || got.NextLinkRevision != latest.NextLinkRevision {
						t.Fatal("newer next-link intent or its revision was overwritten")
					}
					if stale != got || !got.LinkHealthy {
						t.Fatal("caller was not refreshed or unrelated snapshot fields were lost")
					}
					if operation == "commit" && got.Link != metadataNextLink {
						t.Fatal("successful connection was not committed")
					}
				})
			}
		})
	}
}

func TestMetadataConsumedIntentCannotBeResurrected(t *testing.T) {
	dir, current := metadataFixture(t)
	stale := current
	current.PrevLink, current.Link, current.NextLink = current.Link, current.NextLink, ""
	if err := saveMeta(dir, &current); err != nil {
		t.Fatal(err)
	}
	if current.NextLinkRevision == stale.NextLinkRevision {
		t.Fatal("consuming an intent did not revise it")
	}
	// An unrelated full write carrying the old pending intent must not resurrect it.
	stale.Link = current.Link
	if err := saveMeta(dir, &stale); err != nil {
		t.Fatal(err)
	}
	got, err := readMetaFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.NextLink != "" || stale.NextLink != "" || got.NextLinkRevision != current.NextLinkRevision {
		t.Fatal("consumed intent resurrected")
	}
}

func TestMetadataMTLSSetPanelPreservesEnrollment(t *testing.T) {
	dir := t.TempDir()
	m := Meta{Panel: "panel.example.com:443", SNI: "agent.example.com", NodeID: "node"}
	if err := saveMeta(dir, &m); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, fileIdentity), []byte("enrolled"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetPanelLink(dir, metadataNewLink); err != nil {
		t.Fatal(err)
	}
	after := readMeta(t, dir)
	if after.NodeID != m.NodeID || after.Panel != m.Panel || after.SNI != m.SNI || after.Link != "" {
		t.Fatal("set-panel changed the mTLS enrollment")
	}
	if after.NextLink != metadataNewLink || after.NextLinkRevision == "" {
		t.Fatal("set-panel did not persist a revisioned next link")
	}
}

func TestMetadataSubprocessHelper(t *testing.T) {
	dir := os.Getenv("MISTGATE_META_TEST_DIR")
	if dir == "" {
		return
	}
	operation := os.Getenv("MISTGATE_META_TEST_OP")
	if operation != "stale-commit" {
		fmt.Println("metadata-ready")
	}
	m, err := readMetaFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if operation == "stale-commit" {
		fmt.Println("metadata-ready")
		if !bufio.NewScanner(os.Stdin).Scan() {
			t.Fatal("missing commit signal")
		}
		m.PrevLink, m.Link, m.NextLink = m.Link, m.NextLink, ""
	}
	if operation == "set-panel" {
		err = SetPanelLink(dir, metadataNewLink)
	} else {
		m.LinkHealthy = true
		err = saveMeta(dir, &m)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestMetadataCrossProcessStaleCommit(t *testing.T) {
	dir, _ := metadataFixture(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestMetadataSubprocessHelper$")
	cmd.Env = append(os.Environ(), "MISTGATE_META_TEST_DIR="+dir, "MISTGATE_META_TEST_OP=stale-commit")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	timer := time.AfterFunc(10*time.Second, func() { _ = cmd.Process.Kill() })
	defer timer.Stop()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "metadata-ready" {
		t.Fatal("child did not load its initial metadata snapshot")
	}
	if err := SetPanelLink(dir, metadataNewLink); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(stdin, "commit"); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("child: %v: %s", err, stderr.String())
	}
	got, err := readMetaFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.Link != metadataNextLink || got.NextLink != metadataNewLink {
		t.Fatal("stale subprocess commit consumed the newer CLI intent")
	}
}

func TestMetadataCrossProcessLock(t *testing.T) {
	for _, operation := range []string{"set-panel", "save"} {
		t.Run(operation, func(t *testing.T) {
			dir, _ := metadataFixture(t)
			cmd := exec.Command(os.Args[0], "-test.run=^TestMetadataSubprocessHelper$")
			cmd.Env = append(os.Environ(), "MISTGATE_META_TEST_DIR="+dir, "MISTGATE_META_TEST_OP="+operation)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			finished := make(chan error, 1)
			err = withMetaLock(dir, func() error {
				if err := cmd.Start(); err != nil {
					return err
				}
				t.Cleanup(func() { _ = cmd.Process.Kill() })
				go func() { finished <- cmd.Wait() }()
				ready := make(chan bool, 1)
				go func() {
					scanner := bufio.NewScanner(stdout)
					for scanner.Scan() {
						if scanner.Text() == "metadata-ready" {
							ready <- true
							return
						}
					}
					ready <- false
				}()
				select {
				case ok := <-ready:
					if !ok {
						return fmt.Errorf("child did not reach metadata operation")
					}
				case <-time.After(10 * time.Second):
					return fmt.Errorf("child readiness timed out")
				}
				select {
				case err := <-finished:
					return fmt.Errorf("metadata writer bypassed process lock: %v", err)
				case <-time.After(150 * time.Millisecond):
					return nil
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case err := <-finished:
				if err != nil {
					t.Fatalf("child: %v: %s", err, stderr.String())
				}
			case <-time.After(10 * time.Second):
				t.Fatal("writer did not proceed after unlock")
			}
			got, err := readMetaFile(dir)
			if err != nil {
				t.Fatal(err)
			}
			if operation == "set-panel" && got.NextLink != metadataNewLink || operation == "save" && !got.LinkHealthy {
				t.Fatal("child write was not persisted")
			}
		})
	}
}

func TestWriteFileAtomicUniqueTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "atomic")
	// A preexisting fixed-name temporary file is neither used nor removed by a writer.
	if err := os.WriteFile(path+".tmp", []byte("sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := range cap(errs) {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs <- writeFileAtomic(path, []byte(strings.Repeat(fmt.Sprint(i), 4096)), 0o600)
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	final, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	complete := false
	for i := range cap(errs) {
		if string(final) == strings.Repeat(fmt.Sprint(i), 4096) {
			complete = true
			break
		}
	}
	if !complete {
		t.Fatal("atomic target contains a partial or mixed write")
	}
	b, err := os.ReadFile(path + ".tmp")
	if err != nil || string(b) != "sentinel" {
		t.Fatal("writer reused the fixed temporary path")
	}
	leftovers, err := filepath.Glob(filepath.Join(dir, ".atomic.tmp-*"))
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("temporary files remain: %v, %v", leftovers, err)
	}
}

// The running agent reads immutable identity fields without metaMu. Saving a mutable link snapshot must not assign
// those fields again, even to the same values; -race detects that write/read collision.
func TestMetadataSaveDoesNotRewriteImmutableIdentity(t *testing.T) {
	dir, m := metadataFixture(t)
	m.Panel, m.SNI = "panel.example.com:443", "agent.example.com"
	if err := saveMeta(dir, &m); err != nil {
		t.Fatal(err)
	}
	ready, stop, result := make(chan struct{}), make(chan struct{}), make(chan bool, 1)
	go func() {
		close(ready)
		for {
			if m.NodeID != "node" || m.Panel != "panel.example.com:443" || m.SNI != "agent.example.com" {
				result <- false
				return
			}
			select {
			case <-stop:
				result <- true
				return
			default:
			}
		}
	}()
	defer func() {
		close(stop)
		if !<-result {
			t.Error("saving a link snapshot changed immutable identity fields")
		}
	}()
	<-ready
	for range 32 {
		if err := SetPanelLink(dir, metadataNewLink); err != nil {
			t.Fatal(err)
		}
		m.LinkHealthy = !m.LinkHealthy
		if err := saveMeta(dir, &m); err != nil {
			t.Fatal(err)
		}
		if m.NextLink != metadataNewLink || m.NextLinkRevision == "" {
			t.Fatal("save did not refresh pending intent")
		}
	}
}

// "enroll --force" can run while the agent is up. The agent's snapshot of the old enrollment must not overwrite it.
func TestMetadataSaveRefusesAnotherEnrollment(t *testing.T) {
	dir, m := metadataFixture(t)
	other := Meta{Panel: "other.example.com:443", SNI: "other-agent.example.com", NodeID: "other-node"}
	raw, err := json.Marshal(other)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, fileMeta)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	m.LinkHealthy = true
	if err := saveMeta(dir, &m); err == nil || !strings.Contains(err.Error(), "another enrollment") {
		t.Fatalf("saveMeta over another enrollment = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(raw, after) {
		t.Fatalf("%s changed: %v", fileMeta, err)
	}
}
