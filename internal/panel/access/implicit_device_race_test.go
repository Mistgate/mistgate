package access

import (
	"testing"

	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/plugin"
)

type revokeDeviceOnIssue struct {
	protocols.Protocol
	revoke func() error
}

func (p *revokeDeviceOnIssue) IssueCredential(in protocols.IssueInput) (protocols.Issued, error) {
	if err := p.revoke(); err != nil {
		return protocols.Issued{}, err
	}
	return p.Protocol.IssueCredential(in)
}

func (p *revokeDeviceOnIssue) PerDevice() bool { return protocols.IsPerDevice(p.Protocol) }

func (e *env) revokeDeviceOnNextIssue(t *testing.T, id string) {
	t.Helper()
	plugins := e.s.reg.List()
	for i, p := range plugins {
		if p.ID() == "hysteria2" {
			plugins[i] = &revokeDeviceOnIssue{Protocol: p, revoke: func() error {
				_, err := e.st.Access().RevokeDevice(e.ctx, id, e.clock)
				return err
			}}
			break
		}
	}
	registry, err := protocols.NewRegistry(plugins...)
	if err != nil {
		t.Fatal(err)
	}
	e.s.reg = registry
}

func TestSubscriptionRecreatesImplicitDeviceRevokedAfterRead(t *testing.T) {
	f := newFixture(t)
	e := f.e
	u := e.user("alice", f.group, amnOnly()).User
	e.sql(`UPDATE user SET app_happ = 1 WHERE id = ?`, u.Id)
	const revokedDeviceID = "dev_subscription_race_old"
	e.sql(`INSERT INTO device (id, user_id, platform, model, os_version, first_seen_at, last_seen_at, created_at) VALUES (?, ?, '', '', '', 0, 0, ?)`, revokedDeviceID, u.Id, e.clock.Unix())
	e.revokeDeviceOnNextIssue(t, revokedDeviceID)

	view, err := e.s.SubscriptionWith(e.ctx, e.tokenOf(u.Id), SubOptions{Format: plugin.FormatURIList})
	if err != nil {
		t.Fatalf("subscription fetch after the implicit device was revoked: %v", err)
	}
	if len(view.Lines) != 1 || view.Servers[0].Protocol != "hysteria2" {
		t.Fatalf("subscription lines = %+v, want the newly issued hysteria2 access", view.Lines)
	}
	var liveDeviceID string
	if err := e.st.R.QueryRowContext(e.ctx, `SELECT id FROM device WHERE user_id = ? AND hwid_hash IS NULL AND revoked_at IS NULL`, u.Id).Scan(&liveDeviceID); err != nil {
		t.Fatal(err)
	}
	if liveDeviceID == revokedDeviceID {
		t.Fatalf("new live implicit device reused revoked id %q", liveDeviceID)
	}
	if n := e.count(`SELECT count(*) FROM device_credential WHERE device_id = ?`, revokedDeviceID); n != 0 {
		t.Fatalf("revoked device has %d credentials", n)
	}
}

func TestEnsureCredsDoesNotAddCredentialToDeviceRevokedAfterRead(t *testing.T) {
	f := newFixture(t)
	e := f.e
	u := e.user("alice", f.group, amnOnly()).User
	e.sql(`UPDATE user SET app_happ = 1 WHERE id = ?`, u.Id)
	const revokedDeviceID = "dev_ensure_creds_race_old"
	e.sql(`INSERT INTO device (id, user_id, platform, model, os_version, first_seen_at, last_seen_at, created_at) VALUES (?, ?, '', '', '', 0, 0, ?)`, revokedDeviceID, u.Id, e.clock.Unix())
	e.revokeDeviceOnNextIssue(t, revokedDeviceID)
	stored := must(e.st.Access().User(e.ctx, u.Id))

	created, err := e.s.ensureCreds(e.ctx, stored, true)
	if err != nil || !created {
		t.Fatalf("ensureCreds after the implicit device was revoked: created=%v err=%v", created, err)
	}
	var credentialDeviceID string
	if err := e.st.R.QueryRowContext(e.ctx, `SELECT device_id FROM device_credential WHERE user_id = ? AND protocol = 'hysteria2' AND revoked_at IS NULL`, stored.ID).Scan(&credentialDeviceID); err != nil {
		t.Fatal(err)
	}
	if credentialDeviceID == revokedDeviceID {
		t.Fatalf("ensureCreds added a live credential to revoked device %q", revokedDeviceID)
	}
	var revoked int
	if err := e.st.R.QueryRowContext(e.ctx, `SELECT revoked_at IS NOT NULL FROM device WHERE id = ?`, revokedDeviceID).Scan(&revoked); err != nil {
		t.Fatal(err)
	}
	if revoked != 1 {
		t.Fatalf("device %q was not revoked during credential issuance", revokedDeviceID)
	}
}
