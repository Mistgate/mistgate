package store

import "context"

// FleetEnabledInbounds returns the enabled inbounds of every node with the name of the profile each deploys:
// node id -> inbound id -> profile name. One query for the whole fleet: the Overview and the node list ask on every
// poll, and the status of a node needs them (no profiles at all, which profile failed, how many there are).
func (s *Store) FleetEnabledInbounds(ctx context.Context) (map[string]map[string]string, error) {
	rows, err := s.R.QueryContext(ctx, `
		SELECT i.node_id, i.id, p.name FROM inbound i JOIN profile p ON p.id = i.profile_id WHERE i.enabled = 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]string{}
	for rows.Next() {
		var node, id, name string
		if err := rows.Scan(&node, &id, &name); err != nil {
			return nil, err
		}
		if out[node] == nil {
			out[node] = map[string]string{}
		}
		out[node][id] = name
	}
	return out, rows.Err()
}
