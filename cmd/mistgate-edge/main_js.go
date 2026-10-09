//go:build js && wasm

// Command mistgate-edge exposes the shared panel handler to a Cloudflare Worker.
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"sort"
	"sync"
	"syscall/js"
	"time"

	"github.com/mistgate/mistgate/internal/panel/app"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/fleet"
	"github.com/mistgate/mistgate/internal/panel/instance"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
	"github.com/mistgate/mistgate/web"
)

type edgeState struct {
	store         *store.Store
	fleet         *fleet.Fleet
	handler       http.Handler
	afterResponse *edgeTaskRunner
}

// edgeTaskRunner tracks after-response work across overlapping requests in this isolate. Every request can wait on
// the current idle promise; it resolves once all work started so far has finished.
type edgeTaskRunner struct {
	mu     sync.Mutex
	active int
	idle   chan struct{}
}

func newEdgeTaskRunner() *edgeTaskRunner {
	idle := make(chan struct{})
	close(idle)
	return &edgeTaskRunner{idle: idle}
}

func (r *edgeTaskRunner) Run(work func()) {
	r.mu.Lock()
	if r.active == 0 {
		r.idle = make(chan struct{})
	}
	r.active++
	r.mu.Unlock()

	go func() {
		defer func() {
			r.mu.Lock()
			r.active--
			if r.active == 0 {
				close(r.idle)
			}
			r.mu.Unlock()
		}()
		work()
	}()
}

func (r *edgeTaskRunner) WaitUntil() js.Value {
	r.mu.Lock()
	if r.active == 0 {
		r.mu.Unlock()
		return js.Global().Get("Promise").Call("resolve")
	}
	idle := r.idle
	r.mu.Unlock()

	executor := js.FuncOf(func(_ js.Value, args []js.Value) any {
		resolve := args[0]
		go func() {
			<-idle
			resolve.Invoke(js.Undefined())
		}()
		return nil
	})
	promise := js.Global().Get("Promise").New(executor)
	executor.Release()
	return promise
}

// Backup.New still requires a path; its file adapter is not available in the Worker and no backup loop is started here.
const edgeNoFilesystemDataDir = "/edge-unavailable"

var (
	stateMu sync.RWMutex
	state   *edgeState
)

func main() {
	api := js.Global().Get("Object").New()
	api.Set("init", js.FuncOf(func(_ js.Value, args []js.Value) any {
		return promise(func() (js.Value, error) {
			if len(args) == 0 || args[0].Type() != js.TypeObject || args[0].IsNull() {
				return js.Undefined(), errInitOptions
			}
			if err := initPanel(args[0]); err != nil {
				return js.Undefined(), err
			}
			return api, nil
		})
	}))
	api.Set("fetch", js.FuncOf(func(_ js.Value, args []js.Value) any {
		return promise(func() (js.Value, error) {
			if len(args) == 0 || args[0].Type() != js.TypeObject || args[0].IsNull() {
				return js.Undefined(), errFetchRequest
			}
			stateMu.RLock()
			current := state
			stateMu.RUnlock()
			if current == nil {
				return js.Undefined(), errNotInitialized
			}
			req, err := requestFromJS(args[0])
			if err != nil {
				return js.Undefined(), err
			}
			resp := serve(current.handler, req)
			out := responseToJS(resp)
			out.Set("waitUntil", current.afterResponse.WaitUntil())
			return out, nil
		})
	}))
	api.Set("link", linkFunc())
	js.Global().Set("mgPanel", api)
	select {}
}

func initPanel(options js.Value) error {
	stateMu.Lock()
	defer stateMu.Unlock()
	if state != nil {
		return nil
	}

	opts, err := parseInitOptions(options)
	if err != nil {
		return err
	}
	st, err := store.OpenD1(context.Background(), opts.d1)
	if err != nil {
		return err
	}
	keepStore := false
	defer func() {
		if !keepStore {
			_ = st.Close()
		}
	}()

	in, pendingSettings, err := loadEdgeInstance(context.Background(), st, opts)
	if err != nil {
		return err
	}
	if !opts.hasNodeLink {
		in.LinkPrefix = ""
	}
	if in.AdminListen != "" {
		return errSeparateAdminListener
	}
	if in.AdminHost == "" && (in.AdminPrefix == "" || in.AdminPrefix == "/") {
		return errMissingEdgeAdminPath
	}

	vlt, err := vault.New(opts.masterKey)
	if err != nil {
		return err
	}
	web.SetAssets(opts.assets) // before app.Build: the admin SPA and sub.html are read through it
	brand, err := instance.Load(context.Background(), st)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	limiter := newEdgeLimiter(opts.limit, log)
	afterResponse := newEdgeTaskRunner()
	var remote fleet.Remote
	if opts.hasNodeLink {
		remote = &edgeRemote{ask: opts.nodeLink.Get("ask"), close: opts.nodeLink.Get("close")}
	}
	authSvc, err := auth.New(st, auth.Config{
		RPID: in.RPID, RPName: brand.BrandName(), Origins: in.RPOrigins, Vault: vlt, SourceURL: opts.sourceURL,
		Limiter: limiter,
	}, log)
	if err != nil {
		return err
	}
	built, err := app.Build(app.Config{
		Store: st, Vault: vlt, Auth: authSvc, Limiter: limiter, MasterKey: opts.masterKey, Clock: time.Now,
		Logger: log, Instance: in, Title: brand.BrandName(), DataDir: edgeNoFilesystemDataDir, AfterResponse: afterResponse.Run,
		Remote: remote,
	})
	if err != nil {
		return err
	}
	if len(pendingSettings) != 0 {
		if err := st.SetSettings(context.Background(), pendingSettings); err != nil {
			return err
		}
	}

	adminExists, expiry, tokenExists, err := st.SetupTokenStatus(context.Background(), time.Now())
	if err != nil {
		return err
	}
	if !adminExists {
		if tokenExists {
			js.Global().Get("console").Call("log", "No admin yet. A setup link was already issued; expires at "+expiry.UTC().Format(time.RFC3339))
		} else {
			token, err := auth.IssueSetupToken(context.Background(), st, time.Now())
			if err != nil {
				return err
			}
			url := in.AdminURL() + "setup#" + token
			js.Global().Get("console").Call("log", "No admin yet. Create one (link works once, 30 minutes):\n  "+url)
		}
	}
	// TODO(phase-2): Cron/alarm expires MCP plans.
	// TODO(phase-2): Cron/alarm invokes the fleet reconciliation background job.
	// TODO(phase-2): Cron/alarm invokes the access cleanup background job.
	// TODO(phase-2): Cron/alarm invokes health checks, evaluation and retention.
	// TODO(phase-2): Cron/alarm invokes update checks, rollout advancement and pruning.
	// TODO(phase-2): Cron/alarm invokes backup scheduling.
	// TODO(phase-2): Cron/alarm invokes Telegram polling and delivery.
	// TODO(phase-2): Cron/alarm invokes provisioning workers. A Worker isolate starts none of these jobs.
	state = &edgeState{store: st, fleet: built.Fleet, handler: withEdgeTestHooks(built.Handler, built.Fleet), afterResponse: afterResponse}
	keepStore = true
	return nil
}

type edgeResponse struct {
	status  int
	headers http.Header
	body    []byte
}

// The edge bridge buffers each response; Connect server-streaming RPCs receive an explicit unsupported error.
func serve(handler http.Handler, req *http.Request) edgeResponse {
	ctx, cancel := context.WithCancel(req.Context())
	defer cancel()
	req = req.WithContext(ctx)
	recorder := httptest.NewRecorder()
	writer := flushRecorder{ResponseRecorder: recorder, cancel: cancel}
	handler.ServeHTTP(writer, req)
	if recorder.Flushed {
		return unsupportedStreamResponse()
	}
	return edgeResponse{status: recorder.Code, headers: recorder.Header(), body: recorder.Body.Bytes()}
}

type flushRecorder struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
}

func (w flushRecorder) Flush() {
	w.ResponseRecorder.Flush()
	w.cancel()
}

func unsupportedStreamResponse() edgeResponse {
	return edgeResponse{
		status:  http.StatusNotImplemented,
		headers: http.Header{"Content-Type": {"application/json"}},
		body:    []byte(`{"code":"unimplemented","message":"server-streaming responses are not supported by the edge bridge"}`),
	}
}

func requestFromJS(value js.Value) (*http.Request, error) {
	method := value.Get("method")
	rawURL := value.Get("url")
	if method.Type() != js.TypeString || method.String() == "" || rawURL.Type() != js.TypeString || rawURL.String() == "" {
		return nil, errInvalidFetchRequest
	}
	u, err := url.Parse(rawURL.String())
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, errInvalidFetchURL
	}

	body, err := bytesFromJS(value.Get("body"))
	if err != nil {
		return nil, err
	}
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.Background(), method.String(), u.String(), bodyReader)
	if err != nil {
		return nil, errInvalidFetchRequest
	}
	headers := value.Get("headers")
	if headers.Type() != js.TypeObject || headers.IsNull() || !js.Global().Get("Array").Call("isArray", headers).Bool() {
		return nil, errInvalidFetchHeaders
	}
	for i := 0; i < headers.Length(); i++ {
		pair := headers.Index(i)
		if pair.Type() != js.TypeObject || pair.IsNull() || pair.Length() != 2 || pair.Index(0).Type() != js.TypeString || pair.Index(1).Type() != js.TypeString {
			return nil, errInvalidFetchHeaders
		}
		req.Header.Add(pair.Index(0).String(), pair.Index(1).String())
	}
	if ip, err := netip.ParseAddr(req.Header.Get("CF-Connecting-IP")); err == nil {
		req.RemoteAddr = net.JoinHostPort(ip.String(), "443")
	}
	req.TLS = &tls.ConnectionState{Version: tls.VersionTLS13, HandshakeComplete: true}
	return req, nil
}

func bytesFromJS(value js.Value) ([]byte, error) {
	if value.Type() == js.TypeUndefined || value.IsNull() {
		return nil, nil
	}
	if value.Type() != js.TypeObject || !value.InstanceOf(js.Global().Get("Uint8Array")) {
		return nil, errInvalidFetchBody
	}
	data := make([]byte, value.Length())
	if n := js.CopyBytesToGo(data, value); n != len(data) {
		return nil, errInvalidFetchBody
	}
	return data, nil
}

func responseToJS(resp edgeResponse) js.Value {
	out := js.Global().Get("Object").New()
	out.Set("status", resp.status)
	pairs := js.Global().Get("Array").New()
	keys := make([]string, 0, len(resp.headers))
	for key := range resp.headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		for _, value := range resp.headers.Values(key) {
			pair := js.Global().Get("Array").New()
			pair.Call("push", key, value)
			pairs.Call("push", pair)
		}
	}
	out.Set("headers", pairs)
	out.Set("body", bytesToJS(resp.body))
	return out
}

func bytesToJS(data []byte) js.Value {
	out := js.Global().Get("Uint8Array").New(len(data))
	js.CopyBytesToJS(out, data)
	return out
}

func promise(run func() (js.Value, error)) js.Value {
	executor := js.FuncOf(func(_ js.Value, args []js.Value) any {
		resolve, reject := args[0], args[1]
		go func() {
			var result js.Value
			var err error
			func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						err = errPromisePanic
					}
				}()
				result, err = run()
			}()
			if err != nil {
				reject.Invoke(js.Global().Get("Error").New(err.Error()))
				return
			}
			resolve.Invoke(result)
		}()
		return nil
	})
	deferred := js.Global().Get("Promise").New(executor)
	executor.Release()
	return deferred
}
