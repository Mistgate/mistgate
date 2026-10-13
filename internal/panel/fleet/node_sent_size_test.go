package fleet

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// A device of its own for every app makes the node_sent digest grow with the devices; D1 takes a row of 2 MB. The size is
// checked when the digest is stored: a digest under the mark is silent, one over it is warned about once per node, and it
// is stored either way (the VPS edition has no limit).
func TestStoreDesiredWarnsOnceWhenNodeSentNearsTheRowLimit(t *testing.T) {
	e := newCoreEnv(t)
	var logs bytes.Buffer
	e.f.log = slog.New(slog.NewTextHandler(&logs, nil))
	e.exec(`INSERT INTO node (id, name, address, state, created_at) VALUES ('nod_big', 'big', 'big.example.com', 'active', 1)`)
	e.exec(`INSERT INTO node (id, name, address, state, created_at) VALUES ('nod_other', 'other', 'other.example.com', 'active', 1)`)
	c := NewSessionCore(e.f)

	small := bytes.Repeat([]byte("a"), 1000)
	if err := c.storeDesired(e.ctx, "nod_big", 1, "h", small); err != nil || logs.Len() != 0 {
		t.Fatalf("a small digest: %v, log %q", err, logs.String())
	}
	big := bytes.Repeat([]byte("b"), maxNodeSentBytes+1)
	for rev := uint64(2); rev <= 3; rev++ {
		if err := c.storeDesired(e.ctx, "nod_big", rev, "h", big); err != nil {
			t.Fatal(err)
		}
	}
	if n := strings.Count(logs.String(), "node_sent"); n != 1 || !strings.Contains(logs.String(), "nod_big") {
		t.Fatalf("warnings for one node over two writes = %d: %q", n, logs.String())
	}
	if err := c.storeDesired(e.ctx, "nod_other", 1, "h", big); err != nil || strings.Count(logs.String(), "node_sent") != 2 {
		t.Fatalf("another node: %v, log %q", err, logs.String())
	}
	_, digest, err := e.st.NodeWithSentDigest(e.ctx, "nod_big")
	if err != nil || len(digest) != len(big) {
		t.Fatalf("the oversized digest was not stored: %d bytes, %v", len(digest), err)
	}
}
