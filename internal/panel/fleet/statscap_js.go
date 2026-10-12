//go:build js

package fleet

// maxStatsDeltas is the cap of the edge edition (the VPS one is 1<<16, statscap_native.go). The stats batch becomes one D1
// statement whose largest bound value is the JSON list of traffic buckets, and D1 refuses a value over 2,000,000 bytes. The
// longest bucket a real node can send (30-character node and user ids, "hysteria2", 9-digit byte counts) is 179 bytes, so
// 10,000 users are 1.79 MB; 11,000 would be 1.97 MB and 12,000 2.15 MB (TestD1IngestStatsAtEdgeCap in the store package
// measures it: also about 40 MB of Go memory of the isolate's 128 MB at 10,000, where 65,536 users take 233 MB). Above the cap
// the batch is rejected whole (stats_rejected, reason too_many_deltas) and the agent's traffic of that interval is lost.
//
// ponytail: a node with more than 10,000 credentials carrying traffic in one batch loses it. Upgrade path: chunk the three
// JSON arrays (buckets, hours, users) over several statements of one batch, then raise the cap towards the memory budget.
const maxStatsDeltas = 10_000
