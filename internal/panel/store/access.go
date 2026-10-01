package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// This file and access_*.go hold the queries of the access module (profiles, inbounds, groups, users,
// devices, credentials). They sit behind Store.Access() and use an "Access" prefix on types and errors so
// they cannot collide with the other modules' files in this package.

var (
	// ErrAccessExists is returned when a unique name or key is already taken.
	ErrAccessExists = errors.New("store: already exists")
	// ErrAccessVersion is returned when an optimistic-concurrency version does not match.
	ErrAccessVersion = errors.New("store: version mismatch")
	// ErrAccessInUse is returned when a row cannot be deleted because others still depend on it.
	ErrAccessInUse = errors.New("store: in use")
)

// Access is the query set of the access module.
type Access struct{ s *Store }

// Access returns the access module's queries.
func (s *Store) Access() Access { return Access{s} }

func accIsUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func accIsFK(err error) bool {
	return err != nil && strings.Contains(err.Error(), "FOREIGN KEY constraint failed")
}

func accBool(b bool) int {
	if b {
		return 1
	}
	return 0
}

// accTime converts a nullable Unix-seconds column; NULL and 0 are the zero time.
func accTime(v sql.NullInt64) time.Time {
	if !v.Valid || v.Int64 == 0 {
		return time.Time{}
	}
	return fromUnix(v.Int64)
}

// accNullStr stores "" as NULL.
func accNullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func accNullUnix(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return unix(t)
}

// accTx runs fn in a write transaction.
func (a Access) tx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := a.s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// AccessNode is the part of a node row the access module reads (the fleet module owns the table).
type AccessNode struct {
	ID, Name, Address, CountryCode, Location, Provider string
	State                                              string // pending | active | retired
}

const accNodeCols = `n.id, n.name, n.address, n.country_code, n.location, n.provider, n.state`

func scanAccessNode(r interface{ Scan(...any) error }) (AccessNode, error) {
	var n AccessNode
	err := r.Scan(&n.ID, &n.Name, &n.Address, &n.CountryCode, &n.Location, &n.Provider, &n.State)
	return n, err
}

// Node returns one node.
func (a Access) Node(ctx context.Context, id string) (AccessNode, error) {
	n, err := scanAccessNode(a.s.R.QueryRowContext(ctx, `SELECT `+accNodeCols+` FROM node n WHERE n.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return n, ErrNotFound
	}
	return n, err
}

// Nodes returns all nodes that are not retired, ordered by name.
func (a Access) Nodes(ctx context.Context) ([]AccessNode, error) {
	rows, err := a.s.R.QueryContext(ctx, `SELECT `+accNodeCols+` FROM node n WHERE n.state <> 'retired' ORDER BY n.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AccessNode
	for rows.Next() {
		n, err := scanAccessNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ExistingNodeIDs returns those of ids that exist.
func (a Access) ExistingNodeIDs(ctx context.Context, ids []string) (map[string]bool, error) {
	rows, err := a.s.R.QueryContext(ctx, `SELECT id FROM node WHERE id IN (SELECT value FROM json_each(?))`, accJSON(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
