package update

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/release"
)

// scan, case by case: the first check that fails decides the status.
func TestBundleScan(t *testing.T) {
	files := map[string][]byte{"mistgate-node-linux-amd64": []byte("binary one"), "mistgate-node-linux-arm64": []byte("binary two")}
	dist := func(e *env) string { return filepath.Join(e.dir, "dist") }
	now := func(e *env) int64 { return e.clk.Now().Unix() }
	other, otherPriv, _ := release.GenerateKey()
	_ = other

	for _, tc := range []struct {
		name   string
		setup  func(e *env)
		noKey  bool
		status adminv1.BundleStatus
		errKey string
		params map[string]string
		shown  bool // the manifest is shown although it is not trusted
	}{
		{name: "no directory", setup: func(e *env) {}, status: adminv1.BundleStatus_BUNDLE_STATUS_MISSING, errKey: "no_manifest"},
		{name: "empty directory", setup: func(e *env) { os.MkdirAll(dist(e), 0o755) }, status: adminv1.BundleStatus_BUNDLE_STATUS_MISSING, errKey: "no_manifest"},
		{name: "no signature", setup: func(e *env) {
			writeBundle(t, dist(e), e.priv, "1.0", 10, now(e)+1000, files)
			os.Remove(filepath.Join(dist(e), release.SignatureName))
		}, status: adminv1.BundleStatus_BUNDLE_STATUS_UNTRUSTED, errKey: "no_signature", shown: true},
		{name: "garbage manifest", setup: func(e *env) {
			os.MkdirAll(dist(e), 0o755)
			os.WriteFile(filepath.Join(dist(e), release.ManifestName), []byte("not json"), 0o644)
			os.WriteFile(filepath.Join(dist(e), release.SignatureName), make([]byte, 64), 0o644)
		}, status: adminv1.BundleStatus_BUNDLE_STATUS_UNTRUSTED, errKey: "bad_manifest"},
		{name: "schema 2", setup: func(e *env) {
			os.MkdirAll(dist(e), 0o755)
			body := []byte(`{"schema":2,"version":"9"}`)
			os.WriteFile(filepath.Join(dist(e), release.ManifestName), body, 0o644)
			os.WriteFile(filepath.Join(dist(e), release.SignatureName), release.Sign(e.priv, body), 0o644)
		}, status: adminv1.BundleStatus_BUNDLE_STATUS_UNTRUSTED, errKey: "unsupported_schema"},
		{name: "tampered manifest", setup: func(e *env) {
			writeBundle(t, dist(e), e.priv, "1.0", 10, now(e)+1000, files)
			p := filepath.Join(dist(e), release.ManifestName)
			b, _ := os.ReadFile(p)
			os.WriteFile(p, bytes.Replace(b, []byte(`"version": "1.0"`), []byte(`"version": "1.1"`), 1), 0o644)
		}, status: adminv1.BundleStatus_BUNDLE_STATUS_UNTRUSTED, errKey: "bad_signature", shown: true},
		{name: "signed by another key", setup: func(e *env) {
			writeBundle(t, dist(e), otherPriv, "1.0", 10, now(e)+1000, files)
		}, status: adminv1.BundleStatus_BUNDLE_STATUS_UNTRUSTED, errKey: "bad_signature", shown: true},
		{name: "no release key in this build", noKey: true, setup: func(e *env) {
			writeBundle(t, dist(e), e.priv, "1.0", 10, now(e)+1000, files)
		}, status: adminv1.BundleStatus_BUNDLE_STATUS_NO_KEY, shown: true},
		{name: "expired", setup: func(e *env) {
			writeBundle(t, dist(e), e.priv, "1.0", 10, now(e)-1, files)
		}, status: adminv1.BundleStatus_BUNDLE_STATUS_UNTRUSTED, errKey: "expired", shown: true},
		{name: "listed file missing", setup: func(e *env) {
			writeBundle(t, dist(e), e.priv, "1.0", 10, now(e)+1000, files)
			os.Remove(filepath.Join(dist(e), "mistgate-node-linux-arm64"))
		}, status: adminv1.BundleStatus_BUNDLE_STATUS_UNTRUSTED, errKey: "file_missing", params: map[string]string{"file": "mistgate-node-linux-arm64"}, shown: true},
		{name: "file one byte off", setup: func(e *env) {
			writeBundle(t, dist(e), e.priv, "1.0", 10, now(e)+1000, files)
			os.WriteFile(filepath.Join(dist(e), "mistgate-node-linux-amd64"), []byte("binary onf"), 0o755) // same size
		}, status: adminv1.BundleStatus_BUNDLE_STATUS_UNTRUSTED, errKey: "file_mismatch", params: map[string]string{"file": "mistgate-node-linux-amd64"}, shown: true},
		{name: "file of another size", setup: func(e *env) {
			writeBundle(t, dist(e), e.priv, "1.0", 10, now(e)+1000, files)
			os.WriteFile(filepath.Join(dist(e), "mistgate-node-linux-amd64"), []byte("binary one, longer"), 0o755)
		}, status: adminv1.BundleStatus_BUNDLE_STATUS_UNTRUSTED, errKey: "file_mismatch", params: map[string]string{"file": "mistgate-node-linux-amd64"}, shown: true},
		{name: "trusted", setup: func(e *env) { writeBundle(t, dist(e), e.priv, "1.0", 10, now(e)+1000, files) },
			status: adminv1.BundleStatus_BUNDLE_STATUS_TRUSTED, shown: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			if tc.noKey {
				e.s = e.newService(nil)
			}
			tc.setup(e)
			b := e.s.rescan()
			v := b.view
			wantKey := ""
			if tc.errKey != "" {
				wantKey = "updates.bundle.err." + tc.errKey
			}
			if v.Status != tc.status || v.ErrorKey != wantKey {
				t.Fatalf("status %v key %q, want %v %q", v.Status, v.ErrorKey, tc.status, wantKey)
			}
			for k, want := range tc.params {
				if v.Params[k] != want {
					t.Errorf("param %s = %q, want %q", k, v.Params[k], want)
				}
			}
			if tc.shown != (v.Version != "" && len(v.Files) == 2) {
				t.Errorf("manifest shown = %v, want %v (%+v)", v.Version != "", tc.shown, v)
			}
			if b.trusted != (tc.status == adminv1.BundleStatus_BUNDLE_STATUS_TRUSTED) {
				t.Errorf("trusted = %v", b.trusted)
			}
			if v.ScannedUnix != e.clk.Now().Unix() {
				t.Errorf("scanned_unix %d", v.ScannedUnix)
			}
		})
	}
}

func TestBundleSymlinkIsNotFollowed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	e := newEnv(t)
	e.defaultBundle()
	if e.s.current().view.Status != adminv1.BundleStatus_BUNDLE_STATUS_TRUSTED {
		t.Fatal("setup")
	}
	dist := filepath.Join(e.dir, "dist")
	target := filepath.Join(e.dir, "elsewhere")
	os.Rename(filepath.Join(dist, "mistgate-node-linux-amd64"), target)
	os.Symlink(target, filepath.Join(dist, "mistgate-node-linux-amd64"))
	if v := e.s.rescan().view; v.ErrorKey != "updates.bundle.err.file_missing" {
		t.Fatalf("a symlink in dist is not a bundle file: %+v", v)
	}
}

// The poll notices a changed directory by itself and leaves an unchanged one alone (the hash is cached).
func TestBundleRescanIfChanged(t *testing.T) {
	e := newEnv(t)
	if e.s.current().view.Status != adminv1.BundleStatus_BUNDLE_STATUS_MISSING {
		t.Fatal("setup")
	}
	first := e.s.current()
	e.s.rescanIfChanged()
	if e.s.current() != first {
		t.Fatal("an unchanged directory must not be rescanned")
	}
	writeBundle(t, filepath.Join(e.dir, "dist"), e.priv, "1.0", 10, e.clk.Now().Unix()+1000, map[string][]byte{"mistgate-node-linux-amd64": []byte("x")})
	e.s.rescanIfChanged()
	if e.s.current().view.Status != adminv1.BundleStatus_BUNDLE_STATUS_TRUSTED {
		t.Fatalf("a new bundle is noticed: %+v", e.s.current().view)
	}
	// files read right after they were written are read again: a same-size rewrite within one timestamp tick would
	// otherwise go unnoticed
	again := e.s.current()
	e.s.rescanIfChanged()
	if e.s.current() == again {
		t.Fatal("a directory scanned right after it changed must be scanned again")
	}
	// once the files are older than the racy window, an unchanged directory is left alone
	dist := filepath.Join(e.dir, "dist")
	old := time.Now().Add(-time.Hour)
	ents, err := os.ReadDir(dist)
	if err != nil {
		t.Fatal(err)
	}
	for _, ent := range ents {
		if err := os.Chtimes(filepath.Join(dist, ent.Name()), old, old); err != nil {
			t.Fatal(err)
		}
	}
	e.s.rescanIfChanged()
	settledScan := e.s.current()
	if settledScan.view.Status != adminv1.BundleStatus_BUNDLE_STATUS_TRUSTED {
		t.Fatalf("the settled bundle is trusted: %+v", settledScan.view)
	}
	e.s.rescanIfChanged()
	if e.s.current() != settledScan {
		t.Fatal("an unchanged directory must not be rescanned")
	}
	// an unchanged directory whose trusted manifest has expired is read again: it is no longer trusted
	e.clk.Advance(1001 * time.Second)
	e.s.rescanIfChanged()
	if v := e.s.current().view; v.Status != adminv1.BundleStatus_BUNDLE_STATUS_UNTRUSTED || v.ErrorKey != "updates.bundle.err.expired" {
		t.Fatalf("an expired bundle stays trusted: %+v", v)
	}
}

type chunkSink struct {
	chunks [][]byte
	totals []uint64
}

func (c *chunkSink) send(chunk []byte, total uint64) error {
	c.chunks = append(c.chunks, append([]byte(nil), chunk...))
	c.totals = append(c.totals, total)
	return nil
}

func (c *chunkSink) bytes() []byte { return bytes.Join(c.chunks, nil) }

func connectCode(err error) connect.Code {
	if err == nil {
		return 0
	}
	return connect.CodeOf(err)
}

func TestServe(t *testing.T) {
	e := newEnv(t)
	blob := randBytes(600 << 10)
	e.bundle("1.0", 10, map[string][]byte{"mistgate-node-linux-amd64": blob, "mistgate-node-linux-arm64": []byte("small")})
	name := "mistgate-node-linux-amd64"

	var all chunkSink
	if err := e.s.Serve(e.ctx, "nod_a", name, 0, all.send); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(all.bytes(), blob) {
		t.Fatal("the stream is not the file")
	}
	for i, c := range all.chunks {
		if len(c) > 256<<10 || len(c) == 0 {
			t.Errorf("chunk %d has %d bytes", i, len(c))
		}
		if all.totals[i] != uint64(len(blob)) {
			t.Errorf("total_size on message %d is %d", i, all.totals[i])
		}
	}
	if len(all.chunks) != 3 {
		t.Errorf("600 KiB in %d chunks, want 3", len(all.chunks))
	}

	// resume from an offset in the middle of a chunk
	var rest chunkSink
	off := 300_000
	if err := e.s.Serve(e.ctx, "nod_a", name, uint64(off), rest.send); err != nil || !bytes.Equal(rest.bytes(), blob[off:]) {
		t.Fatalf("resume: %v, %d bytes", err, len(rest.bytes()))
	}
	// offset at the end: one empty message that carries the total
	var end chunkSink
	if err := e.s.Serve(e.ctx, "nod_a", name, uint64(len(blob)), end.send); err != nil || len(end.chunks) != 1 || len(end.chunks[0]) != 0 || end.totals[0] != uint64(len(blob)) {
		t.Fatalf("offset at the end: %v %+v", err, end)
	}
	// past the end, unknown names, path tricks
	var none chunkSink
	for _, tc := range []struct {
		name string
		off  uint64
		code connect.Code
	}{
		{name, uint64(len(blob)) + 1, connect.CodeNotFound},
		{"mistgate-node-linux-riscv", 0, connect.CodeNotFound},
		{"../manifest.json", 0, connect.CodeNotFound},
		{"manifest.json", 0, connect.CodeNotFound}, // in the directory, but not a listed file
		{"", 0, connect.CodeNotFound},
	} {
		if err := e.s.Serve(e.ctx, "nod_a", tc.name, tc.off, none.send); connectCode(err) != tc.code {
			t.Errorf("Serve(%q, %d): %v, want code %v", tc.name, tc.off, err, tc.code)
		}
	}
	if len(none.chunks) != 0 {
		t.Error("bytes were sent for a refused request")
	}

	// a file that is not the one the scan verified is refused and triggers a rescan
	time.Sleep(10 * time.Millisecond)
	os.WriteFile(filepath.Join(e.dir, "dist", "mistgate-node-linux-arm64"), []byte("SMALL"), 0o755)
	os.Chtimes(filepath.Join(e.dir, "dist", "mistgate-node-linux-arm64"), time.Now().Add(time.Hour), time.Now().Add(time.Hour))
	if err := e.s.Serve(e.ctx, "nod_a", "mistgate-node-linux-arm64", 0, none.send); connectCode(err) != connect.CodeFailedPrecondition {
		t.Errorf("a replaced file: %v", err)
	}
	// nothing trusted, nothing served
	e.s.rescan()
	if err := e.s.Serve(e.ctx, "nod_a", name, 0, none.send); connectCode(err) != connect.CodeFailedPrecondition {
		t.Errorf("an untrusted bundle: %v", err)
	}
}

func TestServeConcurrencyCap(t *testing.T) {
	e := newEnv(t)
	e.bundle("1.0", 10, map[string][]byte{"mistgate-node-linux-amd64": randBytes(300 << 10)})
	name := "mistgate-node-linux-amd64"
	hold, started := make(chan struct{}), make(chan struct{}, maxServes)
	var wg sync.WaitGroup
	nodes := []string{"nod_a", "nod_b", "nod_c", "nod_d"}
	for _, nodeID := range nodes {
		wg.Add(1)
		go func(nodeID string) {
			defer wg.Done()
			first := true
			e.s.Serve(e.ctx, nodeID, name, 0, func([]byte, uint64) error {
				if first {
					first = false
					started <- struct{}{}
					<-hold
				}
				return nil
			})
		}(nodeID)
	}
	for range maxServes {
		<-started
	}
	var sink chunkSink
	if err := e.s.Serve(e.ctx, "nod_b", name, 0, sink.send); connectCode(err) != connect.CodeResourceExhausted {
		t.Errorf("fifth download: %v, want RESOURCE_EXHAUSTED", err)
	}
	close(hold)
	wg.Wait()
	if err := e.s.Serve(e.ctx, "nod_b", name, 0, sink.send); err != nil {
		t.Errorf("after the others finished: %v", err)
	}
}

func TestServeAllowsOnlyOneDownloadPerNode(t *testing.T) {
	e := newEnv(t)
	e.bundle("1.0", 10, map[string][]byte{"mistgate-node-linux-amd64": randBytes(300 << 10)})
	started, hold, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(finished)
		_ = e.s.Serve(e.ctx, "nod_de1", "mistgate-node-linux-amd64", 0, func([]byte, uint64) error {
			select {
			case <-started:
			default:
				close(started)
			}
			<-hold
			return nil
		})
	}()
	<-started
	// a second download that outlasts the short wait is refused
	e.s.nodeSlotWait = 20 * time.Millisecond
	var sink chunkSink
	if err := e.s.Serve(e.ctx, "nod_de1", "mistgate-node-linux-amd64", 0, sink.send); connectCode(err) != connect.CodeResourceExhausted {
		t.Fatalf("second download for one node: %v", err)
	}
	// a resume asks again before the dropped download has ended: it waits for the slot and then runs
	e.s.nodeSlotWait = 10 * time.Second
	resumed := make(chan error, 1)
	go func() {
		var resumeSink chunkSink
		resumed <- e.s.Serve(e.ctx, "nod_de1", "mistgate-node-linux-amd64", 0, resumeSink.send)
	}()
	select {
	case err := <-resumed:
		t.Fatalf("the resume did not wait for the first download: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(hold)
	<-finished
	if err := <-resumed; err != nil {
		t.Fatalf("resume after the first download ended: %v", err)
	}
	if err := e.s.Serve(e.ctx, "nod_de1", "mistgate-node-linux-amd64", 0, sink.send); err != nil {
		t.Fatalf("download after the first finished: %v", err)
	}
}

func TestServeArmsAndClearsWriteDeadlineWhenChunkSendStalls(t *testing.T) {
	e := newEnv(t)
	e.bundle("1.0", 10, map[string][]byte{"mistgate-node-linux-amd64": randBytes(300 << 10)})
	e.s.sendTimeout = 20 * time.Millisecond
	var deadlines []time.Time
	setDeadline := func(deadline time.Time) error {
		deadlines = append(deadlines, deadline)
		return nil
	}
	err := e.s.ServeWithWriteDeadline(e.ctx, "nod_de1", "mistgate-node-linux-amd64", 0, setDeadline, func([]byte, uint64) error {
		return os.ErrDeadlineExceeded
	})
	if connectCode(err) != connect.CodeUnavailable || !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("stalled send = %v", err)
	}
	if len(deadlines) != 2 || deadlines[0].IsZero() || !deadlines[1].IsZero() {
		t.Fatalf("write deadlines = %v, want one arm and one clear", deadlines)
	}
	var sink chunkSink
	if err := e.s.Serve(e.ctx, "nod_de1", "mistgate-node-linux-amd64", 0, sink.send); err != nil {
		t.Fatalf("download slot stayed occupied after a stalled send: %v", err)
	}
}

func TestServeContinuesWhenWriteDeadlineIsNotSupported(t *testing.T) {
	e := newEnv(t)
	want := randBytes(300 << 10)
	e.bundle("1.0", 10, map[string][]byte{"mistgate-node-linux-amd64": want})
	var sink chunkSink
	setDeadline := func(time.Time) error { return http.ErrNotSupported }
	if err := e.s.ServeWithWriteDeadline(e.ctx, "nod_de1", "mistgate-node-linux-amd64", 0, setDeadline, sink.send); err != nil {
		t.Fatalf("download with an unsupported write deadline: %v", err)
	}
	if got := sink.bytes(); !bytes.Equal(got, want) {
		t.Fatalf("downloaded %d bytes, want %d", len(got), len(want))
	}
}

// A file replaced while it is being downloaded does not corrupt that download: the descriptor was opened once.
func TestServeFileReplacedDuringDownload(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("cannot rename over an open file on Windows")
	}
	e := newEnv(t)
	old := randBytes(600 << 10)
	e.bundle("1.0", 10, map[string][]byte{"mistgate-node-linux-amd64": old})
	path := filepath.Join(e.dir, "dist", "mistgate-node-linux-amd64")
	var sink chunkSink
	err := e.s.Serve(e.ctx, "nod_a", "mistgate-node-linux-amd64", 0, func(c []byte, total uint64) error {
		if len(sink.chunks) == 0 {
			tmp := path + ".new"
			os.WriteFile(tmp, randBytes(600<<10), 0o755)
			if err := os.Rename(tmp, path); err != nil {
				t.Error(err)
			}
		}
		return sink.send(c, total)
	})
	if err != nil || !bytes.Equal(sink.bytes(), old) {
		t.Fatalf("download of a replaced file: %v, intact = %v", err, bytes.Equal(sink.bytes(), old))
	}
}

func TestServeStopsWhenTheClientGoesAway(t *testing.T) {
	e := newEnv(t)
	e.bundle("1.0", 10, map[string][]byte{"mistgate-node-linux-amd64": randBytes(600 << 10)})
	ctx, cancel := context.WithCancel(e.ctx)
	n := 0
	err := e.s.Serve(ctx, "nod_a", "mistgate-node-linux-amd64", 0, func([]byte, uint64) error {
		if n++; n == 1 {
			cancel()
		}
		return nil
	})
	if err == nil || n != 1 {
		t.Fatalf("a cancelled download: %v after %d chunks", err, n)
	}
	if strings.Contains(err.Error(), "panic") {
		t.Fatal(err)
	}
}
