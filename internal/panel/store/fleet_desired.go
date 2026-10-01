package store

import (
	"context"
	"database/sql"
	"time"
)

// FleetInboundRow is one inbound of a node joined with its profile.
type FleetInboundRow struct {
	ID, ProfileID, ProfileName, Protocol string
	ProfileVersion                       uint32
	Settings                             string // settings_json, secrets NOT merged
	SecretsEnc                           []byte // vault, AAD = ProfileID; nil if the profile has no secrets
	PortOverride                         uint16
	TLSNameOverride                      string
	SpecVersion                          uint64
	Enabled                              bool
	State, LastError                     string
	CertPin                              string
	CertNotAfter                         time.Time
	// Plugin-neutral material (migration 00014): the vault blob of the inbound (AAD = inbound id; nil for hysteria2) and
	// the public JSON ("{}" = none), and the last agent.v1.AwgHealth the node reported as protojson ("" = never).
	PluginStateEnc []byte
	PluginPublic   string
	AwgHealthJSON  string
	AwgHealthAt    time.Time
}

// FleetInbounds returns the inbounds of a node ordered by id.
func (s *Store) FleetInbounds(ctx context.Context, nodeID string, onlyEnabled bool) ([]FleetInboundRow, error) {
	where := `i.node_id = ?`
	if onlyEnabled {
		where += ` AND i.enabled = 1`
	}
	return s.fleetInbounds(ctx, where, nodeID)
}

// FleetInbound returns one inbound by id, or ErrNotFound.
func (s *Store) FleetInbound(ctx context.Context, inboundID string) (FleetInboundRow, error) {
	rows, err := s.fleetInbounds(ctx, `i.id = ?`, inboundID)
	if err != nil {
		return FleetInboundRow{}, err
	}
	if len(rows) == 0 {
		return FleetInboundRow{}, ErrNotFound
	}
	return rows[0], nil
}

func (s *Store) fleetInbounds(ctx context.Context, where string, arg any) ([]FleetInboundRow, error) {
	q := `
		SELECT i.id, p.id, p.name, p.protocol, p.version, p.settings_json, p.secrets_enc,
		       coalesce(i.port_override, 0), i.tls_server_name_override, i.spec_version, i.enabled,
		       i.state, i.last_error, i.cert_pin_sha256, i.cert_not_after,
		       i.plugin_state_enc, i.plugin_public_json, i.awg_health_json, i.awg_health_at
		FROM inbound i JOIN profile p ON p.id = i.profile_id
		WHERE ` + where
	rows, err := s.R.QueryContext(ctx, q+` ORDER BY i.id`, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FleetInboundRow
	for rows.Next() {
		var r FleetInboundRow
		var spec, na, healthAt int64
		var pv int64
		if err := rows.Scan(&r.ID, &r.ProfileID, &r.ProfileName, &r.Protocol, &pv, &r.Settings, &r.SecretsEnc,
			&r.PortOverride, &r.TLSNameOverride, &spec, &r.Enabled, &r.State, &r.LastError, &r.CertPin, &na,
			&r.PluginStateEnc, &r.PluginPublic, &r.AwgHealthJSON, &healthAt); err != nil {
			return nil, err
		}
		r.ProfileVersion, r.SpecVersion, r.CertNotAfter, r.AwgHealthAt = uint32(pv), uint64(spec), fleetTime(na), fleetTime(healthAt)
		out = append(out, r)
	}
	return out, rows.Err()
}

// FleetUserNames resolves user ids to names (missing ids are absent from the result).
func (s *Store) FleetUserNames(ctx context.Context, ids []string) (map[string]string, error) {
	return s.fleetLookup(ctx, `SELECT name FROM user WHERE id = ?`, ids)
}

// FleetDeviceModels resolves device ids to "platform model" labels.
func (s *Store) FleetDeviceModels(ctx context.Context, ids []string) (map[string]string, error) {
	return s.fleetLookup(ctx, `SELECT trim(platform || ' ' || model) FROM device WHERE id = ?`, ids)
}

func (s *Store) fleetLookup(ctx context.Context, q string, ids []string) (map[string]string, error) {
	out := make(map[string]string, len(ids))
	for _, id := range ids {
		if _, done := out[id]; done {
			continue
		}
		var v string
		err := s.R.QueryRowContext(ctx, q, id).Scan(&v)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return nil, err
		}
		out[id] = v
	}
	return out, nil
}
