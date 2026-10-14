package jsoncheck

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// SingboxBins are the sing-box binaries to ask: SINGBOX_BIN (several paths separated by the OS path-list separator),
// or else every ~/.cache/mistgate-tests/sing-box-* the download step left there. Empty means the test is skipped,
// like the Mihomo core test without MIHOMO_BIN.
func SingboxBins() []string {
	if v := os.Getenv("SINGBOX_BIN"); v != "" {
		return filepath.SplitList(v)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	found, _ := filepath.Glob(filepath.Join(home, ".cache", "mistgate-tests", "sing-box-*"))
	var bins []string
	for _, f := range found {
		if !strings.HasSuffix(f, ".zip") {
			bins = append(bins, f)
		}
	}
	return bins
}

// SingboxConfig wraps one outbound fragment (no tag) into the smallest profile `sing-box check` accepts: the
// outbound tagged "proxy", a direct one and a final route.
func SingboxConfig(t testing.TB, outbound []byte) []byte {
	t.Helper()
	var o map[string]json.RawMessage
	if err := json.Unmarshal(outbound, &o); err != nil {
		t.Fatalf("outbound is not a JSON object: %v\n%s", err, outbound)
	}
	o["tag"] = json.RawMessage(`"proxy"`)
	cfg, err := json.Marshal(map[string]any{
		"log":       map[string]any{"level": "warn"},
		"outbounds": []any{o, map[string]any{"type": "direct", "tag": "direct"}},
		"route":     map[string]any{"final": "proxy"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// SingboxCheck runs `sing-box check -c` for every binary and fails on the first refusal. It returns false when
// there is no binary (the caller skips).
func SingboxCheck(t testing.TB, config []byte) bool {
	t.Helper()
	bins := SingboxBins()
	if len(bins) == 0 {
		return false
	}
	cfg := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(cfg, config, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, bin := range bins {
		if out, err := exec.Command(bin, "check", "-c", cfg).CombinedOutput(); err != nil {
			t.Fatalf("%s check refused the profile: %v\n%s\n%s", filepath.Base(bin), err, out, config)
		}
	}
	return true
}
