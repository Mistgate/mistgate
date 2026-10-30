package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// The IPv6 entry of a node (migrations 00086): nodes.ipv6_entry is the address people's "· IPv6" server connects to, and
// nodes.ipv6_json the document around it. The pair is only ever written together, with a compare-and-swap on both columns
// (UpdateIPv6): a database without transactions across statements (D1) serialises two writers the same way SQLite does.

// ipv6KeepChecks is how many addresses the document remembers a check of: the entry and the few the node lists.
const ipv6KeepChecks = 4

// IPv6State is the document in nodes.ipv6_json. Later steps of the feature add their fields to it (a pending address and its
// state, the address being retired and until when), so reading an older document needs no migration; a field that is absent
// reads as its zero value.
type IPv6State struct {
	// Checks are the latest outside checks, one per address, newest first.
	Checks []IPv6Check `json:"checks,omitempty"`
}

// IPv6 is the node's IPv6 document ({} reads as nothing known).
func (n NodeRow) IPv6() IPv6State { return ParseIPv6State(n.IPv6JSON) }

// ParseIPv6State reads a document; an unreadable one is empty (it is the panel's own, written by IPv6State.JSON).
func ParseIPv6State(s string) IPv6State {
	var st IPv6State
	if s != "" {
		_ = json.Unmarshal([]byte(s), &st)
	}
	return st
}

// JSON is the document as it is stored: "{}" for an empty one, the same bytes for the same content.
func (s IPv6State) JSON() string {
	b, err := json.Marshal(s)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// CheckOf is the latest check of the address.
func (s IPv6State) CheckOf(addr string) (IPv6Check, bool) {
	for _, c := range s.Checks {
		if c.Addr == addr {
			return c, true
		}
	}
	return IPv6Check{}, false
}

// Latest is the newest check of any address.
func (s IPv6State) Latest() (IPv6Check, bool) {
	if len(s.Checks) == 0 {
		return IPv6Check{}, false
	}
	return s.Checks[0], true
}

// With is the document with the check as its newest; an older check of the same address is replaced, and only the newest few
// addresses are kept.
func (s IPv6State) With(c IPv6Check) IPv6State {
	checks := make([]IPv6Check, 0, len(s.Checks)+1)
	checks = append(checks, c)
	for _, old := range s.Checks {
		if old.Addr != c.Addr {
			checks = append(checks, old)
		}
	}
	if len(checks) > ipv6KeepChecks {
		checks = checks[:ipv6KeepChecks]
	}
	s.Checks = checks
	return s
}

// IPv6Row is the pair of columns UpdateIPv6 reads and writes.
type IPv6Row struct {
	Entry string // nodes.ipv6_entry
	State IPv6State
}

// UpdateIPv6 changes a node's IPv6 entry and document atomically: fn gets the current pair and returns the next one (or an error,
// which is returned as it is and writes nothing). The write is a compare-and-swap on both columns; when another writer got in
// between, fn runs again on what it left, so a decision is never made on a stale read. changed is false when fn returned the
// pair it was given. ErrNotFound for a missing or retired node, ErrConflict if it keeps losing.
func (s *Store) UpdateIPv6(ctx context.Context, nodeID string, fn func(cur IPv6Row) (IPv6Row, error)) (next IPv6Row, changed bool, err error) {
	for range 8 {
		var entry, raw, state string
		if err := s.R.QueryRowContext(ctx, `SELECT ipv6_entry, ipv6_json, state FROM node WHERE id = ?`, nodeID).Scan(&entry, &raw, &state); errors.Is(err, sql.ErrNoRows) {
			return IPv6Row{}, false, ErrNotFound
		} else if err != nil {
			return IPv6Row{}, false, err
		}
		if state == "retired" {
			return IPv6Row{}, false, ErrNotFound
		}
		cur := IPv6Row{Entry: entry, State: ParseIPv6State(raw)}
		next, err = fn(cur)
		if err != nil {
			return cur, false, err
		}
		if next.Entry == cur.Entry && next.State.JSON() == cur.State.JSON() {
			return cur, false, nil
		}
		results, err := s.batch(ctx, Stmt{Query: `UPDATE node SET ipv6_entry = ?, ipv6_json = ?
			WHERE id = ? AND ipv6_entry = ? AND ipv6_json = ? AND state <> 'retired'`,
			Args: []any{next.Entry, next.State.JSON(), nodeID, entry, raw}})
		if err != nil {
			return cur, false, err
		}
		if results[0].RowsAffected == 1 {
			return next, true, nil
		}
	}
	return IPv6Row{}, false, ErrConflict
}

// RecordIPv6Check keeps a check as the newest of its address. It never changes the entry.
func (s *Store) RecordIPv6Check(ctx context.Context, nodeID string, c IPv6Check) error {
	_, _, err := s.UpdateIPv6(ctx, nodeID, func(cur IPv6Row) (IPv6Row, error) {
		cur.State = cur.State.With(c)
		return cur, nil
	})
	return err
}
