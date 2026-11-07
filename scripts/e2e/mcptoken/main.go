// Command mcptoken mints an API token for MCP on a test panel and prints the `claude mcp add` line that connects a coding
// agent to it, so an agent can drive a real throwaway panel over MCP (scripts/e2e/mcp-token.sh is the wrapper;
// scripts/e2e-wsl.sh --hold is the panel).
//
// Making a token is the owner's act: it needs the owner's session and a step-up (a fresh authenticator code). The owner
// session cookie and the authenticator secret of the TEST panel come from the environment, never from flags (which a process
// list shows):
//
//	MG_ADMIN_URL      the admin URL with a trailing slash, e.g. http://localhost:8081/
//	MG_OWNER_COOKIE   the value of the __Host-sid cookie of the owner's session
//	MG_TOTP_SECRET    the base32 secret of the owner's authenticator
//
// The token secret is printed once, in the output line only. It lives as long as -ttl-days (default 1) and has the profile
// of -profile (readonly, operator or admin; default operator: reads, tests and day-to-day changes, no owner approvals).
// Never point this at a production panel.
package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

var hc = &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func main() {
	profile := flag.String("profile", "operator", "readonly, operator or admin")
	name := flag.String("name", "", "token name (default agent-<time>)")
	days := flag.Int("ttl-days", 1, "days the token lives (1 to 365)")
	flag.Parse()
	admin, cookie, secret := os.Getenv("MG_ADMIN_URL"), os.Getenv("MG_OWNER_COOKIE"), os.Getenv("MG_TOTP_SECRET")
	if admin == "" || cookie == "" || secret == "" {
		fail("set MG_ADMIN_URL, MG_OWNER_COOKIE and MG_TOTP_SECRET (the test panel's owner session; see scripts/e2e/mcp-token.sh)")
	}
	if !strings.HasSuffix(admin, "/") {
		admin += "/"
	}
	p, ok := map[string]string{"readonly": "TOKEN_PROFILE_READONLY", "operator": "TOKEN_PROFILE_OPERATOR", "admin": "TOKEN_PROFILE_ADMIN"}[*profile]
	if !ok {
		fail("-profile: readonly, operator or admin")
	}
	if *name == "" {
		*name = "agent-" + time.Now().Format("0102-150405")
	}
	c := &client{admin: admin, cookie: cookie}
	body := map[string]any{"name": *name, "profile": p, "ttlDays": *days}
	st, out := c.rpc("ApiTokenService", "CreateApiToken", body)
	if st != 200 { // the sign-in's step-up is over: prove a fresh factor and try once more
		if err := c.stepUp(secret); err != nil {
			fail("step-up: " + err.Error())
		}
		st, out = c.rpc("ApiTokenService", "CreateApiToken", body)
	}
	tok, _ := out["secret"].(string)
	if st != 200 || tok == "" {
		fail(fmt.Sprintf("CreateApiToken: status %d: %v", st, out["message"]))
	}
	fmt.Fprintf(os.Stderr, "token %q (%s) made, valid %d day(s); the secret is in the line below and is not shown again\n", *name, *profile, *days)
	fmt.Printf("claude mcp add --transport http mistgate %smcp --header \"Authorization: Bearer %s\"\n", admin, tok)
}

type client struct{ admin, cookie string }

func (c *client) rpc(svc, method string, body any) (int, map[string]any) {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", c.admin+"api/mistgate.admin.v1."+svc+"/"+method, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", "__Host-sid="+c.cookie)
	resp, err := hc.Do(req)
	if err != nil {
		fail(err.Error())
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	m := map[string]any{}
	_ = json.Unmarshal(raw, &m)
	return resp.StatusCode, m
}

// stepUp proves the authenticator factor. A code is accepted once, and only if it is newer than the last one the session
// used, so after a refusal it waits for the next 30-second step and tries again.
func (c *client) stepUp(secret string) error {
	var last string
	for try := 0; try < 3; try++ {
		step := time.Now().Unix() / 30
		st, out := c.rpc("AuthService", "FinishStepUp", map[string]any{"totpCode": totpCode(secret, step)})
		if st == 200 {
			return nil
		}
		last = fmt.Sprintf("status %d %v", st, out["message"])
		for time.Now().Unix()/30 <= step {
			time.Sleep(500 * time.Millisecond)
		}
	}
	return fmt.Errorf("%s", last)
}

// totpCode is RFC 6238: HMAC-SHA1, 6 digits, 30-second steps.
func totpCode(secret string, step int64) string {
	s := strings.ToUpper(strings.ReplaceAll(secret, " ", ""))
	key, err := base32.StdEncoding.DecodeString(s + strings.Repeat("=", (8-len(s)%8)%8))
	if err != nil {
		fail("MG_TOTP_SECRET is not base32")
	}
	h := hmac.New(sha1.New, key)
	_ = binary.Write(h, binary.BigEndian, uint64(step))
	sum := h.Sum(nil)
	o := sum[len(sum)-1] & 15
	return fmt.Sprintf("%06d", (binary.BigEndian.Uint32(sum[o:o+4])&0x7fffffff)%1000000)
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "mcptoken:", msg)
	os.Exit(1)
}
