package provision

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func testAuthorizedKey(t *testing.T) (ssh.Signer, string) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSuffix(string(ssh.MarshalAuthorizedKey(signer.PublicKey())), "\n") + " alice@laptop"
	return signer, line
}

func TestCanonicalPublicKeyCleansCommentsAndRejectsUnsafeKeys(t *testing.T) {
	_, line := testAuthorizedKey(t)
	canonical, key, err := canonicalPublicKey(strings.Replace(line, "alice@laptop", "alice@laptop\x01tag", 1))
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(canonical, "\r\n") || strings.ContainsAny(key.GetComment(), "\r\n") || key.GetComment() != "alice@laptoptag" {
		t.Fatalf("canonical comment is not one safe line: %q (%q)", canonical, key.GetComment())
	}
	if _, _, err := canonicalPublicKey(line + "\ncommand=never-a-second-line"); err == nil || err.Error() != "ssh_public_key_invalid" {
		t.Fatalf("multiline public key input was accepted: %v", err)
	}
	publicOnly := strings.Fields(line)[:2]
	if _, _, err := canonicalPublicKey(strings.Join(publicOnly, " ") + " " + strings.Repeat("c", 101)); err == nil || err.Error() != "ssh_public_key_invalid" {
		t.Fatalf("oversized public key comment was accepted: %v", err)
	}

	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(private, "alice@laptop")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := canonicalPublicKey(string(pem.EncodeToMemory(block))); err == nil || err.Error() != "ssh_public_key_invalid" {
		t.Fatalf("private key accepted: %v", err)
	}

	weak, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	weakLine := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(mustPublicKey(t, &weak.PublicKey))))
	if _, _, err := canonicalPublicKey(weakLine); err == nil || err.Error() != "ssh_public_key_weak" {
		t.Fatalf("weak RSA key error = %v", err)
	}

	cert := &ssh.Certificate{Key: mustSigner(t, private).PublicKey(), CertType: ssh.UserCert}
	if err := cert.SignCert(rand.Reader, mustSigner(t, private)); err != nil { // an unsigned one cannot be marshalled
		t.Fatal(err)
	}
	certLine := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(cert)))
	if _, _, err := canonicalPublicKey(certLine); err == nil || err.Error() != "ssh_public_key_invalid" {
		t.Fatalf("certificate accepted: %v", err)
	}

	if _, _, err := canonicalPublicKey(line + "\n" + line); err == nil || err.Error() != "ssh_public_key_invalid" {
		t.Fatalf("multiple public key lines accepted: %v", err)
	}
}

func TestAuthorizedKeysParseWithKeyAndWithoutKeyPreserveOtherBytes(t *testing.T) {
	signer, line := testAuthorizedKey(t)
	fingerprint := ssh.FingerprintSHA256(signer.PublicKey())
	otherSigner, otherLine := testAuthorizedKey(t)
	if ssh.FingerprintSHA256(otherSigner.PublicKey()) == fingerprint {
		t.Fatal("test generated duplicate keys")
	}
	contents := []byte("# owner note\r\nfrom=\"192.0.2.0/24\",command=\"echo safe\" " + line + "\r\n\n" + otherLine)
	keys, other := parseAuthorizedKeys(contents, fingerprint)
	if len(keys) != 2 || other != 2 {
		t.Fatalf("parsed %d keys and %d other lines", len(keys), other)
	}
	if !keys[0].GetRestricted() || !keys[0].GetPanelKey() || keys[0].GetLines() != 1 || keys[1].GetRestricted() {
		t.Fatalf("key metadata: %+v", keys)
	}

	updated := withKey([]byte("# note without a newline"), line)
	if got := string(updated); got != "# note without a newline\n"+line+"\n" {
		t.Fatalf("withKey output = %q", got)
	}
	if duplicate := withKey(updated, line); !bytes.Equal(duplicate, updated) {
		t.Fatalf("adding an unrestricted duplicate changed the file: %q", duplicate)
	}
	// contents holds the key only behind from= and command=: a bare copy would let it in without them
	if same := withKey(contents, line); !bytes.Equal(same, contents) {
		t.Fatalf("adding a key the file has with options appended a bare copy: %q", same)
	}

	removed := withoutKey(contents, fingerprint)
	want := []byte("# owner note\r\n\n" + otherLine)
	if !bytes.Equal(removed, want) {
		t.Fatalf("withoutKey changed unrelated bytes:\n got %q\nwant %q", removed, want)
	}
	// the newline appended after otherLine now ends its line and stays with it
	if !bytes.Equal(withoutKey(append(contents, []byte("\n"+line+"\n")...), fingerprint), append(want, '\n')) {
		t.Fatal("withoutKey did not remove every duplicate line")
	}
}

func TestAuthorizedKeysResponseIsTheFileOnly(t *testing.T) {
	file := []byte("ssh-ed25519 AAAA alice@laptop\n")
	sum := sha256.Sum256(file)
	digest := hex.EncodeToString(sum[:])
	for _, c := range []struct {
		name, out, digest string
		file              []byte
	}{
		{name: "no file", out: "none\n", digest: "none"},
		{name: "a file", out: "sha256:" + digest + "\n" + string(file), digest: digest, file: file},
		{name: "bytes after none (the login's shell)", out: "none\nssh-ed25519 AAAA mallory@host\n"},
		{name: "bytes the digest does not cover", out: "sha256:" + digest + "\n" + string(file) + "warning: x\n"},
		{name: "no header line", out: "sha256:" + digest},
	} {
		got, gotDigest, err := parseAuthorizedKeysResponse([]byte(c.out))
		if c.digest == "" {
			if err == nil {
				t.Errorf("%s: accepted as %q", c.name, got)
			}
			continue
		}
		if err != nil || gotDigest != c.digest || !bytes.Equal(got, c.file) {
			t.Errorf("%s: %q, %q, %v", c.name, got, gotDigest, err)
		}
	}
}

func TestAuthorizedKeysRemoteScriptsUseCASAndSafeModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a POSIX shell")
	}
	root := t.TempDir()
	keyData := []byte("ssh-ed25519 AAAA example\n")
	if output, code := runRemoteKeyScript(t, root, "sh -c "+shellQuote(writeAuthorizedKeysScript)+" mg none", keyData); code != 0 || output != "" {
		t.Fatalf("write a new authorized_keys file: output %q, exit %d", output, code)
	}
	sshDir := filepath.Join(root, ".ssh")
	file := filepath.Join(sshDir, "authorized_keys")
	info, err := os.Stat(sshDir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf(".ssh mode = %v, err = %v", info, err)
	}
	info, err = os.Stat(file)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("authorized_keys mode = %v, err = %v", info, err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "environment"), []byte("KEEP=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, code := runRemoteKeyScript(t, root, "sh -c "+shellQuote(writeAuthorizedKeysScript)+" mg wrong", []byte("changed\n")); code != 13 {
		t.Fatalf("hash mismatch exit = %d, want 13", code)
	}
	if _, code := runRemoteKeyScript(t, root, "sh -c "+shellQuote(readAuthorizedKeysScript)+" mg", nil); code != 0 {
		t.Fatalf("read exit = %d", code)
	}
	if got, err := os.ReadFile(filepath.Join(sshDir, "environment")); err != nil || string(got) != "KEEP=1\n" {
		t.Fatalf("authorized_keys operation changed ~/.ssh/environment: %q, %v", got, err)
	}

	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "outside"), file); err != nil {
		t.Fatal(err)
	}
	if _, code := runRemoteKeyScript(t, root, "sh -c "+shellQuote(writeAuthorizedKeysScript)+" mg none", keyData); code != 12 {
		t.Fatalf("authorized_keys symlink exit = %d, want 12", code)
	}

	linkedHome := t.TempDir()
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(linkedHome, ".ssh")); err != nil {
		t.Fatal(err)
	}
	if _, code := runRemoteKeyScript(t, linkedHome, "sh -c "+shellQuote(readAuthorizedKeysScript)+" mg", nil); code != 12 {
		t.Fatalf(".ssh directory symlink read exit = %d, want 12", code)
	}
	if _, code := runRemoteKeyScript(t, linkedHome, "sh -c "+shellQuote(writeAuthorizedKeysScript)+" mg none", keyData); code != 12 {
		t.Fatalf(".ssh directory symlink write exit = %d, want 12", code)
	}

	largeHome := t.TempDir()
	if err := os.Mkdir(filepath.Join(largeHome, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(largeHome, ".ssh", "authorized_keys"), bytes.Repeat([]byte{'x'}, maxAuthorizedKeysBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, code := runRemoteKeyScript(t, largeHome, "sh -c "+shellQuote(readAuthorizedKeysScript)+" mg", nil); code != 14 {
		t.Fatalf("oversized authorized_keys read exit = %d, want 14", code)
	}
	if _, code := runRemoteKeyScript(t, largeHome, "sh -c "+shellQuote(writeAuthorizedKeysScript)+" mg wrong", keyData); code != 14 {
		t.Fatalf("oversized authorized_keys write exit = %d, want 14", code)
	}
}

func runRemoteKeyScript(t *testing.T, home, command string, input []byte) (string, int) {
	t.Helper()
	cmd := exec.Command("sh", "-c", command)
	cmd.Env = append(os.Environ(), "HOME="+home)
	cmd.Stdin = bytes.NewReader(input)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return string(output), 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return string(output), exitErr.ExitCode()
	}
	t.Fatalf("run remote key script: %v: %s", err, output)
	return "", -1
}

func mustPublicKey(t *testing.T, value any) ssh.PublicKey {
	t.Helper()
	key, err := ssh.NewPublicKey(value)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func mustSigner(t *testing.T, value any) ssh.Signer {
	t.Helper()
	signer, err := ssh.NewSignerFromKey(value)
	if err != nil {
		t.Fatal(err)
	}
	return signer
}
