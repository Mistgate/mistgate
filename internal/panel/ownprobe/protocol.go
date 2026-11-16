package ownprobe

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
)

const (
	maxSyncBody = 32 << 10
	maxTargets  = 32
	noJobID     = "0000000000000000"
)

//go:embed mistgate-probe.sh
var probeScript []byte

var errBadSync = errors.New("invalid sync report")

// SyncResult is one numbered result. The target address is never present in the report; the index is matched only to the
// panel's saved job.
type SyncResult struct {
	Index           int
	TCP, TLS, Fetch string
	TCPMS, TLSMS    uint32
	Bytes           uint32
}

// SyncReport is a fully parsed report. Unknown and malformed lines fail ParseSync as a whole.
type SyncReport struct {
	Version, JobID string
	Control        string
	ControlMS      uint32
	Reference      string
	Results        []SyncResult
}

// savedJob is the job as the panel kept it: the only source of addresses when a report comes back.
type savedJob struct {
	Targets []Target `json:"targets"`
}

// ParseSync accepts the exact line protocol of the plan (ru-probes-plan §2.3). It does not accept whitespace
// normalization, CRLF, unknown fields, duplicate fields, repeated or unordered result indexes, or values outside the
// curl/script contract: the first line it does not know cancels the whole report.
func ParseSync(body []byte) (SyncReport, error) {
	if len(body) == 0 || len(body) > maxSyncBody || strings.ContainsAny(string(body), "\r\x00\t") {
		return SyncReport{}, errBadSync
	}
	if body[len(body)-1] != '\n' { // the last line ends like the others
		return SyncReport{}, errBadSync
	}
	lines := strings.Split(string(body[:len(body)-1]), "\n")
	if len(lines) < 6 || lines[0] != "mgp 1" || lines[len(lines)-1] != "end" {
		return SyncReport{}, errBadSync
	}
	v := strings.Split(lines[1], " ")
	job := strings.Split(lines[2], " ")
	ctl := strings.Split(lines[3], " ")
	ref := strings.Split(lines[4], " ")
	if len(v) != 2 || v[0] != "v" || !lowerHex(v[1], 12) ||
		len(job) != 2 || job[0] != "job" || !lowerHex(job[1], 16) ||
		len(ctl) != 3 || ctl[0] != "ctl" || !oneOf(ctl[1], "ok", "fail") ||
		len(ref) != 2 || ref[0] != "ref" || !oneOf(ref[1], "blocked", "open", "fail") {
		return SyncReport{}, errBadSync
	}
	controlMS, ok := parseBoundedUint(ctl[2], 120000)
	if !ok {
		return SyncReport{}, errBadSync
	}
	out := SyncReport{Version: v[1], JobID: job[1], Control: ctl[1], ControlMS: controlMS, Reference: ref[1]}
	lastIndex := 0
	for _, line := range lines[5 : len(lines)-1] {
		f := strings.Split(line, " ")
		if len(f) != 8 || f[0] != "r" {
			return SyncReport{}, errBadSync
		}
		index64, err := strconv.ParseUint(f[1], 10, 8)
		if err != nil || index64 < 1 || index64 > maxTargets || strconv.FormatUint(index64, 10) != f[1] || int(index64) <= lastIndex ||
			!oneOf(f[2], "ok", "refused", "timeout", "unreach") ||
			!oneOf(f[4], "ok", "reset", "timeout", "alert", "fail", "-") ||
			!oneOf(f[6], "full", "freeze", "short", "-") {
			return SyncReport{}, errBadSync
		}
		tcpMS, okTCP := parseBoundedUint(f[3], 120000)
		tlsMS, okTLS := parseBoundedUint(f[5], 120000)
		bytes, okBytes := parseBoundedUint(f[7], 65536)
		if !okTCP || !okTLS || !okBytes {
			return SyncReport{}, errBadSync
		}
		// What was not measured is a dash and a zero: no TLS after a TCP that did not connect, no page after a TLS that failed.
		if (f[2] != "ok" && (f[4] != "-" || f[6] != "-")) || (f[4] != "ok" && f[6] != "-") ||
			(f[4] == "-" && tlsMS != 0) || (f[6] == "-" && bytes != 0) {
			return SyncReport{}, errBadSync
		}
		lastIndex = int(index64)
		out.Results = append(out.Results, SyncResult{Index: lastIndex, TCP: f[2], TCPMS: tcpMS, TLS: f[4], TLSMS: tlsMS, Fetch: f[6], Bytes: bytes})
	}
	if out.JobID == noJobID && len(out.Results) != 0 {
		return SyncReport{}, errBadSync
	}
	return out, nil
}

func oneOf(value string, choices ...string) bool {
	for _, choice := range choices {
		if value == choice {
			return true
		}
	}
	return false
}

func lowerHex(value string, size int) bool {
	if len(value) != size {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func parseBoundedUint(value string, max uint64) (uint32, bool) {
	n, err := strconv.ParseUint(value, 10, 32)
	return uint32(n), err == nil && n <= max && strconv.FormatUint(n, 10) == value
}

// ScriptHash is the SHA-256 of the embedded installation script.
func ScriptHash() string {
	h := sha256.Sum256(probeScript)
	return hex.EncodeToString(h[:])
}

// ScriptVersion is the version identifier carried by sync reports.
func ScriptVersion() string { return ScriptHash()[:12] }

func scriptETag() string { return `"` + ScriptHash() + `"` }

// baseKinds are the target kinds every probe knows: the ones of "mgp 1". A probe rejects a whole answer that has a line it does
// not know, so the panel sends a probe only the kinds its version is known to run.
var baseKinds = []string{"tcp", "tls", "fetch"}

// kindsByVersion lists, per probe version (the "v" of its report: the first 12 characters of the sha256 of its program), the
// kinds it runs beyond baseKinds. A version that is not listed (an old script, a build this panel does not know) gets baseKinds
// only. The Go probe of step 6 adds its versions and "quic", "awg" here; nothing else in the protocol changes.
var kindsByVersion = map[string][]string{}

// kindsFor returns the target kinds a probe of this version can be given.
func kindsFor(version string) []string {
	if extra := kindsByVersion[version]; len(extra) > 0 {
		return append(slicesClone(baseKinds), extra...)
	}
	return baseKinds
}

func kindAllowed(version, kind string) bool {
	for _, k := range kindsFor(version) {
		if k == kind {
			return true
		}
	}
	return false
}

// fits says whether a result can belong to a target of this kind: a tcp target has no TLS or page in it, a tls target no page.
func (r SyncResult) fits(kind string) bool {
	switch kind {
	case "tcp":
		return r.TLS == "-" && r.Fetch == "-"
	case "tls":
		return r.Fetch == "-"
	}
	return true
}
