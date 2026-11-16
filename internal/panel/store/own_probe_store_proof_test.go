package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func parseTargetsForTest(raw string) []any {
	var job struct {
		Targets []any `json:"targets"`
	}
	_ = json.Unmarshal([]byte(raw), &job)
	return job.Targets
}

func ownProbeStoreProof(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	token := "mgp_test_token_de1"
	secret := "mgps_test_secret_de1"
	tokenHash, secretHash := sha256.Sum256([]byte(token)), sha256.Sum256([]byte(secret))
	created := OwnProbe{ID: "prb_store_de1", Name: "probe-de1", City: "de1", Kind: "home", TokenHash: tokenHash[:],
		TokenExpires: now.Add(24 * time.Hour), CreatedAt: now, CreatedBy: "adm_test"}
	if err := st.CreateOwnProbe(ctx, created); err != nil {
		t.Fatal(err)
	}
	var storedToken []byte
	if err := st.R.QueryRowContext(ctx, `SELECT token_hash FROM own_probe WHERE id = ?`, created.ID).Scan(&storedToken); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(storedToken, tokenHash[:]) || bytes.Equal(storedToken, []byte(token)) || len(storedToken) != sha256.Size {
		t.Fatalf("enrollment token storage is not a SHA-256 digest: %x", storedToken)
	}
	if got, err := st.EnrollOwnProbe(ctx, tokenHash[:], secretHash[:], 64512, "ExampleNet", "ExampleNet", "abcdef012345", now.Add(time.Minute)); err != nil ||
		got.State != "active" || !bytes.Equal(got.SecretHash, secretHash[:]) || got.TokenHash != nil {
		t.Fatalf("EnrollOwnProbe = %+v, %v", got, err)
	}
	if _, err := st.EnrollOwnProbe(ctx, tokenHash[:], secretHash[:], 64512, "ExampleNet", "ExampleNet", "abcdef012345", now.Add(2*time.Minute)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second enrollment = %v, want ErrNotFound", err)
	}
	var storedSecret []byte
	if err := st.R.QueryRowContext(ctx, `SELECT secret_hash FROM own_probe WHERE id = ?`, created.ID).Scan(&storedSecret); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(storedSecret, secretHash[:]) || bytes.Equal(storedSecret, []byte(secret)) || len(storedSecret) != sha256.Size {
		t.Fatalf("probe secret storage is not a SHA-256 digest: %x", storedSecret)
	}
	if got, err := st.ObserveOwnProbeNetwork(ctx, created.ID, secretHash[:], 64513, "OtherNet", "", now.Add(2*time.Minute)); err != nil || got.State != "network_changed" {
		t.Fatalf("network change = %+v, %v", got, err)
	}
	if err := st.ConfirmOwnProbeNetwork(ctx, created.ID, "OtherNet"); err != nil {
		t.Fatal(err)
	}
	if got, err := st.OwnProbe(ctx, created.ID); err != nil || got.State != "active" || got.PinnedOperator != "OtherNet" {
		t.Fatalf("confirmed network = %+v, %v", got, err)
	}
	if err := st.SetOwnProbeJob(ctx, created.ID, "0123456789abcdef", `{"targets":[]}`, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := st.CompleteOwnProbeJob(ctx, created.ID, "0123456789abcdef", "abcdef012345", "open", now.Add(3*time.Minute), time.Hour, 2); err != nil {
		t.Fatal(err)
	}
	if err := st.CompleteOwnProbeJob(ctx, created.ID, "0123456789abcdef", "abcdef012345", "open", now.Add(4*time.Minute), time.Hour, 2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("job replay = %v, want ErrNotFound", err)
	}
	if got, err := st.OwnProbe(ctx, created.ID); err != nil || got.ChecksDay != 2 || got.JobID != "" || OwnProbeRef(got) != "open" {
		t.Fatalf("completed job row = %+v, %v", got, err)
	}
	// the reference result outlives the things that drop a job (check now, the switch off, the next job being issued) and is
	// replaced by the next round
	if err := st.DueOwnProbe(ctx, created.ID, now.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.OwnProbe(ctx, created.ID); OwnProbeRef(got) != "open" {
		t.Fatalf("check now dropped the reference result: %q", got.JobJSON)
	}
	if err := st.ClearOwnProbeJobs(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.OwnProbe(ctx, created.ID); OwnProbeRef(got) != "open" {
		t.Fatalf("clearing the jobs dropped the reference result: %q", got.JobJSON)
	}
	if err := st.SetOwnProbeJob(ctx, created.ID, "fedcba9876543210", `{"targets":[{"node_id":"nod_1"}]}`, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.OwnProbe(ctx, created.ID); OwnProbeRef(got) != "open" || len(parseTargetsForTest(got.JobJSON)) != 1 {
		t.Fatalf("a new job lost the reference result or its targets: %q", got.JobJSON)
	}
	if err := st.CompleteOwnProbeJob(ctx, created.ID, "fedcba9876543210", "abcdef012345", "blocked", now.Add(6*time.Minute), time.Hour, 1); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.OwnProbe(ctx, created.ID); OwnProbeRef(got) != "blocked" || got.JobJSON != `{"ref":"blocked"}` {
		t.Fatalf("the round after did not replace it: %q", got.JobJSON)
	}
	if err := st.DueOwnProbe(ctx, created.ID, now.Add(7*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := st.RevokeOwnProbe(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.OwnProbeBySecretHash(ctx, secretHash[:]); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked secret lookup = %v, want ErrNotFound", err)
	}
	if got, err := st.OwnProbe(ctx, created.ID); err != nil || got.State != "revoked" || OwnProbeRef(got) != "" || got.SecretHash != nil || got.JobID != "" {
		t.Fatalf("revoked probe = %+v, %v", got, err)
	}

	expiredToken := "mgp_test_expired_fi1"
	expiredHash := sha256.Sum256([]byte(expiredToken))
	expired := OwnProbe{ID: "prb_store_fi1", Name: "probe-fi1", City: "fi1", Kind: "mobile", TokenHash: expiredHash[:],
		TokenExpires: now.Add(-time.Second), CreatedAt: now, CreatedBy: "adm_test"}
	if err := st.CreateOwnProbe(ctx, expired); err != nil {
		t.Fatal(err)
	}
	if _, err := st.EnrollOwnProbe(ctx, expiredHash[:], secretHash[:], 64512, "ExampleNet", "ExampleNet", "abcdef012345", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired enrollment = %v, want ErrNotFound", err)
	}
}
