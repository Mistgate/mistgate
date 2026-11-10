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

type heldServe struct {
	started <-chan struct{}
	done    <-chan error
	release func()
}

func startHeldServe(wg *sync.WaitGroup, s *Service, ctx context.Context, node, name string) heldServe {
	hold, started := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(hold) }) }
	done := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		first := true
		done <- s.Serve(ctx, node, name, 0, func([]byte, uint64) error {
			if first {
				first = false
				close(started)
				<-hold
			}
			return nil
		})
	}()
	return heldServe{started: started, done: done, release: release}
}

func waitHeldStarted(t *testing.T, held heldServe) {
	t.Helper()
	select {
	case <-held.started:
	case <-time.After(2 * time.Second):
		t.Fatal("download did not reach its blocked send")
	}
}

func waitHeldDone(t *testing.T, held heldServe) error {
	t.Helper()
	select {
	case err := <-held.done:
		return err
	case <-time.After(2 * time.Second):
		t.Fatal("blocked download did not return after release")
		return nil
	}
}

func assertServeSlotsZero(t *testing.T, s *Service) {
	t.Helper()
	s.bmu.Lock()
	defer s.bmu.Unlock()
	if s.serves != 0 || len(s.servingNodes) != 0 || s.zombieTotal != 0 || len(s.zombies) != 0 {
		t.Fatalf("download slots remain: live=%d nodes=%v zombies=%v total=%d", s.serves, s.servingNodes, s.zombies, s.zombieTotal)
	}
}

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
	if err := e.s.Serve(e.ctx, "nod_uk1", name, 0, sink.send); connectCode(err) != connect.CodeResourceExhausted {
		t.Errorf("fifth download: %v, want RESOURCE_EXHAUSTED", err)
	}
	close(hold)
	wg.Wait()
	if err := e.s.Serve(e.ctx, "nod_b", name, 0, sink.send); err != nil {
		t.Errorf("after the others finished: %v", err)
	}
	assertServeSlotsZero(t, e.s)
}

func TestServeAllowsOnlyOneDownloadPerNode(t *testing.T) {
	e := newEnv(t)
	want := randBytes(600 << 10)
	name := "mistgate-node-linux-amd64"
	e.bundle("1.0", 10, map[string][]byte{name: want})
	e.s.nodeSlotWait = 5 * time.Second
	var wg sync.WaitGroup
	first := startHeldServe(&wg, e.s, e.ctx, "nod_de1", name)
	defer func() {
		first.release()
		wg.Wait()
	}()
	waitHeldStarted(t, first)

	type result struct {
		err  error
		sink chunkSink
	}
	resumed := make(chan result, 1)
	startedAt := time.Now()
	go func() {
		var sink chunkSink
		err := e.s.Serve(e.ctx, "nod_de1", name, 0, sink.send)
		resumed <- result{err: err, sink: sink}
	}()
	var got result
	select {
	case got = <-resumed:
	case <-time.After(time.Second):
		t.Fatalf("same-file resume did not complete while the first send was blocked")
	}
	if took := time.Since(startedAt); took > time.Second {
		t.Fatalf("same-file resume took %s, want an immediate hand-over", took)
	}
	if got.err != nil || !bytes.Equal(got.sink.bytes(), want) {
		t.Fatalf("resume error=%v, bytes=%d, want %d intact bytes", got.err, len(got.sink.bytes()), len(want))
	}
	select {
	case err := <-first.done:
		t.Fatalf("first send returned before it was released: %v", err)
	default:
	}
	first.release()
	if err := waitHeldDone(t, first); !errors.Is(err, context.Canceled) {
		t.Fatalf("superseded download returned %v, want context.Canceled", err)
	}
	assertServeSlotsZero(t, e.s)
}

func TestServeWaitsForAnotherFileOnTheSameNode(t *testing.T) {
	e := newEnv(t)
	binary, table := "mistgate-node-linux-amd64", "mistgate-asndb-data-any"
	e.bundle("1.0", 10, map[string][]byte{binary: randBytes(300 << 10), table: randBytes(300 << 10)})
	e.s.nodeSlotWait = 30 * time.Millisecond
	var wg sync.WaitGroup
	first := startHeldServe(&wg, e.s, e.ctx, "nod_de1", binary)
	defer func() {
		first.release()
		wg.Wait()
	}()
	waitHeldStarted(t, first)

	startedAt := time.Now()
	var sink chunkSink
	err := e.s.Serve(e.ctx, "nod_de1", table, 0, sink.send)
	if connectCode(err) != connect.CodeResourceExhausted {
		t.Fatalf("different file for a busy node: %v, want RESOURCE_EXHAUSTED", err)
	}
	if took := time.Since(startedAt); took < 20*time.Millisecond {
		t.Fatalf("different-file request was refused after %s, want it to wait", took)
	}
	first.release()
	if err := waitHeldDone(t, first); err != nil {
		t.Fatalf("original download returned %v", err)
	}
	assertServeSlotsZero(t, e.s)
}

func TestServeNodeZombieCapWaitsUntilZombieReturns(t *testing.T) {
	e := newEnv(t)
	want := randBytes(300 << 10)
	name := "mistgate-node-linux-amd64"
	e.bundle("1.0", 10, map[string][]byte{name: want})
	e.s.nodeSlotWait = 30 * time.Millisecond
	var wg sync.WaitGroup
	first := startHeldServe(&wg, e.s, e.ctx, "nod_de1", name)
	defer func() {
		first.release()
		wg.Wait()
	}()
	waitHeldStarted(t, first)
	second := startHeldServe(&wg, e.s, e.ctx, "nod_de1", name)
	defer second.release()
	waitHeldStarted(t, second)

	thirdSent := make(chan struct{}, 1)
	startedAt := time.Now()
	err := e.s.Serve(e.ctx, "nod_de1", name, 0, func([]byte, uint64) error {
		thirdSent <- struct{}{}
		return nil
	})
	if connectCode(err) != connect.CodeResourceExhausted {
		t.Fatalf("third same-file request while a zombie exists: %v, want RESOURCE_EXHAUSTED", err)
	}
	if took := time.Since(startedAt); took < 20*time.Millisecond {
		t.Fatalf("third request was refused after %s, want it to wait", took)
	}
	select {
	case <-thirdSent:
		t.Fatal("third request sent data while the per-node zombie cap was full")
	default:
	}

	first.release()
	if err := waitHeldDone(t, first); !errors.Is(err, context.Canceled) {
		t.Fatalf("first superseded download returned %v, want context.Canceled", err)
	}
	var resumed chunkSink
	if err := e.s.Serve(e.ctx, "nod_de1", name, 0, resumed.send); err != nil {
		t.Fatalf("request after the zombie returned: %v", err)
	}
	if !bytes.Equal(resumed.bytes(), want) {
		t.Fatalf("request after zombie return got %d bytes, want %d", len(resumed.bytes()), len(want))
	}
	select {
	case err := <-second.done:
		t.Fatalf("second blocked send returned before release: %v", err)
	default:
	}
	second.release()
	if err := waitHeldDone(t, second); !errors.Is(err, context.Canceled) {
		t.Fatalf("second superseded download returned %v, want context.Canceled", err)
	}
	assertServeSlotsZero(t, e.s)
}

func TestServeGlobalZombieCapWithFiveNodes(t *testing.T) {
	e := newEnv(t)
	name := "mistgate-node-linux-amd64"
	e.bundle("1.0", 10, map[string][]byte{name: randBytes(300 << 10)})
	e.s.nodeSlotWait = 30 * time.Millisecond
	var wg sync.WaitGroup
	var held []heldServe
	defer func() {
		for _, download := range held {
			download.release()
		}
		wg.Wait()
	}()
	nodes := []string{"nod_de1", "nod_nl1", "nod_fi1", "nod_pl1"}
	first := make([]heldServe, len(nodes))
	second := make([]heldServe, len(nodes))
	for i, node := range nodes {
		first[i] = startHeldServe(&wg, e.s, e.ctx, node, name)
		held = append(held, first[i])
		waitHeldStarted(t, first[i])
	}
	for i, node := range nodes {
		second[i] = startHeldServe(&wg, e.s, e.ctx, node, name)
		held = append(held, second[i])
		waitHeldStarted(t, second[i])
	}

	second[0].release()
	if err := waitHeldDone(t, second[0]); err != nil {
		t.Fatalf("first live replacement returned %v", err)
	}
	fifth := startHeldServe(&wg, e.s, e.ctx, "nod_uk1", name)
	held = append(held, fifth)
	waitHeldStarted(t, fifth)

	fifthSent := make(chan struct{}, 1)
	startedAt := time.Now()
	err := e.s.Serve(e.ctx, "nod_uk1", name, 0, func([]byte, uint64) error {
		fifthSent <- struct{}{}
		return nil
	})
	if connectCode(err) != connect.CodeResourceExhausted {
		t.Fatalf("fifth zombie: %v, want RESOURCE_EXHAUSTED", err)
	}
	if took := time.Since(startedAt); took < 20*time.Millisecond {
		t.Fatalf("fifth node was refused after %s, want it to wait", took)
	}
	select {
	case <-fifthSent:
		t.Fatal("fifth node sent data while the global zombie cap was full")
	default:
	}

	first[0].release()
	if err := waitHeldDone(t, first[0]); !errors.Is(err, context.Canceled) {
		t.Fatalf("released zombie returned %v, want context.Canceled", err)
	}
	var resumed chunkSink
	if err := e.s.Serve(e.ctx, "nod_uk1", name, 0, resumed.send); err != nil {
		t.Fatalf("fifth node after a zombie returned: %v", err)
	}
	if len(resumed.bytes()) == 0 {
		t.Fatal("fifth node resumed without receiving data")
	}

	for i := 1; i < len(nodes); i++ {
		first[i].release()
		second[i].release()
		if err := waitHeldDone(t, first[i]); !errors.Is(err, context.Canceled) {
			t.Errorf("node %s superseded download returned %v, want context.Canceled", nodes[i], err)
		}
		if err := waitHeldDone(t, second[i]); err != nil {
			t.Errorf("node %s live download returned %v", nodes[i], err)
		}
	}
	fifth.release()
	if err := waitHeldDone(t, fifth); !errors.Is(err, context.Canceled) {
		t.Errorf("fifth node superseded download returned %v, want context.Canceled", err)
	}
	assertServeSlotsZero(t, e.s)
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
