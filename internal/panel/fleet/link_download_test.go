package fleet

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
)

func TestLinkHandlerRoutesEdgeUpdateDownloads(t *testing.T) {
	e := newEnv(t)
	const token = "token-value-that-must-not-be-logged"
	updates := &fakeUpdates{assetURL: "https://downloads.example.com/v1.2.3/agent"}
	e.f.SetUpdates(updates)
	e.f.cfg.Remote = &testRemote{}

	request := func(handler http.Handler) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/update/nod_test/mistgate-node-linux-amd64", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		return w
	}
	if got := request(e.f.LinkHandler()); got.Code != http.StatusNoContent || got.Header().Get(AssetMarkerHeader) != updates.assetURL {
		t.Fatalf("edge asset route = %d %v, want 204 and the release URL", got.Code, got.Header())
	}
	updates.mu.Lock()
	if len(updates.assets) != 1 || updates.assets[0] != (fakeLinkAssetRequest{nodeID: "nod_test", name: "mistgate-node-linux-amd64", token: token}) {
		got := append([]fakeLinkAssetRequest(nil), updates.assets...)
		updates.mu.Unlock()
		t.Fatalf("LinkDownload requests = %+v", got)
	}
	updates.mu.Unlock()

	updates.assetErr = errors.New("store failure")
	if got := request(e.f.LinkHandler()); got.Code != http.StatusServiceUnavailable {
		t.Fatalf("backend error status = %d, want 503", got.Code)
	}

	updates.assetErr = connect.NewError(connect.CodeNotFound, errors.New("not found"))
	if got := request(e.f.LinkHandler()); got.Code != http.StatusNotFound {
		t.Fatalf("unauthorized asset status = %d, want 404", got.Code)
	}

	e.f.cfg.Remote = nil
	updates.assetErr = nil
	before := len(updates.assets)
	if got := request(e.f.LinkHandler()); got.Code != http.StatusNotFound {
		t.Fatalf("VPS update route = %d, want 404 for the decoy", got.Code)
	}
	updates.mu.Lock()
	after := len(updates.assets)
	updates.mu.Unlock()
	if after != before {
		t.Fatalf("VPS update path reached LinkDownload: calls %d -> %d", before, after)
	}
}

func TestLinkHandlerRejectsMalformedUpdatePaths(t *testing.T) {
	e := newEnv(t)
	e.f.cfg.Remote = &testRemote{}
	updates := &fakeUpdates{assetURL: "https://downloads.example.com/asset"}
	e.f.SetUpdates(updates)
	h := e.f.LinkHandler()
	for _, path := range []string{"/update/", "/update/nod_test", "/update/nod_test/file/extra", "/update//file"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, w.Code)
		}
	}
	if len(updates.assets) != 0 {
		t.Fatalf("malformed update paths reached LinkDownload: %+v", updates.assets)
	}
}
