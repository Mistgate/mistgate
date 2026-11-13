package awgprep

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
)

// The AmneziaWG module source (amneziawg-linux-kernel-module, GPL-2.0) calls setup_udp_tunnel_sock and
// udp_tunnel_sock_release through macros in compat/compat.h. The macros of the files below pick the argument (struct socket *
// or struct sock *) by the kernel's version number. A distribution that backports one of the two kernel API changes without
// changing that number (Ubuntu did, in 7.0.0-38) breaks the build, and with it the DKMS configure of every kernel package.
//
// FixCompat repairs that, but only in a file it knows: the sha256 of the file has to be in compatTable, and the file it writes
// has to hash to the entry's pinned result. Any other file is reported with the start of its hash and left alone. When the
// distribution ships a fixed package the file has another hash, so the patch simply does not apply to it.

// CompatState says what FixCompat found in one compat.h.
type CompatState string

const (
	CompatAbsent     CompatState = "absent"      // the source has no compat/compat.h (not installed)
	CompatOK         CompatState = "ok"          // a file known to build on every kernel (also one this function patched earlier)
	CompatPatched    CompatState = "patched"     // the known-bad file was replaced just now
	CompatNeedsPatch CompatState = "needs_patch" // a known-bad file; check mode, or the patch could not be written (Err says)
	CompatUnknown    CompatState = "unknown"     // a file Mistgate does not know: nothing was changed
)

// CompatResult is the outcome for one compat.h.
type CompatResult struct {
	State   CompatState
	Path    string
	SHA12   string // the first 12 hex characters of the sha256 the file had when it was read ("" = unreadable or absent)
	Changed bool
	Err     string // why a read or a write failed
}

// compatNewBlock replaces the two macro lines. It chooses the call shape by the prototype the kernel really has: typeof(&f)
// names the function itself (the macro is not expanded there), __builtin_types_compatible_p compares it with the old shape,
// and the cast to void * keeps the branch that is not chosen valid for the compiler. The names of the macro parameters are
// unlikely ones on purpose: a parameter that is called "net" would also replace the "net" of "struct net".
const compatNewBlock = `/* mistgate: pick the argument by the prototype this kernel really has, not by its version (distributions backport) */
#define setup_udp_tunnel_sock(mg_net, mg_sk, mg_cfg) setup_udp_tunnel_sock(mg_net, (void *)__builtin_choose_expr( \
	__builtin_types_compatible_p(typeof(&setup_udp_tunnel_sock), \
		void (*)(struct net *, struct socket *, struct udp_tunnel_sock_cfg *)), (mg_sk)->sk_socket, (mg_sk)), mg_cfg)
#define udp_tunnel_sock_release(mg_sk) udp_tunnel_sock_release((void *)__builtin_choose_expr( \
	__builtin_types_compatible_p(typeof(&udp_tunnel_sock_release), void (*)(struct socket *)), (mg_sk)->sk_socket, (mg_sk)))
`

type compatEntry struct {
	name   string
	before string // sha256 of the known-bad file
	old    string // the lines of that file compatNewBlock replaces, byte for byte
	after  string // sha256 the patched file must have
}

const (
	// compatSetupLine and compatReleaseLine are the two macro lines of amneziawg-dkms 1.0.0 (the Amnezia PPA), which is also
	// the module source of tag v3.1.20260906, the tag the Debian recipe builds.
	compatSetupLine   = "#define setup_udp_tunnel_sock(net, sk, sock_cfg) setup_udp_tunnel_sock(net, sk->sk_socket, sock_cfg)\n"
	compatReleaseLine = "#define udp_tunnel_sock_release(sk) udp_tunnel_sock_release(sk->sk_socket)\n"

	compatAfter = "e0a3a71e2ae4c8a5d65287af88d02e5f0563c52f0854ee760c2405934d32d590" // both entries end in the same text
)

// compatTable is every known-bad file. Delete an entry when no node holds that file any more.
var compatTable = []compatEntry{
	{name: "amneziawg-dkms 1.0.0 (Amnezia PPA)", before: "b14346040ce0188c47e2db2baad1a4f21aa784510f6c95bbb4aa58d5bbe691c9", old: compatSetupLine + compatReleaseLine, after: compatAfter},
	// A node whose owner deleted the first macro line by hand, which works on kernels where setup_udp_tunnel_sock takes a struct sock * only.
	{name: "amneziawg-dkms 1.0.0 with the setup line deleted by hand", before: "e308aa5c04ce310a9e8d4541e8872a3021646128905559d4ee696bfddc315f1b", old: compatReleaseLine, after: compatAfter},
}

// compatGood are the files that build on every kernel and are left alone: what the table produces, and the file of the
// upstream tag v3.1.20261113 (the fix of upstream commit 70ae2481).
var compatGood = map[string]bool{
	compatAfter: true,
	"b782a60011234c9de74a7db5966a4a83234f82b0099ff568049e9a5f17a40e83": true,
}

// dkmsSourceGlob finds the module source trees DKMS keeps (amneziawg-dkms installs /usr/src/amneziawg-<version>).
const dkmsSourceGlob = "/usr/src/amneziawg-*"

// RealSourceDirs lists the module source trees of this machine.
func RealSourceDirs() []string {
	m, _ := filepath.Glob(dkmsSourceGlob)
	return m
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// FixCompat looks at <dir>/compat/compat.h for every module source dir (the DKMS trees under /usr/src, or the clone of the
// Debian recipe's src/ directory) and repairs a known-bad one. check = true changes nothing. One result per dir, in the order of dirs;
// what it did is also written to w (nil = nowhere). sys needs ReadFile and, unless check, WriteFile (atomic: a temporary
// file and a rename, so a failure leaves the old file).
func FixCompat(sys Sys, dirs []string, check bool, w io.Writer) []CompatResult {
	if w == nil {
		w = io.Discard
	}
	var out []CompatResult
	for _, d := range dirs {
		r := fixOne(sys, path.Join(d, "compat", "compat.h"), check)
		out = append(out, r)
		line := fmt.Sprintf("module source %s: %s", r.Path, r.State)
		if r.SHA12 != "" {
			line += " (sha256 " + r.SHA12 + ")"
		}
		if r.Err != "" {
			line += ": " + r.Err
		}
		fmt.Fprintln(w, line)
	}
	return out
}

func fixOne(sys Sys, p string, check bool) CompatResult {
	r := CompatResult{Path: p}
	if sys.ReadFile == nil {
		r.State, r.Err = CompatUnknown, "reading files is unavailable"
		return r
	}
	orig, err := sys.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		r.State = CompatAbsent
		return r
	}
	if err != nil {
		r.State, r.Err = CompatUnknown, err.Error()
		return r
	}
	sum := sha256Hex(orig)
	r.SHA12 = sum[:12]
	if compatGood[sum] {
		r.State = CompatOK
		return r
	}
	var e *compatEntry
	for i := range compatTable {
		if compatTable[i].before == sum {
			e = &compatTable[i]
		}
	}
	if e == nil {
		r.State = CompatUnknown
		return r
	}
	r.State = CompatNeedsPatch
	if check {
		return r
	}
	if strings.Count(string(orig), e.old) != 1 {
		r.Err = "the lines to replace are not there exactly once; nothing was changed"
		return r
	}
	fixed := strings.Replace(string(orig), e.old, compatNewBlock, 1)
	if sha256Hex([]byte(fixed)) != e.after {
		r.Err = "the patched file would not match the pinned result; nothing was changed"
		return r
	}
	if sys.WriteFile == nil {
		r.Err = "writing files is unavailable"
		return r
	}
	if err := sys.WriteFile(p, fixed); err != nil {
		r.Err = "writing the patched file failed: " + err.Error()
		return r
	}
	// Read it back: a file that is not what was meant is put back as it was.
	if got, err := sys.ReadFile(p); err != nil || sha256Hex(got) != e.after {
		if rerr := sys.WriteFile(p, string(orig)); rerr != nil {
			r.Err = "the written file did not read back as expected, and restoring the original failed: " + rerr.Error()
		} else {
			r.Err = "the written file did not read back as expected; the original was put back"
		}
		return r
	}
	r.State, r.Changed = CompatPatched, true
	return r
}

// CompatChanged says whether any result is a file that was patched.
func CompatChanged(rs []CompatResult) bool {
	for _, r := range rs {
		if r.Changed {
			return true
		}
	}
	return false
}

// CompatUnknownSHA is the start of the hash of the first file Mistgate does not know, "" when there is none.
func CompatUnknownSHA(rs []CompatResult) string {
	for _, r := range rs {
		if r.State == CompatUnknown && r.SHA12 != "" {
			return r.SHA12
		}
	}
	return ""
}

// CompatErr is the first read or write failure among the results.
func CompatErr(rs []CompatResult) error {
	for _, r := range rs {
		if r.Err != "" {
			return fmt.Errorf("module source %s: %s", r.Path, r.Err)
		}
	}
	return nil
}

// UnknownSourceNote words the reason a build fails on a source Mistgate does not know.
func UnknownSourceNote(sha12 string) string {
	return fmt.Sprintf("the module source is not one Mistgate knows (sha256 %s); nothing was changed", sha12)
}
