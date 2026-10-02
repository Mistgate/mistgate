package provision

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestInstallPageEscapesUntrustedValues(t *testing.T) {
	var out strings.Builder
	data := installPageData{
		Step:        "fingerprint",
		Form:        installForm{Host: `"><svg onload=alert(1)>`, Name: `"><script>alert(1)</script>`},
		Fingerprint: `"><img src=x onerror=alert(1)>`,
	}
	if err := installPageTemplate.Execute(&out, data); err != nil {
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

func TestFormatBytes(t *testing.T) {
	for _, tc := range []struct {
		value uint64
		want  string
	}{
		{0, "0 Б"},
		{1024, "1.0 КБ"},
		{1024 * 1024, "1.0 МБ"},
		{1024 * 1024 * 1024, "1.0 ГБ"},
	} {
		if got := formatBytes(tc.value); got != tc.want {
			t.Errorf("formatBytes(%d) = %q, want %q", tc.value, got, tc.want)
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
	if got := eventLabel("install", "starting_agent"); got != "Настройка и запуск systemd-службы" {
		t.Fatalf("install event label = %q", got)
	}
	if got := errorLabel("ssh_host_key_changed"); !strings.Contains(got, "Ключ SSH изменился") {
		t.Fatalf("host-key error label = %q", got)
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
	redirectToJob(response, "prv_example")
	location, err := url.Parse(response.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	resolved := request.URL.ResolveReference(location)
	if resolved.Path != request.URL.Path || resolved.Query().Get("job") != "prv_example" || response.Code != http.StatusSeeOther {
		t.Fatalf("job redirect = %d %q, want same page with job query", response.Code, resolved)
	}
}
