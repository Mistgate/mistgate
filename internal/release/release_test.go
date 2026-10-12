package release

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func testKey(t *testing.T, b byte) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	priv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{b}, ed25519.SeedSize))
	return priv.Public().(ed25519.PublicKey), priv
}

func sampleManifest() Manifest {
	h := sha256.Sum256([]byte("binary"))
	return Manifest{Schema: 1, Version: "0.2.0-abc1234", Built: 1_790_000_000, Expires: 1_792_592_000,
		Files: []File{
			{OS: "linux", Arch: "amd64", Name: "mistgate-node-linux-amd64", Size: 6, SHA256: hex.EncodeToString(h[:])},
			{OS: "linux", Arch: "arm64", Name: "mistgate-node-linux-arm64", Size: 7, SHA256: strings.Repeat("ab", 32)},
		}}
}

func TestSignVerifyRoundTrip(t *testing.T) {
	pub, priv := testKey(t, 1)
	m := sampleManifest()
	b, err := m.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	sig := Sign(priv, b)
	got, err := Verify(pub, b, sig)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != m.Version || got.Built != m.Built || len(got.Files) != 2 {
		t.Fatalf("parsed %+v", got)
	}
}

func TestVerifyRejects(t *testing.T) {
	pub, priv := testKey(t, 1)
	otherPub, _ := testKey(t, 2)
	m := sampleManifest()
	b, _ := m.Marshal()
	sig := Sign(priv, b)

	tampered := bytes.Replace(b, []byte("0.2.0"), []byte("0.9.0"), 1)
	badSig := append([]byte(nil), sig...)
	badSig[0] ^= 1
	for name, tc := range map[string]struct {
		pub      ed25519.PublicKey
		man, sig []byte
	}{
		"tampered manifest": {pub, tampered, sig},
		"flipped signature": {pub, b, badSig},
		"other key":         {otherPub, b, sig},
		"short signature":   {pub, b, sig[:10]},
		"no key":            {nil, b, sig},
	} {
		if _, err := Verify(tc.pub, tc.man, tc.sig); !errors.Is(err, ErrBadSignature) {
			t.Errorf("%s: err = %v, want ErrBadSignature", name, err)
		}
	}
	// A valid signature over garbage is a bad manifest, not a bad signature.
	junk := []byte("not json")
	if _, err := Verify(pub, junk, Sign(priv, junk)); !errors.Is(err, ErrBadManifest) {
		t.Errorf("signed junk: err = %v, want ErrBadManifest", err)
	}
}

// The one key signs both manifests; neither signature may pass as the other.
func TestPanelSignatureIsSeparateFromNodeSignature(t *testing.T) {
	pub, priv := testKey(t, 1)
	m := sampleManifest()
	b, _ := m.Marshal()
	if _, err := VerifyPanel(pub, b, SignPanel(priv, b)); err != nil {
		t.Fatalf("panel round trip: %v", err)
	}
	if _, err := Verify(pub, b, SignPanel(priv, b)); !errors.Is(err, ErrBadSignature) {
		t.Errorf("a panel signature verified as a node bundle: %v", err)
	}
	if _, err := VerifyPanel(pub, b, Sign(priv, b)); !errors.Is(err, ErrBadSignature) {
		t.Errorf("a node signature verified as a panel manifest: %v", err)
	}
}

func TestPanelSSHSIGGoldenRoundTrip(t *testing.T) {
	const golden = `-----BEGIN SSH SIGNATURE-----
U1NIU0lHAAAAAQAAADMAAAALc3NoLWVkMjU1MTkAAAAgiojj3XQJ8ZX9UtstPLpdcspnCb
8dlBIb83SIAbQPb1wAAAAabWlzdGdhdGUtcGFuZWwtbWFuaWZlc3QtdjEAAAAAAAAABnNo
YTUxMgAAAFMAAAALc3NoLWVkMjU1MTkAAABAe/k/vmm1yZ24vx/5D3MlgyDnU3eVnLEvjH
oRcfw8aRK8upbIniYSy3WDeLRpiTG7yZHuLHzVt3LphJ57TtKMCw==
-----END SSH SIGNATURE-----
`
	pub, priv := testKey(t, 1)
	m := sampleManifest()
	manifest, err := m.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	got, err := SignPanelSSHSIG(priv, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(got, []byte("-----BEGIN SSH SIGNATURE-----\n")) || !bytes.HasSuffix(got, []byte("-----END SSH SIGNATURE-----\n")) {
		t.Fatalf("unexpected SSHSIG armor: %q", got)
	}
	if string(got) != golden {
		t.Fatalf("golden SSHSIG mismatch:\n%s", got)
	}
	for _, line := range strings.Split(strings.TrimSuffix(string(got), "\n"), "\n")[1:] {
		if line == "-----END SSH SIGNATURE-----" {
			break
		}
		if len(line) > 70 {
			t.Fatalf("armor line has %d columns, want at most 70", len(line))
		}
	}
	parsed, err := VerifyPanelSSHSIG(pub, manifest, got)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Version != sampleManifest().Version || parsed.Built != sampleManifest().Built {
		t.Fatalf("parsed manifest %+v", parsed)
	}
}

func TestPanelSSHSIGRejectsWrongNamespaceKeyAndMessage(t *testing.T) {
	pub, priv := testKey(t, 1)
	otherPub, _ := testKey(t, 2)
	m := sampleManifest()
	manifest, err := m.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	sig, err := SignPanelSSHSIG(priv, manifest)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := unarmorSSHSIG(sig)
	if err != nil {
		t.Fatal(err)
	}
	wrongNamespaceWire := bytes.Replace(wire, []byte(PanelSSHSIGNamespace), []byte(strings.Repeat("x", len(PanelSSHSIGNamespace))), 1)
	wrongNamespace := armorSSHSIG(wrongNamespaceWire)
	tampered := bytes.Replace(manifest, []byte("0.2.0"), []byte("0.9.0"), 1)
	for name, tc := range map[string]struct {
		pub       ed25519.PublicKey
		manifest  []byte
		signature []byte
	}{
		"wrong namespace":  {pub, manifest, wrongNamespace},
		"wrong key":        {otherPub, manifest, sig},
		"tampered message": {pub, tampered, sig},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := VerifyPanelSSHSIG(tc.pub, tc.manifest, tc.signature); !errors.Is(err, ErrBadSignature) {
				t.Fatalf("err = %v, want ErrBadSignature", err)
			}
		})
	}
}

func TestPanelSSHSIGVerifiesWithSSHKeygen(t *testing.T) {
	sshKeygen, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Skip("ssh-keygen is not installed")
	}
	pub, priv := testKey(t, 3)
	m := sampleManifest()
	manifest, err := m.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	sig, err := SignPanelSSHSIG(priv, manifest)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	allowedSigners := filepath.Join(dir, "allowed_signers")
	if err := os.WriteFile(allowedSigners, []byte(fmt.Sprintf("mistgate-release %s %s\n", key.Type(), base64.StdEncoding.EncodeToString(key.Marshal()))), 0o600); err != nil {
		t.Fatal(err)
	}
	signature := filepath.Join(dir, "panel-manifest.sshsig")
	if err := os.WriteFile(signature, sig, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(sshKeygen, "-Y", "verify", "-f", allowedSigners, "-I", "mistgate-release", "-n", PanelSSHSIGNamespace, "-s", signature)
	cmd.Stdin = bytes.NewReader(manifest)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen rejected the SSHSIG: %v\n%s", err, output)
	}
}

func TestParseIsStrict(t *testing.T) {
	m := sampleManifest()
	b, _ := m.Marshal()
	for name, in := range map[string][]byte{
		"unknown field":            bytes.Replace(b, []byte(`"schema": 1,`), []byte(`"schema": 1, "extra": true,`), 1),
		"trailing data":            append(append([]byte(nil), b...), []byte(`{"x":1}`)...),
		"schema 2":                 bytes.Replace(b, []byte(`"schema": 1`), []byte(`"schema": 2`), 1),
		"schema 2 plus new fields": bytes.Replace(b, []byte(`"schema": 1,`), []byte(`"schema": 2, "sbom": "x",`), 1),
		"empty":                    nil,
	} {
		if _, err := Parse(in); !errors.Is(err, ErrBadManifest) {
			t.Errorf("%s: err = %v, want ErrBadManifest", name, err)
		}
	}
}

func TestValidate(t *testing.T) {
	for name, mut := range map[string]func(*Manifest){
		"empty version":      func(m *Manifest) { m.Version = "" },
		"version with space": func(m *Manifest) { m.Version = "1 0" },
		"no built":           func(m *Manifest) { m.Built = 0 },
		"expires <= built":   func(m *Manifest) { m.Expires = m.Built },
		"no files":           func(m *Manifest) { m.Files = nil },
		"path in name":       func(m *Manifest) { m.Files[0].Name = "../mistgate-node" },
		"slash in name":      func(m *Manifest) { m.Files[0].Name = "a/b" },
		"dotdot in name":     func(m *Manifest) { m.Files[0].Name = "a..b" },
		"zero size":          func(m *Manifest) { m.Files[0].Size = 0 },
		"huge size":          func(m *Manifest) { m.Files[0].Size = MaxFileSize + 1 },
		"upper-case sha":     func(m *Manifest) { m.Files[0].SHA256 = strings.ToUpper(m.Files[0].SHA256) },
		"short sha":          func(m *Manifest) { m.Files[0].SHA256 = "ab" },
		"duplicate platform": func(m *Manifest) { m.Files[1].OS, m.Files[1].Arch = "linux", "amd64" },
		"duplicate name":     func(m *Manifest) { m.Files[1].Name = m.Files[0].Name },
		"bad os":             func(m *Manifest) { m.Files[0].OS = "Linux!" },
	} {
		m := sampleManifest()
		mut(&m)
		if err := m.Validate(); !errors.Is(err, ErrBadManifest) {
			t.Errorf("%s: err = %v, want ErrBadManifest", name, err)
		}
	}
	m := sampleManifest()
	if err := m.Validate(); err != nil {
		t.Fatalf("sample must be valid: %v", err)
	}
	m.Schema = 2
	if err := m.Validate(); !errors.Is(err, ErrUnsupportedSchema) || !errors.Is(err, ErrBadManifest) {
		t.Errorf("schema 2: %v", err)
	}
}

func TestCheck(t *testing.T) {
	m := sampleManifest()
	at := func(unix int64) time.Time { return time.Unix(unix, 0) }
	for _, tc := range []struct {
		name string
		now  int64
		own  int64
		want error
	}{
		{"newer", m.Built + 10, m.Built - 1, nil},
		{"own build is 0 (dev)", m.Built + 10, 0, nil},
		{"same built", m.Built + 10, m.Built, ErrUpToDate},
		{"older", m.Built + 10, m.Built + 1, ErrDowngrade},
		{"expired", m.Expires + 1, m.Built - 1, ErrExpired},
		{"last second", m.Expires, m.Built - 1, nil},
		{"expired wins over same built", m.Expires + 1, m.Built, ErrExpired},
	} {
		if got := m.Check(at(tc.now), tc.own); !errors.Is(got, tc.want) && got != tc.want {
			t.Errorf("%s: %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestFileFor(t *testing.T) {
	m := sampleManifest()
	f, err := m.FileFor("linux", "arm64")
	if err != nil || f.Name != "mistgate-node-linux-arm64" {
		t.Fatalf("got %+v, %v", f, err)
	}
	if _, err := m.FileFor("linux", "riscv64"); !errors.Is(err, ErrNoFile) {
		t.Fatalf("err = %v, want ErrNoFile", err)
	}
	if _, ok := m.FindName("mistgate-node-linux-amd64"); !ok {
		t.Fatal("FindName missed a listed file")
	}
	if _, ok := m.FindName("manifest.json"); ok {
		t.Fatal("FindName found an unlisted file")
	}
}

func TestVerifyFile(t *testing.T) {
	m := sampleManifest()
	f := m.Files[0] // "binary", 6 bytes
	if err := VerifyFile(f, strings.NewReader("binary")); err != nil {
		t.Fatal(err)
	}
	if err := VerifyFile(f, strings.NewReader("binarY")); !errors.Is(err, ErrHashMismatch) {
		t.Errorf("same size, other bytes: %v", err)
	}
	if err := VerifyFile(f, strings.NewReader("binary!")); !errors.Is(err, ErrSizeMismatch) {
		t.Errorf("longer: %v", err)
	}
	if err := VerifyFile(f, strings.NewReader("bin")); !errors.Is(err, ErrSizeMismatch) {
		t.Errorf("shorter: %v", err)
	}
}

func TestFileFromPathAndNames(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "mistgate-node-linux-amd64")
	if err := os.WriteFile(p, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := FileFromPath(p)
	if err != nil {
		t.Fatal(err)
	}
	if f.OS != "linux" || f.Arch != "amd64" || f.Name != "mistgate-node-linux-amd64" || f.Size != 6 {
		t.Fatalf("got %+v", f)
	}
	if err := VerifyFile(f, strings.NewReader("binary")); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := ParseBinaryName("mistgate-node"); err == nil {
		t.Error("a name without os and arch must be refused")
	}
	base, o, a, err := ParseBinaryName("mistgate-node-linux-arm64")
	if err != nil || base != "mistgate-node" || o != "linux" || a != "arm64" {
		t.Errorf("got %q %q %q %v", base, o, a, err)
	}
}

func TestKeyEncoding(t *testing.T) {
	pub, priv, err := GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	back, err := DecodePrivateKey("  " + EncodePrivateKey(priv) + "\n")
	if err != nil || !back.Equal(priv) {
		t.Fatalf("private key round trip: %v", err)
	}
	if !bytes.Equal(back.Public().(ed25519.PublicKey), pub) {
		t.Fatal("public half differs")
	}
	if _, err := DecodePrivateKey("AAAA"); err == nil {
		t.Error("a short key must be refused")
	}
}

func TestCode(t *testing.T) {
	for err, want := range map[error]string{
		ErrBadSignature: "bad_signature", ErrBadManifest: "bad_manifest", ErrExpired: "expired", ErrDowngrade: "downgrade",
		ErrUpToDate: "up_to_date", ErrNoFile: "no_file_for_platform", ErrSizeMismatch: "size_mismatch",
		ErrHashMismatch: "hash_mismatch", errors.New("other"): "",
	} {
		if got := Code(err); got != want {
			t.Errorf("Code(%v) = %q, want %q", err, got, want)
		}
	}
}
