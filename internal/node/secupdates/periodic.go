package secupdates

import (
	"fmt"
	"strings"
)

const ConfigPath = "/etc/apt/apt.conf.d/99mistgate-security-updates"

const AptConfigCommand = "apt-config"

const AptPeriodicArgs = "shell E APT::Periodic::Enable L APT::Periodic::Update-Package-Lists U APT::Periodic::Unattended-Upgrade"

// Periodic contains the effective APT::Periodic values printed by `apt-config shell`.
type Periodic struct {
	Enable             string
	UpdatePackageLists string
	UnattendedUpgrade  string
}

// ParsePeriodicShell reads apt-config's shell assignments without evaluating them.
func ParsePeriodicShell(out string) Periodic {
	values := make(map[string]string, 3)
	for _, part := range strings.FieldsFunc(out, func(r rune) bool { return r == ';' || r == '\n' }) {
		name, raw, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		values[strings.TrimSpace(name)] = shellValue(strings.TrimSpace(raw))
	}
	return Periodic{
		Enable:             values["E"],
		UpdatePackageLists: values["L"],
		UnattendedUpgrade:  values["U"],
	}
}

func shellValue(raw string) string {
	if len(raw) >= 2 && (raw[0] == '\'' && raw[len(raw)-1] == '\'' || raw[0] == '"' && raw[len(raw)-1] == '"') {
		return raw[1 : len(raw)-1]
	}
	return raw
}

// DisabledKeys lists the effective settings that keep automatic package updates off.
func (p Periodic) DisabledKeys() []string {
	var keys []string
	if strings.TrimSpace(p.Enable) == "0" {
		keys = append(keys, "APT::Periodic::Enable")
	}
	if periodicOff(p.UpdatePackageLists) {
		keys = append(keys, "APT::Periodic::Update-Package-Lists")
	}
	if periodicOff(p.UnattendedUpgrade) {
		keys = append(keys, "APT::Periodic::Unattended-Upgrade")
	}
	return keys
}

// OffDetails returns the raw setting names and values used in the doctor warning.
func (p Periodic) OffDetails() []string {
	var details []string
	if strings.TrimSpace(p.Enable) == "0" {
		details = append(details, fmt.Sprintf(`APT::Periodic::Enable %q`, strings.TrimSpace(p.Enable)))
	}
	if periodicOff(p.UpdatePackageLists) {
		details = append(details, fmt.Sprintf(`APT::Periodic::Update-Package-Lists %q`, strings.TrimSpace(p.UpdatePackageLists)))
	}
	if periodicOff(p.UnattendedUpgrade) {
		details = append(details, fmt.Sprintf(`APT::Periodic::Unattended-Upgrade %q`, strings.TrimSpace(p.UnattendedUpgrade)))
	}
	return details
}

// ConfigForDisabled returns only the settings that need to be enabled in Mistgate's override file.
func (p Periodic) ConfigForDisabled() string {
	var b strings.Builder
	b.WriteString("# Managed by mistgate-node enable-security-updates.\n")
	for _, key := range p.DisabledKeys() {
		fmt.Fprintf(&b, "%s \"1\";\n", key)
	}
	return b.String()
}

func periodicOff(value string) bool {
	value = strings.TrimSpace(value)
	return value == "" || value == "0"
}
