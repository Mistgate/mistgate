-- Add the dedicated audit source for server-side assistant MCP calls.

-- +goose Up

CREATE TABLE audit_new (
    id     INTEGER PRIMARY KEY AUTOINCREMENT,
    ts     INTEGER NOT NULL,
    actor  TEXT NOT NULL,
    action TEXT NOT NULL,
    params TEXT NOT NULL DEFAULT '{}',
    result TEXT NOT NULL,
    source TEXT NOT NULL DEFAULT 'panel' CHECK (source IN ('panel', 'bot', 'mcp', 'api', 'assistant')),
    ip     TEXT NOT NULL DEFAULT ''
) STRICT;
INSERT INTO audit_new (id, ts, actor, action, params, result, source, ip)
SELECT id, ts, actor, action, params, result, source, ip FROM audit;
DROP TABLE audit;
ALTER TABLE audit_new RENAME TO audit;
CREATE INDEX audit_ts ON audit(ts);
CREATE INDEX audit_source ON audit(source, id);

-- +goose Down

CREATE TABLE audit_old (
    id     INTEGER PRIMARY KEY AUTOINCREMENT,
    ts     INTEGER NOT NULL,
    actor  TEXT NOT NULL,
    action TEXT NOT NULL,
    params TEXT NOT NULL DEFAULT '{}',
    result TEXT NOT NULL,
    source TEXT NOT NULL DEFAULT 'panel' CHECK (source IN ('panel', 'bot', 'mcp', 'api')),
    ip     TEXT NOT NULL DEFAULT ''
) STRICT;
INSERT INTO audit_old (id, ts, actor, action, params, result, source, ip)
SELECT id, ts, actor, action, params, result, CASE WHEN source = 'assistant' THEN 'mcp' ELSE source END, ip FROM audit;
DROP TABLE audit;
ALTER TABLE audit_old RENAME TO audit;
CREATE INDEX audit_ts ON audit(ts);
CREATE INDEX audit_source ON audit(source, id);
