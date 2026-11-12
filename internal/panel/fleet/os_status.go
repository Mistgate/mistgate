package fleet

import (
	"context"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/osupdate"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

var (
	osPackageName = regexp.MustCompile("^[a-z0-9][a-z0-9.+-]{0,63}$")
	osVersionText = regexp.MustCompile("^[A-Za-z0-9.+:~%-]{1,80}$")
	osKernelText  = regexp.MustCompile("^[A-Za-z0-9.+_-]{1,128}$")
	osDistroText  = regexp.MustCompile("^[a-z0-9._-]{1,64}$")
	osDistroVer   = regexp.MustCompile("^[A-Za-z0-9.+_-]{1,64}$")
	osIPText      = regexp.MustCompile("(?:[0-9]{1,3}[.]){3}[0-9]{1,3}")
	osIPv6Text    = regexp.MustCompile(`(?i)(?:[0-9a-f]{1,4}:){2,7}[0-9a-f]{1,4}|(?:[0-9a-f]{1,4}:){1,7}:(?:[0-9a-f]{1,4})?|::[0-9a-f]{1,4}(?::[0-9a-f]{1,4})*`)
	osBootIDHash  = regexp.MustCompile("^[a-f0-9]{16}$")
)

func (f *Fleet) recordOsStatus(ctx context.Context, nodeID string, status *agentv1.OsStatus, at time.Time) {
	// validOsStatus drops bad entries in place, and the frame it came from is not ours to change.
	status = proto.Clone(status).(*agentv1.OsStatus)
	if !validOsStatus(status, at) {
		f.log.Warn("OS status rejected", "code", "os_status_invalid", "node", nodeID)
		return
	}
	body, err := protojson.Marshal(status)
	if err != nil {
		f.log.Warn("OS status could not be encoded", "code", "os_status_encode_failed", "node", nodeID)
		return
	}
	if err := f.st.PutOsStatus(ctx, nodeID, string(body), time.Unix(status.MeasuredUnix, 0), at); err != nil {
		f.log.Warn("OS status could not be stored", "code", "os_status_store_failed", "node", nodeID, "err", err)
	}
}

// validOsStatus checks an untrusted snapshot against the bounds the panel stores. A bad notable entry is dropped from s
// and the rest is kept; every other violation rejects the snapshot.
func validOsStatus(s *agentv1.OsStatus, now time.Time) bool {
	if s == nil || s.MeasuredUnix <= 0 || time.Unix(s.MeasuredUnix, 0).After(now.Add(5*time.Minute)) || time.Unix(s.MeasuredUnix, 0).Before(now.Add(-7*24*time.Hour)) {
		return false
	}
	if s.Pending > 100000 || s.Security > s.Pending || len(s.Notable) > 16 || len(s.RebootReasons) > 16 || len(s.Held) > 16 || len(s.HalfConfigured) > 16 {
		return false
	}
	// Both may be empty: Debian testing and sid have no VERSION_ID, and a failed os-release read leaves both empty. Such a
	// snapshot is still stored; its state is then UNSUPPORTED or the read error.
	if s.DistroId != "" && (!osDistroText.MatchString(s.DistroId) || !safeOSValue(s.DistroId)) ||
		s.DistroVersion != "" && (!osDistroVer.MatchString(s.DistroVersion) || !safeOSValue(s.DistroVersion)) {
		return false
	}
	if s.KernelRunning != "" && (!osKernelText.MatchString(s.KernelRunning) || !safeOSValue(s.KernelRunning)) ||
		s.KernelNewest != "" && (!osKernelText.MatchString(s.KernelNewest) || !safeOSValue(s.KernelNewest)) {
		return false
	}
	if s.ListsUnix < 0 || s.DpkgUnix < 0 || !oneOf(s.Apt, "ok", "locked", "half_configured", "no_lists", "unknown") ||
		!oneOf(s.AutoUpdates, "on", "off", "not_installed", "unknown") || !oneOf(s.AwgModuleNext, "", "ready", "missing") ||
		!oneOf(s.Error, "", "not_apt", "no_systemd", "read_failed") {
		return false
	}
	if s.Container != "" && (len(s.Container) > 48 || !osKernelText.MatchString(s.Container) || !safeOSValue(s.Container)) ||
		s.BootIdHash != "" && !osBootIDHash.MatchString(s.BootIdHash) {
		return false
	}
	for _, name := range append(append([]string(nil), s.Held...), s.HalfConfigured...) {
		if !safeOSPackage(name) {
			return false
		}
	}
	for _, reason := range s.RebootReasons {
		if reason != "kernel" && !safeOSPackage(reason) {
			return false
		}
	}
	s.Notable = slices.DeleteFunc(s.Notable, func(notable string) bool { return !validNotable(notable) })
	// A job that does not check out is dropped on its own, like a bad notable entry: the rest of the snapshot is stored.
	if s.Job != nil && !validOsJob(s.Job, now) {
		s.Job = nil
	}
	return true
}

var (
	osJobID     = regexp.MustCompile("^(osj_|manual-)[a-z0-9_-]{1,48}$")
	osHostName  = regexp.MustCompile("^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(?:[.][a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)+$")
	osReasonBad = regexp.MustCompile("[\x00-\x1f\x7f]")
)

// validOsJob checks the job of a snapshot: every word is from the vocabulary of agent.proto "OS UPDATES", the sentence is
// one short line without an address, the times are sane.
func validOsJob(j *agentv1.OsJob, now time.Time) bool {
	if !osJobID.MatchString(j.JobId) || !oneOf(j.Action, "refresh", "upgrade", "reboot") || !oneOf(j.Mode, "", "security", "all") ||
		!oneOf(j.State, "running", "done", "failed") || !oneOf(j.Reboot, "", "done", "not_required", "held", "not_asked") {
		return false
	}
	if j.Phase != "" && !slices.Contains(osupdate.Phases, j.Phase) || j.Code != "" && !slices.Contains(osupdate.Codes, j.Code) ||
		j.HeldReason != "" && !slices.Contains(osupdate.Codes, j.HeldReason) {
		return false
	}
	limit := now.Add(5 * time.Minute).Unix()
	for _, t := range []int64{j.StartedUnix, j.UpdatedUnix, j.FinishedUnix} {
		if t < 0 || t > limit {
			return false
		}
	}
	if j.Upgraded > 100000 || len(j.SourcesFailed) > 8 || len(j.Reason) > 300 || osReasonBad.MatchString(j.Reason) ||
		osIPText.MatchString(j.Reason) || osIPv6Text.MatchString(j.Reason) {
		return false
	}
	for _, h := range j.SourcesFailed {
		if h != "a source" && (!osHostName.MatchString(h) || !safeOSValue(h)) {
			return false
		}
	}
	for _, k := range []string{j.KernelFrom, j.KernelTo} {
		if k != "" && (!osKernelText.MatchString(k) || !safeOSValue(k)) {
			return false
		}
	}
	return true
}

func safeOSPackage(s string) bool {
	if !osPackageName.MatchString(s) {
		return false
	}
	_, err := netip.ParseAddr(s)
	return err != nil
}

// validNotable accepts one "package old new s|-" entry exactly as the agent writes it: single spaces, nothing around.
// Stored verbatim, a looser entry would reach the screens and the MCP view with whatever whitespace it carried.
func validNotable(notable string) bool {
	parts := strings.Fields(notable)
	return notable == strings.Join(parts, " ") && len(parts) == 4 && safeOSPackage(parts[0]) &&
		safeOSVersion(parts[1]) && safeOSVersion(parts[2]) && (parts[3] == "s" || parts[3] == "-")
}

// safeOSVersion does not look for addresses: a dotted quad is a legal Debian version, and Ubuntu's own kernel
// meta-packages carry versions like 5.15.0.186.166. Only the character set and length are bounded.
func safeOSVersion(s string) bool {
	return s == "-" || osVersionText.MatchString(s)
}

func safeOSValue(s string) bool {
	if s == "" || len(s) > 128 || strings.ContainsAny(s, "\r\n\x00") || osIPText.MatchString(s) || osIPv6Text.MatchString(s) {
		return false
	}
	_, err := netip.ParseAddr(s)
	return err != nil
}

func oneOf(got string, values ...string) bool {
	for _, v := range values {
		if got == v {
			return true
		}
	}
	return false
}
