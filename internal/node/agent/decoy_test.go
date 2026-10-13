package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/decoy"
	"github.com/mistgate/mistgate/internal/node/engine"
)

func TestDecoyModeCommandPersistsAndIsAdvertisedAfterRestart(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.waitConnected()
	bundle, err := decoy.FromFiles([]decoy.File{{Path: "index.html", Content: []byte("node site")}})
	if err != nil {
		t.Fatal(err)
	}
	files := make([]*pb.DecoyFile, 0, len(bundle.Files()))
	for _, f := range bundle.Files() {
		files = append(files, &pb.DecoyFile{Path: f.Path, Contents: f.Content})
	}
	h.panel.send(&pb.ConnectResponse{Message: &pb.ConnectResponse_SetDecoy{SetDecoy: &pb.SetDecoy{
		RequestId: "decoy_1", Mode: decoy.ModeOwn, BundleSha256: bundle.Hash(), Files: files,
	}}})
	result := h.panel.nextCmd()
	if !result.Ok || result.RequestId != "decoy_1" || result.Params["mode"] != decoy.ModeOwn || result.Params["sha256"] != bundle.Hash() {
		t.Fatalf("set decoy result = %+v", result)
	}
	w := httptest.NewRecorder()
	h.a.masquerade("inb_1").ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusOK || w.Body.String() != "node site" {
		t.Fatalf("active site = %d %q", w.Code, w.Body.String())
	}
	if err := h.stop(); err != nil {
		t.Fatal(err)
	}

	eng := newFakeEngine("fake")
	restarted, err := New(Config{StateDir: h.dir, DoctorEnv: h.doctorEnv}, map[string]engine.Factory{"fake": eng.factory()}, &fakeHost{})
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.closeEngines()
	hello := restarted.hello(context.Background()).GetHello()
	if hello.GetDecoyMode() != decoy.ModeOwn || hello.GetDecoySha256() != bundle.Hash() {
		t.Fatalf("restarted Hello decoy = %q %q", hello.GetDecoyMode(), hello.GetDecoySha256())
	}
	if !contains(hello.GetCapabilities(), capDecoy) {
		t.Fatalf("restarted capabilities %v lack %s", hello.GetCapabilities(), capDecoy)
	}
	w = httptest.NewRecorder()
	restarted.masquerade("inb_1").ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusOK || w.Body.String() != "node site" {
		t.Fatalf("restarted site = %d %q", w.Code, w.Body.String())
	}
}

func TestDecoyModeCommandRejectsInvalidBundle(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.waitConnected()
	h.panel.send(&pb.ConnectResponse{Message: &pb.ConnectResponse_SetDecoy{SetDecoy: &pb.SetDecoy{
		RequestId: "decoy_bad", Mode: decoy.ModeOwn, BundleSha256: "bad", Files: []*pb.DecoyFile{{Path: "../index.html", Contents: []byte("bad")}},
	}}})
	result := h.panel.nextCmd()
	if result.Ok || result.Error != "bad_params" {
		t.Fatalf("invalid decoy result = %+v", result)
	}
	mode, hash := h.a.decoySite.State()
	if mode != decoy.ModeBuiltin || hash != "" {
		t.Fatalf("invalid bundle changed state to %q %q", mode, hash)
	}
}
