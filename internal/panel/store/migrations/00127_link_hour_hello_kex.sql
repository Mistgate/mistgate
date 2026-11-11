-- vless-hello-kex/1 (agent.proto LinkCounters hello_pq, hello_classic, hello_unknown): the ClientHellos of a VLESS REALITY key's
-- connections by the key exchange they offer. hello_pq: an X25519MLKEM768 share before any X25519 share (what a strict REALITY
-- library demands); hello_classic: a hello without it; hello_unknown: a hello the node could not read. Volume counters of the key
-- rows. Rows of older agents keep zeros, which the reads mark as unsupported for them.

-- +goose Up
ALTER TABLE link_hour ADD COLUMN hello_pq INTEGER NOT NULL DEFAULT 0;
ALTER TABLE link_hour ADD COLUMN hello_classic INTEGER NOT NULL DEFAULT 0;
ALTER TABLE link_hour ADD COLUMN hello_unknown INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE link_hour DROP COLUMN hello_unknown;
ALTER TABLE link_hour DROP COLUMN hello_classic;
ALTER TABLE link_hour DROP COLUMN hello_pq;
