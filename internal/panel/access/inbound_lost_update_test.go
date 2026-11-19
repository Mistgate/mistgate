package access

import (
	"sync"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// Two edits of one inbound at once, each a different field: both reported saved means both are in the row; an edit that
// read the row before the other one wrote it is refused (Aborted, stale_inbound), never saved over the other.
func TestUpdateInboundConcurrentEditsNeverLoseAField(t *testing.T) {
	e := newEnv(t)
	e.node("nod_de1", "de1", "de1.example.com", "active")
	in := e.inbound(e.profile("p1", "").Id, "nod_de1")
	refused := 0
	for i := 0; i < 60; i++ {
		port := uint32(20000 + i)
		hidden := i%2 == 0
		var wg sync.WaitGroup
		var portErr, directErr error
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, portErr = e.s.UpdateInbound(e.ctx, req(&adminv1.UpdateInboundRequest{InboundId: in.Id, PortOverride: &port}))
		}()
		go func() {
			defer wg.Done()
			<-start
			shown := !hidden
			_, directErr = e.s.UpdateInbound(e.ctx, req(&adminv1.UpdateInboundRequest{InboundId: in.Id, DirectInSubscriptions: &shown}))
		}()
		close(start)
		wg.Wait()
		for _, err := range []error{portErr, directErr} {
			if err != nil {
				wantCode(t, err, connect.CodeAborted)
				wantMsg(t, err, "aborted: stale_inbound")
				refused++
			}
		}
		got := must(e.st.Access().Inbound(e.ctx, in.Id))
		if portErr == nil && uint32(got.PortOverride) != port {
			t.Fatalf("round %d: the port was saved and then lost: %d, want %d", i, got.PortOverride, port)
		}
		if directErr == nil && got.DirectInSubscriptions == hidden {
			t.Fatalf("round %d: the subscription switch was saved and then lost (direct=%v)", i, got.DirectInSubscriptions)
		}
	}
	t.Logf("refused %d of 120 edits", refused)
}
