//go:build !js

package store

import (
	"context"
	"strconv"
	"time"
)

// AddTrafficParams puts the node's traffic over the last 24 hours and the last 7 days (up and down, all protocols, the hour in
// progress included, from the hourly rollup) in params as traffic_24h_bytes and traffic_7d_bytes. A node without rows has none:
// both are 0. It is one function for the events and the alerts that carry the two numbers.
func (s *Store) AddTrafficParams(ctx context.Context, nodeID string, now time.Time, params map[string]string) error {
	h := hourBucket(now)
	var day, week int64
	err := s.R.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(CASE WHEN hour_start > ? THEN bytes_up + bytes_down END), 0), COALESCE(SUM(bytes_up + bytes_down), 0)
		FROM node_traffic_hour WHERE node_id = ? AND hour_start > ?`, h-24*3600, nodeID, h-7*24*3600).Scan(&day, &week)
	if err == nil {
		params["traffic_24h_bytes"], params["traffic_7d_bytes"] = strconv.FormatInt(day, 10), strconv.FormatInt(week, 10)
	}
	return err
}
