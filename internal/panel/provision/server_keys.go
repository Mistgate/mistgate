package provision

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"golang.org/x/crypto/ssh"
)

const maxAuthorizedKeysBytes = 64 << 10

const readAuthorizedKeysScript = `set -eu
d="$HOME/.ssh"; f="$d/authorized_keys"
[ ! -L "$d" ] && [ ! -L "$f" ] || exit 12
if [ -e "$d" ] && [ ! -d "$d" ]; then exit 12; fi
if [ -e "$f" ] && [ ! -f "$f" ]; then exit 12; fi
if [ -f "$f" ]; then
  size=$(wc -c < "$f")
  [ "$size" -le 65536 ] || exit 14
  sum=$(sha256sum < "$f" | cut -d' ' -f1)
  printf 'sha256:%s\n' "$sum"
  cat -- "$f"
else
  printf 'none\n'
fi`

const writeAuthorizedKeysScript = `set -eu
umask 077
d="$HOME/.ssh"; f="$d/authorized_keys"
[ ! -L "$d" ] && [ ! -L "$f" ] || exit 12
if [ -e "$d" ] && [ ! -d "$d" ]; then exit 12; fi
if [ -e "$f" ] && [ ! -f "$f" ]; then exit 12; fi
cur=none
if [ -f "$f" ]; then
  size=$(wc -c < "$f")
  [ "$size" -le 65536 ] || exit 14
  cur=$(sha256sum < "$f" | cut -d' ' -f1)
fi
[ "$cur" = "$1" ] || exit 13
[ -d "$d" ] || mkdir "$d"
chmod 700 "$d"
tmp=$(mktemp "$d/.authorized_keys.XXXXXX")
trap 'rm -f "$tmp"' EXIT HUP INT TERM
cat > "$tmp"
chmod 600 "$tmp"
cur=none
if [ -f "$f" ]; then cur=$(sha256sum < "$f" | cut -d' ' -f1); fi
[ "$cur" = "$1" ] || exit 13
mv -f "$tmp" "$f"
command -v restorecon >/dev/null 2>&1 && restorecon "$f" || true`

type parsedAuthorizedKey struct {
	line       []byte
	publicKey  ssh.PublicKey
	comment    string
	restricted bool
}

// CanonicalPublicServerKey validates and canonicalizes a single public key for planning tools.
func CanonicalPublicServerKey(input string) (string, *adminv1.NodeServerKey, error) {
	return canonicalPublicKey(input)
}

func canonicalPublicKey(input string) (string, *adminv1.NodeServerKey, error) {
	if len(input) == 0 || len(input) > 16<<10 {
		return "", nil, errors.New("ssh_public_key_invalid")
	}
	input = strings.TrimSpace(input)
	if strings.ContainsAny(input, "\r\n") {
		return "", nil, errors.New("ssh_public_key_invalid")
	}
	fields := strings.Fields(input)
	if len(fields) < 2 || strings.Contains(fields[0], "-cert-v01@openssh.com") {
		return "", nil, errors.New("ssh_public_key_invalid")
	}
	blob, err := base64.StdEncoding.DecodeString(fields[1])
	if err != nil {
		return "", nil, errors.New("ssh_public_key_invalid")
	}
	key, err := ssh.ParsePublicKey(blob)
	if err != nil || key.Type() != fields[0] {
		return "", nil, errors.New("ssh_public_key_invalid")
	}
	if _, ok := key.(*ssh.Certificate); ok {
		return "", nil, errors.New("ssh_public_key_invalid")
	}
	bits, accepted := serverKeyBits(key)
	if !accepted {
		return "", nil, errors.New("ssh_public_key_invalid")
	}
	if key.Type() == ssh.KeyAlgoRSA && bits < 2048 {
		return "", nil, errors.New("ssh_public_key_weak")
	}
	comment := cleanSSHComment(strings.Join(fields[2:], " "))
	if len([]rune(comment)) > 100 {
		return "", nil, errors.New("ssh_public_key_invalid")
	}
	line := strings.TrimSuffix(string(ssh.MarshalAuthorizedKey(key)), "\n")
	if comment != "" {
		line += " " + comment
	}
	return line, &adminv1.NodeServerKey{
		Type: key.Type(), Bits: uint32(bits), Fingerprint: ssh.FingerprintSHA256(key), Comment: comment, Lines: 1,
	}, nil
}

func serverKeyBits(key ssh.PublicKey) (int, bool) {
	switch key.Type() {
	case ssh.KeyAlgoED25519:
		return 256, true
	case ssh.KeyAlgoECDSA256:
		return 256, true
	case ssh.KeyAlgoECDSA384:
		return 384, true
	case ssh.KeyAlgoECDSA521:
		return 521, true
	case ssh.KeyAlgoRSA:
		crypto, ok := key.(ssh.CryptoPublicKey)
		if !ok {
			return 0, false
		}
		public, ok := crypto.CryptoPublicKey().(*rsa.PublicKey)
		if !ok || public.N == nil {
			return 0, false
		}
		return public.N.BitLen(), true
	case "sk-ssh-ed25519@openssh.com", "sk-ecdsa-sha2-nistp256@openssh.com":
		return 256, true
	default:
		return 0, false
	}
}

func cleanSSHComment(value string) string {
	var b strings.Builder
	for _, r := range value {
		if !unicode.IsControl(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func displaySSHComment(value string) string {
	cleaned := []rune(cleanSSHComment(value))
	if len(cleaned) > 100 {
		cleaned = cleaned[:100]
	}
	return string(cleaned)
}

func parseAuthorizedKeys(contents []byte, panelFingerprint string) ([]*adminv1.NodeServerKey, uint32) {
	keys := make([]*adminv1.NodeServerKey, 0)
	byFingerprint := make(map[string]*adminv1.NodeServerKey)
	var other uint32
	for len(contents) > 0 {
		line := contents
		if at := bytes.IndexByte(contents, '\n'); at >= 0 {
			line, contents = contents[:at+1], contents[at+1:]
		} else {
			contents = nil
		}
		parsed, ok := parseAuthorizedKeyLine(line)
		if !ok {
			other++
			continue
		}
		fingerprint := ssh.FingerprintSHA256(parsed.publicKey)
		key := byFingerprint[fingerprint]
		if key == nil {
			bits, _ := serverKeyBits(parsed.publicKey)
			key = &adminv1.NodeServerKey{
				Type: parsed.publicKey.Type(), Bits: uint32(bits), Fingerprint: fingerprint,
				Comment: displaySSHComment(parsed.comment), PanelKey: fingerprint == panelFingerprint,
			}
			byFingerprint[fingerprint] = key
			keys = append(keys, key)
		}
		key.Lines++
		key.Restricted = key.Restricted || parsed.restricted
		if key.Comment == "" && parsed.comment != "" {
			key.Comment = displaySSHComment(parsed.comment)
		}
	}
	return keys, other
}

func parseAuthorizedKeyLine(line []byte) (parsedAuthorizedKey, bool) {
	line = bytes.TrimSuffix(line, []byte("\n"))
	line = bytes.TrimSuffix(line, []byte("\r"))
	trimmed := strings.TrimSpace(string(line))
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return parsedAuthorizedKey{}, false
	}
	key, comment, options, rest, err := ssh.ParseAuthorizedKey([]byte(trimmed))
	if err != nil || len(strings.TrimSpace(string(rest))) != 0 {
		return parsedAuthorizedKey{}, false
	}
	return parsedAuthorizedKey{line: line, publicKey: key, comment: comment, restricted: len(options) > 0}, true
}

func withKey(contents []byte, canonical string) []byte {
	canonicalBytes := []byte(canonical)
	keyLine, ok := parseAuthorizedKeyLine(canonicalBytes)
	if !ok {
		return append([]byte(nil), contents...)
	}
	for rest := contents; len(rest) > 0; {
		line := rest
		if at := bytes.IndexByte(rest, '\n'); at >= 0 {
			line, rest = rest[:at+1], rest[at+1:]
		} else {
			rest = nil
		}
		// A line of the same key with options (from=, command=) counts too: a bare copy after it would let the key in
		// without the owner's restriction (sshd tries the next line when one refuses the source).
		parsed, valid := parseAuthorizedKeyLine(line)
		if valid && ssh.FingerprintSHA256(parsed.publicKey) == ssh.FingerprintSHA256(keyLine.publicKey) {
			return append([]byte(nil), contents...)
		}
	}
	out := append([]byte(nil), contents...)
	if len(out) > 0 && out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	out = append(out, canonicalBytes...)
	return append(out, '\n')
}

func withoutKey(contents []byte, fingerprint string) []byte {
	out := make([]byte, 0, len(contents))
	for rest := contents; len(rest) > 0; {
		line := rest
		if at := bytes.IndexByte(rest, '\n'); at >= 0 {
			line, rest = rest[:at+1], rest[at+1:]
		} else {
			rest = nil
		}
		parsed, valid := parseAuthorizedKeyLine(line)
		if !valid || ssh.FingerprintSHA256(parsed.publicKey) != fingerprint {
			out = append(out, line...)
		}
	}
	return out
}

func (s *Service) installPanelSSHKey(ctx context.Context, conn *Connection, target Target, username, hostFingerprint string) (string, error) {
	key, _, err := s.ensurePanelSSHKey(ctx)
	if err != nil {
		return "", err
	}
	contents, digest, err := readRemoteAuthorizedKeys(ctx, conn)
	if err != nil {
		return "", err
	}
	updated := withKey(contents, key.PublicKey)
	wrote := !bytes.Equal(updated, contents)
	if wrote {
		if err := writeRemoteAuthorizedKeys(ctx, conn, updated, digest); err != nil {
			return "", err
		}
	}
	verified, _, err := s.dialPanelKey(ctx, target, username, hostFingerprint)
	if err != nil {
		if wrote {
			rollbackDigest := authorizedKeysDigest(updated)
			if rollbackErr := writeRemoteAuthorizedKeys(ctx, conn, contents, rollbackDigest); rollbackErr != nil {
				s.cfg.Log.Warn("rollback unaccepted panel SSH key", "code", publicAuthorizedKeysCode(rollbackErr))
			}
		}
		return "", errors.New("ssh_panel_key_not_accepted")
	}
	_ = verified.Close()
	return key.Fingerprint, nil
}

func authorizedKeysDigest(contents []byte) string {
	digest := sha256.Sum256(contents)
	return hex.EncodeToString(digest[:])
}

func publicAuthorizedKeysCode(err error) string {
	if err == nil {
		return ""
	}
	switch err.Error() {
	case "ssh_panel_key_not_accepted", "authorized_keys_changed", "authorized_keys_unsafe", "authorized_keys_too_large",
		"ssh_authentication_failed", "node_server_password_not_saved":
		return err.Error()
	default:
		return "ssh_authorized_keys_unavailable"
	}
}

// savedAccessError is a failed login with the saved access, coded like sshConnectError but never Unauthenticated: to the
// web client that code is the admin's own session, and the key list is a query whose error would sign the admin out.
func savedAccessError(err error) error {
	if e := sshConnectError(err); connect.CodeOf(e) != connect.CodeUnauthenticated {
		return e
	}
	return connect.NewError(connect.CodeFailedPrecondition, errors.New("ssh_authentication_failed"))
}

func keyOperationError(err error) error {
	if err == nil {
		return nil
	}
	if strings.HasPrefix(err.Error(), "authorized_keys_") {
		return authorizedKeysConnectError(err)
	}
	if err.Error() == "ssh_panel_key_not_accepted" {
		return connect.NewError(connect.CodeFailedPrecondition, err)
	}
	return connect.NewError(connect.CodeUnavailable, errors.New(publicAuthorizedKeysCode(err)))
}

func (s *Service) AddNodeServerKey(ctx context.Context, req *connect.Request[adminv1.AddNodeServerKeyRequest]) (*connect.Response[adminv1.AddNodeServerKeyResponse], error) {
	if err := s.cfg.StepUp(ctx); err != nil {
		return nil, err
	}
	if req.Msg.NodeId == "" || len(req.Msg.NodeId) > 64 || !req.Msg.Confirm || req.Msg.PanelKey && req.Msg.PublicKey != "" || !req.Msg.PanelKey && req.Msg.PublicKey == "" {
		return nil, invalidArgument("confirmation, node and one public key are required")
	}
	s.accessMu.Lock()
	defer s.accessMu.Unlock()
	access, err := s.st.NodeServerAccess(ctx, req.Msg.NodeId)
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("node_server_access_not_found"))
	}
	if err != nil {
		s.cfg.Log.Error("read SSH access before adding a server key", "err", err)
		return nil, internalConnectError()
	}
	if access.NodeRetired {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("node_retired"))
	}
	var canonical string
	var entry *adminv1.NodeServerKey
	var panelFingerprint string
	if req.Msg.PanelKey {
		panel, _, err := s.ensurePanelSSHKey(ctx)
		if err != nil {
			return nil, internalConnectError()
		}
		canonical = panel.PublicKey
		panelFingerprint = panel.Fingerprint
		_, entry, err = canonicalPublicKey(canonical)
		if err != nil {
			return nil, internalConnectError()
		}
	} else {
		canonical, entry, err = canonicalPublicKey(req.Msg.PublicKey)
		if err != nil {
			return nil, publicKeyConnectError(err)
		}
		panel, panelErr := s.st.PanelSSHKey(ctx)
		if panelErr == nil && panel.Fingerprint == entry.Fingerprint {
			panelFingerprint = panel.Fingerprint
		} else if panelErr != nil && !errors.Is(panelErr, store.ErrNotFound) {
			return nil, internalConnectError()
		}
	}
	conn, _, err := s.dialAccess(ctx, access)
	if err != nil {
		return nil, savedAccessError(err)
	}
	defer conn.Close()
	contents, digest, err := readRemoteAuthorizedKeys(ctx, conn)
	if err != nil {
		return nil, keyOperationError(err)
	}
	updated := withKey(contents, canonical)
	wrote := !bytes.Equal(updated, contents)
	if wrote {
		if err := writeRemoteAuthorizedKeys(ctx, conn, updated, digest); err != nil {
			return nil, keyOperationError(err)
		}
	}
	if panelFingerprint != "" {
		target, err := NewTarget(access.SSHHost, uint32(access.SSHPort))
		if err != nil {
			return nil, internalConnectError()
		}
		verified, _, verifyErr := s.dialPanelKey(ctx, target, access.SSHUser, access.HostFingerprint)
		if verifyErr != nil {
			if wrote {
				if rollbackErr := writeRemoteAuthorizedKeys(ctx, conn, contents, authorizedKeysDigest(updated)); rollbackErr != nil {
					s.cfg.Log.Warn("rollback unaccepted panel SSH key", "code", publicAuthorizedKeysCode(rollbackErr))
				}
			}
			// The recorded panel key stays: a proving login that failed for a moment must not make dialAccess skip it.
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("ssh_panel_key_not_accepted"))
		}
		_ = verified.Close()
		if err := s.st.SetNodeServerPanelKey(ctx, access.NodeID, panelFingerprint); err != nil {
			if wrote {
				if rollbackErr := writeRemoteAuthorizedKeys(ctx, conn, contents, authorizedKeysDigest(updated)); rollbackErr != nil {
					s.cfg.Log.Warn("rollback unrecorded panel SSH key", "code", publicAuthorizedKeysCode(rollbackErr))
				}
			}
			if errors.Is(err, store.ErrConflict) {
				return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("node_retired"))
			}
			s.cfg.Log.Error("save verified panel SSH key access", "err", err)
			return nil, internalConnectError()
		}
		entry.PanelKey = true
	}
	keys, _ := parseAuthorizedKeys(updated, panelFingerprint)
	for _, item := range keys {
		if item.Fingerprint == entry.Fingerprint {
			entry = item
			break
		}
	}
	if err := s.audit(ctx, "node.ssh_key_add", map[string]string{
		"node_id": access.NodeID, "fingerprint": entry.Fingerprint, "type": entry.Type, "panel_key": strconv.FormatBool(panelFingerprint != ""),
	}); err != nil {
		return nil, internalConnectError()
	}
	return connect.NewResponse(&adminv1.AddNodeServerKeyResponse{Key: entry}), nil
}

func publicKeyConnectError(err error) error {
	if err != nil && err.Error() == "ssh_public_key_weak" {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewError(connect.CodeInvalidArgument, errors.New("ssh_public_key_invalid"))
}

func (s *Service) RemoveNodeServerKey(ctx context.Context, req *connect.Request[adminv1.RemoveNodeServerKeyRequest]) (*connect.Response[adminv1.RemoveNodeServerKeyResponse], error) {
	if err := s.cfg.StepUp(ctx); err != nil {
		return nil, err
	}
	if req.Msg.NodeId == "" || len(req.Msg.NodeId) > 64 || !req.Msg.Confirm || !validFingerprint(req.Msg.Fingerprint) {
		return nil, invalidArgument("confirmation, node and key fingerprint are required")
	}
	s.accessMu.Lock()
	defer s.accessMu.Unlock()
	access, err := s.st.NodeServerAccess(ctx, req.Msg.NodeId)
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("node_server_access_not_found"))
	}
	if err != nil {
		s.cfg.Log.Error("read SSH access before removing a server key", "err", err)
		return nil, internalConnectError()
	}
	if access.NodeRetired {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("node_retired"))
	}
	panelFingerprint := ""
	if panel, panelErr := s.st.PanelSSHKey(ctx); panelErr == nil {
		panelFingerprint = panel.Fingerprint
	} else if !errors.Is(panelErr, store.ErrNotFound) {
		return nil, internalConnectError()
	}
	isPanelCandidate := req.Msg.Fingerprint == panelFingerprint || req.Msg.Fingerprint == access.PanelKeyFingerprint
	var conn *Connection
	if isPanelCandidate {
		target, targetErr := NewTarget(access.SSHHost, uint32(access.SSHPort))
		if targetErr != nil {
			return nil, internalConnectError()
		}
		conn, _, err = s.dialPanelKey(ctx, target, access.SSHUser, access.HostFingerprint)
		if err != nil {
			conn = nil
		}
		if conn == nil && len(access.Password) > 0 {
			password, openErr := s.openAccessPassword(access.NodeID, access.Password)
			if openErr != nil {
				return nil, internalConnectError()
			}
			conn, err = s.ssh.DialAs(ctx, target, access.SSHUser, password, access.HostFingerprint)
			password = ""
		}
	} else {
		conn, _, err = s.dialAccess(ctx, access)
	}
	if err != nil || conn == nil {
		return nil, savedAccessError(err)
	}
	defer conn.Close()
	contents, digest, err := readRemoteAuthorizedKeys(ctx, conn)
	if err != nil {
		return nil, keyOperationError(err)
	}
	keys, _ := parseAuthorizedKeys(contents, panelFingerprint)
	var found *adminv1.NodeServerKey
	for _, key := range keys {
		if key.Fingerprint == req.Msg.Fingerprint {
			found = key
			break
		}
	}
	if found == nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("ssh_public_key_not_found"))
	}
	isPanelKey := found.PanelKey || access.PanelKeyFingerprint == found.Fingerprint
	if isPanelKey && !req.Msg.ConfirmPanelKey {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("confirm_panel_key_required"))
	}
	if isPanelKey {
		if len(access.Password) == 0 {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("ssh_key_last_panel_login"))
		}
		password, err := s.openAccessPassword(access.NodeID, access.Password)
		if err != nil {
			return nil, internalConnectError()
		}
		target, err := NewTarget(access.SSHHost, uint32(access.SSHPort))
		if err != nil {
			return nil, internalConnectError()
		}
		passwordConn, loginErr := s.ssh.DialAs(ctx, target, access.SSHUser, password, access.HostFingerprint)
		password = ""
		if loginErr != nil {
			if markErr := s.st.SetNodeServerPasswordLogin(ctx, access.NodeID, false); markErr != nil && !errors.Is(markErr, store.ErrConflict) {
				s.cfg.Log.Error("record failed password proof before panel-key removal", "node_id", access.NodeID, "err", markErr)
				return nil, internalConnectError()
			}
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("ssh_key_last_panel_login"))
		}
		_ = passwordConn.Close()
		if err := s.st.SetNodeServerPasswordLogin(ctx, access.NodeID, true); err != nil {
			if errors.Is(err, store.ErrConflict) {
				return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("node_retired"))
			}
			s.cfg.Log.Error("record password proof before panel-key removal", "node_id", access.NodeID, "err", err)
			return nil, internalConnectError()
		}
		if err := s.st.SetNodeServerPanelKey(ctx, access.NodeID, ""); err != nil {
			if errors.Is(err, store.ErrConflict) {
				return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("node_retired"))
			}
			s.cfg.Log.Error("clear panel SSH key access before removal", "err", err)
			return nil, internalConnectError()
		}
	}
	updated := withoutKey(contents, req.Msg.Fingerprint)
	if !bytes.Equal(updated, contents) {
		if err := writeRemoteAuthorizedKeys(ctx, conn, updated, digest); err != nil {
			return nil, keyOperationError(err)
		}
	}
	if err := s.audit(ctx, "node.ssh_key_remove", map[string]string{
		"node_id": access.NodeID, "fingerprint": found.Fingerprint, "panel_key": strconv.FormatBool(isPanelKey),
	}); err != nil {
		return nil, internalConnectError()
	}
	return connect.NewResponse(&adminv1.RemoveNodeServerKeyResponse{}), nil
}

func (s *Service) AttachNodeServerAccess(ctx context.Context, req *connect.Request[adminv1.AttachNodeServerAccessRequest]) (*connect.Response[adminv1.AttachNodeServerAccessResponse], error) {
	defer func() {
		req.Msg.Password, req.Msg.PrivateKey, req.Msg.PrivateKeyPassphrase = "", "", ""
	}()
	if err := s.cfg.StepUp(ctx); err != nil {
		return nil, err
	}
	if !req.Msg.Confirm || req.Msg.NodeId == "" || len(req.Msg.NodeId) > 64 || req.Msg.SshPort == 0 || req.Msg.SshPort > 65535 ||
		!validFingerprint(req.Msg.Fingerprint) {
		return nil, invalidArgument("confirmation, node and host key fingerprint are required")
	}
	username := sshUsername(req.Msg.Username)
	method, privateKey, loginErr := parseSSHLogin(req.Msg.GetUsePanelKey(), req.Msg.GetPassword(), req.Msg.GetPrivateKey(), req.Msg.GetPrivateKeyPassphrase())
	defer clear(privateKey)
	if !validSSHUsername(username) || loginErr != nil {
		if loginErr != nil && !errors.Is(loginErr, errInvalidSSHLogin) {
			return nil, privateKeyConnectError(loginErr)
		}
		return nil, invalidArgument("exactly one valid SSH login is required")
	}
	if method == "private_key" && !req.Msg.GetInstallPanelKey() {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("ssh_panel_key_required_for_saved_access"))
	}
	s.accessMu.Lock()
	defer s.accessMu.Unlock()
	state, _, err := s.cfg.Nodes.ProvisionNodeState(ctx, req.Msg.NodeId)
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("node_not_found"))
	}
	if err != nil {
		s.cfg.Log.Error("read node state before attaching SSH access", "node_id", req.Msg.NodeId, "err", err)
		return nil, internalConnectError()
	}
	if state == "retired" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("node_retired"))
	}
	if _, err := s.st.NodeServerAccess(ctx, req.Msg.NodeId); err == nil {
		return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("node_server_access_exists"))
	} else if !errors.Is(err, store.ErrNotFound) {
		s.cfg.Log.Error("check existing SSH access before attach", "node_id", req.Msg.NodeId, "err", err)
		return nil, internalConnectError()
	}
	if s.cfg.panelAddress() == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("panel_address_not_configured"))
	}
	target, err := NewTarget(req.Msg.SshHost, req.Msg.SshPort)
	if err != nil {
		return nil, invalidArgument("invalid SSH target")
	}
	var panelKey store.PanelSSHKey
	if method == "panel_key" || method == "private_key" || req.Msg.GetInstallPanelKey() {
		panelKey, _, err = s.ensurePanelSSHKey(ctx)
		if err != nil {
			return nil, internalConnectError()
		}
	}
	var conn *Connection
	switch method {
	case "panel_key":
		conn, _, err = s.dialPanelKey(ctx, target, username, req.Msg.Fingerprint)
	case "private_key":
		var signer ssh.Signer
		var private any
		signer, private, err = signerFromPrivateKey(privateKey)
		if err == nil {
			conn, err = s.ssh.DialKey(ctx, target, username, signer, req.Msg.Fingerprint)
		}
		clearPrivateKey(private)
	default:
		conn, err = s.ssh.DialAs(ctx, target, username, req.Msg.Password, req.Msg.Fingerprint)
	}
	if err != nil {
		return nil, sshConnectError(err)
	}
	defer conn.Close()
	remote, err := readPreflight(ctx, conn, s.cfg.panelAddress())
	if err != nil {
		return nil, preflightConnectError(err)
	}
	if !remote.facts.AlreadyEnrolled || remote.nodeID != req.Msg.NodeId {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("node_identity_mismatch"))
	}
	panelFingerprint := ""
	panelKeyError := ""
	if method == "panel_key" {
		panelFingerprint = panelKey.Fingerprint
	} else if req.Msg.GetInstallPanelKey() {
		panelFingerprint, err = s.installPanelSSHKey(ctx, conn, target, username, req.Msg.Fingerprint)
		if err != nil {
			panelKeyError = publicAuthorizedKeysCode(err)
			if method == "private_key" {
				return nil, keyOperationError(err)
			}
			panelFingerprint = ""
		}
	}
	access := store.NodeServerAccess{
		NodeID: req.Msg.NodeId, SSHHost: target.Host(), SSHPort: target.Port(), SSHUser: username,
		HostFingerprint: req.Msg.Fingerprint, PanelKeyFingerprint: panelFingerprint, ConfiguredAt: s.cfg.Now().UTC(),
	}
	if method == "password" {
		access.Password = s.sealAccessPassword(access.NodeID, req.Msg.Password)
		access.PasswordLogin = true
	}
	if err := s.st.InsertNodeServerAccess(ctx, access); err != nil {
		if errors.Is(err, store.ErrConflict) {
			return nil, connect.NewError(connect.CodeAlreadyExists, errors.New("node_server_access_exists"))
		}
		s.cfg.Log.Error("save attached node SSH access", "node_id", access.NodeID, "err", err)
		return nil, internalConnectError()
	}
	saved, err := s.st.NodeServerAccess(ctx, req.Msg.NodeId)
	if err != nil {
		s.cfg.Log.Error("read attached node SSH access", "node_id", req.Msg.NodeId, "err", err)
		return nil, internalConnectError()
	}
	if err := s.audit(ctx, "node.ssh_access_attach", map[string]string{
		"node_id": access.NodeID, "auth": method, "panel_key": strconv.FormatBool(panelFingerprint != ""),
	}); err != nil {
		return nil, internalConnectError()
	}
	return connect.NewResponse(&adminv1.AttachNodeServerAccessResponse{
		Access: nodeServerAccessView(saved), PanelKeyError: panelKeyError,
	}), nil
}

func readRemoteAuthorizedKeys(ctx context.Context, conn *Connection) ([]byte, string, error) {
	output, exitCode, err := runSSHCaptureAsUser(ctx, conn, "sh -c "+shellQuote(readAuthorizedKeysScript)+" mg", nil, 10*time.Second, maxAuthorizedKeysBytes+128)
	if err != nil {
		return nil, "", err
	}
	if err := authorizedKeysCommandError(exitCode); err != nil {
		return nil, "", err
	}
	return parseAuthorizedKeysResponse(output)
}

// parseAuthorizedKeysResponse reads the read script's output: "none", or "sha256:<hex>" and the file's bytes.
func parseAuthorizedKeysResponse(output []byte) ([]byte, string, error) {
	lineEnd := strings.IndexByte(string(output), '\n')
	if lineEnd < 0 {
		return nil, "", errors.New("provision: invalid authorized_keys response")
	}
	header := string(output[:lineEnd])
	contents := append([]byte(nil), output[lineEnd+1:]...)
	if len(contents) > maxAuthorizedKeysBytes {
		return nil, "", errors.New("authorized_keys_too_large")
	}
	if header == "none" {
		// No file prints nothing after the marker: bytes there came from the login's shell and must never become the file.
		if len(contents) != 0 {
			return nil, "", errors.New("provision: authorized_keys response has bytes after none")
		}
		return nil, "none", nil
	}
	if !strings.HasPrefix(header, "sha256:") || len(header) != len("sha256:")+64 {
		return nil, "", errors.New("provision: invalid authorized_keys digest")
	}
	// The file can change between the script's sha256sum and cat, and whatever the login's shell prints lands here too:
	// the digest the write compares and swaps must be the one of exactly these bytes.
	if digest := strings.TrimPrefix(header, "sha256:"); digest == authorizedKeysDigest(contents) {
		return contents, digest, nil
	}
	return nil, "", errors.New("provision: authorized_keys digest mismatch")
}

func writeRemoteAuthorizedKeys(ctx context.Context, conn *Connection, contents []byte, expected string) error {
	if len(contents) > maxAuthorizedKeysBytes {
		return errors.New("authorized_keys_too_large")
	}
	command := "sh -c " + shellQuote(writeAuthorizedKeysScript) + " mg " + shellQuote(expected)
	_, exitCode, err := runSSHCaptureAsUser(ctx, conn, command, strings.NewReader(string(contents)), 15*time.Second, 1024)
	if err != nil {
		return err
	}
	return authorizedKeysCommandError(exitCode)
}

func authorizedKeysCommandError(exitCode int) error {
	switch exitCode {
	case 0:
		return nil
	case 12:
		return errors.New("authorized_keys_unsafe")
	case 13:
		return errors.New("authorized_keys_changed")
	case 14:
		return errors.New("authorized_keys_too_large")
	default:
		return fmt.Errorf("provision: authorized_keys command exited %d", exitCode)
	}
}

// ListNodeServerKeys reads and parses the login user's authorized_keys without privilege escalation.
func (s *Service) ListNodeServerKeys(ctx context.Context, req *connect.Request[adminv1.ListNodeServerKeysRequest]) (*connect.Response[adminv1.ListNodeServerKeysResponse], error) {
	if req.Msg.NodeId == "" || len(req.Msg.NodeId) > 64 {
		return nil, invalidArgument("invalid node id")
	}
	s.accessMu.Lock()
	defer s.accessMu.Unlock()
	access, err := s.st.NodeServerAccess(ctx, req.Msg.NodeId)
	if errors.Is(err, store.ErrNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("node_server_access_not_found"))
	}
	if err != nil {
		s.cfg.Log.Error("read node SSH access for key list", "err", err)
		return nil, internalConnectError()
	}
	if access.NodeRetired {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("node_retired"))
	}
	conn, _, err := s.dialAccess(ctx, access)
	if err != nil {
		return nil, savedAccessError(err)
	}
	defer conn.Close()
	contents, _, err := readRemoteAuthorizedKeys(ctx, conn)
	if err != nil {
		return nil, authorizedKeysConnectError(err)
	}
	panelFingerprint := ""
	if key, keyErr := s.st.PanelSSHKey(ctx); keyErr == nil {
		panelFingerprint = key.Fingerprint
	} else if !errors.Is(keyErr, store.ErrNotFound) {
		s.cfg.Log.Error("read panel SSH fingerprint for key list", "err", keyErr)
		return nil, internalConnectError()
	}
	keys, other := parseAuthorizedKeys(contents, panelFingerprint)
	return connect.NewResponse(&adminv1.ListNodeServerKeysResponse{Keys: keys, OtherLines: other}), nil
}

func authorizedKeysConnectError(err error) error {
	switch err.Error() {
	case "authorized_keys_unsafe", "authorized_keys_changed", "authorized_keys_too_large":
		return connect.NewError(connect.CodeFailedPrecondition, err)
	default:
		return connect.NewError(connect.CodeUnavailable, errors.New("ssh_authorized_keys_unavailable"))
	}
}
