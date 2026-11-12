package osupdate

import (
	"context"
	"strings"
	"testing"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

// The reader asks apt for Automatic-Reboot as a boolean ("/b"), so any spelling the configuration uses (true, yes, on, 1, with,
// enable) comes back as true or false.
func TestReaderAsksAutomaticRebootAsABoolean(t *testing.T) {
	for _, tc := range []struct {
		answer string
		want   bool
	}{{"R='true'", true}, {"R='false'", false}} {
		var asked string
		r := &Reader{Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			switch name {
			case "dpkg-query":
				return []byte("install ok installed"), nil
			case "apt-config":
				asked = strings.Join(args, " ")
				return []byte("E='1'; L='1'; U='1'; " + tc.answer + ";\n"), nil
			case "systemctl":
				if args[0] == "is-enabled" {
					return []byte("enabled\nenabled\n"), nil
				}
				return []byte("active\nactive\n"), nil
			}
			return nil, nil
		}}
		s := &agentv1.OsStatus{}
		r.autoUpdates(context.Background(), s)
		if !strings.Contains(asked, "Unattended-Upgrade::Automatic-Reboot/b") || s.AutoReboot != tc.want {
			t.Errorf("answer %s: asked %q, AutoReboot=%v", tc.answer, asked, s.AutoReboot)
		}
	}
}
