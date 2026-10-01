-- "This is normal for this node" (HealthService.AcceptDoctorItem): the owner accepted a WARN of the doctor (Docker's nft
-- tables, a host without IPv6). An acceptance holds for one detail code of one check of one node: the panel drops it
-- when the check turns FAIL or its detail code changes, so it never hides a new problem. Additive: one new table.

-- +goose Up
CREATE TABLE doctor_accept (
    node_id             TEXT    NOT NULL REFERENCES node (id) ON DELETE CASCADE,
    check_id            TEXT    NOT NULL,
    detail_code         TEXT    NOT NULL DEFAULT '',             -- the DoctorResult.detail_code that was accepted
    accepted_by         TEXT    NOT NULL,                        -- admin id
    accepted_at         INTEGER NOT NULL,
    PRIMARY KEY (node_id, check_id)
) STRICT, WITHOUT ROWID;

-- +goose Down
DROP TABLE doctor_accept;
