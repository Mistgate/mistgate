package provision

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

func TestInstallPageEscapesUntrustedValues(t *testing.T) {
	var out strings.Builder
	data := installPageData{
		Step:        "fingerprint",
		Form:        installForm{Host: `"><svg onload=alert(1)>`, Name: `"><script>alert(1)</script>`},
		Fingerprint: `"><img src=x onerror=alert(1)>`,
	}
	if err := installPageTemplates[pageRU].Execute(&out, data); err != nil {
		t.Fatal(err)
	}
	page := out.String()
	if strings.Contains(page, "<svg onload") || strings.Contains(page, "<script>alert") || strings.Contains(page, "<img src=x") {
		t.Fatalf("untrusted page value was rendered as markup: %s", page)
	}
	if !strings.Contains(page, "&lt;svg") || !strings.Contains(page, "&lt;script&gt;") {
		t.Fatalf("expected escaped values in the confirmation form: %s", page)
	}
}

func TestInstallWizardOffersSudoLoginAndPasswordRotationWithoutReveal(t *testing.T) {
	var out strings.Builder
	data := installPageData{Step: "fingerprint", Form: installForm{Username: "deploy"}, Fingerprint: "SHA256:pin"}
	if err := installPageTemplates[pageRU].Execute(&out, data); err != nil {
		t.Fatal(err)
	}
	page := out.String()
	for _, want := range []string{`name="username"`, `value="deploy"`, "sudo -n", "Пароль SSH"} {
		if !strings.Contains(page, want) {
			t.Errorf("install page lacks %q", want)
		}
	}
	if strings.Contains(strings.ToLower(page), "reveal password") {
		t.Fatal("install page must not reveal a saved SSH password")
	}
	out.Reset()
	data = installPageData{Step: "host", Access: []serverAccessView{{ID: "nod_example", Name: "edge-1", Host: "node.example.com:22", Username: "deploy"}}}
	if err := installPageTemplates[pageRU].Execute(&out, data); err != nil {
		t.Fatal(err)
	}
	accessPage := out.String()
	if !strings.Contains(accessPage, `name="new_password"`) || !strings.Contains(accessPage, `name="confirm_rotation"`) || strings.Contains(accessPage, "saved-password-value") {
		t.Fatalf("access page does not offer a confirmed password rotation safely: %s", accessPage)
	}
}

func TestFormatBytes(t *testing.T) {
	for _, tc := range []struct {
		value  uint64
		ru, en string
	}{
		{0, "0 Б", "0 B"},
		{1024, "1.0 КБ", "1.0 KB"},
		{1024 * 1024, "1.0 МБ", "1.0 MB"},
		{1024 * 1024 * 1024, "1.0 ГБ", "1.0 GB"},
	} {
		if got := pageRU.bytes(tc.value); got != tc.ru {
			t.Errorf("ru bytes(%d) = %q, want %q", tc.value, got, tc.ru)
		}
		if got := pageEN.bytes(tc.value); got != tc.en {
			t.Errorf("en bytes(%d) = %q, want %q", tc.value, got, tc.en)
		}
	}
}

// The page speaks the admin's language: what the SPA passed, else the browser's first choice.
func TestInstallPageLanguage(t *testing.T) {
	for _, tc := range []struct {
		query, accept string
		want          pageLang
	}{
		{"", "ru-RU,ru;q=0.9,en;q=0.8", pageRU},
		{"", "en-US,en;q=0.9,ru;q=0.5", pageEN},
		{"", "de-DE", pageEN},
		{"?lang=en", "ru-RU", pageEN},
		{"?lang=ru", "en-US", pageRU},
		{"?lang=xx", "ru", pageRU},
	} {
		r := httptest.NewRequest(http.MethodGet, AdminPagePath+tc.query, nil)
		r.Header.Set("Accept-Language", tc.accept)
		if got := pageLanguage(r); got != tc.want {
			t.Errorf("%q %q: %s, want %s", tc.query, tc.accept, got, tc.want)
		}
	}
}

// An English page has no Russian word left: every step, every state, phase, event and error, and the links keep the language.
func TestInstallPageInEnglish(t *testing.T) {
	cyrillic := func(s string) bool {
		return strings.ContainsFunc(s, func(r rune) bool { return r >= 0x400 && r <= 0x4ff })
	}
	jobs := []jobView{}
	for _, st := range []string{"queued", "running", "cancel_requested", "cancelled", "completed", "failed", "odd"} {
		jobs = append(jobs, makeJobView(&adminv1.NodeProvisionJob{Id: "prv_" + st, Name: "edge-1", SshHost: "203.0.113.5", SshPort: 22, State: st}))
	}
	var events []eventView
	for _, phase := range []string{"queued", "preflight", "firewall", "transfer", "enrollment", "install", "waiting_node", "completed", "cancelling", "cancelled", "failed", "odd"} {
		events = append(events, eventView{Label: eventLabel(phase, "")}, eventView{Label: eventLabel(phase, "some_new_code")})
	}
	for _, code := range []string{"queued", "retry_requested", "cancel_requested", "cancelled_before_start", "remote_outcome_unknown", "checking_host",
		"preparing_host_firewall", "uploading_agent", "enrolling_node", "starting_agent", "waiting_for_agent", "agent_connected"} {
		events = append(events, eventView{Label: eventLabel("install", code)})
	}
	var errs []string
	for _, code := range []string{"remote_outcome_unknown", "ssh_target_invalid", "ssh_host_key_changed", "ssh_authentication_failed", "ssh_connection_timeout",
		"unsupported_os", "unsupported_architecture", "systemd_required", "panel_unreachable", "insufficient_resources", "insufficient_disk_space",
		"node_not_connected", "ssh_connection_failed", "ssh_connection_refused", "ssh_preflight_failed", "node_identity_mismatch", "node_state_unavailable",
		"node_retired", "host_firewall_configuration_failed", "agent_bundle_unavailable", "agent_transfer_failed", "node_enrollment_failed", "node_name_taken",
		"systemd_install_failed", "a_code_from_tomorrow"} {
		errs = append(errs, errorLabel(code))
	}
	for _, code := range []connect.Code{connect.CodePermissionDenied, connect.CodeUnauthenticated, connect.CodeAlreadyExists, connect.CodeDeadlineExceeded,
		connect.CodeFailedPrecondition, connect.CodeInvalidArgument} {
		errs = append(errs, userError(connect.NewError(code, errors.New("x")), ""))
	}
	job := jobs[5] // failed: the retry form
	pages := []installPageData{
		{Step: "host", Jobs: jobs, Access: []serverAccessView{{ID: "nod_1", Name: "edge-1", Host: "203.0.113.5:22", Username: "deploy"}, {ID: "nod_2", Name: "edge-2", Retired: true, Pending: true}}},
		{Step: "fingerprint", Fingerprint: "SHA256:pin", Message: "Сначала получите и подтвердите отпечаток SSH host key."},
		{Step: "preflight", Fingerprint: "SHA256:pin", Preflight: &preflightView{Memory: 2 << 30, Disk: 20 << 30, Systemd: true}},
		{Step: "preflight", Preflight: &preflightView{}},
		{Step: "job", Job: &job, Events: events, Refresh: true},
		{Step: "job", Job: &jobs[1]},
		{Step: "job", Job: &jobs[2]},
		{Step: "job", Job: &jobs[4]},
	}
	for _, e := range errs {
		pages = append(pages, installPageData{Step: "host", Message: e})
	}
	for i, page := range pages {
		page.Lang = pageEN
		var out strings.Builder
		if err := installPageTemplates[pageEN].Execute(&out, page); err != nil {
			t.Fatal(err)
		}
		html := out.String()
		if cyrillic(html) {
			for _, line := range strings.Split(html, "\n") {
				if cyrillic(line) {
					t.Errorf("page %d (%s) keeps Russian: %s", i, page.Step, strings.TrimSpace(line))
				}
			}
		}
		if !strings.Contains(html, `lang="en"`) {
			t.Errorf("page %d is not marked English", i)
		}
	}
	var out strings.Builder
	if err := installPageTemplates[pageEN].Execute(&out, installPageData{Lang: pageEN, Step: "host", Jobs: jobs[:1]}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `href="?lang=en&job=prv_queued"`) {
		t.Errorf("the job link drops the language: %s", out.String())
	}
}

// root or a sudo user: the page never says "root password" where the login is someone else's.
func TestInstallPageWordsFitSudoLogins(t *testing.T) {
	for _, l := range []pageLang{pageRU, pageEN} {
		failed := makeJobView(&adminv1.NodeProvisionJob{Id: "prv_1", State: "failed", ErrorCode: "ssh_authentication_failed"})
		var out strings.Builder
		if err := installPageTemplates[l].Execute(&out, installPageData{Lang: l, Step: "job", Job: &failed}); err != nil {
			t.Fatal(err)
		}
		page := strings.ToLower(out.String())
		for _, bad := range []string{"пароль root", "root password", "password of root"} {
			if strings.Contains(page, bad) {
				t.Errorf("%s: the page says %q", l, bad)
			}
		}
		if !strings.Contains(page, "sudo") {
			t.Errorf("%s: the retry does not say a sudo login works", l)
		}
	}
}

func TestFormTargetRequiresValidPort(t *testing.T) {
	for _, tc := range []struct {
		form installForm
		want uint32
		ok   bool
	}{
		{installForm{Host: "node.example.com"}, 22, true},
		{installForm{Host: "node.example.com", Port: "2222"}, 2222, true},
		{installForm{Host: "node.example.com", Port: "0"}, 0, false},
		{installForm{Host: "node.example.com", Port: "65536"}, 0, false},
		{installForm{Host: "node.example.com", Port: "ssh"}, 0, false},
		{installForm{Port: "22"}, 0, false},
	} {
		_, port, ok := formTarget(tc.form)
		if port != tc.want || ok != tc.ok {
			t.Errorf("formTarget(%+v) = port %d, ok %v; want port %d, ok %v", tc.form, port, ok, tc.want, tc.ok)
		}
	}
}

func TestProvisionProgressLabelsAreStable(t *testing.T) {
	if got := stateLabel("running"); got != "Установка" {
		t.Fatalf("running label = %q", got)
	}
	if got := phaseLabel("waiting_node"); got != "Ожидание подключения" {
		t.Fatalf("waiting phase label = %q", got)
	}
	if got := phaseLabel("firewall"); got != "Настройка firewall сервера" {
		t.Fatalf("firewall phase label = %q", got)
	}
	if got := eventLabel("firewall", "preparing_host_firewall"); got != "Настройка активного firewall сервера" {
		t.Fatalf("firewall event label = %q", got)
	}
	if got := errorLabel("host_firewall_configuration_failed"); !strings.Contains(got, "хостера") {
		t.Fatalf("host firewall error label = %q", got)
	}
	if got := eventLabel("install", "starting_agent"); got != "Настройка и запуск systemd-службы" {
		t.Fatalf("install event label = %q", got)
	}
	if got := errorLabel("ssh_host_key_changed"); !strings.Contains(got, "Ключ SSH изменился") {
		t.Fatalf("host-key error label = %q", got)
	}
}

func TestFingerprintTimeoutExplainsPanelNetworkPath(t *testing.T) {
	err := connect.NewError(connect.CodeDeadlineExceeded, errors.New("ssh_fingerprint_timeout"))
	got := userError(err, "fallback")
	for _, want := range []string{"Панель не получила ответ", "firewall", "сервера панели"} {
		if !strings.Contains(got, want) {
			t.Errorf("timeout message %q does not contain %q", got, want)
		}
	}
}

func TestPageHandlerRejectsMissingAdminContext(t *testing.T) {
	response := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, AdminPagePath, nil)
	(&Service{}).PageHandler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("page without admin context returned %d, want 401", response.Code)
	}
}

func TestPostFormIgnoresQueryParametersAndEnforcesLimit(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, AdminPagePath+"?action=start&confirm_install=yes", strings.NewReader("action=fingerprint"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := parseInstallForm(httptest.NewRecorder(), request); err != nil {
		t.Fatal(err)
	}
	if request.PostForm.Get("action") != "fingerprint" || request.PostForm.Get("confirm_install") != "" {
		t.Fatalf("POST form mixed with query parameters: %v", request.PostForm)
	}

	large := httptest.NewRequest(http.MethodPost, AdminPagePath, strings.NewReader(strings.Repeat("x", pageBodyLimit+1)))
	large.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := parseInstallForm(httptest.NewRecorder(), large); err == nil {
		t.Fatal("oversized request form was accepted")
	}
}

func TestPageLinksAndRedirectKeepAdminPrefix(t *testing.T) {
	for _, tc := range []struct {
		path  string
		home  string
		nodes string
	}{
		{"/secret/nodes/install", "../", "../nodes"},
		{"/secret/nodes/install/", "../../", "../../nodes"},
	} {
		home, nodes := pageLinks(tc.path)
		if home != tc.home || nodes != tc.nodes {
			t.Errorf("pageLinks(%q) = %q, %q; want %q, %q", tc.path, home, nodes, tc.home, tc.nodes)
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/secret/nodes/install", nil)
	response := httptest.NewRecorder()
	redirectToJob(response, "prv_example", pageEN)
	location, err := url.Parse(response.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	resolved := request.URL.ResolveReference(location)
	if resolved.Path != request.URL.Path || resolved.Query().Get("job") != "prv_example" || resolved.Query().Get("lang") != "en" || response.Code != http.StatusSeeOther {
		t.Fatalf("job redirect = %d %q, want same page with job query", response.Code, resolved)
	}
}
