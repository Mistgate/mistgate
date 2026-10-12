//go:build !js

package fleet

// maxStatsDeltas bounds the work of one stats batch: a node cannot have more credentials than this. Above it the batch is
// rejected whole (stats_rejected, reason too_many_deltas); the sessions of a batch are cut at it.
const maxStatsDeltas = 1 << 16
