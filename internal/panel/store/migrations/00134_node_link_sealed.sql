-- Whether the node ever opened a sealed link session (1) or not (0). A node that did is not accepted again on a plain
-- one: a party between the agent and the panel cannot turn its sealed link back into a readable one. Re-enrolling the node
-- clears it, for an agent rolled back to a build that cannot seal.
--
-- Agents from v0.1.66 on always ask to seal, so a node whose recorded agent version is at least that is marked here: it
-- is protected before its first reconnect after the panel update. Only a plain "[v]MAJOR.MINOR.PATCH" counts (numbers of
-- at most six digits); an empty, dev or otherwise unrecognised version is left unmarked.

-- +goose Up
ALTER TABLE node ADD COLUMN link_sealed INTEGER NOT NULL DEFAULT 0 CHECK (link_sealed IN (0, 1));

UPDATE node SET link_sealed = 1 WHERE id IN (
    WITH v(id, s) AS (
        SELECT id, CASE WHEN agent_version LIKE 'v%' THEN substr(agent_version, 2) ELSE agent_version END FROM node
    ), a(id, major, r) AS (
        SELECT id, substr(s, 1, instr(s, '.') - 1), substr(s, instr(s, '.') + 1) FROM v WHERE instr(s, '.') > 0
    ), b(id, major, minor, patch) AS (
        SELECT id, major, substr(r, 1, instr(r, '.') - 1), substr(r, instr(r, '.') + 1) FROM a WHERE instr(r, '.') > 0
    )
    SELECT id FROM b
    WHERE major <> '' AND minor <> '' AND patch <> ''
      AND length(major) <= 6 AND length(minor) <= 6 AND length(patch) <= 6
      AND major NOT GLOB '*[^0-9]*' AND minor NOT GLOB '*[^0-9]*' AND patch NOT GLOB '*[^0-9]*'
      AND (CAST(major AS INTEGER) > 0
           OR (CAST(major AS INTEGER) = 0 AND (CAST(minor AS INTEGER) > 1
                                               OR (CAST(minor AS INTEGER) = 1 AND CAST(patch AS INTEGER) >= 66))))
);

-- +goose Down
ALTER TABLE node DROP COLUMN link_sealed;
