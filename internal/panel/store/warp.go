package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Queries of the WARP module (migration 00015). One Cloudflare WARP account per node.
// The secrets (private key, access token, device id, license) live in SecretEnc, sealed by the vault with the node
// id as AAD; nothing else in the row is secret.

// WARP account sources, the values of warp_account.source.
const (
	WarpRegistered = "registered"
	WarpImported   = "imported"
)

// WarpAccountRow is one warp_account row.
type WarpAccountRow struct {
	NodeID, Source         string
	SecretEnc              []byte
	PeerPublicKey          string
	EndpointV4, EndpointV6 string // IP literals, no port
	Ports                  []uint16
	AddressV4, AddressV6   string
	MTU                    int
	ClientID               string // base64; its first three bytes are the "reserved" bytes
	UseReserved            bool
	AccountType            string
	Enabled                bool
	TOSURL, TOSAcceptedBy  string
	TOSAcceptedAt          time.Time // zero for an import
	RegisteredWith         string
	Attention              string // reason code while the owner has to decide; "" = fine
	HealthJSON             string // last agent.v1.WarpHealth (protojson); "" = never reported
	HealthAt               time.Time
	CreatedAt, UpdatedAt   time.Time
}

const warpCols = `node_id, source, secret_enc, peer_public_key, endpoint_v4, endpoint_v6, ports_json, address_v4, address_v6,
	mtu, client_id, use_reserved, account_type, enabled, tos_url, tos_accepted_by, tos_accepted_at, registered_with,
	attention, health_json, health_at, created_at, updated_at`

func scanWarp(r rowScanner) (WarpAccountRow, error) {
	var a WarpAccountRow
	var ports string
	var reserved, enabled, tosAt, healthAt, created, updated int64
	err := r.Scan(&a.NodeID, &a.Source, &a.SecretEnc, &a.PeerPublicKey, &a.EndpointV4, &a.EndpointV6, &ports, &a.AddressV4,
		&a.AddressV6, &a.MTU, &a.ClientID, &reserved, &a.AccountType, &enabled, &a.TOSURL, &a.TOSAcceptedBy, &tosAt,
		&a.RegisteredWith, &a.Attention, &a.HealthJSON, &healthAt, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return WarpAccountRow{}, ErrNotFound
	}
	if err != nil {
		return WarpAccountRow{}, err
	}
	_ = json.Unmarshal([]byte(ports), &a.Ports)
	a.UseReserved, a.Enabled = reserved == 1, enabled == 1
	a.TOSAcceptedAt, a.HealthAt = fleetTime(tosAt), fleetTime(healthAt)
	a.CreatedAt, a.UpdatedAt = fromUnix(created), fromUnix(updated)
	return a, nil
}

// WarpAccount returns the account of a node or ErrNotFound.
func (s *Store) WarpAccount(ctx context.Context, nodeID string) (WarpAccountRow, error) {
	return scanWarp(s.R.QueryRowContext(ctx, `SELECT `+warpCols+` FROM warp_account WHERE node_id = ?`, nodeID))
}

// WarpAccounts returns every account (for the node list badges and the desired state of all nodes).
func (s *Store) WarpAccounts(ctx context.Context) ([]WarpAccountRow, error) {
	rows, err := s.R.QueryContext(ctx, `SELECT `+warpCols+` FROM warp_account ORDER BY node_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []WarpAccountRow
	for rows.Next() {
		a, err := scanWarp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// CreateWarpAccount stores a new account. ErrConflict when the node already has one (one per node: delete first),
// ErrNotFound when there is no such node.
func (s *Store) CreateWarpAccount(ctx context.Context, a WarpAccountRow) error {
	return insertWarp(ctx, s.W, a)
}

// ReplaceWarpAccount swaps the account of the node for a new one in one transaction (a re-registration: the old
// account must survive a failure to store the new one). ErrNotFound when the node has no account.
func (s *Store) ReplaceWarpAccount(ctx context.Context, a WarpAccountRow) error {
	tx, err := s.W.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM warp_account WHERE node_id = ?`, a.NodeID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if err := insertWarp(ctx, tx, a); err != nil {
		return err
	}
	return tx.Commit()
}

func insertWarp(ctx context.Context, db execer, a WarpAccountRow) error {
	ports, _ := json.Marshal(a.Ports)
	_, err := db.ExecContext(ctx, `INSERT INTO warp_account (`+warpCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.NodeID, a.Source, a.SecretEnc, a.PeerPublicKey, a.EndpointV4, a.EndpointV6, string(ports), a.AddressV4, a.AddressV6,
		a.MTU, a.ClientID, b2i(a.UseReserved), a.AccountType, b2i(a.Enabled), a.TOSURL, a.TOSAcceptedBy, fleetUnix(a.TOSAcceptedAt),
		a.RegisteredWith, a.Attention, a.HealthJSON, fleetUnix(a.HealthAt), unix(a.CreatedAt), unix(a.UpdatedAt))
	switch {
	case err == nil:
		return nil
	case strings.Contains(err.Error(), "UNIQUE constraint failed"):
		return ErrConflict
	case strings.Contains(err.Error(), "FOREIGN KEY constraint failed"):
		return ErrNotFound
	}
	return err
}

// WarpRefresh is what a read of the account at Cloudflare may change.
type WarpRefresh struct {
	PeerPublicKey          string
	EndpointV4, EndpointV6 string
	Ports                  []uint16
	AddressV4, AddressV6   string
	ClientID               string
	UseReserved            bool
	AccountType            string
}

// UpdateWarpRefresh replaces the connection facts of the account and clears "needs attention". ErrNotFound when
// the node has no account.
func (s *Store) UpdateWarpRefresh(ctx context.Context, nodeID string, r WarpRefresh, now time.Time) error {
	ports, _ := json.Marshal(r.Ports)
	return s.warpExec(ctx, `UPDATE warp_account SET peer_public_key = ?, endpoint_v4 = ?, endpoint_v6 = ?, ports_json = ?,
		address_v4 = ?, address_v6 = ?, client_id = ?, use_reserved = ?, account_type = ?, attention = '', updated_at = ?
		WHERE node_id = ?`, r.PeerPublicKey, r.EndpointV4, r.EndpointV6, string(ports), r.AddressV4, r.AddressV6, r.ClientID,
		b2i(r.UseReserved), r.AccountType, unix(now), nodeID)
}

// SetWarpEnabled pauses or resumes the account. ErrNotFound when the node has no account.
func (s *Store) SetWarpEnabled(ctx context.Context, nodeID string, enabled bool, now time.Time) error {
	return s.warpExec(ctx, `UPDATE warp_account SET enabled = ?, updated_at = ? WHERE node_id = ?`, b2i(enabled), unix(now), nodeID)
}

// SetWarpAttention records why the owner has to decide ("" = nothing to decide). ErrNotFound when no account.
func (s *Store) SetWarpAttention(ctx context.Context, nodeID, reason string, now time.Time) error {
	return s.warpExec(ctx, `UPDATE warp_account SET attention = ?, updated_at = ? WHERE node_id = ?`, reason, unix(now), nodeID)
}

// SetWarpHealth stores the last WarpHealth the node reported (protojson, "" is not allowed). The account's
// updated_at is not touched: health arrives every stats batch and is not a change of the account.
func (s *Store) SetWarpHealth(ctx context.Context, nodeID, healthJSON string, at time.Time) error {
	return s.warpExec(ctx, `UPDATE warp_account SET health_json = ?, health_at = ? WHERE node_id = ?`, healthJSON, unix(at), nodeID)
}

// DeleteWarpAccount removes the account. ErrNotFound when the node has none.
func (s *Store) DeleteWarpAccount(ctx context.Context, nodeID string) error {
	return s.warpExec(ctx, `DELETE FROM warp_account WHERE node_id = ?`, nodeID)
}

func (s *Store) warpExec(ctx context.Context, q string, args ...any) error {
	res, err := s.W.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
