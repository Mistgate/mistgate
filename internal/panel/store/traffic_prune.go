package store

import (
	"context"
	"time"
)

// A first prune may face years of rows and the writer is shared, so a DELETE removes a slice and a call does a few slices;
// the next hourly sweep continues.
const (
	pruneTrafficBatch = 5000
	pruneTrafficMax   = 20
)

// PruneTrafficBuckets drops the per-person hourly traffic older than the cut (traffic_bucket_hour, migration 00106, serves the scan).
func (s *Store) PruneTrafficBuckets(ctx context.Context, before time.Time) (int64, error) {
	return s.pruneByHour(ctx, `DELETE FROM traffic_bucket WHERE (user_id, node_id, protocol, hour_start) IN
		(SELECT user_id, node_id, protocol, hour_start FROM traffic_bucket WHERE hour_start < ? LIMIT ?)`, before)
}

// PruneNodeTrafficHours drops the per-node hourly traffic older than the cut (node_traffic_hour_hour serves the scan).
func (s *Store) PruneNodeTrafficHours(ctx context.Context, before time.Time) (int64, error) {
	return s.pruneByHour(ctx, `DELETE FROM node_traffic_hour WHERE (node_id, protocol, hour_start) IN
		(SELECT node_id, protocol, hour_start FROM node_traffic_hour WHERE hour_start < ? LIMIT ?)`, before)
}

func (s *Store) pruneByHour(ctx context.Context, query string, before time.Time) (int64, error) {
	var total int64
	for i := 0; i < pruneTrafficMax; i++ {
		res, err := s.W.ExecContext(ctx, query, hourBucket(before), pruneTrafficBatch)
		if err != nil {
			return total, err
		}
		n, _ := res.RowsAffected()
		total += n
		if n < pruneTrafficBatch {
			break
		}
	}
	return total, nil
}
