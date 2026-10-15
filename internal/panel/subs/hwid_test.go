package subs_test

import (
	"strings"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/subs"
)

const (
	hwidA = "0f8fad5b-d9cb-469f-a165-70867728950e"
	hwidB = "7c9e6679-7425-40de-944b-e07fc1f90ae7"
)

// Credentials belong to a device, so the cached view of a token must too: the second HWID that fetches within MinInterval
// gets its own credentials, not the first one's.
func TestTwoHWIDsWithinMinIntervalGetDifferentCredentials(t *testing.T) {
	r := newRig(t, "/k3xq8")
	h, _ := r.handler(func(c *subs.Config) { c.MinInterval = 10 * time.Minute; c.MaxPerHour = -1 })
	uid, tok := r.user("alice", nil)

	a := fetch(h, "/"+tok, happUA, "X-HWID", hwidA, "X-Device-OS", "iOS")
	b := fetch(h, "/"+tok, happUA, "X-HWID", hwidB, "X-Device-OS", "Android")
	none := fetch(h, "/"+tok, happUA)
	if a.Code != 200 || b.Code != 200 || none.Code != 200 {
		t.Fatalf("status %d %d %d", a.Code, b.Code, none.Code)
	}
	la, lb, ln := decode(t, a.Body.String()), decode(t, b.Body.String()), decode(t, none.Body.String())
	if !strings.HasPrefix(la, "hysteria2://") || la == lb || la == ln || lb == ln {
		t.Fatalf("the views of three devices within MinInterval:\n%s\n%s\n%s", la, lb, ln)
	}
	// Each repeats from its own cache, and the same HWID spelled otherwise is the same device.
	for name, rec := range map[string]string{
		"a":         decode(t, fetch(h, "/"+tok, happUA, "X-HWID", hwidA).Body.String()),
		"a, upper":  decode(t, fetch(h, "/"+tok, happUA, "X-HWID", strings.ToUpper(hwidA)).Body.String()),
		"a, plain":  decode(t, fetch(h, "/"+tok, happUA, "X-HWID", strings.ReplaceAll(hwidA, "-", "")).Body.String()),
		"a, spaces": decode(t, fetch(h, "/"+tok, happUA, "X-HWID", "  "+hwidA+" ").Body.String()),
	} {
		if rec != la {
			t.Errorf("%s: another view than the first fetch of the device:\n%s\n%s", name, rec, la)
		}
	}
	if rec := decode(t, fetch(h, "/"+tok, happUA, "X-HWID", hwidB).Body.String()); rec != lb {
		t.Errorf("b from the cache: %s, want %s", rec, lb)
	}
	var devices int
	if err := r.st.R.QueryRow(`SELECT count(*) FROM device WHERE user_id = ?`, uid).Scan(&devices); err != nil || devices != 3 {
		t.Fatalf("devices = %d, %v; want three", devices, err)
	}
	// The page, opened in between, is not an app and does not shadow them.
	if rec := fetch(h, "/"+tok, chrome, "X-HWID", hwidA); rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("browser: %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	if rec := decode(t, fetch(h, "/"+tok, happUA, "X-HWID", hwidA).Body.String()); rec != la {
		t.Errorf("an app after the page: %s", rec)
	}
}

// The headers an app sends are the facts of its device.
func TestHWIDHeadersBecomeTheFactsOfTheDevice(t *testing.T) {
	r := newRig(t, "/k3xq8")
	h, _ := r.handler(nil)
	uid, tok := r.user("alice", nil)
	fetch(h, "/"+tok, "Happ/3.4.2 (iOS 18.1)", "X-HWID", hwidA, "X-Device-OS", "iOS", "X-Ver-OS", "18.1", "X-Device-Model", "iPhone 15")
	var platform, model, osVersion, client string
	var hashed bool
	if err := r.st.R.QueryRow(`SELECT platform, model, os_version, client, hwid_hash IS NOT NULL AND length(hwid_hash) = 32 FROM device WHERE user_id = ?`, uid).
		Scan(&platform, &model, &osVersion, &client, &hashed); err != nil {
		t.Fatal(err)
	}
	if platform != "ios" || model != "iPhone 15" || osVersion != "18.1" || client != "Happ/3.4.2" || !hashed {
		t.Fatalf("device facts = %q %q %q %q hashed %v", platform, model, osVersion, client, hashed)
	}
	// The raw HWID is nowhere in the database.
	rows, err := r.st.R.Query(`SELECT hwid_hash FROM device WHERE user_id = ?`, uid)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var hash []byte
		rows.Scan(&hash)
		if strings.Contains(string(hash), strings.ReplaceAll(hwidA, "-", "")) || strings.Contains(string(hash), hwidA) {
			t.Fatal("the HWID is stored as it came")
		}
	}
}

// A HWID the standard does not allow is no HWID: the app is served from the shared device, and no device is made for the value.
func TestInvalidHWIDHeadersAreTheSharedDevice(t *testing.T) {
	r := newRig(t, "/k3xq8")
	h, _ := r.handler(func(c *subs.Config) { c.MinInterval = -1; c.MaxPerHour = -1 })
	uid, tok := r.user("alice", nil)
	plain := decode(t, fetch(h, "/"+tok, happUA).Body.String())
	for _, bad := range []string{"123456789", strings.Repeat("a", 65), "has a space inside", "semi;colon-in-it-0000", "кириллица-в-hwid-00"} {
		if got := decode(t, fetch(h, "/"+tok, happUA, "X-HWID", bad).Body.String()); got != plain {
			t.Errorf("X-HWID %q was taken for a device", bad)
		}
	}
	var devices, shared int
	count := func() {
		t.Helper()
		if err := r.st.R.QueryRow(`SELECT count(*), coalesce(sum(hwid_hash IS NULL), 0) FROM device WHERE user_id = ?`, uid).Scan(&devices, &shared); err != nil {
			t.Fatal(err)
		}
	}
	if count(); devices != 1 || shared != 1 {
		t.Fatalf("after the invalid ones: %d devices, %d shared; want the shared one alone", devices, shared)
	}
	// Lengths 10 and 64 are the edges that do count: the first takes the shared device over, the second is a device of its own.
	fetch(h, "/"+tok, happUA, "X-HWID", "abcdefghij")
	if count(); devices != 1 || shared != 0 {
		t.Fatalf("a HWID of 10 characters: %d devices, %d shared; want the shared device taken over", devices, shared)
	}
	fetch(h, "/"+tok, happUA, "X-HWID", strings.Repeat("b", 64))
	if count(); devices != 2 || shared != 0 {
		t.Fatalf("a HWID of 64 characters: %d devices, %d shared; want a second device", devices, shared)
	}
}

// A browser reporting a HWID (an extension, a test) is not an app: the page makes no device.
func TestBrowserHWIDMakesNoDevice(t *testing.T) {
	r := newRig(t, "/k3xq8")
	h, _ := r.handler(nil)
	uid, tok := r.user("alice", nil)
	fetch(h, "/"+tok, chrome, "X-HWID", hwidA)
	var devices int
	if err := r.st.R.QueryRow(`SELECT count(*) FROM device WHERE user_id = ?`, uid).Scan(&devices); err != nil || devices != 0 {
		t.Fatalf("devices = %d, %v; want none", devices, err)
	}
}

// A link pasted into Telegram or WhatsApp is opened by the messenger's server for a preview: it gets the decoy, not the
// subscription, and takes no device (it used to show as an app without a device ID, "TelegramBot").
func TestLinkPreviewBotGetsTheDecoyAndMakesNoDevice(t *testing.T) {
	r := newRig(t, "/k3xq8")
	h, _ := r.handler(nil)
	uid, tok := r.user("alice", nil)
	for _, ua := range []string{"TelegramBot (like TwitterBot)", "WhatsApp/2.24.6.77 A", "facebookexternalhit/1.1", "Mozilla/5.0 (compatible; Discordbot/2.0; +https://discordapp.com)"} {
		if rec := fetch(h, "/"+tok, ua); rec.Code != 404 || rec.Body.String() != decoyBody {
			t.Errorf("%s: %d %q", ua, rec.Code, rec.Body.String())
		}
	}
	var devices int
	if err := r.st.R.QueryRow(`SELECT count(*) FROM device WHERE user_id = ?`, uid).Scan(&devices); err != nil || devices != 0 {
		t.Fatalf("devices = %d, %v; want none", devices, err)
	}
	// the control: an app with no device ID makes the shared device
	fetch(h, "/"+tok, "Happ/3.2.1")
	if err := r.st.R.QueryRow(`SELECT count(*) FROM device WHERE user_id = ?`, uid).Scan(&devices); err != nil || devices != 1 {
		t.Fatalf("after an app: devices = %d, %v; want one", devices, err)
	}
}
