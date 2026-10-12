//go:build linux

package doctor

import (
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

func TestDirSizeCountsAllocatedBlocksForSparseJournal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "system.journal")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(16 << 20); err != nil {
		f.Close()
		t.Fatal(err)
	}
	data := make([]byte, 9<<20)
	if _, err := rand.New(rand.NewSource(1)).Read(data); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if n, err := f.WriteAt(data, 0); err != nil || n != len(data) {
		f.Close()
		t.Fatalf("write sparse file: wrote %d bytes, err %v", n, err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	e := Env{}
	e.defaults()
	got := dirSize(&e, dir)
	if got < 8<<20 || got > 10<<20 {
		t.Fatalf("dirSize = %d bytes, want about 9 MiB allocated", got)
	}
}
