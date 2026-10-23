-- kl!ck reports (internal/panel/clientprobe, client_probe.proto): the local server check of the kl!ck VPN app, sent by the
-- person to POST <subscription link>/probe. One row per server result that matched a node of the person's subscription;
-- report groups the rows of one report (a random id, nothing else). kind and port are the protocol type and port the app named,
-- verdict is ok | freeze | tcp_blocked | handshake_failed | no_answer, detail_json what was measured
-- ({"flaky":false,"tcp_ok":3,"tcp_tries":3,"tcp_ms":52,"delay_ms":140,"dl_bytes":10985,"dl_stalled":true,"dns":"same"}; a value
-- that was not measured is absent). vpn: the app's VPN was on during the check; direct_checked: TCP and DNS were measured.
-- asn, operator, country, region and net_type (mobile | home | business | hosting | vpn) are the network as the app saw it ('' and
-- 0 = unknown). NO address of the sender, no user, no device and no server address is stored. Rows live 7 days (the health
-- retention sweep). No foreign key on node_id, as in the other country-check tables.

-- +goose Up
CREATE TABLE client_probe_report (
    report         TEXT    NOT NULL,
    at             INTEGER NOT NULL,
    node_id        TEXT    NOT NULL,
    kind           TEXT    NOT NULL,
    port           INTEGER NOT NULL,
    verdict        TEXT    NOT NULL,
    detail_json    TEXT    NOT NULL DEFAULT '{}',
    vpn            INTEGER NOT NULL DEFAULT 0 CHECK (vpn IN (0, 1)),
    direct_checked INTEGER NOT NULL DEFAULT 0 CHECK (direct_checked IN (0, 1)),
    asn            INTEGER NOT NULL DEFAULT 0,
    operator       TEXT    NOT NULL DEFAULT '',
    country        TEXT    NOT NULL DEFAULT '',
    region         TEXT    NOT NULL DEFAULT '',
    net_type       TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (report, node_id, kind, port)
) STRICT;
CREATE INDEX client_probe_report_at ON client_probe_report (at);

-- +goose Down
DROP TABLE client_probe_report;
