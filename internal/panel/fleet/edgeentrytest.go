//go:build edgeentrytest

package fleet

import (
	"crypto/ecdsa"

	"github.com/mistgate/mistgate/internal/panel/store"
)

// IssueNodeCert signs a node certificate for the edge bridge test. Built only with that tag.
func (f *Fleet) IssueNodeCert(nodeID string, pub *ecdsa.PublicKey) (store.CertRow, error) {
	return f.ca.issueNode(nodeID, pub, f.now().UTC())
}
