//go:build !js

package app

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/buildinfo"
)

// buildLogged builds a panel and returns what it logged and its data directory.
func buildLogged(t *testing.T, edge bool) (string, string) {
	t.Helper()
	var logs bytes.Buffer
	dir := t.TempDir()
	if edge {
		dir = filepath.Join(dir, "absent") // like the edge's placeholder data directory: it does not exist
	}
	var remote *edgeTickRemote
	if edge {
		remote = &edgeTickRemote{}
	}
	tweak := func(c *Config) {
		c.Logger = slog.New(slog.NewTextHandler(&logs, nil))
		c.DataDir = dir
	}
	if edge {
		buildPanelWith(t, remote, tweak)
	} else {
		buildPanelWith(t, nil, tweak)
	}
	return logs.String(), dir
}

func withReleaseKey(t *testing.T, key string) {
	t.Helper()
	old := buildinfo.ReleaseKey
	buildinfo.ReleaseKey = key
	t.Cleanup(func() { buildinfo.ReleaseKey = old })
}

// The edge uses the compiled-in release key without a word and never looks for release.pub, which a Worker has no
// directory for; the VPS still keeps the key in its data directory.
func TestEdgeUsesTheCompiledReleaseKeyWithoutTheFile(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	withReleaseKey(t, base64.StdEncoding.EncodeToString(pub))

	logs, dir := buildLogged(t, true)
	if strings.Contains(logs, "release key") {
		t.Errorf("edge, compiled-in key: logged about the release key:\n%s", logs)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("edge touched its data directory (stat err %v)", err)
	}

	logs, dir = buildLogged(t, false)
	if strings.Contains(logs, "release key") {
		t.Errorf("VPS, compiled-in key: logged about the release key:\n%s", logs)
	}
	if _, err := os.Stat(filepath.Join(dir, "release.pub")); err != nil {
		t.Errorf("VPS did not store release.pub: %v", err)
	}
}

// A development build is silent on the edge too; a key that is stamped but broken is still reported.
func TestEdgeReleaseKeyWarnsOnlyWhenBroken(t *testing.T) {
	withReleaseKey(t, "")
	if logs, _ := buildLogged(t, true); strings.Contains(logs, "release key") {
		t.Errorf("edge, unsigned build: logged about the release key:\n%s", logs)
	}
	withReleaseKey(t, "not base64!")
	if logs, _ := buildLogged(t, true); !strings.Contains(logs, "release key is unusable") {
		t.Errorf("edge, broken key: no warning in:\n%s", logs)
	}
}
