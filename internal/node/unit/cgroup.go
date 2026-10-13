package unit

import (
	"strings"
)

// DetectSystemdService reports whether the process belongs to a systemd cgroup and whether it belongs to the exact
// requested service. Cgroup path components are compared as complete names so similarly named units do not match.
func DetectSystemdService(cgroup []byte, service string) (managed, matches bool) {
	for _, line := range strings.Split(string(cgroup), "\n") {
		fields := strings.SplitN(line, ":", 3)
		if len(fields) != 3 {
			continue
		}
		controllers, group := fields[1], fields[2]
		if controllers == "name=systemd" {
			managed = true
		}
		for _, part := range strings.Split(strings.Trim(group, "/"), "/") {
			if part == service {
				managed, matches = true, true
			}
			if isSystemdUnitComponent(part) {
				managed = true
			}
		}
	}
	return managed, matches
}

func isSystemdUnitComponent(part string) bool {
	for _, suffix := range []string{".service", ".scope", ".slice", ".target", ".mount", ".automount", ".timer", ".socket", ".path", ".device"} {
		if strings.HasSuffix(part, suffix) {
			return true
		}
	}
	return false
}
