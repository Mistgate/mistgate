package agent

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestMetadataPromotionRejectsSupersededIntent(t *testing.T) {
	for _, requested := range []string{metadataOldLink, metadataNextLink, metadataNewLink} {
		t.Run(requested, func(t *testing.T) {
			dir, m := metadataFixture(t)
			attemptRevision := m.NextLinkRevision
			before := m
			if err := SetPanelLink(dir, requested); err != nil {
				t.Fatal(err)
			}
			latest, err := readMetaFile(dir)
			if err != nil {
				t.Fatal(err)
			}
			promoted, err := promoteNextLink(dir, &m, metadataNextLink, attemptRevision, 123)
			if err != nil || promoted {
				t.Fatalf("superseded promotion = %v, %v", promoted, err)
			}
			before.NextLink, before.NextLinkRevision = latest.NextLink, latest.NextLinkRevision
			if m != before {
				t.Fatalf("rejected promotion changed more than pending intent: got %+v, want %+v", m, before)
			}
			got, err := readMetaFile(dir)
			if err != nil {
				t.Fatal(err)
			}
			if got != latest {
				t.Fatal("rejected promotion changed persisted metadata")
			}
		})
	}
}

func TestMetadataPromotionRequiresMatchingPendingAddress(t *testing.T) {
	dir, m := metadataFixture(t)
	before := m
	promoted, err := promoteNextLink(dir, &m, metadataNewLink, m.NextLinkRevision, 123)
	if err != nil || promoted || m != before {
		t.Fatalf("mismatched candidate promotion = %v, %v; metadata %+v", promoted, err, m)
	}
	got, err := readMetaFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != before {
		t.Fatal("mismatched candidate changed persisted metadata")
	}
}

func TestMetadataPromotionPreservesKnownGoodLink(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prev    string
		healthy bool
		want    string
	}{
		{name: "first", want: metadataOldLink},
		{name: "unproven", prev: metadataNewLink, want: metadataNewLink},
		{name: "healthy", prev: metadataNewLink, healthy: true, want: metadataOldLink},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, m := metadataFixture(t)
			m.PrevLink, m.LinkHealthy = tc.prev, tc.healthy
			if err := saveMeta(dir, &m); err != nil {
				t.Fatal(err)
			}
			attemptRevision := m.NextLinkRevision
			// Identity fields in the caller must not be assigned from the persisted snapshot.
			m.NodeID, m.Panel, m.SNI = "caller-node", "caller-panel", "caller-sni"
			promoted, err := promoteNextLink(dir, &m, metadataNextLink, attemptRevision, 123)
			if err != nil || !promoted {
				t.Fatalf("promotion = %v, %v", promoted, err)
			}
			if m.Link != metadataNextLink || m.PrevLink != tc.want || m.LinkHealthy || m.LinkSince != 123 || m.NextLink != "" || m.NextLinkRevision == attemptRevision || m.NextLinkRevision == "" {
				t.Fatalf("incorrect promoted metadata: %+v", m)
			}
			if m.NodeID != "caller-node" || m.Panel != "caller-panel" || m.SNI != "caller-sni" {
				t.Fatal("promotion rewrote immutable caller identity")
			}
			got, err := readMetaFile(dir)
			if err != nil {
				t.Fatal(err)
			}
			want := m
			want.NodeID, want.Panel, want.SNI = "node", "", ""
			if got != want {
				t.Fatalf("persisted promotion = %+v, want %+v", got, want)
			}
			if promoted, err := promoteNextLink(dir, &m, metadataNextLink, attemptRevision, 456); err != nil || promoted {
				t.Fatalf("consumed revision promoted again: %v, %v", promoted, err)
			}
		})
	}
}

func TestMetadataRollbackConsumesDestinationIntent(t *testing.T) {
	for _, tc := range []struct {
		name      string
		requested string
		newer     bool
	}{
		{name: "matching-pending", requested: metadataOldLink},
		{name: "newer-matching-pending", requested: metadataOldLink, newer: true},
		{name: "unrelated-pending", requested: metadataNewLink},
		{name: "newer-unrelated-pending", requested: metadataNewLink, newer: true},
		{name: "newer-clear", requested: metadataNextLink, newer: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, m := metadataFixture(t)
			m.Link, m.PrevLink, m.LinkSince = metadataNextLink, metadataOldLink, 123
			m.LinkHealthy = true
			m.NextLink = tc.requested
			if err := saveMeta(dir, &m); err != nil {
				t.Fatal(err)
			}
			if tc.newer {
				// The rollback attempt already observed m before the CLI reasserts its intent.
				if err := SetPanelLink(dir, tc.requested); err != nil {
					t.Fatal(err)
				}
			}
			latest, err := readMetaFile(dir)
			if err != nil {
				t.Fatal(err)
			}
			m.NodeID, m.Panel, m.SNI = "caller-node", "caller-panel", "caller-sni"
			rolledBack, err := rollbackPreviousLink(dir, &m, metadataOldLink)
			if err != nil || !rolledBack {
				t.Fatalf("rollback = %v, %v", rolledBack, err)
			}
			want := latest
			want.Link, want.PrevLink, want.LinkSince = metadataOldLink, "", 0
			want.LinkHealthy = false
			if latest.NextLink == metadataOldLink {
				if m.NextLinkRevision == latest.NextLinkRevision || m.NextLinkRevision == "" {
					t.Fatal("rollback did not revise the fulfilled pending intent")
				}
				want.NextLink, want.NextLinkRevision = "", m.NextLinkRevision
			}
			got, err := readMetaFile(dir)
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("persisted rollback = %+v, want %+v", got, want)
			}
			want.NodeID, want.Panel, want.SNI = "caller-node", "caller-panel", "caller-sni"
			if m != want {
				t.Fatalf("caller rollback = %+v, want %+v", m, want)
			}
			if rolledBack, err := rollbackPreviousLink(dir, &m, metadataOldLink); err != nil || rolledBack {
				t.Fatalf("rollback repeated = %v, %v", rolledBack, err)
			}
		})
	}
}

func TestMetadataRollbackRejectsStalePair(t *testing.T) {
	for _, changed := range []string{"current", "fallback"} {
		t.Run(changed, func(t *testing.T) {
			dir, m := metadataFixture(t)
			m.Link, m.PrevLink, m.LinkSince = metadataNextLink, metadataOldLink, 123
			if err := saveMeta(dir, &m); err != nil {
				t.Fatal(err)
			}
			latest := m
			if changed == "current" {
				latest.Link = metadataNewLink
			} else {
				latest.PrevLink = metadataNewLink
			}
			latest.NextLink = metadataNewLink
			if err := saveMeta(dir, &latest); err != nil {
				t.Fatal(err)
			}
			before := m
			rolledBack, err := rollbackPreviousLink(dir, &m, metadataOldLink)
			if err != nil || rolledBack {
				t.Fatalf("stale rollback = %v, %v", rolledBack, err)
			}
			before.NextLink, before.NextLinkRevision = latest.NextLink, latest.NextLinkRevision
			if m != before {
				t.Fatalf("stale rollback changed more than pending intent: got %+v, want %+v", m, before)
			}
			got, err := readMetaFile(dir)
			if err != nil {
				t.Fatal(err)
			}
			if got != latest {
				t.Fatal("stale rollback changed persisted metadata")
			}
		})
	}
}

func TestMetadataPromotionConsumesSelfIntentWithoutTransition(t *testing.T) {
	for _, tc := range []struct {
		name    string
		prev    string
		healthy bool
	}{
		{name: "no-fallback"},
		{name: "unproven", prev: metadataNewLink},
		{name: "healthy", prev: metadataNewLink, healthy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, m := metadataFixture(t)
			m.NextLink, m.PrevLink = m.Link, tc.prev
			m.LinkHealthy, m.LinkSince = tc.healthy, 123
			if err := saveMeta(dir, &m); err != nil {
				t.Fatal(err)
			}
			want := m
			attemptRevision := m.NextLinkRevision
			promoted, err := promoteNextLink(dir, &m, m.Link, attemptRevision, 456)
			if err != nil || promoted {
				t.Fatalf("self promotion = %v, %v", promoted, err)
			}
			if m.NextLinkRevision == attemptRevision || m.NextLinkRevision == "" {
				t.Fatal("self promotion did not revise the fulfilled pending intent")
			}
			want.NextLink, want.NextLinkRevision = "", m.NextLinkRevision
			if m != want {
				t.Fatalf("self promotion changed transition state: got %+v, want %+v", m, want)
			}
			got, err := readMetaFile(dir)
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("persisted self promotion = %+v, want %+v", got, want)
			}
		})
	}
}

func TestMetadataStartupReadSubprocessHelper(t *testing.T) {
	dir := os.Getenv("MISTGATE_META_STARTUP_READ_DIR")
	if dir == "" {
		return
	}
	fmt.Println("metadata-read-ready")
	m, err := readMetaFile(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.Link != metadataOldLink || m.NextLink != metadataNextLink {
		t.Fatalf("unexpected startup metadata: %+v", m)
	}
}

func TestMetadataStartupReadWaitsForLock(t *testing.T) {
	dir, _ := metadataFixture(t)
	cmd := exec.Command(os.Args[0], "-test.run=^TestMetadataStartupReadSubprocessHelper$")
	cmd.Env = append(os.Environ(), "MISTGATE_META_STARTUP_READ_DIR="+dir)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
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
			ready <- scanner.Scan() && scanner.Text() == "metadata-read-ready"
		}()
		select {
		case ok := <-ready:
			if !ok {
				return fmt.Errorf("child did not reach startup metadata read")
			}
		case <-time.After(10 * time.Second):
			return fmt.Errorf("startup reader readiness timed out")
		}
		select {
		case err := <-finished:
			return fmt.Errorf("startup metadata read bypassed lock: %v", err)
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
			t.Fatalf("startup reader: %v: %s", err, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("startup metadata read did not proceed after unlock")
	}
}
