package osupdate

import (
	"net/netip"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/mistgate/mistgate/internal/node/awgprep"
)

// parseDpkgTable reads `dpkg-query -W -f '${db:Status-Abbrev}<TAB>${Package}<TAB>${Version}<NL>'`: the version of every
// installed package, and the packages dpkg left half installed, unpacked, half configured or with triggers waiting (second
// status letter H, U, F, W or t).
func parseDpkgTable(out string) (versions map[string]string, half []string) {
	versions = map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		parts := strings.SplitN(strings.TrimRight(line, "\r"), "\t", 3)
		if len(parts) < 2 {
			continue
		}
		name, ok := normalizePackageName(strings.TrimSpace(parts[1]))
		if !ok {
			continue
		}
		status := parts[0]
		if len(status) > 1 && strings.ContainsRune("HUFWt", rune(status[1])) {
			half = append(half, name)
		}
		if len(status) > 1 && status[1] == 'i' && len(parts) == 3 {
			versions[name] = strings.TrimSpace(parts[2])
		}
	}
	return versions, boundedSorted(half)
}

func installedStatus(versions map[string]string, name string) bool {
	_, ok := versions[name]
	return ok
}

// countChanged is the number of packages that are new or have another version than before.
func countChanged(before, after map[string]string) uint32 {
	var n uint32
	for name, v := range after {
		if old, ok := before[name]; !ok || old != v {
			n++
		}
	}
	return n
}

var errProcessing = regexp.MustCompile(`dpkg: error processing package ([a-z0-9][a-z0-9.+-]{0,63})(?::[a-z0-9-]{1,32})? `)

// failedPackage is the package named by dpkg's "error processing package X" line, or "".
func failedPackage(out string) string {
	if m := errProcessing.FindStringSubmatch(out); m != nil && validPackageName(m[1]) {
		return m[1]
	}
	return ""
}

var hostName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$`)

// classifyUpdate reads the "Err:" lines of `apt-get update`: the hosts whose failure is a security pocket (a suite named
// "<release>-security", on Ubuntu and on Debian alike) and the hosts of every other failed source, each list at most 8,
// names only. A source given by address is reported as "a source": an address never leaves the node.
func classifyUpdate(out string) (security, other []string) {
	sec, oth := map[string]bool{}, map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "Err:")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) < 3 { // "<n> <url> <suite> ..."
			continue
		}
		u, err := url.Parse(fields[1])
		if err != nil || u.Hostname() == "" {
			continue
		}
		h := strings.ToLower(u.Hostname())
		if _, err := netip.ParseAddr(h); err == nil || !hostName.MatchString(h) {
			h = "a source"
		}
		if strings.Contains(fields[2], "-security") || strings.HasPrefix(h, "security.") {
			sec[h] = true
		} else {
			oth[h] = true
		}
	}
	return boundedHosts(sec), boundedHosts(oth)
}

func boundedHosts(m map[string]bool) []string {
	var out []string
	for h := range m {
		out = append(out, h)
	}
	sort.Strings(out)
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}

// dkmsStatusInstalled reads `dkms status -m amneziawg -k K`: "amneziawg/3.1, 6.8.0-1-generic, x86_64: installed".
func dkmsStatusInstalled(out, kernel string) bool {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.Contains(line, ", "+kernel+",") && strings.Contains(line, ": installed") && !strings.Contains(line, "WARNING") {
			return true
		}
	}
	return false
}

// ownDKMSFailure is the shared, exact question: does the output show the AmneziaWG module's own DKMS build failing? (awgprep
// asks it the same way before it rolls a step back.)
func ownDKMSFailure(out string) bool { return awgprep.OwnDKMSFailure(out) }
