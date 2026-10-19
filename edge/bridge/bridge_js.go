//go:build js && wasm

// Package bridge is the JavaScript side of the edge Workers' wasm: the init options, the promise and request/response plumbing,
// the after-response task runner, the adapters for the Worker's bindings (D1, node link, limiter, SSH sockets) and the
// mgPanel object a Worker script calls. Each Worker has a wasm of its own (cmd/mistgate-edge-*) that calls Run with its Role,
// which names what it builds and which mgPanel members it has; cmd/mistgate-edge is the one with every role.
package bridge

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"slices"
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

// Role is what one Worker's wasm is: the panel it builds and the members of mgPanel it exposes.
type Role struct {
	Name string
	// Build assembles the panel of this role (app.Build, app.BuildAdmin ...).
	Build func(app.Config) (*app.Panel, error)
	// Auth says whether the role needs the auth service: every role but the public one.
	Auth bool
	// CreatesInstance says whether this Worker creates the instance (the prefixes, the agent name, the WebAuthn identity) on a
	// database that has none, and offers the setup token. The admin does; every other Worker answers errNoInstance until the
	// admin has been opened once.
	CreatesInstance bool
	// Ops are the members of mgPanel besides init, which every role has. A role lists only the operations it serves, so the
	// code behind the others is not linked into its wasm; calling an operation the role lacks is a TypeError in the Worker.
	Ops []Op
}

// Op adds one member to mgPanel.
type Op func(api js.Value)

// active is the role this wasm runs; Run sets it. (The tests of this package set it themselves.)
var active Role

type edgeState struct {
	store         *store.Store
	fleet         *fleet.Fleet
	panel         *app.Panel
	handler       http.Handler
	afterResponse *edgeTaskRunner
	serve         bool // the Provisioner role: runs SSH installs (mgPanel.provision) and refuses requests of another build
}

var (
	errCronRequest = errors.New("mgPanel.cron requires an object with an at timestamp")
	errCronArgs    = errors.New("mgPanel.cron requires an integer at timestamp")
)

// edgeTaskRunner tracks after-response work across overlapping requests in this isolate. Every request can wait on
// the current idle promise; it resolves once all queued or running work has finished.
type edgeTaskRunner struct {
	mu      sync.Mutex
	active  int
	idle    chan struct{}
	pending []func()
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
	r.pending = append(r.pending, work)
	r.mu.Unlock()
}

// release starts queued work on a later JavaScript task, after the response or job that queued it has finished.
func (r *edgeTaskRunner) release() {
	r.mu.Lock()
	work := r.pending
	r.pending = nil
	r.mu.Unlock()
	if len(work) == 0 {
		return
	}
	var start js.Func
	start = js.FuncOf(func(js.Value, []js.Value) any {
		start.Release()
		for _, w := range work {
			go r.run(w)
		}
		return nil
	})
	js.Global().Call("setTimeout", start, 0)
}

func (r *edgeTaskRunner) run(work func()) {
	defer func() {
		r.release()
		r.mu.Lock()
		r.active--
		if r.active == 0 {
			close(r.idle)
		}
		r.mu.Unlock()
	}()
	work()
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

// initTimeout bounds one initPanel (its D1 work). A variable so that a test can shorten it.
var initTimeout = 15 * time.Second

// OpFetch is mgPanel.fetch: one buffered HTTP request through the role's handler.
func OpFetch(api js.Value) {
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
			defer current.afterResponse.release()
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
}

// OpCron is mgPanel.cron: the scheduled work (EdgeTick).
func OpCron(api js.Value) {
	api.Set("cron", js.FuncOf(func(_ js.Value, args []js.Value) any {
		return promise(func() (js.Value, error) { return cron(args) })
	}))
}

// OpLink is mgPanel.link: one step of the agent link for the NodeLink Durable Object.
func OpLink(api js.Value) { api.Set("link", linkFunc()) }

// OpProvision is mgPanel.provision: one pass of the Provisioner's install queue. Only a role with this operation gets the SSH
// client into its wasm.
func OpProvision(api js.Value) {
	provisioner = sshProvisioner
	api.Set("provision", js.FuncOf(func(js.Value, []js.Value) any {
		return promise(provisionPass)
	}))
}

// Run publishes mgPanel for the role and keeps the program alive. It does not return.
func Run(role Role) {
	active = role
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
	for _, op := range role.Ops {
		op(api)
	}
	js.Global().Set("mgPanel", api)
	select {}
}

func cron(args []js.Value) (js.Value, error) {
	if len(args) != 1 || args[0].Type() != js.TypeObject || args[0].IsNull() {
		return js.Undefined(), errCronRequest
	}
	at, err := integerInt64(args[0].Get("at"))
	if err != nil {
		return js.Undefined(), errCronArgs
	}
	stateMu.RLock()
	current := state
	stateMu.RUnlock()
	if current == nil {
		return js.Undefined(), errNotInitialized
	}
	defer current.afterResponse.release()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	if err := current.panel.EdgeTick(ctx, time.UnixMilli(at)); err != nil {
		js.Global().Get("console").Call("error", "edge tick failed:", err.Error())
	}
	out := js.Global().Get("Object").New()
	out.Set("waitUntil", current.afterResponse.WaitUntil())
	return out, nil
}

func initPanel(options js.Value) error {
	stateMu.Lock()
	defer stateMu.Unlock()
	if state != nil {
		return nil
	}
	// A D1 call whose Worker request was cancelled never settles; without a deadline this init would hold stateMu for ever and
	// every fetch (RLock) behind it. The Worker gives up on an init after worker/src/shell.ts PANEL_START_BOUND_MS and retries.
	ctx, cancel := context.WithTimeout(context.Background(), initTimeout)
	defer cancel()

	opts, err := parseInitOptions(options)
	if err != nil {
		return err
	}
	st, err := store.OpenD1(ctx, opts.d1)
	if err != nil {
		return err
	}
	keepStore := false
	defer func() {
		if !keepStore {
			_ = st.Close()
		}
	}()

	in, pendingSettings, err := loadEdgeInstance(ctx, st, opts)
	creates := active.CreatesInstance && !opts.provision.serve
	if !creates && (errors.Is(err, errFreshPublicURL) || err == nil && pendingSettings["rp_id"] != "") {
		// Only the admin creates an instance (the Provisioner never does, in whatever Worker): every other role reads what
		// the admin stored, and answers errNoInstance until it has.
		return errNoInstance
	}
	if err != nil {
		return err
	}
	if !opts.hasNodeLink {
		in.LinkPrefix = ""
	}
	if err := checkEdgeAdminAddress(in); err != nil {
		return err
	}

	vlt, err := vault.New(opts.masterKey)
	if err != nil {
		return err
	}
	web.SetAssets(opts.assets) // before app.Build: the admin SPA and sub.html are read through it
	brand, err := instance.Load(ctx, st)
	if err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	limiter := newEdgeLimiter(opts.limit, log)
	afterResponse := newEdgeTaskRunner()
	var remote fleet.Remote
	if opts.hasNodeLink {
		remote = &edgeRemote{ask: opts.nodeLink.Get("ask"), send: opts.nodeLink.Get("send"), close: opts.nodeLink.Get("close"), poke: opts.nodeLink.Get("poke")}
	}
	newAuth := func(in app.InstanceConfig) (*auth.Service, error) {
		return auth.New(st, auth.Config{
			RPID: in.RPID, RPName: brand.BrandName(), Origins: in.RPOrigins, Vault: vlt, SourceURL: opts.sourceURL,
			Limiter: limiter,
		}, log)
	}
	// Everything that can refuse these settings runs before the first write: a failed first init stores nothing.
	var authSvc *auth.Service
	if active.Auth {
		if authSvc, err = newAuth(in); err != nil {
			return err
		}
	}
	if len(pendingSettings) != 0 {
		rpID, rpOrigins := in.RPID, in.RPOrigins
		if in, err = settleEdgeInstance(ctx, st, in, pendingSettings); err != nil {
			return err
		}
		if !opts.hasNodeLink {
			in.LinkPrefix = ""
		}
		if err := checkEdgeAdminAddress(in); err != nil {
			return err
		}
		if active.Auth && (in.RPID != rpID || !slices.Equal(in.RPOrigins, rpOrigins)) {
			if authSvc, err = newAuth(in); err != nil {
				return err
			}
		}
	}
	cfg := app.Config{
		Store: st, Vault: vlt, Auth: authSvc, Limiter: limiter, MasterKey: opts.masterKey, Clock: time.Now,
		Logger: log, Instance: in, Title: brand.BrandName(), DataDir: edgeNoFilesystemDataDir, AfterResponse: afterResponse.Run,
		Remote: remote, ProvisionForward: !opts.provision.serve,
	}
	if opts.provision.kick.Type() == js.TypeFunction {
		cfg.ProvisionKick = opts.provision.wake
	}
	if opts.provision.serve {
		if provisioner == nil {
			return errNotProvisioner
		}
		cfg.SSHClient, cfg.NodeBinaries = provisioner(opts.provision.sshConnect)
	}
	built, err := active.Build(cfg)
	if err != nil {
		return err
	}
	if creates { // the Provisioner never offers a setup token
		if err := offerSetup(ctx, st, opts, in, time.Now()); err != nil {
			return err
		}
	}
	handler := built.Handler
	if handler != nil { // the scheduled-work role serves no HTTP
		handler = withEdgeTestHooks(handler, built.Fleet)
		if opts.provision.serve {
			handler = requireBuild(handler)
		}
	}
	// EdgeTick owns the bounded edge phases; app.edgeDeferred records the VPS phases still deferred on the Worker.
	state = &edgeState{store: st, fleet: built.Fleet, panel: built, handler: handler, afterResponse: afterResponse, serve: opts.provision.serve}
	keepStore = true
	return nil
}

func checkEdgeAdminAddress(in app.InstanceConfig) error {
	if in.AdminListen != "" {
		return errSeparateAdminListener
	}
	if in.AdminHost == "" && (in.AdminPrefix == "" || in.AdminPrefix == "/") {
		return errMissingEdgeAdminPath
	}
	return nil
}

// offerSetup makes sure an owner can still create the first admin. With a setup token hash from the deployer
// (SETUP_TOKEN_SHA256) the panel offers that hash and logs nothing but its expiry; otherwise it prints a link with a
// random token once.
func offerSetup(ctx context.Context, st *store.Store, opts initOptions, in app.InstanceConfig, now time.Time) error {
	console := js.Global().Get("console")
	if opts.setupTokenHash != nil {
		offer, err := st.OfferSetupToken(ctx, opts.setupTokenHash, now.Add(auth.SetupTokenTTL))
		if err != nil || offer.AdminExists {
			return err
		}
		when := offer.Expires.UTC().Format(time.RFC3339)
		if offer.Used || !offer.Expires.After(now) {
			console.Call("log", "No admin yet. The setup token of SETUP_TOKEN_SHA256 has expired or was used (at "+when+"); set a new SETUP_TOKEN_SHA256 and deploy again")
		} else {
			console.Call("log", "No admin yet. The setup token of SETUP_TOKEN_SHA256 is accepted until "+when)
		}
		return nil
	}
	adminExists, expiry, tokenExists, err := st.SetupTokenStatus(ctx, now)
	if err != nil || adminExists {
		return err
	}
	if tokenExists {
		console.Call("log", "No admin yet. A setup link was already issued; expires at "+expiry.UTC().Format(time.RFC3339))
		return nil
	}
	token, err := auth.IssueSetupToken(ctx, st, now)
	if err != nil {
		return err
	}
	console.Call("log", "No admin yet. Create one (link works once, 30 minutes):\n  "+in.AdminURL()+"setup#"+token)
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
	w := &responseBuffer{code: http.StatusOK, header: make(http.Header), cancel: cancel}
	handler.ServeHTTP(w, req)
	if w.flushed {
		return unsupportedStreamResponse()
	}
	return edgeResponse{status: w.code, headers: w.header, body: w.body.Bytes()}
}

// responseBuffer is the http.ResponseWriter of one buffered response. It does what net/http/httptest's ResponseRecorder
// did here, which the bridge used before and which pulled the test server's flag and TLS code into every panel.wasm:
// the status is 200 until WriteHeader says otherwise (the first call wins; a code outside 100-999 panics), a handler that
// set no Content-Type gets one sniffed from its first write, and a body written for a status without one is kept and
// refused with http.ErrBodyNotAllowed. A Flush ends the request: the response is unsupported (streaming) and cancel stops
// the handler.
type responseBuffer struct {
	code        int
	header      http.Header
	body        bytes.Buffer
	wroteHeader bool
	flushed     bool
	cancel      context.CancelFunc
}

func (w *responseBuffer) Header() http.Header { return w.header }

func (w *responseBuffer) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	if code < 100 || code > 999 {
		panic(fmt.Sprintf("invalid WriteHeader code %v", code))
	}
	w.code = code
	w.wroteHeader = true
}

func (w *responseBuffer) Write(data []byte) (int, error) {
	if !w.wroteHeader {
		_, hasType := w.header["Content-Type"]
		if !hasType && w.header.Get("Transfer-Encoding") == "" {
			w.header.Set("Content-Type", http.DetectContentType(data))
		}
		w.WriteHeader(http.StatusOK)
	}
	w.body.Write(data)
	if w.code >= 100 && w.code <= 199 || w.code == http.StatusNoContent || w.code == http.StatusNotModified {
		return 0, http.ErrBodyNotAllowed
	}
	return len(data), nil
}

func (w *responseBuffer) Flush() {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	w.flushed = true
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
