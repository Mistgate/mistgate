package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// States of AwgPrepareRow.State.
const (
	AwgPrepareRunning = "running"
	AwgPrepareDone    = "done"
	AwgPrepareFailed  = "failed"
)

// AwgPrepareRow is what the panel knows about the build of the AmneziaWG kernel module on a node (migration 00019).
// The zero value is "nothing was ever asked".
type AwgPrepareRow struct {
	State string `json:"state,omitempty"` // "" | running | done | failed
	Since int64  `json:"since,omitempty"` // running: when it began; done / failed: when it ended
	// Want: the admin chose the kernel module for this node, so the panel sets awg_backend to "kernel" when the build is
	// done. Cleared by any later choice of a backend.
	Want   bool   `json:"want,omitempty"`
	Code   string `json:"code,omitempty"`   // failed: the node's code (timeout, apt_lock, step_failed, ...)
	Reason string `json:"reason,omitempty"` // failed: one short English sentence
}

func parseAwgPrepare(s string) AwgPrepareRow {
	var r AwgPrepareRow
	if s != "" {
		_ = json.Unmarshal([]byte(s), &r)
	}
	return r
}

func (r AwgPrepareRow) json() string {
	if r == (AwgPrepareRow{}) {
		return ""
	}
	b, _ := json.Marshal(r)
	return string(b)
}

// AwgPrepare is the node's preparation state.
func (n NodeRow) AwgPrepare() AwgPrepareRow { return parseAwgPrepare(n.AwgPrepareJSON) }

// SetAwgPrepare replaces the state (the zero value clears it).
func (s *Store) SetAwgPrepare(ctx context.Context, nodeID string, r AwgPrepareRow) error {
	res, err := s.W.ExecContext(ctx, `UPDATE node SET awg_prepare_json = ? WHERE id = ?`, r.json(), nodeID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// AwgPrepareStarted records that a build is running (the node said so, or the admin asked and the node accepted). A build
// that is already recorded as running keeps its start time.
func (s *Store) AwgPrepareStarted(ctx context.Context, nodeID string, since int64) error {
	return s.awgPrepareTx(ctx, nodeID, func(cur AwgPrepareRow, _ string) (AwgPrepareRow, string) {
		if cur.State == AwgPrepareRunning {
			return cur, ""
		}
		return AwgPrepareRow{State: AwgPrepareRunning, Since: since, Want: cur.Want}, ""
	})
}

// AwgPrepareFinish records the end of a build. When it succeeded and the admin still wants the module (Want) the node's
// awg_backend becomes "kernel" in the same transaction (switched = true): this is the only place the panel switches a node
// to the kernel module by itself, and only after the node said the module is built, loaded and verified. A failure
// changes nothing but the record.
func (s *Store) AwgPrepareFinish(ctx context.Context, nodeID string, ok bool, at int64, code, reason string) (switched bool, err error) {
	err = s.awgPrepareTx(ctx, nodeID, func(cur AwgPrepareRow, backend string) (AwgPrepareRow, string) {
		next := AwgPrepareRow{State: AwgPrepareDone, Since: at}
		if !ok {
			next = AwgPrepareRow{State: AwgPrepareFailed, Since: at, Code: code, Reason: reason}
			return next, ""
		}
		if cur.Want && backend != "kernel" {
			switched = true
			return next, "kernel"
		}
		return next, ""
	})
	return switched, err
}

// awgPrepareTx reads the node's state and backend, lets fn decide the next state and (when non-empty) a new awg_backend,
// and writes both in one transaction.
func (s *Store) awgPrepareTx(ctx context.Context, nodeID string, fn func(cur AwgPrepareRow, backend string) (AwgPrepareRow, string)) error {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw, backend string
	if err := tx.QueryRowContext(ctx, `SELECT awg_prepare_json, awg_backend FROM node WHERE id = ?`, nodeID).Scan(&raw, &backend); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	next, setBackend := fn(parseAwgPrepare(raw), backend)
	if _, err := tx.ExecContext(ctx, `UPDATE node SET awg_prepare_json = ? WHERE id = ?`, next.json(), nodeID); err != nil {
		return err
	}
	if setBackend != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE node SET awg_backend = ? WHERE id = ?`, setBackend, nodeID); err != nil {
			return err
		}
	}
	return tx.Commit()
}
