package awg

import "testing"

// The interface name is what the host's firewall and Cleanup know the interface by; the engine must build exactly it.
func TestIfaceNameIsWhatTheEngineBuilds(t *testing.T) {
	for _, port := range []uint16{1, 51842, 65535} {
		cfg, err := parseSpec(spec(t, "inb_a", port, nil))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.name != IfaceName(port) {
			t.Errorf("port %d: the engine builds %q, IfaceName says %q", port, cfg.name, IfaceName(port))
		}
		if len(IfaceName(port)) > 15 {
			t.Errorf("%q does not fit an interface name", IfaceName(port))
		}
	}
}
