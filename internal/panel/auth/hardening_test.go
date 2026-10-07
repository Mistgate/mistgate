package auth

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/descope/virtualwebauthn"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// --- helpers ---

// cookieReq is a request carrying the session cookie the way a browser sends it.
func cookieReq[T any](msg *T, cookie string) *connect.Request[T] {
	r := connect.NewRequest(msg)
	if cookie != "" {
		r.Header().Set("Cookie", CookieName+"="+cookie)
	}
	return r
}

// authed is the context RequireSession would build for the session behind cookie.
func authed(t *testing.T, s *Service, cookie string) context.Context {
	t.Helper()
	sess, admin, err := s.resolveSession(context.Background(), cookie)
	if err != nil {
		t.Fatalf("resolve session: %v", err)
	}
	ctx := context.WithValue(context.Background(), adminKey{}, admin)
	return context.WithValue(ctx, sessionKey{}, sess)
}

func sessionCookie(t *testing.T, h http.Header) string {
	t.Helper()
	for _, c := range (&http.Response{Header: h}).Cookies() {
		if c.Name == CookieName {
			return c.Value
		}
	}
	t.Fatal("no session cookie in the response")
	return ""
}

func codeOf(err error) connect.Code {
	if err == nil {
		return 0
	}
	return connect.CodeOf(err)
}

type keyring struct {
	rp   virtualwebauthn.RelyingParty
	dev  virtualwebauthn.Authenticator
	cred virtualwebauthn.Credential
}

// passkeyAdmin runs the passkey setup through the service with a software authenticator and returns the admin
// and the cookie of the session that setup opened.
func passkeyAdmin(t *testing.T, s *Service, st *store.Store) (store.Admin, string, *keyring) {
	t.Helper()
	ctx := context.Background()
	tok, err := IssueSetupToken(ctx, st, s.now())
	if err != nil {
		t.Fatal(err)
	}
	k := &keyring{
		rp:   virtualwebauthn.RelyingParty{Name: "Mistgate", ID: "localhost", Origin: "http://localhost:8081"},
		dev:  virtualwebauthn.NewAuthenticator(),
		cred: virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2),
	}
	begin, err := s.BeginSetup(ctx, connect.NewRequest(&adminv1.BeginSetupRequest{SetupToken: tok, DisplayName: "Ada"}))
	if err != nil {
		t.Fatal(err)
	}
	opts, err := virtualwebauthn.ParseAttestationOptions(begin.Msg.OptionsJson)
	if err != nil {
		t.Fatal(err)
	}
	k.dev.Options.UserHandle = []byte(opts.UserID)
	cj := virtualwebauthn.CreateAttestationResponse(k.rp, k.dev, k.cred, *opts)
	fin, err := s.FinishSetup(ctx, connect.NewRequest(&adminv1.FinishSetupRequest{SetupToken: tok, CeremonyId: begin.Msg.CeremonyId, CredentialJson: cj}))
	if err != nil {
		t.Fatal(err)
	}
	k.dev.AddCredential(k.cred)
	a, err := st.Admin(ctx, fin.Msg.Admin.Id)
	if err != nil {
		t.Fatal(err)
	}
	return a, sessionCookie(t, fin.Header()), k
}

func TestConcurrentFinishLoginConsumesCeremonyOnce(t *testing.T) {
	s, st, _ := newTestService(t)
	_, _, k := passkeyAdmin(t, s, st)
	ctx := context.Background()
	begin, err := s.BeginLogin(ctx, connect.NewRequest(&adminv1.BeginLoginRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	opts, err := virtualwebauthn.ParseAssertionOptions(begin.Msg.OptionsJson)
	if err != nil {
		t.Fatal(err)
	}
	credentialJSON := virtualwebauthn.CreateAssertionResponse(k.rp, k.dev, k.cred, *opts)
	finish := &adminv1.FinishLoginRequest{CeremonyId: begin.Msg.CeremonyId, CredentialJson: credentialJSON}
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, err := s.FinishLogin(ctx, connect.NewRequest(finish))
			results <- err
		}()
	}
	close(start)
	successes := 0
	for range 2 {
		if err := <-results; err == nil {
			successes++
		} else if codeOf(err) != connect.CodeUnauthenticated {
			t.Fatalf("losing finish: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("two finishes succeeded %d times, want once", successes)
	}
}

func TestConcurrentCeremonyFinishesConsumeOnce(t *testing.T) {
	type finishCase struct {
		name      string
		loserCode connect.Code
		prepare   func(*testing.T, *Service, *store.Store, *time.Time) (func() error, func(*testing.T))
	}
	verifySetup := func(t *testing.T, st *store.Store) {
		t.Helper()
		n, err := st.AdminCount(context.Background())
		if err != nil || n != 1 {
			t.Fatalf("created admins = %d, err=%v; want one", n, err)
		}
		var id, name, role string
		if err := st.R.QueryRowContext(context.Background(), `SELECT id, display_name, role FROM admin`).Scan(&id, &name, &role); err != nil {
			t.Fatal(err)
		}
		if id == "" || name != "Ada" || role != store.RoleOwner {
			t.Fatalf("created admin = id %q, name %q, role %q; want Ada as owner", id, name, role)
		}
	}
	cases := []finishCase{
		{
			name:      "passkey setup",
			loserCode: connect.CodeInvalidArgument,
			prepare: func(t *testing.T, s *Service, st *store.Store, _ *time.Time) (func() error, func(*testing.T)) {
				tok, err := IssueSetupToken(context.Background(), st, s.now())
				if err != nil {
					t.Fatal(err)
				}
				k := &keyring{rp: virtualwebauthn.RelyingParty{Name: "Mistgate", ID: "localhost", Origin: "http://localhost:8081"}, dev: virtualwebauthn.NewAuthenticator(), cred: virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2)}
				begin, err := s.BeginSetup(context.Background(), connect.NewRequest(&adminv1.BeginSetupRequest{SetupToken: tok, DisplayName: "Ada"}))
				if err != nil {
					t.Fatal(err)
				}
				opts, err := virtualwebauthn.ParseAttestationOptions(begin.Msg.OptionsJson)
				if err != nil {
					t.Fatal(err)
				}
				k.dev.Options.UserHandle = []byte(opts.UserID)
				credentialJSON := virtualwebauthn.CreateAttestationResponse(k.rp, k.dev, k.cred, *opts)
				finishReq := &adminv1.FinishSetupRequest{SetupToken: tok, CeremonyId: begin.Msg.CeremonyId, CredentialJson: credentialJSON}
				finish := func() error {
					_, err := s.FinishSetup(context.Background(), connect.NewRequest(finishReq))
					return err
				}
				verify := func(t *testing.T) {
					verifySetup(t, st)
				}
				return finish, verify
			},
		},
		{
			name:      "password setup",
			loserCode: connect.CodeInvalidArgument,
			prepare: func(t *testing.T, s *Service, st *store.Store, clock *time.Time) (func() error, func(*testing.T)) {
				tok, err := IssueSetupToken(context.Background(), st, s.now())
				if err != nil {
					t.Fatal(err)
				}
				begin, err := s.BeginSetup(context.Background(), connect.NewRequest(&adminv1.BeginSetupRequest{
					SetupToken: tok, DisplayName: "Ada", Method: adminv1.SetupMethod_SETUP_METHOD_PASSWORD, Login: "ada", Password: testPassword,
				}))
				if err != nil {
					t.Fatal(err)
				}
				code := totpCode(secretOf(t, begin.Msg.TotpSecret), totpStep(*clock))
				finishReq := &adminv1.FinishSetupRequest{SetupToken: tok, CeremonyId: begin.Msg.CeremonyId, TotpCode: code}
				finish := func() error {
					_, err := s.FinishSetup(context.Background(), connect.NewRequest(finishReq))
					return err
				}
				verify := func(t *testing.T) {
					verifySetup(t, st)
				}
				return finish, verify
			},
		},
		{
			name:      "step-up",
			loserCode: connect.CodeUnauthenticated,
			prepare: func(t *testing.T, s *Service, st *store.Store, clock *time.Time) (func() error, func(*testing.T)) {
				admin, cookie, k := passkeyAdmin(t, s, st)
				*clock = clock.Add(StepUpWindow + time.Second)
				begin, err := s.BeginStepUp(authed(t, s, cookie), cookieReq(&adminv1.BeginStepUpRequest{}, cookie))
				if err != nil {
					t.Fatal(err)
				}
				opts, err := virtualwebauthn.ParseAssertionOptions(begin.Msg.OptionsJson)
				if err != nil {
					t.Fatal(err)
				}
				credentialJSON := virtualwebauthn.CreateAssertionResponse(k.rp, k.dev, k.cred, *opts)
				finishReq := &adminv1.FinishStepUpRequest{CeremonyId: begin.Msg.CeremonyId, CredentialJson: credentialJSON}
				ctx := authed(t, s, cookie)
				finish := func() error {
					_, err := s.FinishStepUp(ctx, cookieReq(finishReq, cookie))
					return err
				}
				verify := func(t *testing.T) {
					pks, err := st.PasskeysByAdmin(context.Background(), admin.ID)
					if err != nil || len(pks) != 1 || countAuditResult(t, st, "stepup", "ok") != 1 {
						t.Fatalf("step-up state: passkeys=%d err=%v", len(pks), err)
					}
				}
				return finish, verify
			},
		},
		{
			name:      "add passkey",
			loserCode: connect.CodeInvalidArgument,
			prepare: func(t *testing.T, s *Service, st *store.Store, _ *time.Time) (func() error, func(*testing.T)) {
				admin, cookie, _ := passkeyAdmin(t, s, st)
				begin, err := s.BeginAddPasskey(authed(t, s, cookie), cookieReq(&adminv1.BeginAddPasskeyRequest{}, cookie))
				if err != nil {
					t.Fatal(err)
				}
				opts, err := virtualwebauthn.ParseAttestationOptions(begin.Msg.OptionsJson)
				if err != nil {
					t.Fatal(err)
				}
				dev, cred := virtualwebauthn.NewAuthenticator(), virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2)
				dev.Options.UserHandle = []byte(opts.UserID)
				credentialJSON := virtualwebauthn.CreateAttestationResponse(virtualwebauthn.RelyingParty{Name: "Mistgate", ID: "localhost", Origin: "http://localhost:8081"}, dev, cred, *opts)
				finishReq := &adminv1.FinishAddPasskeyRequest{CeremonyId: begin.Msg.CeremonyId, CredentialJson: credentialJSON}
				ctx := authed(t, s, cookie)
				finish := func() error {
					_, err := s.FinishAddPasskey(ctx, cookieReq(finishReq, cookie))
					return err
				}
				verify := func(t *testing.T) {
					pks, err := st.PasskeysByAdmin(context.Background(), admin.ID)
					if err != nil || len(pks) != 2 {
						t.Fatalf("passkeys after finish = %d, err=%v; want two", len(pks), err)
					}
				}
				return finish, verify
			},
		},
		{
			name:      "TOTP enrollment",
			loserCode: connect.CodeInvalidArgument,
			prepare: func(t *testing.T, s *Service, st *store.Store, clock *time.Time) (func() error, func(*testing.T)) {
				adminID, oldSecret := passwordAdmin(t, s, st, "ada")
				cookie := signedInPassword(t, s, clock, "ada", testPassword, oldSecret)
				begin, err := beginTOTP(s, cookie, "", "")
				if err != nil {
					t.Fatal(err)
				}
				newSecret := secretOf(t, begin.Msg.TotpSecret)
				code := totpCode(newSecret, totpStep(*clock))
				finish := func() error {
					_, err := finishTOTP(s, cookie, begin.Msg.CeremonyId, code)
					return err
				}
				verify := func(t *testing.T) {
					pw, err := st.PasswordByAdmin(context.Background(), adminID)
					if err != nil {
						t.Fatal(err)
					}
					got, err := s.vault.Open(pw.TOTPSecret, totpAAD(adminID))
					if err != nil || !bytes.Equal(got, newSecret) || countAuditResult(t, st, "totp_rebind", "ok") != 1 {
						t.Fatalf("TOTP enrollment state: secret updated=%v err=%v", bytes.Equal(got, newSecret), err)
					}
				}
				return finish, verify
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, st, clock := newTestService(t)
			finish, verify := tc.prepare(t, s, st, clock)
			start := make(chan struct{})
			results := make(chan error, 2)
			for range 2 {
				go func() {
					<-start
					results <- finish()
				}()
			}
			close(start)
			successes := 0
			for range 2 {
				if err := <-results; err == nil {
					successes++
				} else if codeOf(err) != tc.loserCode {
					t.Fatalf("losing finish: %v", err)
				}
			}
			if successes != 1 {
				t.Fatalf("two finishes succeeded %d times, want once", successes)
			}
			verify(t)
		})
	}
}

func TestCeremonyConsumeFailuresUseAuthenticationFailureResponses(t *testing.T) {
	t.Run("login", func(t *testing.T) {
		s, st, _ := newTestService(t)
		_, _, _ = passkeyAdmin(t, s, st)
		var events []Event
		s.SetEventHook(func(e Event) { events = append(events, e) })
		begin, err := s.BeginLogin(context.Background(), connect.NewRequest(&adminv1.BeginLoginRequest{}))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.W.Exec(`CREATE TRIGGER reject_login_ceremony_consume BEFORE DELETE ON auth_ceremony BEGIN SELECT RAISE(ABORT, 'consume denied'); END`); err != nil {
			t.Fatal(err)
		}
		_, err = s.FinishLogin(context.Background(), connect.NewRequest(&adminv1.FinishLoginRequest{CeremonyId: begin.Msg.CeremonyId}))
		if codeOf(err) != connect.CodeUnauthenticated {
			t.Fatalf("failed ceremony consume returned %v; want sign-in failure", err)
		}
		if countAuditResult(t, st, "login", "fail") != 1 || len(events) != 1 || events[0].Kind != EventSignInFailed {
			t.Fatalf("failed login was not audited and emitted: audit=%d events=%+v", countAuditResult(t, st, "login", "fail"), events)
		}
	})

	t.Run("step-up", func(t *testing.T) {
		s, st, clock := newTestService(t)
		admin, cookie, _ := passkeyAdmin(t, s, st)
		*clock = clock.Add(StepUpWindow + time.Second)
		begin, err := s.BeginStepUp(authed(t, s, cookie), cookieReq(&adminv1.BeginStepUpRequest{}, cookie))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := st.W.Exec(`CREATE TRIGGER reject_stepup_ceremony_consume BEFORE DELETE ON auth_ceremony BEGIN SELECT RAISE(ABORT, 'consume denied'); END`); err != nil {
			t.Fatal(err)
		}
		_, err = s.FinishStepUp(authed(t, s, cookie), cookieReq(&adminv1.FinishStepUpRequest{CeremonyId: begin.Msg.CeremonyId}, cookie))
		if codeOf(err) != connect.CodeUnauthenticated {
			t.Fatalf("failed ceremony consume returned %v; want step-up failure", err)
		}
		if countAuditResult(t, st, "stepup", "fail") != 1 {
			t.Fatal("failed step-up consume was not audited")
		}
		failure, err := st.LoginFailures(context.Background(), stepUpLockKey+admin.ID, *clock, FailureWindow)
		if err != nil || failure.Failures != 1 {
			t.Fatalf("step-up failures = %+v, err=%v; want one", failure, err)
		}
	})
}

func insertAdmin(t *testing.T, st *store.Store, role string) store.Admin {
	t.Helper()
	a := store.Admin{ID: store.NewID("adm_"), DisplayName: role, Role: role, UserHandle: []byte(store.NewID("h"))}
	if _, err := st.W.Exec(`INSERT INTO admin (id, display_name, role, user_handle, created_at) VALUES (?, ?, ?, ?, 1)`, a.ID, a.DisplayName, a.Role, a.UserHandle); err != nil {
		t.Fatal(err)
	}
	return a
}

// --- F11: roles ---

// Every procedure of the admin API must be given a level on purpose: a new RPC that nobody classified would
// otherwise be owner-only by default, and a stale entry would hide that an RPC was removed.
func TestEveryProcedureHasAPolicy(t *testing.T) {
	registered := map[string]bool{}
	protoregistry.GlobalFiles.RangeFilesByPackage("mistgate.admin.v1", func(fd protoreflect.FileDescriptor) bool {
		for i := 0; i < fd.Services().Len(); i++ {
			svc := fd.Services().Get(i)
			for j := 0; j < svc.Methods().Len(); j++ {
				registered["/"+string(svc.FullName())+"/"+string(svc.Methods().Get(j).Name())] = true
			}
		}
		return true
	})
	if len(registered) < 40 {
		t.Fatalf("found only %d procedures: the walk is broken", len(registered))
	}
	for path := range registered {
		_, listed := procedureLevels[path]
		if publicProcedures[path] {
			if listed {
				t.Errorf("%s is public and also has a role level", path)
			}
			continue
		}
		if !listed {
			t.Errorf("%s has no entry in procedureLevels (it would silently be owner-only)", path)
		}
	}
	for path := range procedureLevels {
		if !registered[path] {
			t.Errorf("procedureLevels names %s, which is not a registered procedure", path)
		}
	}
	for path := range publicProcedures {
		if !registered[path] {
			t.Errorf("publicProcedures names %s, which is not a registered procedure", path)
		}
	}
}

func TestRolePolicyIsEnforcedBySessionMiddleware(t *testing.T) {
	s, st, _ := newTestService(t)
	owner := makeAdmin(t, s, st)
	helper, readonly := insertAdmin(t, st, store.RoleHelper), insertAdmin(t, st, store.RoleReadonly)
	cookies := map[string]string{}
	for role, a := range map[string]store.Admin{store.RoleOwner: owner, store.RoleHelper: helper, store.RoleReadonly: readonly} {
		c, err := s.newSession(context.Background(), a.ID, "", "")
		if err != nil {
			t.Fatal(err)
		}
		cookies[role] = c.Value
	}

	reached := 0
	h := s.RequireSession(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached++; w.WriteHeader(http.StatusOK) }))
	call := func(role, path string) int {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
		r.Header.Set("Content-Type", "application/json")
		if c := cookies[role]; c != "" {
			r.AddCookie(&http.Cookie{Name: CookieName, Value: c})
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}

	// Spot checks that state the intent, independent of the table's layout.
	for _, tc := range []struct {
		path                  string
		owner, helper, readon int
	}{
		{adminv1connect.UserServiceListUsersProcedure, 200, 200, 200},
		{adminv1connect.NodeServiceGetNodeProcedure, 200, 200, 200},
		{adminv1connect.AuthServiceMeProcedure, 200, 200, 200},
		{adminv1connect.UserServiceCreateUserProcedure, 200, 200, 403},
		{adminv1connect.UserServiceDeleteUsersProcedure, 200, 200, 403},
		{adminv1connect.UserServiceGetSubscriptionLinkProcedure, 200, 200, 403},
		{adminv1connect.GroupServiceUpdateGroupProcedure, 200, 200, 403},
		{adminv1connect.NodeServiceRetireNodeProcedure, 200, 403, 403},
		{adminv1connect.NodeServiceCreateEnrollmentProcedure, 200, 403, 403},
		{adminv1connect.ProfileServiceUpdateProfileProcedure, 200, 403, 403},
		{adminv1connect.InstanceServiceUpdateInstanceProcedure, 200, 403, 403},
		{adminv1connect.AuthServiceListAuditProcedure, 200, 403, 403},
		{adminv1connect.AuthServiceUpdateSecuritySettingsProcedure, 200, 403, 403},
		{"/mistgate.admin.v1.NoSuchService/Anything", 200, 403, 403}, // not listed: owner only
	} {
		got := [3]int{call(store.RoleOwner, tc.path), call(store.RoleHelper, tc.path), call(store.RoleReadonly, tc.path)}
		if got != [3]int{tc.owner, tc.helper, tc.readon} {
			t.Errorf("%s: owner/helper/readonly = %v, want %v", tc.path, got, [3]int{tc.owner, tc.helper, tc.readon})
		}
	}

	// Across the whole table: the owner reaches everything, a readonly admin only what is read level.
	for path, l := range procedureLevels {
		if got := call(store.RoleOwner, path); got != 200 {
			t.Errorf("owner on %s: %d", path, got)
		}
		wantRO, wantHelper := 403, 403
		if l == levelRead {
			wantRO = 200
		}
		if l != levelOwner {
			wantHelper = 200
		}
		if got := call(store.RoleReadonly, path); got != wantRO {
			t.Errorf("readonly on %s: %d, want %d", path, got, wantRO)
		}
		if got := call(store.RoleHelper, path); got != wantHelper {
			t.Errorf("helper on %s: %d, want %d", path, got, wantHelper)
		}
	}

	// Public procedures need no session, every other path needs one.
	if got := call("", adminv1connect.AuthServiceGetLoginInfoProcedure); got != 200 {
		t.Errorf("public procedure without a session: %d", got)
	}
	if got := call("", adminv1connect.UserServiceListUsersProcedure); got != 401 {
		t.Errorf("no session: %d", got)
	}
	// A forbidden call never reaches the handler, and is a Connect error the client understands.
	before := reached
	r := httptest.NewRequest(http.MethodPost, adminv1connect.NodeServiceRetireNodeProcedure, strings.NewReader("{}"))
	r.AddCookie(&http.Cookie{Name: CookieName, Value: cookies[store.RoleHelper]})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if reached != before || !strings.Contains(w.Body.String(), `"permission_denied"`) || w.Header().Get("Content-Type") != "application/json" {
		t.Errorf("forbidden response: reached=%v body=%s", reached != before, w.Body.String())
	}
	// The handlers of owner-only calls check the role themselves as well (defence in depth).
	ctx := authed(t, s, cookies[store.RoleHelper])
	if _, err := s.GetSecuritySettings(ctx, connect.NewRequest(&adminv1.GetSecuritySettingsRequest{})); codeOf(err) != connect.CodePermissionDenied {
		t.Errorf("GetSecuritySettings as a helper: %v", err)
	}
	if _, err := s.ListAudit(ctx, connect.NewRequest(&adminv1.ListAuditRequest{})); codeOf(err) != connect.CodePermissionDenied {
		t.Errorf("ListAudit as a helper: %v", err)
	}
}

// --- F11: step-up ---

func stepUpRequired(t *testing.T, err error) *adminv1.StepUpRequired {
	t.Helper()
	var ce *connect.Error
	if !errors.As(err, &ce) || ce.Code() != connect.CodePermissionDenied {
		t.Fatalf("want PERMISSION_DENIED, got %v", err)
	}
	for _, d := range ce.Details() {
		if v, err := d.Value(); err == nil {
			if f, ok := v.(*adminv1.StepUpRequired); ok {
				return f
			}
		}
	}
	t.Fatalf("no StepUpRequired detail in %v", err)
	return nil
}

func TestStepUpGuardsAccountChangesAndPasswordAdminUsesTOTP(t *testing.T) {
	s, st, clock := newTestService(t)
	adminID, secret := passwordAdmin(t, s, st, "ada")
	*clock = clock.Add(31 * time.Second) // setup used the current authenticator step; a code works once
	pr, err := login(s, "ada", testPassword, totpCode(secret, totpStep(*clock)))
	if err != nil {
		t.Fatal(err)
	}
	cookie := sessionCookie(t, pr.Header())
	other, err := s.newSession(context.Background(), adminID, "198.51.100.9", "other browser")
	if err != nil {
		t.Fatal(err)
	}

	guarded := func(name string, call func(ctx context.Context) error) {
		t.Helper()
		ctx := authed(t, s, cookie)
		if err := call(ctx); codeOf(err) == connect.CodePermissionDenied {
			t.Errorf("%s right after signing in needed a step-up: %v", name, err)
		}
		*clock = clock.Add(StepUpWindow + time.Second)
		ctx = authed(t, s, cookie)
		need := stepUpRequired(t, call(ctx))
		if !need.Totp || need.Passkey {
			t.Errorf("%s: StepUpRequired = %+v, want totp only for a password admin", name, need)
		}
		*clock = clock.Add(-StepUpWindow - time.Second)
	}
	guarded("BeginAddPasskey", func(ctx context.Context) error {
		_, err := s.BeginAddPasskey(ctx, cookieReq(&adminv1.BeginAddPasskeyRequest{}, cookie))
		return err
	})
	guarded("RemovePasskey", func(ctx context.Context) error {
		_, err := s.RemovePasskey(ctx, cookieReq(&adminv1.RemovePasskeyRequest{Id: "pk_none"}, cookie))
		return err
	})
	guarded("EndOtherSessions", func(ctx context.Context) error {
		_, err := s.EndOtherSessions(ctx, cookieReq(&adminv1.EndOtherSessionsRequest{}, cookie))
		return err
	})
	other, err = s.newSession(context.Background(), adminID, "198.51.100.9", "other browser") // EndOtherSessions above ended the first one
	if err != nil {
		t.Fatal(err)
	}
	otherID := func() string {
		sess, _, _ := s.resolveSession(context.Background(), other.Value)
		return hexOf(sess.TokenHash)
	}()
	guarded("EndSession (another session)", func(ctx context.Context) error {
		_, err := s.EndSession(ctx, cookieReq(&adminv1.EndSessionRequest{Id: otherID}, cookie))
		if err == nil { // it ended for real once allowed: put it back for the next round
			other, _ = s.newSession(context.Background(), adminID, "198.51.100.9", "other browser")
			sess, _, _ := s.resolveSession(context.Background(), other.Value)
			otherID = hexOf(sess.TokenHash)
		}
		return err
	})

	// Stale: ending the CURRENT session (a logout) never needs a step-up.
	*clock = clock.Add(StepUpWindow + time.Second)
	cur, _, _ := s.resolveSession(context.Background(), cookie)
	if _, err := s.EndSession(authed(t, s, cookie), cookieReq(&adminv1.EndSessionRequest{Id: hexOf(cur.TokenHash)}, cookie)); err != nil {
		t.Errorf("ending the current session needed a step-up: %v", err)
	}
	// Sign in again (fresh session, stale clock again) for the TOTP round trip.
	*clock = clock.Add(31 * time.Second)
	pr, err = login(s, "ada", testPassword, totpCode(secret, totpStep(*clock)))
	if err != nil {
		t.Fatal(err)
	}
	cookie = sessionCookie(t, pr.Header())
	*clock = clock.Add(StepUpWindow + time.Second)
	ctx := authed(t, s, cookie)
	rm := func() error {
		_, err := s.RemovePasskey(ctx, cookieReq(&adminv1.RemovePasskeyRequest{Id: "pk_none"}, cookie))
		return err
	}
	stepUpRequired(t, rm())

	// Me says when the window ends.
	if me, _ := s.Me(ctx, connect.NewRequest(&adminv1.MeRequest{})); me.Msg.StepUpUntilUnix != 0 {
		t.Errorf("Me.step_up_until_unix = %d on a stale session", me.Msg.StepUpUntilUnix)
	}

	// The code that signed in (or an older one) cannot be replayed as a step-up: it must be newer.
	stale := totpCode(secret, totpStep(*clock)-2)
	if _, err := s.FinishStepUp(ctx, cookieReq(&adminv1.FinishStepUpRequest{TotpCode: stale}, cookie)); codeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("an old code as step-up: %v", err)
	}
	*clock = clock.Add(61 * time.Second) // two steps later: a code nobody has used
	ctx = authed(t, s, cookie)
	fresh := totpCode(secret, totpStep(*clock))
	resp, err := s.FinishStepUp(ctx, cookieReq(&adminv1.FinishStepUpRequest{TotpCode: fresh}, cookie))
	if err != nil {
		t.Fatalf("step-up with a fresh code: %v", err)
	}
	if want := clock.Add(StepUpWindow).Unix(); resp.Msg.StepUpUntilUnix != want {
		t.Errorf("step_up_until = %d, want %d", resp.Msg.StepUpUntilUnix, want)
	}
	ctx = authed(t, s, cookie) // the next request of the same session sees the proof
	if err := rm(); codeOf(err) == connect.CodePermissionDenied {
		t.Errorf("RemovePasskey after a step-up: %v", err)
	}
	if me, _ := s.Me(ctx, connect.NewRequest(&adminv1.MeRequest{})); me.Msg.StepUpUntilUnix != clock.Add(StepUpWindow).Unix() {
		t.Errorf("Me.step_up_until_unix = %d", me.Msg.StepUpUntilUnix)
	}
	// Single use: the same code again is refused.
	if _, err := s.FinishStepUp(ctx, cookieReq(&adminv1.FinishStepUpRequest{TotpCode: fresh}, cookie)); codeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("a used code: %v", err)
	}
	// The proof belongs to that session: another session of the admin is still unproven.
	*clock = clock.Add(StepUpWindow + time.Second)
	c2, _ := s.newSession(context.Background(), adminID, "", "")
	*clock = clock.Add(StepUpWindow + time.Second)
	stepUpRequired(t, errOf(s.RemovePasskey(authed(t, s, c2.Value), cookieReq(&adminv1.RemovePasskeyRequest{Id: "x"}, c2.Value))))

	// Five wrong codes lock step-up, even for the right code afterwards.
	for i := 0; i < MaxSignInFailures; i++ {
		s.FinishStepUp(authed(t, s, c2.Value), cookieReq(&adminv1.FinishStepUpRequest{TotpCode: "000000"}, c2.Value))
	}
	*clock = clock.Add(61 * time.Second)
	good := totpCode(secret, totpStep(*clock))
	if _, err := s.FinishStepUp(authed(t, s, c2.Value), cookieReq(&adminv1.FinishStepUpRequest{TotpCode: good}, c2.Value)); codeOf(err) != connect.CodeResourceExhausted {
		t.Errorf("step-up after five wrong codes: %v, want RESOURCE_EXHAUSTED", err)
	}
}

func hexOf(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&15])
	}
	return string(out)
}

func TestStepUpWithPasskey(t *testing.T) {
	s, st, clock := newTestService(t)
	admin, cookie, k := passkeyAdmin(t, s, st)
	_ = admin

	addPasskey := func() error {
		_, err := s.BeginAddPasskey(authed(t, s, cookie), cookieReq(&adminv1.BeginAddPasskeyRequest{}, cookie))
		return err
	}
	if err := addPasskey(); err != nil {
		t.Fatalf("adding a passkey right after setup: %v", err)
	}
	*clock = clock.Add(StepUpWindow + time.Minute)
	need := stepUpRequired(t, addPasskey())
	if !need.Passkey || need.Totp {
		t.Errorf("StepUpRequired = %+v, want passkey only", need)
	}

	begin, err := s.BeginStepUp(authed(t, s, cookie), cookieReq(&adminv1.BeginStepUpRequest{}, cookie))
	if err != nil || begin.Msg.OptionsJson == "" {
		t.Fatalf("BeginStepUp: %v", err)
	}
	opts, err := virtualwebauthn.ParseAssertionOptions(begin.Msg.OptionsJson)
	if err != nil || len(opts.AllowCredentials) != 1 {
		t.Fatalf("options %v, allowCredentials %v: the assertion must be limited to the admin's own passkeys", err, opts)
	}

	// Another session of the same admin cannot finish this ceremony, and a wrong assertion counts as a failure.
	c2, _ := s.newSession(context.Background(), admin.ID, "", "")
	cj := virtualwebauthn.CreateAssertionResponse(k.rp, k.dev, k.cred, *opts)
	if _, err := s.FinishStepUp(authed(t, s, c2.Value), cookieReq(&adminv1.FinishStepUpRequest{CeremonyId: begin.Msg.CeremonyId, CredentialJson: cj}, c2.Value)); codeOf(err) != connect.CodeUnauthenticated {
		t.Errorf("a ceremony finished from another session: %v", err)
	}
	// (that burned the ceremony) start again and do it properly
	begin, _ = s.BeginStepUp(authed(t, s, cookie), cookieReq(&adminv1.BeginStepUpRequest{}, cookie))
	opts, _ = virtualwebauthn.ParseAssertionOptions(begin.Msg.OptionsJson)
	cj = virtualwebauthn.CreateAssertionResponse(k.rp, k.dev, k.cred, *opts)
	resp, err := s.FinishStepUp(authed(t, s, cookie), cookieReq(&adminv1.FinishStepUpRequest{CeremonyId: begin.Msg.CeremonyId, CredentialJson: cj}, cookie))
	if err != nil {
		t.Fatalf("FinishStepUp: %v", err)
	}
	if resp.Msg.StepUpUntilUnix != clock.Add(StepUpWindow).Unix() {
		t.Errorf("step_up_until = %d", resp.Msg.StepUpUntilUnix)
	}
	if err := addPasskey(); err != nil {
		t.Errorf("BeginAddPasskey after the step-up: %v", err)
	}
	// A password-less, passkey-less call: FinishStepUp with a code for an admin who has no authenticator.
	if _, err := s.FinishStepUp(authed(t, s, cookie), cookieReq(&adminv1.FinishStepUpRequest{TotpCode: "123456"}, cookie)); codeOf(err) != connect.CodeFailedPrecondition {
		t.Errorf("a code for an admin without an authenticator app: %v", err)
	}
}

// --- F11: lockout per source ---

func TestPasswordLockoutPerSourceAddress(t *testing.T) {
	s, st, clock := newTestService(t)
	_, secret := passwordAdmin(t, s, st, "ada")
	*clock = clock.Add(31 * time.Second) // setup used the current authenticator step; a code works once
	s.trust = NewProxyTrust(mustProxies(t, "127.0.0.0/8"))

	mux := http.NewServeMux()
	p, h := s.Handler()
	mux.Handle(p, s.RequireSession(h))
	srv := httptest.NewServer(mux)
	defer srv.Close()
	cli := adminv1connect.NewAuthServiceClient(srv.Client(), srv.URL)
	attempt := func(from, login, pw, code string) error {
		r := connect.NewRequest(&adminv1.PasswordLoginRequest{Login: login, Password: pw, TotpCode: code})
		r.Header().Set("X-Forwarded-For", from)
		_, err := cli.PasswordLogin(context.Background(), r)
		return err
	}

	// Ten failures from one address against ten different logins: no login reaches its own limit of five.
	bad := "203.0.113.50"
	for i := 0; i < MaxSourceFailures; i++ {
		if err := attempt(bad, "nobody"+string(rune('a'+i)), "wrong password here", "000000"); codeOf(err) != connect.CodeUnauthenticated {
			t.Fatalf("attempt %d: %v", i, err)
		}
	}
	// The address is now locked, for the right credentials too, and the answer says so.
	err := attempt(bad, "ada", testPassword, totpCode(secret, totpStep(*clock)))
	f := failureOf(t, err)
	if f.LockedUntilUnix == 0 || f.AttemptsLeft != 0 {
		t.Errorf("locked address: %+v", f)
	}
	if n := countAudit(t, st, "lockout"); n == 0 {
		t.Error("the source lockout is not in the audit log")
	}
	// Other sources are not affected: a different IPv4, a neighbour address, another IPv6 /64.
	if err := attempt("203.0.113.51", "ada", testPassword, totpCode(secret, totpStep(*clock))); err != nil {
		t.Errorf("another IPv4 address: %v", err)
	}
	*clock = clock.Add(31 * time.Second)
	if err := attempt("2001:db8:aa:bb::1", "ada", testPassword, totpCode(secret, totpStep(*clock))); err != nil {
		t.Errorf("another IPv6 network: %v", err)
	}

	// An IPv6 /64 is one source: ten failures from ten addresses of the same /64 lock all of them.
	for i := 0; i < MaxSourceFailures; i++ {
		attempt(fmt.Sprintf("2001:db8:1:2:%x::1", i+1), fmt.Sprintf("ghost%d", i), "wrong password here", "000000")
	}
	*clock = clock.Add(31 * time.Second)
	if f := failureOf(t, attempt("2001:db8:1:2:ffff::7", "ada", testPassword, totpCode(secret, totpStep(*clock)))); f.LockedUntilUnix == 0 {
		t.Errorf("the /64 is not locked as one source: %+v", f)
	}

	// The lock ends (15 minutes), and the old failures age out after the window.
	*clock = clock.Add(LockDuration + time.Second)
	if err := attempt(bad, "ada", testPassword, totpCode(secret, totpStep(*clock))); err != nil {
		t.Errorf("after the lock time: %v", err)
	}
}

func mustProxies(t *testing.T, list ...string) []netip.Prefix {
	t.Helper()
	p, err := ParseProxies(list)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func countAudit(t *testing.T, st *store.Store, action string) int {
	t.Helper()
	var n int
	if err := st.R.QueryRow(`SELECT count(*) FROM audit WHERE action = ?`, action).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// --- F4: a browser header cannot break ListSessions ---

func TestSessionUserAgentIsClipped(t *testing.T) {
	s, st, _ := newTestService(t)
	a := makeAdmin(t, s, st)
	ua := "x" + strings.Repeat("я", 300) + string([]byte{0xff, 0xfe}) // the 256-byte cut lands inside a letter; plus invalid bytes
	c, err := s.newSession(context.Background(), a.ID, "", ua)
	if err != nil {
		t.Fatal(err)
	}
	ctx := authed(t, s, c.Value)
	resp, err := s.ListSessions(ctx, cookieReq(&adminv1.ListSessionsRequest{}, c.Value))
	if err != nil || len(resp.Msg.Sessions) != 1 {
		t.Fatalf("ListSessions: %v", err)
	}
	got := resp.Msg.Sessions[0].UserAgent
	if !utf8.ValidString(got) || len(got) > 256 {
		t.Errorf("user agent: valid=%v len=%d", utf8.ValidString(got), len(got))
	}
	if _, err := proto.Marshal(resp.Msg); err != nil {
		t.Errorf("ListSessions does not marshal: %v", err)
	}
}
