//go:build js && wasm

// Package web serves the built admin SPA (web/dist). The edge build does not embed it: the Worker's static assets hold
// it, and the shell passes a read callback to SetAssets.
package web

import (
	"bytes"
	"context"
	"io/fs"
	"sync"
	"syscall/js"
	"time"

	"github.com/mistgate/mistgate/edge/d1driver"
)

var (
	assetsMu  sync.Mutex
	assetsFn  js.Value
	assetsHit = map[string][]byte{} // per isolate; only files that exist (a miss is one cheap binding call)
)

// SetAssets registers the JavaScript callback async (path: string) => Uint8Array | null that reads one file of the SPA
// build by its path relative to dist/ (no leading slash). It must be called before the panel handler is built.
func SetAssets(read js.Value) {
	assetsMu.Lock()
	defer assetsMu.Unlock()
	assetsFn = read
	clear(assetsHit)
}

// Dist returns the SPA build output rooted at dist/, read through the callback of SetAssets. Without a callback every
// file is missing, which callers already cope with (an unbuilt SPA).
func Dist() fs.FS { return assetFS{} }

type assetFS struct{}

func (assetFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	if name == "." { // directories are not served
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
	assetsMu.Lock()
	data, ok := assetsHit[name]
	read := assetsFn
	assetsMu.Unlock()
	if !ok {
		if read.Type() != js.TypeFunction {
			return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
		}
		var err error
		if data, err = readAsset(read, name); err != nil {
			return nil, &fs.PathError{Op: "open", Path: name, Err: err}
		}
		if data == nil {
			return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
		}
		assetsMu.Lock()
		assetsHit[name] = data
		assetsMu.Unlock()
	}
	return &assetFile{Reader: bytes.NewReader(data), name: name}, nil
}

// readAsset returns nil, nil when the callback answers null.
func readAsset(read js.Value, name string) (data []byte, err error) {
	defer func() {
		if recover() != nil {
			data, err = nil, fs.ErrInvalid
		}
	}()
	v, err := d1driver.Await(context.Background(), read.Invoke(name))
	if err != nil {
		return nil, err
	}
	if v.Type() == js.TypeUndefined || v.IsNull() {
		return nil, nil
	}
	if v.Type() != js.TypeObject || !v.InstanceOf(js.Global().Get("Uint8Array")) {
		return nil, fs.ErrInvalid
	}
	data = make([]byte, v.Length())
	js.CopyBytesToGo(data, v)
	return data, nil
}

type assetFile struct {
	*bytes.Reader
	name string
}

func (f *assetFile) Stat() (fs.FileInfo, error) { return assetInfo{name: f.name, size: f.Size()}, nil }
func (f *assetFile) Close() error               { return nil }

type assetInfo struct {
	name string
	size int64
}

func (i assetInfo) Name() string       { return i.name }
func (i assetInfo) Size() int64        { return i.size }
func (i assetInfo) Mode() fs.FileMode  { return 0o444 }
func (i assetInfo) ModTime() time.Time { return time.Time{} }
func (i assetInfo) IsDir() bool        { return false }
func (i assetInfo) Sys() any           { return nil }
