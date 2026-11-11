package access

import (
	"crypto/sha256"
	"slices"
	"strings"
	"testing"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

// A basic fetch reads the person's state once and writes after it. When a client enrols the same HWID in between, the fetch must
// find that out at its write and refuse, never hand the new device's credentials to whoever sent the HWID with the link.

type enrolRace struct {
	e      *env
	user   string
	token  string
	hint   DeviceHint
	enrol  func() // enrols the HWID of hint, once
	keyed  [32]byte
	enrols int
}

// newEnrolRace is a person of the Happ and Amnezia apps on a group with a Hysteria2 and an AmneziaWG profile; the hook enrols
// the HWID of hint right after the next subscription view has read its snapshot.
func newEnrolRace(t *testing.T) *enrolRace {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	hy2 := e.profile("hy2 443", "")
	e.inbound(hy2.Id, "nod_de1")
	awgProfile := e.awgProfile("awg31", "")
	e.inbound(awgProfile.Id, "nod_de1")
	g := e.group("default", hy2.Id, awgProfile.Id)
	u := e.userBare("alice", g, nil).User
	r := &enrolRace{e: e, user: u.Id, token: e.tokenOf(u.Id), hint: phone(), keyed: sha256.Sum256([]byte("a key of the client"))}
	r.enrol = func() {
		r.enrols++
		if _, err := e.s.EnrolLinkDevice(e.ctx, r.user, r.token, r.hint, r.keyed[:]); err != nil {
			t.Errorf("enrol in the window: %v", err)
		}
	}
	return r
}

// armed makes the next view's snapshot hook enrol the device (once).
func (r *enrolRace) armed() {
	done := false
	r.e.s.SetSnapshotHookForTest(func() {
		if !done {
			done = true
			r.enrol()
		}
	})
}

func (r *enrolRace) enrolledCreds() []string {
	r.e.t.Helper()
	rows, err := r.e.st.R.Query(`SELECT c.id FROM device_credential c JOIN device d ON d.id = c.device_id WHERE d.user_id = ? AND d.key_hash IS NOT NULL AND c.revoked_at IS NULL ORDER BY c.id`, r.user)
	if err != nil {
		r.e.t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			r.e.t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

var enrolFormats = []plugin.ClientFormat{plugin.FormatURIList, plugin.FormatMihomo, plugin.FormatXrayJSON, plugin.FormatSingbox}

// The snapshot has no device for the HWID; the enrolment makes one before the fetch writes.
func TestBasicFetchRacedByAnEnrolmentOfANewHWIDIsRefused(t *testing.T) {
	for _, format := range enrolFormats {
		t.Run(string(format)+"|", func(t *testing.T) {
			r := newEnrolRace(t)
			e := r.e
			r.armed()
			v := fetchAs(e, r.token, r.hint, format)
			if r.enrols != 1 {
				t.Fatalf("the enrolment ran %d times", r.enrols)
			}
			if v.Refusal != RefusalClientAPIEnrolled || len(v.Lines) != 0 || len(v.Servers) != 0 {
				t.Fatalf("refusal %q, %d lines, %d servers; want %q and nothing to connect with", v.Refusal, len(v.Lines), len(v.Servers), RefusalClientAPIEnrolled)
			}
			creds := r.enrolledCreds()
			if len(creds) == 0 {
				t.Fatal("the enrolled device has no credentials")
			}
			if n := e.count(`SELECT count(*) FROM awg_peer p JOIN device_credential c ON c.id = p.credential_id JOIN device d ON d.id = c.device_id WHERE d.key_hash IS NOT NULL`); n != 0 {
				t.Fatalf("%d AWG peers issued to the enrolled device", n)
			}
			if n := e.count(`SELECT count(*) FROM device WHERE user_id = ? AND revoked_at IS NULL`, r.user); n != 1 {
				t.Fatalf("%d live devices, want the enrolled one only", n)
			}
			// Later fetches, with no race, are refused the same way and write nothing.
			e.s.SetSnapshotHookForTest(nil)
			for _, f := range enrolFormats {
				if v := fetchAs(e, r.token, r.hint, f); v.Refusal != RefusalClientAPIEnrolled || len(v.Lines) != 0 {
					t.Errorf("%s after the enrolment: refusal %q, %d lines", f, v.Refusal, len(v.Lines))
				}
			}
			if got := r.enrolledCreds(); !slices.Equal(got, creds) {
				t.Fatalf("the credentials of the enrolled device changed: %v -> %v", creds, got)
			}
		})
	}
}

// The snapshot holds the HWID's keyless device (with its AmneziaWG peer); the enrolment replaces it before the fetch writes. The
// fetch may still show what the snapshot held (dead credentials: they were revoked by the enrolment) but never the new ones, and
// never a peer for the enrolled device.
func TestBasicFetchRacedByAnEnrolmentOfAKeylessDeviceIsRefused(t *testing.T) {
	for _, format := range enrolFormats {
		t.Run(string(format)+"|", func(t *testing.T) {
			r := newEnrolRace(t)
			e := r.e
			before := fetchAs(e, r.token, r.hint, format)
			if len(before.Lines) == 0 {
				t.Fatal("the keyless device is served nothing")
			}
			r.armed()
			v := fetchAs(e, r.token, r.hint, format)
			if r.enrols != 1 {
				t.Fatalf("the enrolment ran %d times", r.enrols)
			}
			if v.Refusal != RefusalClientAPIEnrolled && !slices.Equal(v.Lines, before.Lines) {
				t.Fatalf("refusal %q and lines %q; want the refusal, or at most what the snapshot held (%q)", v.Refusal, v.Lines, before.Lines)
			}
			for _, line := range v.Lines {
				for _, stale := range before.Lines {
					if line != stale && strings.Contains(line, "://") {
						t.Fatalf("a line that the snapshot did not hold: %q", line)
					}
				}
			}
			if n := e.count(`SELECT count(*) FROM awg_peer p JOIN device_credential c ON c.id = p.credential_id JOIN device d ON d.id = c.device_id WHERE d.key_hash IS NOT NULL AND c.revoked_at IS NULL`); n != 0 {
				t.Fatalf("%d live AWG peers on the enrolled device", n)
			}
			// The next fetch has the new snapshot: refused in every format.
			e.s.SetSnapshotHookForTest(nil)
			for _, f := range enrolFormats {
				if v := fetchAs(e, r.token, r.hint, f); v.Refusal != RefusalClientAPIEnrolled || len(v.Lines) != 0 {
					t.Errorf("%s after the enrolment: refusal %q, %d lines", f, v.Refusal, len(v.Lines))
				}
			}
		})
	}
}

// A person whose access ended is told so, and the fetch of an enrolled device's HWID does not touch the device or its peers.
func TestBasicFetchOfAnEnrolledHWIDOfAnInactivePersonWritesNothing(t *testing.T) {
	r := newEnrolRace(t)
	e := r.e
	r.enrol()
	e.sql(`UPDATE user SET disabled = 1 WHERE id = ?`, r.user)
	e.sql(`UPDATE device SET last_seen_at = 1 WHERE user_id = ?`, r.user)
	e.resetNotify()
	for _, format := range enrolFormats {
		v := fetchAs(e, r.token, r.hint, format)
		if v.Status != StatusDisabled || len(v.Lines) != 0 {
			t.Fatalf("%s: status %q, %d lines", format, v.Status, len(v.Lines))
		}
	}
	e.s.afterResponse = func(work func()) { work() }
	fetchAs(e, r.token, r.hint, plugin.FormatMihomo)
	if n := e.count(`SELECT count(*) FROM device WHERE user_id = ? AND last_seen_at = 1`, r.user); n != 1 {
		t.Fatal("the fetch touched the enrolled device")
	}
	if e.notify.n.Load() != 0 {
		t.Fatal("the fetch told the nodes about a change")
	}
}

// An enrolment that takes a person past the limit in TRACK is the same sighting for the owner as a basic fetch that does.
func TestEnrolPastTheLimitInTrackIsASighting(t *testing.T) {
	f := newFixture(t)
	e := f.e
	e.syncAfterResponse()
	e.setLinkMode(store.LinkModeTrack)
	u := e.userBare("alice", f.group, func(m *adminv1.CreateUserRequest) { m.DeviceLimit = 1 }).User
	token := e.tokenOf(u.Id)
	if v := fetchAs(e, token, phone(), ""); v.Refusal != "" {
		t.Fatalf("the first device: refusal %q", v.Refusal)
	}
	if n := e.events("device_limit_reached"); n != 0 {
		t.Fatalf("%d events at the limit", n)
	}
	key := sha256.Sum256([]byte("a key"))
	res, err := e.s.EnrolLinkDevice(e.ctx, u.Id, token, tablet(), key[:])
	if err != nil || !res.Born {
		t.Fatalf("enrol past the limit in TRACK = %+v, %v", res, err)
	}
	if n := e.events("device_limit_reached"); n != 1 {
		t.Fatalf("%d device_limit_reached events after the enrolment, want 1", n)
	}
}
