package app

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/buildinfo"
	"github.com/mistgate/mistgate/internal/panel/update"
)

type coreUpdateSource struct{}

func (coreUpdateSource) Sync(context.Context, int64) (bool, error) { return false, nil }
func (coreUpdateSource) BundleStatus() (bool, string)              { return false, "" }

type coreUpdateGate struct{}

func (coreUpdateGate) RunChecksNow(context.Context, string) (int, int, time.Duration, error) {
	return 0, 0, 0, nil
}
func (coreUpdateGate) NodeChecksSince(context.Context, string, time.Time) (int, int, int, error) {
	return 0, 0, 0, nil
}

func TestEdgeNodeUpdateCoreWiring(t *testing.T) {
	oldReleaseKey := buildinfo.ReleaseKey
	t.Cleanup(func() { buildinfo.ReleaseKey = oldReleaseKey })
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	otherPublic, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	buildinfo.ReleaseKey = base64.StdEncoding.EncodeToString(public)
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dataDir, "release.pub"), []byte(base64.StdEncoding.EncodeToString(otherPublic)), 0o600); err != nil {
		t.Fatal(err)
	}

	key, err := nodeUpdateReleaseKey(dataDir, true)
	if err != nil || !key.Equal(public) {
		t.Fatalf("edge update key = %x, %v; want the compiled key", key, err)
	}
	if _, err := nodeUpdateReleaseKey(dataDir, false); !errors.Is(err, buildinfo.ErrReleaseKeyMismatch) {
		t.Fatalf("VPS key load = %v, want release-key mismatch", err)
	}

	source, gate := coreUpdateSource{}, coreUpdateGate{}
	edgeConfig, edgeGate := nodeUpdateConfig(dataDir, true, true, key, nil, nil, source, gate, nil)
	if edgeConfig.DataDir != "" || edgeConfig.NodeBundleSource != nil || !edgeConfig.Edge || !edgeConfig.Key.Equal(public) || edgeGate != nil {
		t.Fatalf("edge update wiring = {dataDir:%q source:%v edge:%t key:%x gate:%v}",
			edgeConfig.DataDir, edgeConfig.NodeBundleSource != nil, edgeConfig.Edge, edgeConfig.Key, edgeGate)
	}

	vpsConfig, vpsGate := nodeUpdateConfig(dataDir, false, false, key, nil, nil, source, gate, nil)
	if vpsConfig.DataDir != dataDir || vpsConfig.NodeBundleSource == nil || vpsConfig.Edge || vpsGate == nil {
		t.Fatalf("VPS update wiring = {dataDir:%q source:%v edge:%t gate:%v}",
			vpsConfig.DataDir, vpsConfig.NodeBundleSource != nil, vpsConfig.Edge, vpsGate)
	}
	if _, ok := vpsGate.(coreUpdateGate); !ok {
		t.Fatalf("VPS gate = %T, want configured health gate", vpsGate)
	}
}

var _ update.NodeBundleSource = coreUpdateSource{}
var _ update.Health = coreUpdateGate{}
