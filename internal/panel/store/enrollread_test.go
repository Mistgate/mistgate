//go:build !js

package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Enroll is open to anyone who can reach the agent endpoint, so a failing attempt must not queue on the single
// writer connection (stats ingestion and every admin write share it). Here the writer is occupied: a guess, an
// expired token and a token whose replay window is over must still be answered at once.
func TestEnrollTurnsGuessesAwayWithoutTheWriter(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	now := time.Now().UTC()
	issue := func(string) (CertRow, error) { t.Error("a certificate was issued"); return CertRow{}, nil }

	mk := func(id, name, tok string, expires time.Time) {
		t.Helper()
		n := &NodeRow{ID: id, Name: name, Address: name + ".example.com"}
		if _, err := s.CreateEnrollment(ctx, n, "", []byte(tok), "adm_test", now, expires); err != nil {
			t.Fatal(err)
		}
	}
	mk("nod_exp", "expired", "tok-expired", now.Add(-time.Minute))
	mk("nod_old", "oldused", "tok-used", now.Add(time.Hour))
	if _, err := s.W.ExecContext(ctx, `UPDATE enrollment_token SET used_at = ? WHERE token_hash = ?`, now.Add(-time.Hour).Unix(), []byte("tok-used")); err != nil {
		t.Fatal(err)
	}

	w, err := s.W.Conn(ctx) // the only writer connection, held by "somebody else"
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	for name, tok := range map[string]string{"unknown": "tok-unknown", "expired": "tok-expired", "used long ago": "tok-used"} {
		tctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		start := time.Now()
		_, err := s.Enroll(tctx, []byte(tok), []byte("key"), now, issue)
		cancel()
		if !errors.Is(err, ErrEnrollToken) {
			t.Errorf("%s: err = %v, want ErrEnrollToken", name, err)
		}
		if d := time.Since(start); d > time.Second {
			t.Errorf("%s: took %v, it waited for the writer connection", name, d)
		}
	}
}
