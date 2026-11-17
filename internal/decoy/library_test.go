package decoy

import (
	"io/fs"
	"testing"
)

func TestBundleAsFileSystem(t *testing.T) {
	b, err := FromFiles([]File{{Path: "index.html", Content: []byte("home")}, {Path: "a/b/page.html", Content: []byte("deep")}})
	if err != nil {
		t.Fatal(err)
	}
	fsys := b.FS()
	if got, err := fs.ReadFile(fsys, "a/b/page.html"); err != nil || string(got) != "deep" {
		t.Fatalf("file = %q, %v", got, err)
	}
	for _, dir := range []string{"a", "a/b", "."} {
		if st, err := fs.Stat(fsys, dir); err != nil || !st.IsDir() {
			t.Errorf("%q = %v, %v; want a directory", dir, st, err)
		}
	}
	for _, name := range []string{"missing", "a/c", "index.htm", "a/b/page.html/x"} {
		if _, err := fs.Stat(fsys, name); err == nil {
			t.Errorf("%q exists", name)
		}
	}
	if _, err := fsys.Open("../x"); err == nil {
		t.Error("opened a path outside the site")
	}
}

func TestBundleWithAndWithout(t *testing.T) {
	b, _ := FromFiles([]File{{Path: "index.html", Content: []byte("home")}, {Path: "a.css", Content: []byte("p{}")}})
	next, err := b.With("a.css", []byte("q{}"))
	if err != nil || next.Hash() == b.Hash() {
		t.Fatalf("replace = %v, %v", next, err)
	}
	if got, _ := next.File("a.css"); string(got) != "q{}" || len(next.Infos()) != 2 {
		t.Fatalf("after replace: %q, %d files", got, len(next.Infos()))
	}
	if next, err = b.With("new/page.html", []byte("n")); err != nil || len(next.Infos()) != 3 {
		t.Fatalf("add = %v, %v", next, err)
	}
	if _, err := b.With("../x", []byte("n")); err == nil || ReasonOf(err) != ReasonBadPath {
		t.Fatalf("unsafe path = %v", err)
	}
	if next, found, err := b.Without("a.css"); err != nil || !found || len(next.Infos()) != 1 {
		t.Fatalf("delete = %v %v %v", next, found, err)
	}
	if _, found, _ := b.Without("nothing"); found {
		t.Fatal("deleted a file that is not there")
	}
	if _, _, err := b.Without("index.html"); err == nil || ReasonOf(err) != ReasonNoIndex {
		t.Fatalf("deleting the index = %v", err)
	}
	if b.Size() != 7 {
		t.Fatalf("size = %d", b.Size())
	}
}
