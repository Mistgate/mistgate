package agent

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// withMetaLock locks a separate, stable inode: agent.json itself is replaced by rename.
// Never remove the lock file, including on unlock, or competing processes could lock different inodes.
func withMetaLock(dir string, fn func() error) error {
	f, err := os.OpenFile(filepath.Join(dir, fileMeta+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := lockMetaFile(f); err != nil {
		return err
	}
	defer unlockMetaFile(f)
	return fn()
}

// readMetaFileUnlocked requires withMetaLock, including for reads during metadata transactions.
func readMetaFileUnlocked(dir string) (Meta, error) {
	var m Meta
	b, err := os.ReadFile(filepath.Join(dir, fileMeta))
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return m, fmt.Errorf("%s: %w", fileMeta, err)
	}
	return m, nil
}

// reviseNextLink gives even a clear intent a revision, avoiding ABA when the same address is requested again.
func reviseNextLink(m *Meta) error {
	var revision [16]byte
	if _, err := rand.Read(revision[:]); err != nil {
		return err
	}
	m.NextLinkRevision = hex.EncodeToString(revision[:])
	return nil
}

// writeMeta requires withMetaLock. All readers see either the old or new complete document.
func writeMeta(dir string, m Meta) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, fileMeta), b, 0o600)
}

// saveMeta persists the agent snapshot, merging a newer next-link intent under the same lock used by set-panel.
// The revision belongs to the observed intent, so stale snapshots cannot consume a later request.
// Refresh only pending intent after success. Immutable identity fields have lock-free readers and must not be rewritten.
// The caller owns mutable metadata (the running agent holds metaMu).
func saveMeta(dir string, m *Meta) error {
	return withMetaLock(dir, func() error {
		next := *m
		current, err := readMetaFileUnlocked(dir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil && current.NodeID != m.NodeID {
			// "enroll --force" ran while the agent was up: this snapshot belongs to the old enrollment.
			return errors.New(fileMeta + " belongs to another enrollment")
		}
		if err == nil && current.NextLinkRevision != next.NextLinkRevision {
			next.NextLink = current.NextLink
			next.NextLinkRevision = current.NextLinkRevision
		} else if next.NextLink != current.NextLink {
			if err := reviseNextLink(&next); err != nil {
				return err
			}
		}
		if err := writeMeta(dir, next); err != nil {
			return err
		}
		m.NextLink, m.NextLinkRevision = next.NextLink, next.NextLinkRevision
		return nil
	})
}

// promoteNextLink commits only the pending intent that authorized this connection attempt.
// A matching intent for the current address is consumed without changing fallback or health state and returns false.
// The caller owns mutable metadata (the running agent holds metaMu).
func promoteNextLink(dir string, m *Meta, next, attemptRevision string, since int64) (bool, error) {
	promoted := false
	err := withMetaLock(dir, func() error {
		latest, err := readMetaFileUnlocked(dir)
		if err != nil {
			return err
		}
		if latest.NextLinkRevision != attemptRevision || latest.NextLink != next {
			m.NextLink, m.NextLinkRevision = latest.NextLink, latest.NextLinkRevision
			return nil
		}
		changed := latest.Link != next
		if changed {
			if (latest.PrevLink == "" && !latest.PrevMTLS) || latest.LinkHealthy {
				latest.PrevLink, latest.PrevMTLS = latest.Link, latest.Link == ""
			}
			latest.Link = next
			latest.LinkHealthy = false
			latest.LinkSince = since
			if latest.PrevLink == next {
				// Returning explicitly to the retained fallback is a rollback, not a new unproven address with
				// itself as its fallback. A proven current address was selected above and remains a valid fallback.
				latest.PrevLink, latest.LinkSince = "", 0
			}
		}
		latest.NextLink = ""
		if err := reviseNextLink(&latest); err != nil {
			return err
		}
		if err := writeMeta(dir, latest); err != nil {
			return err
		}
		m.Link, m.PrevLink, m.PrevMTLS = latest.Link, latest.PrevLink, latest.PrevMTLS
		m.LinkHealthy, m.LinkSince = latest.LinkHealthy, latest.LinkSince
		m.NextLink, m.NextLinkRevision = latest.NextLink, latest.NextLinkRevision
		promoted = changed
		return nil
	})
	return promoted, err
}

// rollbackPreviousLink makes the observed fallback current only while the persisted current/fallback pair still matches.
// A pending request for the rollback destination is fulfilled even if its revision changed during the connection attempt.
// Unrelated pending requests survive. False means no rollback was committed; errors leave the caller unchanged.
// The caller owns mutable metadata (the running agent holds metaMu); immutable identity fields are never refreshed.
func rollbackPreviousLink(dir string, m *Meta, prev string) (bool, error) {
	rolledBack := false
	err := withMetaLock(dir, func() error {
		latest, err := readMetaFileUnlocked(dir)
		if err != nil {
			return err
		}
		if (prev == "" && !m.PrevMTLS) || m.PrevLink != prev || latest.PrevLink != prev ||
			latest.PrevMTLS != m.PrevMTLS || latest.Link != m.Link || latest.Link == prev {
			m.NextLink, m.NextLinkRevision = latest.NextLink, latest.NextLinkRevision
			return nil
		}
		latest.Link, latest.PrevLink, latest.PrevMTLS, latest.LinkSince = prev, "", false, 0
		latest.LinkHealthy = false
		if prev != "" && latest.NextLink == prev {
			latest.NextLink = ""
			if err := reviseNextLink(&latest); err != nil {
				return err
			}
		}
		if err := writeMeta(dir, latest); err != nil {
			return err
		}
		m.Link, m.PrevLink, m.PrevMTLS = latest.Link, latest.PrevLink, latest.PrevMTLS
		m.LinkHealthy, m.LinkSince = latest.LinkHealthy, latest.LinkSince
		m.NextLink, m.NextLinkRevision = latest.NextLink, latest.NextLinkRevision
		rolledBack = true
		return nil
	})
	return rolledBack, err
}
