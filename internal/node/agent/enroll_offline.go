package agent

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Offline enrollment: the first subscription host of a panel behind NAT cannot reach the panel before it is the host, so the
// panel enrolls it in the same SSH session (design/adr/0018-panel-behind-nat.md). `enroll --offline-csr` keeps a new key and
// prints its CSR; the panel signs it; `enroll --offline-finish` reads, from stdin, the role and the link address (secrets:
// never on the command line), the node certificate and the CA certificate.

// OfflineCSR starts an offline enrollment: it keeps a new key in enroll-pending.pem and returns the DER CSR for the panel. A
// pending key is reused, so running it again gives a CSR for the same key.
func OfflineCSR(stateDir string, force bool) ([]byte, error) {
	if stateDir == "" {
		return nil, errors.New("state dir is required")
	}
	if !force && IsEnrolled(stateDir) {
		return nil, errors.New("already enrolled; use --force to replace the identity")
	}
	_, csr, err := pendingKeyAndCSR(stateDir, false)
	return csr, err
}

// OfflineRole is the first line of `enroll --offline-finish`'s stdin, JSON.
type OfflineRole struct {
	LinkURL string       `json:"link_url"` // wss://<subscription name>/<secret prefix>/
	SubHost *SubHostMeta `json:"sub_host"`
}

// OfflineEnrollConfig is the input of `enroll --offline-finish`.
type OfflineEnrollConfig struct {
	StateDir string
	NodeID   string
	CASHA256 string // hex fingerprint of the panel CA certificate
	Role     OfflineRole
	// PEM holds the node certificate, then the CA certificate.
	PEM   []byte
	Force bool
}

// EnrollOffline finishes an OfflineCSR enrollment with the certificate the panel issued for its CSR: the CA must have the
// pinned fingerprint, the certificate must be for the pending key and chain to it. The node is then a link-only agent with
// the subscription host role. Everything is written atomically, identity.pem last: an interrupted run leaves the node not
// enrolled, and running it again finishes it.
func EnrollOffline(cfg OfflineEnrollConfig) (Meta, error) {
	pin, err := parsePin(cfg.CASHA256)
	if err != nil {
		return Meta{}, err
	}
	if _, err := parseLinkBase(cfg.Role.LinkURL); err != nil {
		return Meta{}, errors.New("the link address is invalid") // it holds a secret: not echoed
	}
	host := cfg.Role.SubHost
	if host == nil || len(host.Names) == 0 || host.Path == "" {
		return Meta{}, errors.New("the subscription host role is missing")
	}
	if cfg.StateDir == "" || !validLinkNodeID(cfg.NodeID) {
		return Meta{}, errors.New("state dir and node id are required")
	}
	if !cfg.Force && IsEnrolled(cfg.StateDir) {
		return Meta{}, errors.New("already enrolled; use --force to replace the identity")
	}
	if _, err := os.Stat(filepath.Join(cfg.StateDir, filePendingKey)); err != nil {
		return Meta{}, errors.New("no pending enrollment key: run `mistgate-node enroll --offline-csr` first")
	}
	key, _, err := pendingKeyAndCSR(cfg.StateDir, false) // loads the pending key
	if err != nil {
		return Meta{}, err
	}
	var certPEM, caPEM []byte
	for rest := cfg.PEM; certPEM == nil || caPEM == nil; {
		var blk *pem.Block
		if blk, rest = pem.Decode(rest); blk == nil {
			return Meta{}, errors.New("expected the node certificate and then the CA certificate in PEM")
		}
		if blk.Type != "CERTIFICATE" {
			continue
		}
		if certPEM == nil {
			certPEM = pem.EncodeToMemory(blk)
		} else {
			caPEM = pem.EncodeToMemory(blk)
		}
	}
	caCert, err := parseOneCert(caPEM)
	if err != nil {
		return Meta{}, fmt.Errorf("panel CA certificate: %w", err)
	}
	if sum := sha256.Sum256(caCert.Raw); subtle.ConstantTimeCompare(sum[:], pin[:]) != 1 {
		return Meta{}, errors.New("the CA certificate does not match --ca-sha256")
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	if _, err := checkLeaf(certPEM, key, pool); err != nil {
		return Meta{}, fmt.Errorf("node certificate: %w", err)
	}
	meta := Meta{NodeID: cfg.NodeID, Link: cfg.Role.LinkURL, SubHost: host}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return Meta{}, err
	}
	_ = os.Chmod(cfg.StateDir, 0o700)
	for _, f := range []struct {
		name string
		data []byte
	}{
		{fileCA, caPEM},
		{fileMeta, metaJSON},
		{fileIdentity, identityPEM(key, string(certPEM))}, // last: its presence means "enrolled"
	} {
		if err := writeFileAtomic(filepath.Join(cfg.StateDir, f.name), f.data, 0o600); err != nil {
			return Meta{}, err
		}
	}
	if err := os.Remove(filepath.Join(cfg.StateDir, filePendingKey)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Meta{}, err
	}
	return meta, nil
}

// validLinkNodeID is the shape of a node id the panel gives (nod_ and 26 base32 letters).
func validLinkNodeID(id string) bool {
	if len(id) != len("nod_")+26 || id[:4] != "nod_" {
		return false
	}
	for _, ch := range id[4:] {
		if !('a' <= ch && ch <= 'z') && !('2' <= ch && ch <= '7') {
			return false
		}
	}
	return true
}
