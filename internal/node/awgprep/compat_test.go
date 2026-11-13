package awgprep

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"strings"
	"testing"
)

// The fixtures are the real compat.h files (testdata/compat/README.md). The hashes are written out here on purpose: a change
// of the table or of the block has to show up in this file too.
const (
	hashPPA      = "b14346040ce0188c47e2db2baad1a4f21aa784510f6c95bbb4aa58d5bbe691c9"
	hashHand     = "e308aa5c04ce310a9e8d4541e8872a3021646128905559d4ee696bfddc315f1b"
	hashUpstream = "b782a60011234c9de74a7db5966a4a83234f82b0099ff568049e9a5f17a40e83"
	hashFixed    = "e0a3a71e2ae4c8a5d65287af88d02e5f0563c52f0854ee760c2405934d32d590"
)

func fixture(t *testing.T, name, wantHash string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/compat/" + name)
	if err != nil {
		t.Fatal(err)
	}
	if got := sha256Hex(b); got != wantHash {
		t.Fatalf("fixture %s has sha256 %s, want %s (was it reformatted?)", name, got, wantHash)
	}
	return string(b)
}

// memFS is a machine with files: Sys over a map. failWrite makes WriteFile fail, corrupt makes it write something else.
type memFS struct {
	files     map[string]string
	writes    int
	failWrite bool
	corrupt   bool
}

const srcDir = "/usr/src/amneziawg-1.0.0"

func compatPath(dir string) string { return path.Join(dir, "compat", "compat.h") }

func newMemFS(body string) *memFS { return &memFS{files: map[string]string{compatPath(srcDir): body}} }

func (m *memFS) sys() Sys {
	return Sys{
		ReadFile: func(p string) ([]byte, error) {
			if s, ok := m.files[p]; ok {
				return []byte(s), nil
			}
			return nil, fs.ErrNotExist
		},
		WriteFile: func(p, body string) error {
			m.writes++
			if m.failWrite {
				return errors.New("read-only file system")
			}
			if m.corrupt && strings.Contains(body, "mistgate:") {
				body += "// corrupted\n"
			}
			m.files[p] = body
			return nil
		},
		SourceDirs: func() []string { return []string{srcDir} },
	}
}

func TestFixCompatPatchesTheKnownFilesToThePinnedResult(t *testing.T) {
	for name, tc := range map[string]struct{ file, hash string }{
		"the PPA file":        {"ppa-1.0.0.h", hashPPA},
		"the hand-fixed file": {"handfixed.h", hashHand},
	} {
		t.Run(name, func(t *testing.T) {
			m := newMemFS(fixture(t, tc.file, tc.hash))
			rs := FixCompat(m.sys(), []string{srcDir}, false, nil)
			if len(rs) != 1 || rs[0].State != CompatPatched || !rs[0].Changed || rs[0].Err != "" || rs[0].SHA12 != tc.hash[:12] {
				t.Fatalf("result = %+v", rs)
			}
			got := m.files[compatPath(srcDir)]
			if sha256Hex([]byte(got)) != hashFixed {
				t.Fatalf("patched file hashes to %s, want %s", sha256Hex([]byte(got)), hashFixed)
			}
			if !strings.Contains(got, compatNewBlock) || strings.Contains(got, "sk->sk_socket") {
				t.Fatal("the old macros are still in the file, or the new block is not")
			}
			if !strings.Contains(got, "#if LINUX_VERSION_CODE < KERNEL_VERSION(7, 1, 5)\n#include <net/udp_tunnel.h>\n"+compatNewBlock+"#endif\n") {
				t.Fatal("the block is not inside the original version guard")
			}
			// a second run changes nothing
			writes := m.writes
			rs = FixCompat(m.sys(), []string{srcDir}, false, nil)
			if rs[0].State != CompatOK || rs[0].Changed || m.writes != writes || rs[0].SHA12 != hashFixed[:12] {
				t.Fatalf("second run = %+v, writes %d -> %d", rs, writes, m.writes)
			}
		})
	}
}

func TestFixCompatLeavesAFixedUpstreamFileAlone(t *testing.T) {
	m := newMemFS(fixture(t, "upstream-v3.1.20261113.h", hashUpstream))
	rs := FixCompat(m.sys(), []string{srcDir}, false, nil)
	if rs[0].State != CompatOK || rs[0].Changed || m.writes != 0 {
		t.Fatalf("result = %+v, writes %d", rs, m.writes)
	}
}

func TestFixCompatReportsAnUnknownFileAndLeavesIt(t *testing.T) {
	body := fixture(t, "ppa-1.0.0.h", hashPPA) + "/* a local change */\n"
	m := newMemFS(body)
	var out strings.Builder
	rs := FixCompat(m.sys(), []string{srcDir}, false, &out)
	want := sha256Hex([]byte(body))[:12]
	if rs[0].State != CompatUnknown || rs[0].SHA12 != want || rs[0].Changed || m.writes != 0 || m.files[compatPath(srcDir)] != body {
		t.Fatalf("result = %+v writes %d", rs, m.writes)
	}
	if CompatUnknownSHA(rs) != want || !strings.Contains(out.String(), want) || !strings.Contains(UnknownSourceNote(want), "nothing was changed") {
		t.Fatalf("sha %q, output %q", CompatUnknownSHA(rs), out.String())
	}
}

func TestFixCompatCheckWritesNothing(t *testing.T) {
	body := fixture(t, "ppa-1.0.0.h", hashPPA)
	m := newMemFS(body)
	rs := FixCompat(m.sys(), []string{srcDir}, true, nil)
	if rs[0].State != CompatNeedsPatch || rs[0].Changed || m.writes != 0 || m.files[compatPath(srcDir)] != body {
		t.Fatalf("result = %+v writes %d", rs, m.writes)
	}
}

func TestFixCompatAWriteFailureLeavesTheOriginal(t *testing.T) {
	body := fixture(t, "ppa-1.0.0.h", hashPPA)
	m := newMemFS(body)
	m.failWrite = true
	rs := FixCompat(m.sys(), []string{srcDir}, false, nil)
	if rs[0].State != CompatNeedsPatch || rs[0].Changed || rs[0].Err == "" || m.files[compatPath(srcDir)] != body {
		t.Fatalf("result = %+v", rs)
	}
	if CompatErr(rs) == nil {
		t.Fatal("CompatErr did not report the failure")
	}
}

// A file that does not read back as the pinned result is put back as it was.
func TestFixCompatRestoresAnOriginalThatWasWrittenWrong(t *testing.T) {
	body := fixture(t, "ppa-1.0.0.h", hashPPA)
	m := newMemFS(body)
	m.corrupt = true
	rs := FixCompat(m.sys(), []string{srcDir}, false, nil)
	if rs[0].State != CompatNeedsPatch || rs[0].Changed || !strings.Contains(rs[0].Err, "put back") || m.files[compatPath(srcDir)] != body {
		t.Fatalf("result = %+v, file restored = %v", rs, m.files[compatPath(srcDir)] == body)
	}
}

func TestFixCompatAbsentAndEveryDir(t *testing.T) {
	m := newMemFS(fixture(t, "ppa-1.0.0.h", hashPPA))
	m.files[compatPath("/usr/src/amneziawg-1.0.1")] = fixture(t, "upstream-v3.1.20261113.h", hashUpstream)
	rs := FixCompat(m.sys(), []string{srcDir, "/usr/src/amneziawg-1.0.1", "/usr/src/amneziawg-0.9"}, false, nil)
	if len(rs) != 3 || rs[0].State != CompatPatched || rs[1].State != CompatOK || rs[2].State != CompatAbsent || rs[2].SHA12 != "" {
		t.Fatalf("results = %+v", rs)
	}
	if rs := FixCompat(m.sys(), nil, false, nil); len(rs) != 0 {
		t.Fatalf("no dirs: %+v", rs)
	}
}

// The old macro lines of the table are lines of the fixtures, exactly once each.
func TestCompatTableOldBlocksAreInTheFixtures(t *testing.T) {
	for _, tc := range []struct{ file, hash string }{{"ppa-1.0.0.h", hashPPA}, {"handfixed.h", hashHand}} {
		body := fixture(t, tc.file, tc.hash)
		found := 0
		for _, e := range compatTable {
			if e.before == tc.hash {
				found++
				if strings.Count(body, e.old) != 1 || e.after != hashFixed {
					t.Errorf("%s: entry %q does not fit", tc.file, e.name)
				}
			}
		}
		if found != 1 {
			t.Errorf("%s has %d table entries", tc.file, found)
		}
	}
}
