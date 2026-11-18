-- 00145 started to record when the panel made a node measure its speed; the measurements before it were never marked, and the
-- traffic peak of their hours kept an idle node "overloaded" for a week. Mark them once, from what was written down then and
-- carries a node id and a time: the stored path speed tests (both ends), the audit rows of the node's own bandwidth measurement
-- and the events of the first measurement after enrolling. A measurement that failed left none of these, and the audit row of
-- a path speed test names its nodes only by name: those are not guessed at, and a peak is never marked from the shape of the
-- traffic. Only the window the capacity report reads is filled (what NoteSpeedTest keeps).

-- +goose Up
INSERT OR IGNORE INTO node_speed_test (node_id, at)
SELECT node_id, at FROM (
    SELECT from_node_id AS node_id, at_unix AS at FROM path_speed
    UNION ALL
    SELECT to_node_id, at_unix FROM path_speed
    UNION ALL
    SELECT CASE WHEN json_valid(params) THEN json_extract(params, '$.node_id') END, ts FROM audit WHERE action = 'node.bandwidth_measure'
    UNION ALL
    SELECT node_id, ts FROM event WHERE code IN ('bandwidth_measured', 'bandwidth_upload_missing') AND node_id IS NOT NULL
)
WHERE at >= CAST(strftime('%s', 'now') AS INTEGER) - 721 * 3600
  AND node_id IN (SELECT id FROM node);

-- +goose Down
-- The rows are measurements that happened; there is nothing to take back.
SELECT 1;
