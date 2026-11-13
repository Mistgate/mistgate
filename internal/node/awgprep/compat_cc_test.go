package awgprep

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The replacement block is C that only a kernel tree compiles for real. This test compiles it against three fake sets of
// prototypes (testdata/compat/macro_test.c) with the host's C compiler and checks which pointer each call receives. It is
// skipped where there is no C compiler.
func TestCompatBlockPicksTheArgumentByThePrototype(t *testing.T) {
	cc := ""
	for _, name := range []string{"cc", "gcc", "clang"} {
		if p, err := exec.LookPath(name); err == nil {
			cc = p
			break
		}
	}
	if cc == "" {
		t.Skip("no C compiler (cc, gcc or clang) on this machine")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "block.h"), []byte(compatNewBlock), 0o644); err != nil {
		t.Fatal(err)
	}
	src, err := filepath.Abs("testdata/compat/macro_test.c")
	if err != nil {
		t.Fatal(err)
	}
	for _, set := range []string{"SET_OLD", "SET_BACKPORT", "SET_NEW"} {
		t.Run(set, func(t *testing.T) {
			exe := filepath.Join(dir, strings.ToLower(set)+exeSuffix())
			out, err := exec.Command(cc, "-std=gnu11", "-Wall", "-Wextra", "-Werror", "-O0", "-D"+set, "-I", dir, "-o", exe, src).CombinedOutput()
			if err != nil {
				t.Fatalf("%s did not compile: %v\n%s", set, err, out)
			}
			if out, err := exec.Command(exe).CombinedOutput(); err != nil {
				t.Fatalf("%s: a call received the wrong pointer: %v\n%s", set, err, out)
			}
		})
	}
}

func exeSuffix() string {
	if filepath.Separator == '\\' {
		return ".exe"
	}
	return ""
}
