package nftmissing

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestRestKeepsOtherFailures(t *testing.T) {
	sysctl := errors.New("sysctl net.core.rmem_max: read-only")
	missing := fmt.Errorf("ssh guard: %w", fmt.Errorf("%w: exec: \"nft\": executable file not found in $PATH", ErrMissing))
	joined := errors.Join(sysctl, missing)
	if !Is(joined) {
		t.Fatal("a joined error does not report the missing nft")
	}
	rest := Rest(joined)
	if rest == nil || !strings.Contains(rest.Error(), "read-only") || Is(rest) {
		t.Fatalf("rest = %v", rest)
	}
	if Rest(missing) != nil || Rest(errors.Join(missing)) != nil {
		t.Fatal("nothing else failed, nothing is left")
	}
	if Rest(nil) != nil || Rest(sysctl) != sysctl {
		t.Fatal("an unrelated error must come back as it was")
	}
	if got := missing.Error(); !strings.Contains(got, "nft is not installed") {
		t.Fatalf("the text the logs are searched for changed: %q", got)
	}
}

func TestWatchSaysTwiceNotEveryCycle(t *testing.T) {
	var w Watch
	if w.Gone() {
		t.Fatal("nothing had failed")
	}
	if !w.Appeared() || w.Appeared() || w.Appeared() {
		t.Fatal("only the first failure of a run is news")
	}
	if !w.Gone() || w.Gone() {
		t.Fatal("recovery is news once")
	}
	if !w.Appeared() {
		t.Fatal("a new run of failures is news again")
	}
}
