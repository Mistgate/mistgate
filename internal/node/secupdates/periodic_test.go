package secupdates

import (
	"reflect"
	"testing"
)

func TestParsePeriodicShellAndDisabledSettings(t *testing.T) {
	for _, tc := range []struct {
		name   string
		output string
		keys   []string
		detail []string
	}{
		{name: "all enabled", output: "E='1'; L='1'; U='always';"},
		{
			name:   "missing settings are disabled",
			output: "E='1';",
			keys:   []string{"APT::Periodic::Update-Package-Lists", "APT::Periodic::Unattended-Upgrade"},
			detail: []string{`APT::Periodic::Update-Package-Lists ""`, `APT::Periodic::Unattended-Upgrade ""`},
		},
		{
			name:   "zero values are disabled",
			output: `E='0'; L='0'; U="1";`,
			keys:   []string{"APT::Periodic::Enable", "APT::Periodic::Update-Package-Lists"},
			detail: []string{`APT::Periodic::Enable "0"`, `APT::Periodic::Update-Package-Lists "0"`},
		},
		{
			name:   "always is an enabled value",
			output: "L='always'; U='1';",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ParsePeriodicShell(tc.output)
			if keys := got.DisabledKeys(); !reflect.DeepEqual(keys, tc.keys) {
				t.Errorf("disabled keys = %#v, want %#v", keys, tc.keys)
			}
			if details := got.OffDetails(); !reflect.DeepEqual(details, tc.detail) {
				t.Errorf("off details = %#v, want %#v", details, tc.detail)
			}
		})
	}
}

func TestConfigForDisabledWritesOnlyNeededKeys(t *testing.T) {
	got := ParsePeriodicShell("E='0'; L='0'; U='1';").ConfigForDisabled()
	want := "# Managed by mistgate-node enable-security-updates.\n" +
		"APT::Periodic::Enable \"1\";\n" +
		"APT::Periodic::Update-Package-Lists \"1\";\n"
	if got != want {
		t.Fatalf("config = %q, want %q", got, want)
	}
}
