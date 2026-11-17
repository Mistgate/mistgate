package decoy

import (
	"bytes"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"
)

// FileInfo is a file of a bundle without its content.
type FileInfo struct {
	Path string
	Size int
}

// Infos lists the files of the bundle, sorted by path.
func (b *Bundle) Infos() []FileInfo {
	if b == nil {
		return nil
	}
	out := make([]FileInfo, len(b.files))
	for i, f := range b.files {
		out[i] = FileInfo{Path: f.Path, Size: len(f.Content)}
	}
	return out
}

// Size is the sum of the file contents.
func (b *Bundle) Size() int {
	n := 0
	if b != nil {
		for _, f := range b.files {
			n += len(f.Content)
		}
	}
	return n
}

// File returns the content of one file. The slice belongs to the bundle: do not change it.
func (b *Bundle) File(name string) ([]byte, bool) {
	if b != nil {
		i := sort.Search(len(b.files), func(i int) bool { return b.files[i].Path >= name })
		if i < len(b.files) && b.files[i].Path == name {
			return b.files[i].Content, true
		}
	}
	return nil, false
}

// With returns the bundle with one file added or replaced (the other files and the rules are those of FromFiles).
func (b *Bundle) With(name string, content []byte) (*Bundle, error) {
	files := make([]File, 0, len(b.files)+1)
	for _, f := range b.files {
		if f.Path != name {
			files = append(files, f)
		}
	}
	return FromFiles(append(files, File{Path: name, Content: content}))
}

// Without returns the bundle without one file. ok is false when the bundle has no such file.
func (b *Bundle) Without(name string) (nb *Bundle, ok bool, err error) {
	files := make([]File, 0, len(b.files))
	for _, f := range b.files {
		if f.Path == name {
			ok = true
			continue
		}
		files = append(files, f)
	}
	if !ok {
		return nil, false, nil
	}
	nb, err = FromFiles(files)
	return nb, true, err
}

// FS is the bundle as a read-only file system: the panel serves its own public site from it with the rules of a directory.
// Directories exist only as the parents of files.
func (b *Bundle) FS() fs.FS { return memFS{b} }

type memFS struct{ b *Bundle }

func (m memFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	if content, ok := m.b.File(name); ok {
		return &memFile{Reader: bytes.NewReader(content), info: memInfo{name: path.Base(name), size: int64(len(content))}}, nil
	}
	prefix := name + "/"
	if name == "." {
		prefix = ""
	}
	for _, f := range m.b.files {
		if strings.HasPrefix(f.Path, prefix) {
			return &memFile{Reader: bytes.NewReader(nil), info: memInfo{name: path.Base(name), dir: true}}, nil
		}
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

type memFile struct {
	*bytes.Reader
	info memInfo
}

func (f *memFile) Stat() (fs.FileInfo, error) { return f.info, nil }
func (f *memFile) Close() error               { return nil }

type memInfo struct {
	name string
	size int64
	dir  bool
}

func (i memInfo) Name() string { return i.name }
func (i memInfo) Size() int64  { return i.size }
func (i memInfo) Mode() fs.FileMode {
	if i.dir {
		return fs.ModeDir | 0o555
	}
	return 0o444
}
func (i memInfo) ModTime() time.Time { return time.Time{} }
func (i memInfo) IsDir() bool        { return i.dir }
func (i memInfo) Sys() any           { return nil }
