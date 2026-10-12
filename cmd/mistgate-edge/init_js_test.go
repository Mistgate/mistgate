//go:build js && wasm

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"syscall/js"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/app"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func openFakeD1(t *testing.T) *store.Store {
	t.Helper()
	d1 := js.Global().Get("__d1")
	d1.Call("__reset")
	st, err := store.OpenD1(context.Background(), d1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// Isolates that start on an empty database together each generate their own prefixes and agent SNI; whatever order
// their writes arrive in, they must all end up with the values of the first write.
func TestConcurrentFirstInitsStoreOneInstance(t *testing.T) {
	ctx := context.Background()
	st := openFakeD1(t)
	opts := initOptions{publicURL: "https://example.com", adminPath: "/test-admin/", hasNodeLink: true}

	const isolates = 4
	var loaded, settle sync.WaitGroup
	loaded.Add(isolates)
	settle.Add(1)
	var done sync.WaitGroup
	results := make([]app.InstanceConfig, isolates)
	generated := make([]map[string]string, isolates)
	errs := make([]error, isolates)
	for i := range isolates {
		done.Add(1)
		go func() {
			defer done.Done()
			in, pending, err := loadEdgeInstance(ctx, st, opts)
			generated[i] = pending
			loaded.Done()
			settle.Wait() // every isolate has generated its own settings before any of them writes
			if err == nil {
				in, err = settleEdgeInstance(ctx, st, in, pending)
			}
			results[i], errs[i] = in, err
		}()
	}
	loaded.Wait()
	settle.Done()
	done.Wait()

	distinct := map[string]bool{}
	for i := range isolates {
		if errs[i] != nil {
			t.Fatalf("isolate %d: %v", i, errs[i])
		}
		distinct[generated[i]["link_prefix"]+generated[i]["sub_prefix"]+generated[i]["agent_sni"]] = true
	}
	if len(distinct) != isolates {
		t.Fatalf("the isolates generated %d distinct sets of secrets, want %d (the test would prove nothing)", len(distinct), isolates)
	}
	first := results[0]
	if first.LinkPrefix == "" || first.AgentSNI == "" || first.SubPrefix == "" {
		t.Fatalf("settled instance has empty secrets: %+v", first)
	}
	for i, got := range results {
		if got.LinkPrefix != first.LinkPrefix || got.AgentSNI != first.AgentSNI || got.SubPrefix != first.SubPrefix ||
			got.AdminPrefix != first.AdminPrefix || got.RPID != first.RPID || got.PublicURL != first.PublicURL {
			t.Errorf("isolate %d settled %+v, isolate 0 %+v", i, got, first)
		}
	}
	stored, err := st.SettingValues(ctx, edgeInstanceKeys)
	if err != nil {
		t.Fatal(err)
	}
	if stored["link_prefix"] != first.LinkPrefix || stored["agent_sni"] != first.AgentSNI || stored["sub_prefix"] != first.SubPrefix {
		t.Errorf("stored settings %v differ from what the isolates built from: %+v", stored, first)
	}
	// a cold isolate that starts later reads the same values
	later, fill, err := loadEdgeInstance(ctx, st, opts)
	if err != nil || len(fill) != 0 || later.LinkPrefix != first.LinkPrefix || later.AgentSNI != first.AgentSNI {
		t.Fatalf("later isolate: %+v, fill %v, %v", later, fill, err)
	}
}

// An older database lacks link_prefix: two isolates fill it in at the same time and still agree.
func TestConcurrentFillOfMissingLinkPrefix(t *testing.T) {
	ctx := context.Background()
	st := openFakeD1(t)
	_, pending, err := newEdgeInstance(initOptions{publicURL: "https://example.com", adminPath: "/test-admin/"})
	if err != nil {
		t.Fatal(err)
	}
	delete(pending, "link_prefix")
	if err := st.SetSettings(ctx, pending); err != nil {
		t.Fatal(err)
	}
	var got [2]app.InstanceConfig
	var wg sync.WaitGroup
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			in, fill, err := loadEdgeInstance(ctx, st, initOptions{})
			if err == nil {
				in, err = settleEdgeInstance(ctx, st, in, fill)
			}
			if err != nil {
				t.Error(err)
			}
			got[i] = in
		}()
	}
	wg.Wait()
	if got[0].LinkPrefix == "" || got[0].LinkPrefix != got[1].LinkPrefix {
		t.Fatalf("link prefixes %q and %q", got[0].LinkPrefix, got[1].LinkPrefix)
	}
}

func captureConsoleLog(t *testing.T) *[]string {
	t.Helper()
	console := js.Global().Get("console")
	original := console.Get("log")
	var lines []string
	capture := js.FuncOf(func(_ js.Value, args []js.Value) any {
		parts := make([]string, len(args))
		for i, a := range args {
			parts[i] = a.String()
		}
		lines = append(lines, strings.Join(parts, " "))
		return nil
	})
	console.Set("log", capture)
	t.Cleanup(func() {
		console.Set("log", original)
		capture.Release()
	})
	return &lines
}

func TestParseSetupTokenHash(t *testing.T) {
	good := sha256.Sum256([]byte("token"))
	if got, err := parseSetupTokenHash(hex.EncodeToString(good[:])); err != nil || string(got) != string(good[:]) {
		t.Fatalf("valid hash: %x, %v", got, err)
	}
	for _, bad := range []string{"abc", strings.Repeat("a", 63), strings.Repeat("a", 65), strings.Repeat("g", 64), strings.ToUpper(hex.EncodeToString(good[:]))} {
		if _, err := parseSetupTokenHash(bad); !errors.Is(err, errInvalidSetupHash) {
			t.Errorf("parseSetupTokenHash(%q) = %v, want errInvalidSetupHash", bad, err)
		}
	}
}

// With a hash from the deployer the panel offers it as the setup token and logs its expiry only; an expired token is
// not revived by a later start.
func TestOfferSetupWithHashLogsOnlyTheExpiry(t *testing.T) {
	ctx := context.Background()
	st := openFakeD1(t)
	const token = "tok-9f8e7d6c5b4a39281706f5e4d3c2b1a0"
	sum := sha256.Sum256([]byte(token))
	hashHex := hex.EncodeToString(sum[:])
	opts := initOptions{setupTokenHash: sum[:]}
	in := app.InstanceConfig{RPOrigins: []string{"https://example.com"}, AdminPrefix: "/test-admin/"}
	lines := captureConsoleLog(t)

	now := time.Now()
	if err := offerSetup(ctx, st, opts, in, now); err != nil {
		t.Fatal(err)
	}
	if len(*lines) != 1 || !strings.Contains((*lines)[0], "accepted until "+now.Add(auth.SetupTokenTTL).UTC().Format(time.RFC3339)) {
		t.Fatalf("log after the first offer: %q", *lines)
	}
	if err := st.CheckSetupToken(ctx, sum[:], now); err != nil {
		t.Fatalf("the offered token must work: %v", err)
	}
	// another isolate starts later: same expiry, still one row
	if err := offerSetup(ctx, st, opts, in, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains((*lines)[1], "accepted until "+now.Add(auth.SetupTokenTTL).UTC().Format(time.RFC3339)) {
		t.Fatalf("the second offer changed the expiry: %q", (*lines)[1])
	}
	// after the expiry
	later := now.Add(auth.SetupTokenTTL + time.Minute)
	if err := offerSetup(ctx, st, opts, in, later); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains((*lines)[2], "expired") {
		t.Fatalf("log after the expiry: %q", (*lines)[2])
	}
	if err := st.CheckSetupToken(ctx, sum[:], later); !errors.Is(err, store.ErrSetupClosed) {
		t.Fatalf("an expired token was revived: %v", err)
	}
	for _, line := range *lines {
		if strings.Contains(line, token) || strings.Contains(line, hashHex) || strings.Contains(line, "/setup#") || strings.Contains(line, "test-admin") {
			t.Errorf("the log leaks the token, the hash or the admin address: %q", line)
		}
	}
}
