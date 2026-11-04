package provision

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"golang.org/x/crypto/ssh"
)

const panelSSHKeyAAD = "panel-ssh-key"

// GetPanelSSHKey lazily creates the panel's single ed25519 SSH identity and returns only its public half.
func (s *Service) GetPanelSSHKey(ctx context.Context, _ *connect.Request[adminv1.GetPanelSSHKeyRequest]) (*connect.Response[adminv1.GetPanelSSHKeyResponse], error) {
	key, created, err := s.ensurePanelSSHKey(ctx)
	if err != nil {
		s.cfg.Log.Error("load panel SSH key", "err", err)
		return nil, internalConnectError()
	}
	if created {
		if err := s.audit(ctx, "panel.ssh_key_create", map[string]string{"fingerprint": key.Fingerprint}); err != nil {
			s.cfg.Log.Error("audit panel SSH key creation", "err", err)
			return nil, internalConnectError()
		}
	}
	return connect.NewResponse(&adminv1.GetPanelSSHKeyResponse{PublicKey: key.PublicKey, Fingerprint: key.Fingerprint}), nil
}

func (s *Service) ensurePanelSSHKey(ctx context.Context) (store.PanelSSHKey, bool, error) {
	key, err := s.st.PanelSSHKey(ctx)
	if err == nil {
		return key, false, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return store.PanelSSHKey{}, false, err
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return store.PanelSSHKey{}, false, err
	}
	defer clear(private)
	publicKey, err := ssh.NewPublicKey(public)
	if err != nil {
		return store.PanelSSHKey{}, false, err
	}
	line := strings.TrimSuffix(string(ssh.MarshalAuthorizedKey(publicKey)), "\n") + " mistgate-panel"
	seed := private.Seed()
	ciphertext := s.vault.Seal(seed, panelSSHKeyAAD)
	clear(seed)
	candidate := store.PanelSSHKey{
		PrivateKey: ciphertext, PublicKey: line, Fingerprint: ssh.FingerprintSHA256(publicKey), CreatedAt: s.cfg.Now().UTC(),
	}
	created, err := s.st.CreatePanelSSHKeyIfAbsent(ctx, candidate)
	if err != nil {
		return store.PanelSSHKey{}, false, err
	}
	key, err = s.st.PanelSSHKey(ctx)
	return key, created, err
}

func (s *Service) panelKeySigner(ctx context.Context) (store.PanelSSHKey, ssh.Signer, ed25519.PrivateKey, error) {
	key, _, err := s.ensurePanelSSHKey(ctx)
	if err != nil {
		return store.PanelSSHKey{}, nil, nil, err
	}
	seed, err := s.vault.Open(key.PrivateKey, panelSSHKeyAAD)
	if err != nil {
		return store.PanelSSHKey{}, nil, nil, err
	}
	defer clear(seed)
	if len(seed) != ed25519.SeedSize {
		return store.PanelSSHKey{}, nil, nil, errors.New("provision: invalid sealed panel SSH key")
	}
	private := ed25519.NewKeyFromSeed(seed)
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		clear(private)
		return store.PanelSSHKey{}, nil, nil, err
	}
	return key, signer, private, nil
}

func (s *Service) dialPanelKey(ctx context.Context, target Target, username, hostFingerprint string) (*Connection, string, error) {
	key, signer, private, err := s.panelKeySigner(ctx)
	if err != nil {
		return nil, "", err
	}
	defer clear(private)
	conn, err := s.ssh.DialKey(ctx, target, username, signer, hostFingerprint)
	return conn, key.Fingerprint, err
}

// dialAccess uses a key already recorded for the node first, then a saved password that was last proven to work.
func (s *Service) dialAccess(ctx context.Context, access store.NodeServerAccess) (*Connection, bool, error) {
	target, err := NewTarget(access.SSHHost, uint32(access.SSHPort))
	if err != nil {
		return nil, false, err
	}
	var keyErr error
	if access.PanelKeyFingerprint != "" {
		key, _, err := s.ensurePanelSSHKey(ctx)
		if err != nil {
			return nil, false, err
		}
		if key.Fingerprint == access.PanelKeyFingerprint {
			conn, _, err := s.dialPanelKey(ctx, target, access.SSHUser, access.HostFingerprint)
			if err == nil {
				return conn, true, nil
			}
			if !errors.Is(err, ErrAuthRejected) {
				return nil, false, err
			}
			keyErr = err
		} else {
			keyErr = ErrAuthRejected
		}
	}
	if len(access.Password) > 0 && access.PasswordLogin {
		password, err := s.openAccessPassword(access.NodeID, access.Password)
		if err != nil {
			return nil, false, err
		}
		conn, err := s.ssh.DialAs(ctx, target, access.SSHUser, password, access.HostFingerprint)
		password = ""
		return conn, false, err
	}
	if keyErr != nil {
		return nil, false, keyErr
	}
	return nil, false, ErrAuthRejected
}
