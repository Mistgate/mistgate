//go:build !js

package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// RenewCert's guard: the presenting certificate must be valid for the node (a certificate inside its grace still is),
// the node must not be retired, and the node gets at most RenewMax certificates per RenewWindow.
func TestRenewCertGuard(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	now := time.Unix(1_800_000_000, 0).UTC()
	if err := s.InsertCA(ctx, CARow{ID: "cas_test", CertPEM: "ca", Fingerprint: "fingerprint", KeyEnc: []byte{1},
		NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(1, 0, 0)}, now); err != nil {
		t.Fatal(err)
	}
	enrol := func(id, name string) {
		t.Helper()
		if _, err := s.CreateEnrollment(ctx, &NodeRow{ID: id, Name: name, Address: name + ".example.com"}, "", []byte("tok-"+id), "adm_test", now, now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Enroll(ctx, []byte("tok-"+id), []byte("key-"+id), now, func(nodeID string) (CertRow, error) {
			return certRow("e-"+id, nodeID, now), nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	renew := func(id, serial, from string, at time.Time) error {
		return s.RenewCert(ctx, certRow(serial, id, at), from, at, time.Hour)
	}

	t.Run("valid, grace and unknown", func(t *testing.T) {
		enrol("nod_a", "node-a")
		at := now.Add(time.Minute)
		if err := renew("nod_a", "r1", "e-nod_a", at); err != nil {
			t.Fatalf("renewal on the current certificate: %v", err)
		}
		// The old certificate is scheduled, not revoked: it may retry (a lost answer).
		if err := renew("nod_a", "r2", "e-nod_a", at.Add(time.Minute)); err != nil {
			t.Fatalf("retry on the certificate inside its grace: %v", err)
		}
		if err := renew("nod_a", "r3", "e-nod_a", at.Add(2*time.Hour)); !errors.Is(err, ErrRenewRefused) {
			t.Errorf("renewal on a certificate past its grace: %v, want ErrRenewRefused", err)
		}
		for _, from := range []string{"", "nope"} {
			if err := renew("nod_a", "rx", from, at); !errors.Is(err, ErrRenewRefused) {
				t.Errorf("renewal from serial %q: %v, want ErrRenewRefused", from, err)
			}
		}
		if err := renew("nod_a", "r4", "e-nod_b", at); !errors.Is(err, ErrRenewRefused) {
			t.Errorf("renewal from another node's certificate: %v", err)
		}
	})

	t.Run("re-enrolment revokes at once", func(t *testing.T) {
		enrol("nod_b", "node-b")
		at := now.Add(time.Minute)
		if _, err := s.CreateEnrollment(ctx, nil, "nod_b", []byte("tok-again"), "adm_test", at, at.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Enroll(ctx, []byte("tok-again"), []byte("key-again"), at, func(nodeID string) (CertRow, error) {
			return certRow("e2-nod_b", nodeID, at), nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := renew("nod_b", "late", "e-nod_b", at.Add(time.Second)); !errors.Is(err, ErrRenewRefused) {
			t.Errorf("renewal on a re-enrolled-away certificate: %v, want ErrRenewRefused", err)
		}
		// A renewal that read its clock before the re-enrolment committed (revoked_at is then after its now) still fails:
		// only a renewal's grace lets a revoked certificate renew.
		if err := renew("nod_b", "early-clock", "e-nod_b", at.Add(-time.Second)); !errors.Is(err, ErrRenewRefused) {
			t.Errorf("renewal with a clock read before the re-enrolment: %v, want ErrRenewRefused", err)
		}
		var serial string
		if err := s.R.QueryRowContext(ctx, `SELECT cert_serial FROM node WHERE id = 'nod_b'`).Scan(&serial); err != nil || serial != "e2-nod_b" {
			t.Errorf("node.cert_serial = %q, %v, want the re-enrolment certificate", serial, err)
		}
	})

	t.Run("retired node", func(t *testing.T) {
		enrol("nod_c", "node-c")
		if err := s.RetireNode(ctx, "nod_c", now.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		if err := renew("nod_c", "r", "e-nod_c", now.Add(2*time.Second)); !errors.Is(err, ErrRenewRefused) {
			t.Errorf("renewal of a retired node: %v, want ErrRenewRefused", err)
		}
	})

	t.Run("hourly quota", func(t *testing.T) {
		enrol("nod_d", "node-d")
		for i := 1; i < RenewMax; i++ { // the enrolment certificate is the first
			if err := renew("nod_d", "q"+string(rune('0'+i)), "e-nod_d", now.Add(time.Duration(i)*time.Second)); err != nil {
				t.Fatalf("renewal %d: %v", i, err)
			}
		}
		if err := renew("nod_d", "over", "e-nod_d", now.Add(10*time.Second)); !errors.Is(err, ErrRenewRefused) {
			t.Errorf("renewal over the quota: %v, want ErrRenewRefused", err)
		}
		var n int
		if err := s.R.QueryRowContext(ctx, `SELECT count(*) FROM node_cert WHERE node_id = 'nod_d'`).Scan(&n); err != nil || n != RenewMax {
			t.Errorf("certificates = %d, %v, want %d: a refused renewal writes nothing", n, err, RenewMax)
		}
		if err := renew("nod_d", "later", "q3", now.Add(RenewWindow+time.Minute)); err != nil {
			t.Errorf("renewal after the window: %v", err)
		}
	})
}

func certRow(serial, nodeID string, at time.Time) CertRow {
	return CertRow{Serial: serial, NodeID: nodeID, CAID: "cas_test", PEM: "pem-" + serial, NotBefore: at, NotAfter: at.Add(30 * 24 * time.Hour), IssuedAt: at}
}
