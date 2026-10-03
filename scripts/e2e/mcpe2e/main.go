// Command mcpe2e drives a real panel (the `mistgate` binary, on loopback, a throwaway data dir) through the whole
// API-token and MCP surface: tokens of the three profiles made by an owner session with a step-up,
// a real go-sdk MCP client over Streamable HTTP and over the `mistgate mcp` stdio proxy, plan and apply, the owner's
// approval and rejection, a revoked token, the audit trail, the decoy on the public listener, and a scan of everything an
// agent can read for the secrets of the fixtures.
//
//	go build -o bin/mistgate ./cmd/mistgate && go run ./scripts/e2e/mcpe2e -bin bin/mistgate        # both admin modes
//	scripts/e2e-mcp.sh                                                                                   # the same, builds first
//
// -mode listener|prefix|both: the admin on its own listener (`setup --admin-listen`, the dev layout), or on the public
// listener under a secret path prefix (the production layout, where /mcp outside the prefix must be the decoy).
// Everything is plain HTTP on 127.0.0.1 (no firewall prompt on Windows), the data lives in a temp dir that is removed at the
// end (-keep leaves it and prints where). It prints a table of checks and exits 1 if one failed.
package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mistgate/mistgate/internal/panel/mcp"
)

var (
	binPath = flag.String("bin", "", "path to the mistgate binary (required)")
	mode    = flag.String("mode", "both", "listener, prefix or both")
	keep    = flag.Bool("keep", false, "keep the temp dir (logs, database)")

	results []result
	cleanup func() // stops the panel and removes its data; fatal runs it before it exits
	failed  bool
)

type result struct {
	mode, name string
	ok         bool
	detail     string
}

func main() {
	flag.Parse()
	if *binPath == "" {
		fmt.Fprintln(os.Stderr, "usage: mcpe2e -bin <mistgate binary> [-mode listener|prefix|both] [-keep]")
		os.Exit(2)
	}
	abs, err := filepath.Abs(*binPath)
	if err != nil {
		fatal(err)
	}
	*binPath = abs
	var modes []string
	switch *mode {
	case "both":
		modes = []string{"listener", "prefix"}
	case "listener", "prefix":
		modes = []string{*mode}
	default:
		fatal(errors.New("-mode: listener, prefix or both"))
	}
	for _, m := range modes {
		if err := run(m); err != nil {
			results = append(results, result{m, "run aborted", false, err.Error()})
			failed = true
		}
	}
	fmt.Printf("\n%-9s %-4s %s\n", "mode", "", "check")
	for _, r := range results {
		st := "PASS"
		if !r.ok {
			st = "FAIL"
		}
		fmt.Printf("%-9s %-4s %s", r.mode, st, r.name)
		if r.detail != "" {
			fmt.Printf("  [%s]", r.detail)
		}
		fmt.Println()
	}
	if failed {
		fmt.Println("\nFAILED")
		os.Exit(1)
	}
	fmt.Printf("\nall %d checks passed\n", len(results))
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "mcpe2e:", err)
	if cleanup != nil {
		cleanup()
	}
	os.Exit(2)
}

// ------------------------------------------------------------------------------------------------ the run

type env struct {
	mode, dir      string
	pub, admin     string // base URLs, admin ends with "/"
	cookie         string
	totp           string
	lastStep       int64
	stepUntil      int64
	secrets        map[string]string // name -> value that no agent-facing output may contain
	leakScan       bytes.Buffer      // everything an agent read
	audit          *os.File
	panelLog       string
	fixtureAddress string
}

func (e *env) check(name string, ok bool, detail string, a ...any) {
	if !ok {
		failed = true
	}
	d := fmt.Sprintf(detail, a...)
	if ok && len(d) > 90 {
		d = d[:90]
	}
	results = append(results, result{e.mode, name, ok, d})
	if !ok {
		fmt.Printf("[%s] FAIL %s: %s\n", e.mode, name, d)
	}
}

func run(m string) error {
	dir, err := os.MkdirTemp("", "mgm-e2e-mcp-")
	if err != nil {
		return err
	}
	e := &env{mode: m, dir: dir, secrets: map[string]string{}, fixtureAddress: "203.0.113.77"}
	var cmd *exec.Cmd
	cleanup = func() {
		cleanup = nil
		if cmd != nil && cmd.Process != nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
		if *keep {
			fmt.Println("kept:", dir)
			return
		}
		for i := 0; i < 20; i++ {
			if os.RemoveAll(dir) == nil {
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
		fmt.Println("could not remove", dir)
	}
	defer func() {
		if cleanup != nil {
			cleanup()
		}
	}()

	pubPort, admPort := freePort(), freePort()
	data := filepath.Join(dir, "data")
	setupArgs := []string{"setup", "--data-dir", data, "--public-url", "http://localhost:" + pubPort}
	if m == "listener" {
		setupArgs = append(setupArgs, "--admin-listen", "127.0.0.1:"+admPort)
	}
	out, err := exec.Command(*binPath, setupArgs...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("setup: %v: %s", err, out)
	}
	link := regexp.MustCompile(`Setup link: (\S+)setup#(\S+)`).FindStringSubmatch(string(out))
	if link == nil {
		return errors.New("no setup link in the output of setup")
	}
	setupToken := link[2]
	e.secrets["setup token"] = setupToken
	e.pub = "http://127.0.0.1:" + pubPort + "/"
	if m == "listener" {
		e.admin = "http://127.0.0.1:" + admPort + "/"
	} else {
		u, err := url.Parse(link[1])
		if err != nil {
			return err
		}
		e.admin = e.pub[:len(e.pub)-1] + u.Path // the secret prefix, ends with "/"
	}

	e.panelLog = filepath.Join(dir, "panel.log")
	lf, err := os.Create(e.panelLog)
	if err != nil {
		return err
	}
	defer lf.Close()
	cmd = exec.Command(*binPath, "serve", "--data-dir", data, "--listen", "127.0.0.1:"+pubPort, "--agent-addr", "127.0.0.1:"+pubPort)
	cmd.Stdout, cmd.Stderr = lf, lf
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := waitFor(20*time.Second, func() bool {
		st, _ := e.rpc("", "", "AuthService", "GetLoginInfo", nil)
		return st == 200
	}); err != nil {
		return fmt.Errorf("panel did not start: %w", err)
	}

	if err := e.signup(setupToken); err != nil {
		return fmt.Errorf("owner setup: %w", err)
	}
	e.scenario()
	e.leaks()
	return nil
}

// ------------------------------------------------------------------------------------------------ HTTP helpers

var hc = &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// rpc is one Connect JSON call; cookie and bearer are the two ways in ("" = none).
func (e *env) rpc(cookie, bearer, svc, method string, body any) (int, map[string]any) {
	if body == nil {
		body = map[string]any{}
	}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", e.admin+"api/mistgate.admin.v1."+svc+"/"+method, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if cookie != "" {
		req.Header.Set("Cookie", "__Host-sid="+cookie)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return 0, map[string]any{"error": err.Error()}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		m = map[string]any{"raw": string(raw)}
	}
	m["_status"] = float64(resp.StatusCode)
	m["_retry"] = resp.Header.Get("Retry-After")
	return resp.StatusCode, m
}

// owner calls as the owner session, opening a step-up first when asked.
func (e *env) owner(svc, method string, body any, stepUp bool) (int, map[string]any) {
	if stepUp {
		if err := e.stepup(); err != nil {
			return 0, map[string]any{"error": err.Error()}
		}
	}
	return e.rpc(e.cookie, "", svc, method, body)
}

func (e *env) signup(setupToken string) error {
	login, pass := "e2e-admin", "pw-"+strconv.FormatInt(time.Now().UnixNano(), 36)+"-e2e"
	e.secrets["owner password"] = pass
	st, b := e.rpc("", "", "AuthService", "BeginSetup", map[string]any{
		"setupToken": setupToken, "displayName": "E2E owner", "method": "SETUP_METHOD_PASSWORD", "login": login, "password": pass,
	})
	if st != 200 {
		return fmt.Errorf("BeginSetup %d %v", st, b)
	}
	e.totp, _ = b["totpSecret"].(string)
	e.secrets["totp secret"] = e.totp
	cer, _ := b["ceremonyId"].(string)
	step := time.Now().Unix() / 30
	req, _ := http.NewRequest("POST", e.admin+"api/mistgate.admin.v1.AuthService/FinishSetup", strings.NewReader(mustJSON(map[string]any{
		"setupToken": setupToken, "ceremonyId": cer, "totpCode": totpCode(e.totp, step),
	})))
	req.Header.Set("Content-Type", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("FinishSetup %d", resp.StatusCode)
	}
	for _, c := range resp.Cookies() {
		if c.Name == "__Host-sid" {
			e.cookie = c.Value
		}
	}
	if e.cookie == "" {
		return errors.New("FinishSetup set no session cookie")
	}
	e.secrets["owner session"] = e.cookie
	e.lastStep = step
	e.stepUntil = time.Now().Unix() + 280 // the sign-in proved a factor
	if st, b := e.rpc(e.cookie, "", "AuthService", "Me", nil); st != 200 {
		return fmt.Errorf("Me %d %v", st, b)
	}
	return nil
}

// stepup keeps the owner session's step-up open; a password admin proves it with an authenticator code newer than the last.
func (e *env) stepup() error {
	if e.stepUntil-time.Now().Unix() > 60 {
		return nil
	}
	for time.Now().Unix()/30 <= e.lastStep {
		time.Sleep(500 * time.Millisecond)
	}
	step := time.Now().Unix() / 30
	st, b := e.rpc(e.cookie, "", "AuthService", "FinishStepUp", map[string]any{"totpCode": totpCode(e.totp, step)})
	if st != 200 {
		return fmt.Errorf("FinishStepUp %d %v", st, b)
	}
	e.lastStep = step
	e.stepUntil = int64(num(b["stepUpUntilUnix"]))
	return nil
}

func totpCode(secret string, step int64) string {
	s := strings.ToUpper(secret)
	key, err := base32.StdEncoding.DecodeString(s + strings.Repeat("=", (8-len(s)%8)%8))
	if err != nil {
		return "000000"
	}
	h := hmac.New(sha1.New, key)
	binary.Write(h, binary.BigEndian, uint64(step))
	sum := h.Sum(nil)
	o := sum[len(sum)-1] & 15
	return fmt.Sprintf("%06d", (binary.BigEndian.Uint32(sum[o:o+4])&0x7fffffff)%1000000)
}

func num(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		f, _ := strconv.ParseFloat(x, 64)
		return f
	}
	return 0
}

func str(m map[string]any, path ...string) string {
	var cur any = m
	for _, p := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = mm[p]
	}
	s, _ := cur.(string)
	return s
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

func freePort() string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fatal(err)
	}
	defer l.Close()
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
}

func waitFor(d time.Duration, f func() bool) error {
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(200 * time.Millisecond) {
		if f() {
			return nil
		}
	}
	return errors.New("timeout")
}

// ------------------------------------------------------------------------------------------------ MCP helpers

type bearerRT struct{ secret string }

func (b bearerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.secret)
	return http.DefaultTransport.RoundTrip(r)
}

func (e *env) connect(secret string) (*sdk.ClientSession, error) {
	c := sdk.NewClient(&sdk.Implementation{Name: "mcpe2e", Version: "1"}, nil)
	return c.Connect(context.Background(), &sdk.StreamableClientTransport{
		Endpoint: e.admin + "mcp", HTTPClient: &http.Client{Transport: bearerRT{secret}}, DisableStandaloneSSE: true, MaxRetries: -1,
	}, nil)
}

func (e *env) stdio(secret string, stderr *bytes.Buffer) (*sdk.ClientSession, error) {
	f := filepath.Join(e.dir, "token-"+strconv.FormatInt(time.Now().UnixNano(), 36))
	if err := os.WriteFile(f, []byte(secret+"\n"), 0o600); err != nil {
		return nil, err
	}
	cmd := exec.Command(*binPath, "mcp", "--url", e.admin, "--token-file", f)
	cmd.Stderr = stderr
	c := sdk.NewClient(&sdk.Implementation{Name: "mcpe2e-stdio", Version: "1"}, nil)
	return c.Connect(context.Background(), &sdk.CommandTransport{Command: cmd}, nil)
}

func toolNames(s *sdk.ClientSession) (names []string, descs map[string]string, err error) {
	descs = map[string]string{}
	for t, err := range s.Tools(context.Background(), nil) {
		if err != nil {
			return nil, nil, err
		}
		names = append(names, t.Name)
		descs[t.Name] = t.Description
	}
	return names, descs, nil
}

// call returns the text of a tool result; err is a protocol error (unknown tool), isErr the tool's own error.
func (e *env) call(s *sdk.ClientSession, name string, args map[string]any) (text string, isErr bool, err error) {
	r, err := s.CallTool(context.Background(), &sdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return err.Error(), true, err
	}
	var sb strings.Builder
	for _, c := range r.Content {
		if tc, ok := c.(*sdk.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	e.leakScan.WriteString("\n#" + name + "\n" + sb.String())
	return sb.String(), r.IsError, nil
}

func (e *env) mustCall(s *sdk.ClientSession, name string, args map[string]any) string {
	t, isErr, err := e.call(s, name, args)
	if err != nil || isErr {
		fatal(fmt.Errorf("%s: %s", name, t))
	}
	return t
}

func isWrite(n string) bool { return strings.HasSuffix(n, "_plan") || strings.HasSuffix(n, "_apply") }

// ------------------------------------------------------------------------------------------------ the scenario

func (e *env) scenario() {
	// -- tokens, made by the owner session with a step-up
	mk := func(name, profile string, ttl, rate int) (id, secret string, st int, body map[string]any) {
		st, body = e.owner("ApiTokenService", "CreateApiToken", map[string]any{"name": name, "profile": profile, "ttlDays": ttl, "rateLimitPerMin": rate}, true)
		return str(body, "token", "id"), str(body, "secret"), st, body
	}
	_, roSecret, st, b := mk("looker", "TOKEN_PROFILE_READONLY", 7, 600)
	e.check("owner creates a readonly token", st == 200 && strings.HasPrefix(roSecret, "tk1_") && len(roSecret) == 47, "status %d", st)
	opID, opSecret, _, _ := mk("ops", "TOKEN_PROFILE_OPERATOR", 7, 600)
	adID, adSecret, _, _ := mk("boss", "TOKEN_PROFILE_ADMIN", 7, 600)
	_, slowSecret, _, _ := mk("slow", "TOKEN_PROFILE_READONLY", 7, 5)
	for n, s := range map[string]string{"readonly": roSecret, "operator": opSecret, "admin": adSecret, "slow": slowSecret} {
		e.secrets["token secret "+n] = s
	}
	_, _, st, b = mk("forever", "TOKEN_PROFILE_READONLY", 400, 0)
	e.check("a token cannot live longer than a year", st != 200, "status %d %v", st, str(b, "message"))
	_, _, st, _ = mk("looker", "TOKEN_PROFILE_READONLY", 7, 0)
	e.check("a live token name is unique", st != 200, "status %d", st)
	st, b = e.rpc("", adSecret, "ApiTokenService", "CreateApiToken", map[string]any{"name": "x", "profile": "TOKEN_PROFILE_READONLY"})
	e.check("a token (even an admin one) cannot create tokens", st == 403, "status %d", st)
	st, b = e.rpc("", adSecret, "ApprovalService", "Approve", map[string]any{"id": "pln_x"})
	e.check("a token cannot approve", st == 403, "status %d", st)
	st, b = e.owner("ApiTokenService", "ListApiTokens", nil, false)
	listed := mustJSON(b)
	e.check("the token list shows hints, never a secret", st == 200 && strings.Contains(listed, opID) && !strings.Contains(listed, opSecret) && !strings.Contains(listed, roSecret), "status %d", st)

	// -- fixtures: a group, three users, a node
	_, b = e.owner("GroupService", "CreateGroup", map[string]any{"name": "everyone"}, false)
	groupID := str(b, "group", "id")
	var userIDs []string
	for _, n := range []string{"ann", "bob", "cat"} {
		st, b = e.owner("UserService", "CreateUser", map[string]any{"name": n, "groupId": groupID}, false)
		if st != 200 {
			fatal(fmt.Errorf("CreateUser %s: %d %v", n, st, b))
		}
		userIDs = append(userIDs, str(b, "user", "id"))
		if su := str(b, "subscriptionUrl"); su != "" {
			if u, err := url.Parse(su); err == nil {
				for i, seg := range strings.Split(strings.Trim(u.Path, "/"), "/") {
					if len(seg) >= 16 {
						e.secrets["subscription path "+n+strconv.Itoa(i)] = seg
					}
				}
			}
		}
	}
	st, b = e.owner("NodeService", "CreateEnrollment", map[string]any{"name": "de1", "address": e.fixtureAddress, "countryCode": "DE"}, true)
	e.check("fixture node enrolled", st == 200, "status %d", st)
	for i, f := range strings.Fields(str(b, "installCommand")) {
		if len(f) >= 24 && !strings.ContainsAny(f, "/:") && !strings.HasPrefix(f, "--") {
			e.secrets["enrollment token "+strconv.Itoa(i)] = f
		}
	}
	e.secrets["panel CA fingerprint"] = strings.TrimPrefix(str(b, "caFingerprint"), "sha256:")

	// -- the door: no token, a wrong one, the public listener
	post := func(u, bearer string) (int, string) {
		req, _ := http.NewRequest("POST", u, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := hc.Do(req)
		if err != nil {
			return 0, err.Error()
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return resp.StatusCode, string(raw)
	}
	cs, _ := post(e.admin+"mcp", "")
	e.check("admin /mcp without a token is refused", cs == 401, "status %d", cs)
	cs, _ = post(e.admin+"mcp", "tk1_"+strings.Repeat("x", 43))
	e.check("admin /mcp with an unknown token is refused", cs == 401, "status %d", cs)
	cs, body := post(e.pub+"mcp", roSecret)
	e.check("public listener: /mcp is the decoy (404, no JSON-RPC)", cs == 404 && !strings.Contains(body, "jsonrpc"), "status %d", cs)
	if e.mode == "prefix" {
		cs, body = post(e.pub+"api/mistgate.admin.v1.AuthService/Me", roSecret)
		e.check("public listener: /api is the decoy too", cs == 404, "status %d", cs)
	} else {
		cs, _ = post(e.pub+"api/mistgate.admin.v1.UserService/ListUsers", roSecret)
		e.check("public listener: no admin API there", cs == 404, "status %d", cs)
	}

	// -- profiles over Streamable HTTP
	ro, err := e.connect(roSecret)
	if err != nil {
		fatal(fmt.Errorf("connect readonly: %w", err))
	}
	defer ro.Close()
	op, err := e.connect(opSecret)
	if err != nil {
		fatal(err)
	}
	defer op.Close()
	ad, err := e.connect(adSecret)
	if err != nil {
		fatal(err)
	}
	defer ad.Close()
	roN, roD, _ := toolNames(ro)
	opN, _, _ := toolNames(op)
	adN, _, _ := toolNames(ad)
	e.check("tool counts per profile 14 / 28 / 41", len(roN) == 14 && len(opN) == 28 && len(adN) == 41, "%d / %d / %d", len(roN), len(opN), len(adN))
	writes := 0
	for _, n := range roN {
		if isWrite(n) {
			writes++
		}
	}
	e.check("readonly sees reads only", writes == 0, "%d write tools visible", writes)
	_, isErr, perr := e.call(ro, "user_create_plan", map[string]any{"name": "x", "group_id": groupID})
	e.check("a hidden tool cannot be called by name", perr != nil || isErr, "")
	_, isErr, perr = e.call(op, "rollout_start_plan", map[string]any{})
	e.check("operator cannot call a fleet tool", perr != nil || isErr, "")
	initRes := ro.InitializeResult()
	e.check("server instructions warn about untrusted data", initRes != nil && strings.Contains(strings.ToLower(initRes.Instructions), "untrusted"), "")
	missing := 0
	for n, d := range roD {
		if n != "updates_status" && !strings.Contains(strings.ToLower(d), "untrusted") && n != "fleet_status" {
			missing++
		}
	}
	e.check("data tools say their text is untrusted", missing <= 3, "%d read tool descriptions without the warning", missing)

	// -- stdio proxy
	var perrBuf bytes.Buffer
	ps, err := e.stdio(roSecret, &perrBuf)
	if err != nil {
		e.check("stdio proxy connects", false, "%v %s", err, perrBuf.String())
	} else {
		n, _, _ := toolNames(ps)
		fs, isErr, _ := e.call(ps, "fleet_status", map[string]any{})
		e.check("`mistgate mcp` stdio proxy: 14 tools, fleet_status answers", len(n) == 14 && !isErr && strings.Contains(fs, "nodes_total"), "%d tools", len(n))
		ps.Close()
		e.check("the proxy never prints the token", !strings.Contains(perrBuf.String(), roSecret), "")
	}
	{
		var b2 bytes.Buffer
		ps2, err := e.stdio(adSecret, &b2)
		if err == nil {
			n, _, _ := toolNames(ps2)
			e.check("stdio proxy with an admin token: 46 tools", len(n) == 46, "%d", len(n))
			ps2.Close()
		} else {
			e.check("stdio proxy with an admin token", false, "%v", err)
		}
	}

	// -- every read tool, as admin (the scan for secrets reads these)
	allReads := map[string]map[string]any{
		"fleet_status": {}, "node_get": {"node": "de1"}, "node_metrics": {"node": "de1"}, "node_doctor": {"node": "de1"},
		"users_search": {}, "groups_list": {}, "user_get": {"user_id": userIDs[0]}, "user_traffic": {"user_id": userIDs[0]},
		"user_devices": {"user_id": userIDs[0]}, "subscription_preview": {"user_id": userIDs[0]}, "alerts_list": {"include_history": true},
		"events_search": {}, "checks_results": {}, "audit_search": {}, "updates_status": {},
	}
	bad := []string{}
	for n, a := range allReads {
		t, isErr, err := e.call(ad, n, a)
		if err != nil || (isErr && n != "node_metrics" && n != "node_doctor" && n != "checks_results") {
			bad = append(bad, n+": "+t)
		}
	}
	e.check("all 15 read tools answer for an admin token", len(bad) == 0, "%v", bad)
	for n, a := range allReads {
		if n == "audit_search" {
			continue
		}
		if t, isErr, _ := e.call(ro, n, a); isErr && n != "node_metrics" && n != "node_doctor" && n != "checks_results" {
			e.check("readonly can call "+n, false, "%s", t)
		}
	}

	// -- operator: user_create through plan and apply, no approval
	pl := decode[mcp.PlanOut](e.mustCall(op, "user_create_plan", map[string]any{"name": "dan", "group_id": groupID, "term_days": 30, "reason": "e2e"}))
	e.check("user_create_plan: a confirm token, no approval needed", pl.ConfirmToken != "" && !pl.NeedsApproval, "needs_approval=%v", pl.NeedsApproval)
	tx, isErr, _ := e.call(ro, "user_create_apply", map[string]any{"confirm_token": pl.ConfirmToken})
	e.check("another token cannot apply someone else's plan", isErr, "%.60s", tx)
	ap := decode[mcp.ApplyOut](e.mustCall(op, "user_create_apply", map[string]any{"confirm_token": pl.ConfirmToken}))
	e.check("user_create_apply creates the user", ap.Status == "applied" && strings.Contains(ap.Result, "usr_"), "%s", ap.Status)
	ap2 := decode[mcp.ApplyOut](e.mustCall(op, "user_create_apply", map[string]any{"confirm_token": pl.ConfirmToken}))
	e.check("a second apply returns the saved result, does not repeat", ap2.Result == ap.Result, "")
	if i := strings.Index(ap.Result, "usr_"); i >= 0 {
		id := ap.Result[i:]
		if j := strings.IndexAny(id, ") "); j > 0 {
			id = id[:j]
		}
		userIDs = append(userIDs, id)
	}
	tx, isErr, _ = e.call(op, "alert_mute_plan", map[string]any{"alert_id": "alt_unknown", "duration_s": 60})
	e.check("alert_mute_plan on an unknown alert is a clean error", isErr && !strings.Contains(tx, "panic"), "%.60s", tx)

	// -- the owner's inbox and a dangerous change: four users at once
	dis := decode[mcp.PlanOut](e.mustCall(op, "user_disable_plan", map[string]any{"user_ids": userIDs, "reason": "e2e cleanup"}))
	e.check("bulk user_disable_plan waits for a human", dis.NeedsApproval && len(dis.Danger) > 0, "%+v", dis.Danger)
	tx, isErr, _ = e.call(op, "user_disable_apply", map[string]any{"confirm_token": dis.ConfirmToken})
	e.check("apply before the approval is refused", isErr && strings.Contains(tx, "waiting for the owner"), "%.80s", tx)
	st, b = e.owner("ApprovalService", "ListApprovals", map[string]any{}, false)
	e.check("the plan is in the owner's inbox", st == 200 && strings.Contains(mustJSON(b), dis.PlanID) && num(b["awaiting"]) >= 1, "awaiting=%v", b["awaiting"])
	st, _ = e.rpc("", opSecret, "ApprovalService", "ListApprovals", nil)
	e.check("the inbox is closed to tokens", st == 403, "status %d", st)
	st, b = e.owner("ApprovalService", "Approve", map[string]any{"id": dis.PlanID}, true)
	e.check("owner approves (with a step-up)", st == 200, "status %d %v", st, str(b, "message"))
	ap = decode[mcp.ApplyOut](e.mustCall(op, "user_disable_apply", map[string]any{"confirm_token": dis.ConfirmToken}))
	e.check("apply after the approval disables 4 users", ap.Status == "applied" && strings.Contains(ap.Result, "4 users disabled"), "%s %s", ap.Status, ap.Result)
	tx = e.mustCall(ro, "users_search", map[string]any{})
	e.check("users_search shows them disabled", strings.Count(tx, `"disabled"`) >= 4, "")
	en := decode[mcp.PlanOut](e.mustCall(op, "user_enable_plan", map[string]any{"user_ids": userIDs}))
	e.mustCall(op, "user_enable_apply", map[string]any{"confirm_token": en.ConfirmToken})
	rt := decode[mcp.PlanOut](e.mustCall(op, "user_reset_traffic_plan", map[string]any{"user_ids": userIDs[:1]}))
	ap = decode[mcp.ApplyOut](e.mustCall(op, "user_reset_traffic_apply", map[string]any{"confirm_token": rt.ConfirmToken}))
	e.check("user_reset_traffic plan/apply resets one user", !rt.NeedsApproval && ap.Status == "applied" && strings.Contains(ap.Result, "1 user"), "%s %s", ap.Status, ap.Result)

	rej := decode[mcp.PlanOut](e.mustCall(op, "user_disable_plan", map[string]any{"user_ids": userIDs}))
	st, b = e.owner("ApprovalService", "Reject", map[string]any{"id": rej.PlanID}, false)
	e.check("owner rejects", st == 200, "status %d", st)
	tx, isErr, _ = e.call(op, "user_disable_apply", map[string]any{"confirm_token": rej.ConfirmToken})
	e.check("apply after a rejection is refused", isErr && strings.Contains(tx, "rejected"), "%.80s", tx)

	// -- the fleet: rollouts need a signed bundle and a node
	rpText, rpErr, _ := e.call(ad, "rollout_start_plan", map[string]any{})
	// a fresh panel has no signed bundle (needs a release key and a node: scripts/e2e-wsl.sh): the plan is a clean refusal
	e.check("rollout_start_plan on a panel without a bundle: a clean refusal, no plan", rpErr && strings.Contains(rpText, "not trusted"), "%.80s", rpText)
	st, _ = e.rpc("", adSecret, "UpdateService", "StartRollout", nil)
	e.check("StartRollout over /api with an admin token is closed", st == 403, "status %d", st)
	np := e.call3(ad, "node_fix_plan", map[string]any{"node": "de1", "fix_id": "ntp_sync"})
	e.check("node_fix_plan reaches the doctor (dry run) without an approval error", !strings.Contains(np, "not allowed") && !strings.Contains(np, "step-up"), "%.100s", np)

	// -- the API with a Bearer token: allowed, audited as token:<id>, rate limited
	st, b = e.rpc("", opSecret, "UserService", "ListUsers", nil)
	e.check("operator token over /api: ListUsers works", st == 200, "status %d", st)
	st, _ = e.rpc("", roSecret, "UserService", "CreateUser", map[string]any{"name": "zed", "groupId": groupID})
	e.check("readonly token over /api cannot write", st == 403, "status %d", st)
	st, _ = e.rpc("", opSecret, "AuthService", "ListSessions", nil)
	e.check("a token cannot reach the owner's session list", st == 403 || st == 404, "status %d", st)
	n429, retry := 0, ""
	for i := 0; i < 14; i++ {
		st, b = e.rpc("", slowSecret, "UserService", "ListUsers", nil)
		if st == 429 {
			n429++
			retry, _ = b["_retry"].(string)
		}
	}
	e.check("rate limit: 5 per minute gives 429 with Retry-After", n429 > 0 && retry != "", "%d refusals, Retry-After %q", n429, retry)

	// -- revoke: stops at the very next request; open plans are cancelled
	open := decode[mcp.PlanOut](e.mustCall(op, "user_enable_plan", map[string]any{"user_ids": userIDs[:1]}))
	st, b = e.owner("ApiTokenService", "RevokeApiToken", map[string]any{"id": opID}, true)
	e.check("owner revokes the operator token", st == 200, "status %d", st)
	_, isErr, perr = e.call(op, "user_enable_apply", map[string]any{"confirm_token": open.ConfirmToken})
	e.check("the revoked token's open session stops", perr != nil || isErr, "")
	_, err = e.connect(opSecret)
	e.check("the revoked token cannot connect over HTTP", err != nil, "")
	cs, _ = post(e.admin+"mcp", opSecret)
	e.check("the revoked token gets 401 on /mcp", cs == 401, "status %d", cs)
	st, _ = e.rpc("", opSecret, "UserService", "ListUsers", nil)
	e.check("the revoked token gets 401 on /api", st == 401, "status %d", st)
	var rb bytes.Buffer
	if ps, err := e.stdio(opSecret, &rb); err == nil {
		ps.Close()
		e.check("the stdio proxy refuses a revoked token", false, "it connected")
	} else {
		e.check("the stdio proxy refuses a revoked token", !strings.Contains(rb.String(), opSecret), "%.80s", strings.ReplaceAll(rb.String(), "\n", " "))
	}

	// -- the audit trail, as the owner sees it
	var rows []map[string]any
	for before := float64(0); ; {
		_, b = e.owner("AuthService", "ListAudit", map[string]any{"pageSize": 200, "beforeId": before}, false)
		arr, _ := b["entries"].([]any)
		for _, a := range arr {
			if m, ok := a.(map[string]any); ok {
				rows = append(rows, m)
			}
		}
		before = num(b["nextBeforeId"])
		if before == 0 || len(rows) > 3000 {
			break
		}
	}
	seen := map[string]bool{}
	auditText := mustJSON(rows)
	for _, r := range rows {
		seen[str(r, "source")+"/"+str(r, "action")+"/"+str(r, "actorId")] = true
	}
	for _, want := range []string{
		"AUDIT_SOURCE_MCP/mcp_plan/mcp:" + opID, "AUDIT_SOURCE_MCP/mcp_apply/mcp:" + opID,
		"AUDIT_SOURCE_PANEL/approval_approve/",
	} {
		ok := seen[want]
		if strings.HasSuffix(want, "/") { // any admin
			ok = false
			for k := range seen {
				ok = ok || strings.HasPrefix(k, want)
			}
		}
		e.check("audit has "+want, ok, "")
	}
	adRows := []string{}
	for _, r := range rows {
		if str(r, "actorId") == "mcp:"+adID {
			adRows = append(adRows, str(r, "action"))
		}
	}
	e.check("audit has rows of the admin token as mcp:<id> (plan refusals, reads)", len(adRows) > 0, "%v", adRows)
	apiRow, mcpWrite := false, false
	for _, r := range rows {
		if str(r, "source") == "AUDIT_SOURCE_API" && strings.HasPrefix(str(r, "actorId"), "token:") {
			apiRow = true
		}
		if str(r, "source") == "AUDIT_SOURCE_MCP" && strings.HasPrefix(str(r, "actorId"), "mcp:") && str(r, "action") != "mcp_plan" && str(r, "action") != "mcp_apply" {
			mcpWrite = true
		}
	}
	e.check("audit has token:<id> rows for the API channel", apiRow, "")
	e.check("audit has the modules' own rows under mcp:<id>", mcpWrite, "")
	e.check("audit carries no secret and no confirm token", !containsAny(auditText, e.secretVals()) && !strings.Contains(auditText, "cf_"), "")
	e.leakScan.WriteString("\n#audit(owner)\n" + auditText)
	_, ab := e.owner("ApprovalService", "ListApprovals", map[string]any{"historyLimit": 200}, false)
	e.leakScan.WriteString("\n#approvals(owner)\n" + mustJSON(ab))
	_, ab = e.owner("ApiTokenService", "ListApiTokens", nil, false)
	e.leakScan.WriteString("\n#tokens(owner)\n" + mustJSON(ab))
}

func (e *env) call3(s *sdk.ClientSession, n string, a map[string]any) string {
	t, _, err := e.call(s, n, a)
	if err != nil {
		return err.Error()
	}
	return t
}

func decode[T any](s string) T {
	var v T
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		fatal(fmt.Errorf("decode %.200q: %v", s, err))
	}
	return v
}

func (e *env) secretVals() map[string]string { return e.secrets }

func containsAny(text string, m map[string]string) bool {
	for _, v := range m {
		if len(v) >= 8 && strings.Contains(text, v) {
			return true
		}
	}
	return false
}

// leaks checks what an agent could read, the owner's lists and the panel's own log for the fixtures' secrets.
func (e *env) leaks() {
	text := e.leakScan.String()
	var hit []string
	for n, v := range e.secrets {
		if len(v) >= 8 && strings.Contains(text, v) {
			hit = append(hit, n)
		}
	}
	e.check(fmt.Sprintf("no fixture secret (%d tracked) in %d KiB of tool output, audit, approvals, token list", len(e.secrets), len(text)/1024),
		len(hit) == 0 && len(e.secrets) >= 10, "leaked: %v", hit)
	e.check("no node address in tool output", !strings.Contains(text, e.fixtureAddress), "")
	subPrefix := regexp.MustCompile(`/[a-z0-9]{20,}/`).FindString(text)
	e.check("no secret-looking path segment in tool output", subPrefix == "", "%.40s", subPrefix)
	log, _ := os.ReadFile(e.panelLog)
	var lhit []string
	for n, v := range e.secrets {
		if len(v) >= 8 && bytes.Contains(log, []byte(v)) {
			lhit = append(lhit, n)
		}
	}
	e.check(fmt.Sprintf("no secret in the panel log (%d bytes)", len(log)), len(lhit) == 0, "leaked: %v", lhit)
}
