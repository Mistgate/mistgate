//go:build edgeentrytest

package fleet

import (
	"crypto/ecdsa"

	"github.com/mistgate/mistgate/internal/panel/store"
)

// IssueNodeCert signs a node certificate for the edge bridge test (scripts/test-edge-entry.ps1): the D1 store cannot
// enrol yet (Enroll is still a transaction), so the test inserts the returned row itself. Built only with that tag.
func (f *Fleet) IssueNodeCert(nodeID string, pub *ecdsa.PublicKey) (store.CertRow, error) {
	return f.ca.issueNode(nodeID, pub, f.now().UTC())
}
