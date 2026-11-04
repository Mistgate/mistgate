package provision

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"testing"

	"github.com/mistgate/mistgate/internal/panel/vault"
	"golang.org/x/crypto/ssh"
)

func TestNormalizePrivateKeySupportsEncryptedKeysAndReportsPassphraseErrors(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(private)
	const passphrase = "correct key passphrase"
	block, err := ssh.MarshalPrivateKeyWithPassphrase(private, "owner laptop", []byte(passphrase))
	if err != nil {
		t.Fatal(err)
	}
	raw := string(pem.EncodeToMemory(block))

	if _, err := normalizePrivateKey(raw, ""); !errors.Is(err, errPrivateKeyPassphraseNeeded) {
		t.Fatalf("missing passphrase error = %v", err)
	}
	if _, err := normalizePrivateKey(raw, "incorrect"); !errors.Is(err, errPrivateKeyPassphraseWrong) {
		t.Fatalf("wrong passphrase error = %v", err)
	}
	normalized, err := normalizePrivateKey(raw, passphrase)
	if err != nil {
		t.Fatalf("normalize with passphrase: %v", err)
	}
	defer clear(normalized)
	parsed, err := ssh.ParseRawPrivateKey(normalized)
	if err != nil {
		t.Fatalf("normalized key is not parseable: %v", err)
	}
	defer clearPrivateKey(parsed)
	signer, err := ssh.NewSignerFromKey(parsed)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := ssh.NewSignerFromKey(private)
	if !bytes.Equal(signer.PublicKey().Marshal(), want.PublicKey().Marshal()) {
		t.Fatal("normalization changed the key pair")
	}
	if _, err := normalizePrivateKey(string(bytes.Repeat([]byte{'x'}, maxPrivateKeyBytes+1)), ""); !errors.Is(err, errPrivateKeyInvalid) {
		t.Fatalf("oversized key error = %v", err)
	}
}

func TestSSHLoginRequiresOneMethodAndPrivateKeyInstall(t *testing.T) {
	if method, _, err := parseSSHLogin(false, authTestPassword, "", ""); err != nil || method != "password" {
		t.Fatalf("password login: method %q, err %v", method, err)
	}
	if method, _, err := parseSSHLogin(true, "", "", ""); err != nil || method != "panel_key" {
		t.Fatalf("panel-key login: method %q, err %v", method, err)
	}
	for _, tc := range []struct {
		name       string
		panel      bool
		password   string
		privateKey string
		passphrase string
	}{
		{name: "neither"},
		{name: "password and panel key", panel: true, password: authTestPassword},
		{name: "password and private key", password: authTestPassword, privateKey: "bad"},
		{name: "passphrase without key", passphrase: "secret"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := parseSSHLogin(tc.panel, tc.password, tc.privateKey, tc.passphrase); !errors.Is(err, errInvalidSSHLogin) {
				t.Fatalf("parseSSHLogin error = %v", err)
			}
		})
	}
}

func TestPrivateKeyCredentialsAreSealedAndRequirePanelKeyFallback(t *testing.T) {
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(private)
	block, err := ssh.MarshalPrivateKey(private, "owner laptop")
	if err != nil {
		t.Fatal(err)
	}
	encoded := pem.EncodeToMemory(block)
	normalized, err := normalizePrivateKey(string(encoded), "")
	if err != nil {
		t.Fatal(err)
	}
	defer clear(normalized)
	vlt, err := vault.New(make([]byte, vault.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	svc := &Service{vault: vlt}
	sealed, err := svc.sealCredentials("prv_owner_key", credentials{Method: "private_key", Username: "root", PrivateKey: normalized, InstallPanelKey: true})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, normalized) {
		t.Fatal("the private key appears in the sealed job secret")
	}
	opened, err := svc.openCredentials("prv_owner_key", sealed)
	if err != nil || opened.Method != "private_key" || !opened.InstallPanelKey || !bytes.Equal(opened.PrivateKey, normalized) {
		t.Fatalf("opened private-key credentials = %+v, err %v", opened, err)
	}
	clear(opened.PrivateKey)

	invalid, err := svc.sealCredentials("prv_no_fallback", credentials{Method: "private_key", Username: "root", PrivateKey: normalized})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.openCredentials("prv_no_fallback", invalid); err == nil {
		t.Fatal("private-key credentials without a panel key fallback were accepted")
	}
}
