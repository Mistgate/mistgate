package fleet

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/store"
)

type sqliteCodeError struct {
	code int
	text string
}

func (e sqliteCodeError) Error() string { return e.text }
func (e sqliteCodeError) Code() int     { return e.code }

func TestStatsPoisonDropRequiresPermanentErrorOrElapsedWindow(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	transient := errors.New("database is locked")
	firstFailure := now.Add(-4*time.Minute - 59*time.Second)
	if shouldDropStatsBatch(transient, firstFailure, now) {
		t.Fatal("a transient failure inside the retry window was marked for dropping")
	}
	if !shouldDropStatsBatch(transient, now.Add(-statsPoisonRetryWindow), now) {
		t.Fatal("a transient failure older than the retry window was not marked for dropping")
	}
	for _, err := range []error{
		sqliteCodeError{code: 2067, text: "constraint"},
		sqliteCodeError{code: 20, text: "datatype mismatch"},
		sqliteCodeError{code: 25, text: "range"},
		sqliteCodeError{code: 18, text: "too big"},
		store.ErrNotFound,
	} {
		if !shouldDropStatsBatch(err, now, now) {
			t.Errorf("permanent error %q was not marked for dropping", err)
		}
	}
	misuse := sqliteCodeError{code: 21, text: "misuse"}
	if permanentStatsIngestError(misuse) || shouldDropStatsBatch(misuse, now, now) {
		t.Fatal("SQLite MISUSE was treated as a permanent error of the node's batch")
	}
	for _, err := range []error{errors.New("transport decode invalid"), errors.New("invalid constraint description from peer")} {
		if permanentStatsIngestError(err) {
			t.Errorf("text-only transport error %q was marked permanent", err)
		}
	}
	wrapped := fmt.Errorf("write stats: %w", sqliteCodeError{code: 2067, text: "constraint"})
	if !permanentStatsIngestError(wrapped) {
		t.Fatal("wrapped SQLite extended constraint code was not recognized")
	}
}
