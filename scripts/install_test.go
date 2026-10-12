package scripts

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/release"
	"golang.org/x/crypto/ssh"
)

const installerTestVersion = "v1.2.3"
const installerTestBinary = "mistgate-linux-amd64"
const installerTestToken = "setup-token-for-test"

func TestInstallerReleaseKeyMatchesConfiguration(t *testing.T) {
	script, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	rawMatch := regexp.MustCompile(`(?m)^readonly RELEASE_PUBLIC_KEY_BASE64="([^"]+)"$`).FindSubmatch(script)
	if len(rawMatch) != 2 {
		t.Fatal("install.sh does not contain its embedded release key")
	}
	sshMatch := regexp.MustCompile(`(?m)^readonly RELEASE_PUBLIC_KEY_SSH_BASE64="([^"]+)"$`).FindSubmatch(script)
	if len(sshMatch) != 2 {
		t.Fatal("install.sh does not contain its embedded SSH release key")
	}
	decoded, err := base64.StdEncoding.DecodeString(string(rawMatch[1]))
	if err != nil || len(decoded) != ed25519.PublicKeySize {
		t.Fatalf("embedded release key must be base64-encoded 32-byte Ed25519 public key: %v", err)
	}
	sshPub, err := ssh.NewPublicKey(ed25519.PublicKey(decoded))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(sshMatch[1]), base64.StdEncoding.EncodeToString(sshPub.Marshal()); got != want {
		t.Fatalf("embedded SSH release key differs from the raw Ed25519 key: got %q, want %q", got, want)
	}
	configured := os.Getenv("MISTGATE_RELEASE_PUBLIC_KEY")
	if configured != "" && string(rawMatch[1]) != configured {
		t.Fatalf("installer release key differs from MISTGATE_RELEASE_PUBLIC_KEY: got %q", rawMatch[1])
	}
}

// installerBash returns bash when the installer can run here. CI can require either verification path explicitly.
func installerBash(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the installer targets Linux")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not installed")
	}
	if os.Getenv("MISTGATE_INSTALL_REQUIRE_OPENSSL") == "1" && !installerOpenSSL32Available() {
		t.Fatalf("OpenSSL 3.2+ is required, found %q", installerOpenSSLVersion())
	}
	if os.Getenv("MISTGATE_INSTALL_REQUIRE_SSH_KEYGEN") == "1" && !installerSSHKeygenYAvailable() {
		t.Fatal("ssh-keygen with -Y verify is required")
	}
	return bash
}

func installerOpenSSLVersion() string {
	if _, err := exec.LookPath("openssl"); err != nil {
		return "not installed"
	}
	out, _ := exec.Command("openssl", "version").CombinedOutput()
	return strings.TrimSpace(string(out))
}

func installerOpenSSL32Available() bool {
	out, err := exec.Command("openssl", "version").Output()
	if err != nil {
		return false
	}
	m := regexp.MustCompile(`^OpenSSL (\d+)\.(\d+)`).FindSubmatch(out)
	if m == nil {
		return false
	}
	major, _ := strconv.Atoi(string(m[1]))
	minor, _ := strconv.Atoi(string(m[2]))
	return major > 3 || (major == 3 && minor >= 2)
}

func installerSSHKeygenYAvailable() bool {
	sshKeygen, err := exec.LookPath("ssh-keygen")
	if err != nil {
		return false
	}
	out, _ := exec.Command(sshKeygen, "-Y").CombinedOutput()
	return strings.Contains(string(out), "verify")
}

func TestInstallerAgainstFakeRelease(t *testing.T) {
	bash := installerBash(t)
	if !installerSSHKeygenYAvailable() && !installerOpenSSL32Available() {
		t.Skip("neither supported signature verification path is installed")
	}

	for _, tc := range []struct {
		name       string
		mutate     func(*fakePanelRelease)
		arch       string
		existing   bool
		wantErr    string
		wantOutput string
	}{
		{
			name:       "signed release passes dry run",
			wantOutput: "Dry run complete; the host was not changed",
		},
		{
			name: "tampered binary fails SHA256SUMS",
			mutate: func(r *fakePanelRelease) {
				r.assets[installerTestBinary] = []byte("tampered panel binary")
			},
			wantErr: "SHA256SUMS verification failed",
		},
		{
			name: "manifest must list the binary",
			mutate: func(r *fakePanelRelease) {
				r.manifest.Files[0].Name = "mistgate-linux-arm64"
				r.manifest.Files[0].Arch = "arm64"
				r.resign(t)
			},
			wantErr: "does not list mistgate-linux-amd64",
		},
		{
			name: "bad signature is refused",
			mutate: func(r *fakePanelRelease) {
				r.assets[release.PanelSignatureName][0] ^= 0xff
				r.assets[release.PanelSSHSIGName][0] ^= 0xff
			},
			wantErr: "did not verify with the official release key",
		},
		{
			name: "expired manifest is refused",
			mutate: func(r *fakePanelRelease) {
				r.manifest.Built = 100
				r.manifest.Expires = 200
				r.resign(t)
			},
			wantErr: "panel-manifest.json expired",
		},
		{
			name:    "unsupported architecture is refused",
			arch:    "riscv64",
			wantErr: "unsupported architecture riscv64",
		},
		{
			name:     "existing install is refused",
			existing: true,
			wantErr:  "already exists; update the panel from its admin page",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			release := newFakePanelRelease(t)
			if tc.mutate != nil {
				tc.mutate(release)
			}
			server := release.server()
			defer server.Close()
			binDir := filepath.ToSlash(t.TempDir())
			if tc.existing {
				if err := os.WriteFile(filepath.Join(filepath.FromSlash(binDir), "mistgate"), []byte("existing"), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			arch := "x86_64"
			if tc.arch != "" {
				arch = tc.arch
			}
			args := []string{"--public-url", "https://panel.example.com", "--version", installerTestVersion, "--dry-run"}
			output, err := runInstaller(t, bash, server.URL, release.publicKey, release.sshPublicKey, binDir, arch, args...)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(output, tc.wantErr) {
					t.Fatalf("output %q; error %v; want error containing %q", output, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("installer failed: %v\n%s", err, output)
			}
			if !strings.Contains(output, tc.wantOutput) {
				t.Fatalf("output %q does not contain %q", output, tc.wantOutput)
			}
		})
	}
}

func TestInstallerSSHSIGPath(t *testing.T) {
	bash := installerBash(t)
	if !installerSSHKeygenYAvailable() {
		t.Skip("ssh-keygen with -Y verify is not installed")
	}
	for _, badSignature := range []bool{false, true} {
		name := "valid SSHSIG"
		if badSignature {
			name = "bad SSHSIG"
		}
		t.Run(name, func(t *testing.T) {
			fake := newFakePanelRelease(t)
			if badSignature {
				fake.assets[release.PanelSSHSIGName][0] ^= 0xff
			}
			server := fake.server()
			defer server.Close()
			binDir := filepath.ToSlash(t.TempDir())
			args := []string{"--public-url", "https://panel.example.com", "--version", installerTestVersion, "--dry-run"}
			output, err := runInstaller(t, bash, server.URL, fake.publicKey, fake.sshPublicKey, binDir, "x86_64", args...)
			if badSignature {
				if err == nil || !strings.Contains(output, "panel-manifest.sshsig did not verify with the official release key") {
					t.Fatalf("output %q; error %v; want bad SSHSIG refusal", output, err)
				}
				return
			}
			if err != nil || !strings.Contains(output, "Verified panel manifest with ssh-keygen.") {
				t.Fatalf("output %q; error %v; want ssh-keygen verification", output, err)
			}
		})
	}
}

func TestInstallerOpenSSLFallbackForLegacyRelease(t *testing.T) {
	bash := installerBash(t)
	if !installerOpenSSL32Available() {
		t.Skip("OpenSSL 3.2+ is not installed")
	}
	fake := newFakePanelReleaseForVersion(t, "v0.1.33")
	delete(fake.assets, release.PanelSSHSIGName)
	server := fake.server()
	defer server.Close()
	args := []string{"--public-url", "https://panel.example.com", "--version", "v0.1.33", "--dry-run"}
	output, err := runInstaller(t, bash, server.URL, fake.publicKey, fake.sshPublicKey, filepath.ToSlash(t.TempDir()), "x86_64", args...)
	if err != nil || !strings.Contains(output, "Release v0.1.33 predates panel-manifest.sshsig; using OpenSSL 3.2 or newer.") ||
		!strings.Contains(output, "Verified panel manifest with OpenSSL") {
		t.Fatalf("output %q; error %v; want legacy OpenSSL fallback", output, err)
	}
}

func TestInstallerCurrentReleaseRequiresSSHSIG(t *testing.T) {
	bash := installerBash(t)
	fake := newFakePanelRelease(t)
	delete(fake.assets, release.PanelSSHSIGName)
	server := fake.server()
	defer server.Close()
	args := []string{"--public-url", "https://panel.example.com", "--version", installerTestVersion, "--dry-run"}
	output, err := runInstaller(t, bash, server.URL, fake.publicKey, fake.sshPublicKey, filepath.ToSlash(t.TempDir()), "x86_64", args...)
	if err == nil || !strings.Contains(output, "panel-manifest.sshsig is missing from release v1.2.3 (required since v0.1.34)") {
		t.Fatalf("output %q; error %v; want missing SSHSIG refusal for a current release", output, err)
	}
}

func TestInstallerLegacyReleaseRefusesWithoutVerifier(t *testing.T) {
	bash := installerBash(t)
	if installerOpenSSL32Available() {
		t.Skip("OpenSSL 3.2+ is available")
	}
	fake := newFakePanelReleaseForVersion(t, "v0.1.33")
	delete(fake.assets, release.PanelSSHSIGName)
	server := fake.server()
	defer server.Close()
	args := []string{"--public-url", "https://panel.example.com", "--version", "v0.1.33", "--dry-run"}
	output, err := runInstaller(t, bash, server.URL, fake.publicKey, fake.sshPublicKey, filepath.ToSlash(t.TempDir()), "x86_64", args...)
	if err == nil || !strings.Contains(output, "release v0.1.33 predates panel-manifest.sshsig; OpenSSL 3.2 or newer is required") {
		t.Fatalf("output %q; error %v; want refusal because neither verifier can handle this legacy release", output, err)
	}
}

func TestInstallerRefusesWithoutSignatureVerifier(t *testing.T) {
	bash := installerBash(t)
	fake := newFakePanelRelease(t)
	server := fake.server()
	defer server.Close()
	args := []string{"--public-url", "https://panel.example.com", "--version", installerTestVersion, "--dry-run"}
	path := installerPathWithoutSignatureTools(t)
	output, err := runInstallerWithPath(t, bash, server.URL, fake.publicKey, fake.sshPublicKey,
		filepath.ToSlash(t.TempDir()), "x86_64", path, args...)
	if err == nil || !strings.Contains(output, "ssh-keygen with -Y verify or OpenSSL 3.2 or newer is required") {
		t.Fatalf("output %q; error %v; want refusal when both signature verifiers are unavailable", output, err)
	}
}

func TestInstallerInsecureSkipSignature(t *testing.T) {
	bash := installerBash(t)
	fake := newFakePanelRelease(t)
	fake.assets[release.PanelSignatureName][0] ^= 0xff
	fake.assets[release.PanelSSHSIGName][0] ^= 0xff
	server := fake.server()
	defer server.Close()
	args := []string{"--public-url", "https://panel.example.com", "--version", installerTestVersion, "--dry-run", "--insecure-skip-signature"}
	output, err := runInstaller(t, bash, server.URL, fake.publicKey, fake.sshPublicKey, filepath.ToSlash(t.TempDir()), "x86_64", args...)
	if err != nil || !strings.Contains(output, "signature verification is disabled by --insecure-skip-signature") {
		t.Fatalf("output %q; error %v; want explicit insecure-skip warning", output, err)
	}
}

func TestInstallerSkipsSystemdInTestMode(t *testing.T) {
	bash := installerBash(t)
	release := newFakePanelRelease(t)
	release.assets[installerTestBinary] = []byte("#!/usr/bin/env bash\nif [[ ${1:-} == setup ]]; then\n  printf '%s\\n' 'Data dir: /tmp/mistgate-test' 'Admin URL: https://panel.example.com/test/' 'Setup link: https://panel.example.com/test/setup#" + installerTestToken + "' 'The link works once and expires in 30 minutes.'\n  exit 0\nfi\nexit 2\n")
	release.updateBinaryMetadata(t)
	release.resign(t)
	server := release.server()
	defer server.Close()
	base := t.TempDir()
	binDir := filepath.Join(base, "bin")
	dataDir := filepath.Join(base, "data")
	args := []string{"--public-url", "https://panel.example.com", "--version", installerTestVersion, "--data-dir", dataDir, "--yes"}
	output, err := runInstaller(t, bash, server.URL, release.publicKey, release.sshPublicKey, filepath.ToSlash(binDir), "x86_64", args...)
	if err != nil {
		t.Fatalf("installer failed: %v\n%s", err, output)
	}
	if !strings.Contains(output, "Systemd actions skipped by the test harness") ||
		!strings.Contains(output, "Admin URL: https://panel.example.com/test/") ||
		!strings.Contains(output, "Setup link (valid for 30 minutes): https://panel.example.com/test/setup#"+installerTestToken) {
		t.Fatalf("unexpected installer output:\n%s", output)
	}
	if _, err := os.Stat(filepath.Join(binDir, "mistgate")); err != nil {
		t.Fatalf("installed test panel is missing: %v", err)
	}
}

type fakePanelRelease struct {
	assets       map[string][]byte
	manifest     release.Manifest
	private      ed25519.PrivateKey
	publicKey    string
	sshPublicKey string
}

func newFakePanelRelease(t *testing.T) *fakePanelRelease {
	return newFakePanelReleaseForVersion(t, installerTestVersion)
}

func newFakePanelReleaseForVersion(t *testing.T, version string) *fakePanelRelease {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	r := &fakePanelRelease{
		assets:       map[string][]byte{installerTestBinary: []byte("fake panel binary")},
		private:      priv,
		publicKey:    base64.StdEncoding.EncodeToString(pub),
		sshPublicKey: base64.StdEncoding.EncodeToString(sshPub.Marshal()),
		manifest: release.Manifest{
			Schema: 1, Version: version,
			Built: time.Now().Add(-time.Hour).Unix(), Expires: time.Now().Add(24 * time.Hour).Unix(),
		},
	}
	r.updateBinaryMetadata(t)
	r.resign(t)
	return r
}

func (r *fakePanelRelease) updateBinaryMetadata(t *testing.T) {
	t.Helper()
	body := r.assets[installerTestBinary]
	h := sha256.Sum256(body)
	r.manifest.Files = []release.File{{
		OS: "linux", Arch: "amd64", Name: installerTestBinary, Size: int64(len(body)), SHA256: hex.EncodeToString(h[:]),
	}}
	r.assets["SHA256SUMS"] = []byte(fmt.Sprintf("%x  %s\n", h, installerTestBinary))
}

func (r *fakePanelRelease) resign(t *testing.T) {
	t.Helper()
	manifest, err := r.manifest.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	r.assets["panel-manifest.json"] = manifest
	r.assets[release.PanelSignatureName] = release.SignPanel(r.private, manifest)
	sshsig, err := release.SignPanelSSHSIG(r.private, manifest)
	if err != nil {
		t.Fatal(err)
	}
	r.assets[release.PanelSSHSIGName] = sshsig
}

func (r *fakePanelRelease) server() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		asset := filepath.Base(req.URL.Path)
		body, ok := r.assets[asset]
		if !ok {
			http.NotFound(w, req)
			return
		}
		_, _ = w.Write(body)
	}))
}

func runInstaller(t *testing.T, bash, baseURL, key, sshPublicKey, binDir, arch string, args ...string) (string, error) {
	return runInstallerWithPath(t, bash, baseURL, key, sshPublicKey, binDir, arch, "", args...)
}

func runInstallerWithPath(t *testing.T, bash, baseURL, key, sshPublicKey, binDir, arch, path string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(bash, append([]string{"install.sh"}, args...)...)
	cmd.Env = installerTestEnv(baseURL, key, sshPublicKey, binDir, arch, "1", "1")
	if path != "" {
		env := make([]string, 0, len(cmd.Env))
		for _, item := range cmd.Env {
			if !strings.HasPrefix(item, "PATH=") {
				env = append(env, item)
			}
		}
		cmd.Env = append(env, "PATH="+path)
	}
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func installerPathWithoutSignatureTools(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"curl", "sha256sum", "base64", "awk", "grep", "install", "mktemp", "sed", "wc", "tr", "date", "uname", "rm"} {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Fatalf("required installer command %s: %v", name, err)
		}
		if err := os.Symlink(path, filepath.Join(dir, name)); err != nil {
			t.Fatalf("link installer command %s: %v", name, err)
		}
	}
	return dir
}

func installerTestEnv(baseURL, key, sshPublicKey, binDir, arch, skipSystemd, skipPortCheck string) []string {
	env := make([]string, 0, len(os.Environ())+8)
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, "MISTGATE_INSTALL_") {
			env = append(env, item)
		}
	}
	return append(env,
		"MISTGATE_INSTALL_TEST=1",
		"MISTGATE_INSTALL_BASE_URL="+baseURL,
		"MISTGATE_INSTALL_TEST_KEY="+key,
		"MISTGATE_INSTALL_TEST_SSH_PUBLIC_KEY_BASE64="+sshPublicKey,
		"MISTGATE_INSTALL_TEST_ARCH="+arch,
		"MISTGATE_INSTALL_TEST_OS=Linux",
		"MISTGATE_INSTALL_TEST_BIN_DIR="+binDir,
		"MISTGATE_INSTALL_SKIP_SYSTEMD="+skipSystemd,
		"MISTGATE_INSTALL_SKIP_PORT_CHECK="+skipPortCheck,
	)
}

// The unit the installer writes must stay the unit the manual-install page shows, in both languages.
func TestInstallerUnitMatchesDocs(t *testing.T) {
	script, err := os.ReadFile("install.sh")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?s)<<UNIT\n(.*?)\nUNIT\n`).FindSubmatch(script)
	if m == nil {
		t.Fatal("install.sh has no unit template")
	}
	unit := strings.NewReplacer(
		"$service_command", "/usr/local/bin/mistgate serve --listen :443 --acme-domain panel.example.com",
		"$data_dir", "/var/lib/mistgate",
	).Replace(string(m[1]))
	for _, lang := range []string{"en", "ru"} {
		doc, err := os.ReadFile(filepath.Join("..", "docs", lang, "getting-started", "install-panel.md"))
		if err != nil {
			t.Fatal(err)
		}
		d := regexp.MustCompile("(?s)```ini /etc/systemd/system/mistgate.service\n(.*?)\n```").FindSubmatch(doc)
		if d == nil {
			t.Fatalf("%s install page has no unit", lang)
		}
		if got := stripComments(string(d[1])); got != stripComments(unit) {
			t.Errorf("%s install page unit differs from install.sh:\n%s\n---\n%s", lang, got, unit)
		}
	}
}

func TestInstallerRefusals(t *testing.T) {
	bash := installerBash(t)
	for _, tc := range []struct {
		name string
		env  []string
		args []string
		want string
	}{
		{"public URL is required", nil, []string{"--dry-run"}, "--public-url is required"},
		{"admin host and listener exclude each other", nil, []string{"--public-url", "https://panel.example.com", "--admin-host", "a.example.com", "--admin-listen", "127.0.0.1:8081"}, "cannot be combined"},
		{"test overrides need the test flag", []string{"MISTGATE_INSTALL_BASE_URL=http://127.0.0.1:1"}, []string{"--public-url", "https://panel.example.com"}, "only available when MISTGATE_INSTALL_TEST=1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(bash, append([]string{"install.sh"}, tc.args...)...)
			for _, item := range os.Environ() {
				if !strings.HasPrefix(item, "MISTGATE_INSTALL_") {
					cmd.Env = append(cmd.Env, item)
				}
			}
			cmd.Env = append(cmd.Env, tc.env...)
			out, err := cmd.CombinedOutput()
			if err == nil || !strings.Contains(string(out), tc.want) {
				t.Fatalf("output %q; error %v; want a failure containing %q", out, err, tc.want)
			}
		})
	}
}

// stripComments drops "#" lines: the Russian page translates the comment in the unit.
func stripComments(s string) string {
	var keep []string
	for _, line := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		if !strings.HasPrefix(line, "#") {
			keep = append(keep, line)
		}
	}
	return strings.Join(keep, "\n")
}
