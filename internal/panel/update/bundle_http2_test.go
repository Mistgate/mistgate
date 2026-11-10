package update

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/gen/mistgate/agent/v1/agentv1connect"
)

type bundleHTTP2Handler struct {
	service *Service
	done    chan error
}

func (h *bundleHTTP2Handler) Connect(context.Context, *connect.BidiStream[agentv1.ConnectRequest, agentv1.ConnectResponse]) error {
	return connect.NewError(connect.CodeUnimplemented, errors.New("not used in this test"))
}

func (h *bundleHTTP2Handler) FetchUpdate(ctx context.Context, req *connect.Request[agentv1.FetchUpdateRequest], stream *connect.ServerStream[agentv1.FetchUpdateResponse]) error {
	err := h.service.ServeWithWriteDeadline(ctx, "nod_http2_stall", req.Msg.Name, req.Msg.Offset, WriteDeadlineFromContext(ctx),
		func(data []byte, total uint64) error {
			return stream.Send(&agentv1.FetchUpdateResponse{Data: data, TotalSize: total})
		})
	h.done <- err
	return err
}

func TestBundleHTTP2StalledReaderReturnsDeadlineAndFreesSlot(t *testing.T) {
	e := newEnv(t)
	data := make([]byte, 16<<20)
	if _, err := rand.Read(data); err != nil {
		t.Fatal(err)
	}
	e.bundle("1.0", 10, map[string][]byte{"mistgate-node-linux-amd64": data})
	e.s.sendTimeout = 300 * time.Millisecond
	h := &bundleHTTP2Handler{service: e.s, done: make(chan error, 2)}
	path, rpc := agentv1connect.NewAgentServiceHandler(h)
	mux := http.NewServeMux()
	mux.Handle(path, rpc)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		controller := http.NewResponseController(w)
		ctx := WithWriteDeadline(r.Context(), controller.SetWriteDeadline)
		mux.ServeHTTP(w, r.WithContext(ctx))
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	client := agentv1connect.NewAgentServiceClient(server.Client(), server.URL)

	open := func() (*connect.ServerStreamForClient[agentv1.FetchUpdateResponse], context.CancelFunc, error) {
		ctx, cancel := context.WithCancel(context.Background())
		stream, err := client.FetchUpdate(ctx, connect.NewRequest(&agentv1.FetchUpdateRequest{Name: "mistgate-node-linux-amd64"}))
		if err != nil {
			cancel()
			return nil, nil, err
		}
		return stream, cancel, nil
	}

	first, cancelFirst, err := open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cancelFirst)
	firstErr := waitBundleSendResult(t, h.done)
	if connect.CodeOf(firstErr) != connect.CodeUnavailable || !errors.Is(firstErr, os.ErrDeadlineExceeded) {
		t.Fatalf("stalled HTTP/2 send returned %v, want unavailable wrapping a write deadline error", firstErr)
	}
	cancelFirst()
	if err := first.Close(); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("reset first HTTP/2 response: %v", err)
	}

	second, cancelSecond, err := open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cancelSecond)
	secondErr := waitBundleSendResult(t, h.done)
	if connect.CodeOf(secondErr) != connect.CodeUnavailable || !errors.Is(secondErr, os.ErrDeadlineExceeded) {
		t.Fatalf("second HTTP/2 download returned %v; the first request may still own the node slot", secondErr)
	}
	cancelSecond()
	if err := second.Close(); err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("reset second HTTP/2 response: %v", err)
	}
}

func waitBundleSendResult(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("stalled HTTP/2 stream did not return after its write deadline")
		return nil
	}
}
