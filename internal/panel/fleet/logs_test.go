package fleet

import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

// startLogs opens StreamLogs in the background: a Connect client call returns only once the server has
// written something, which here happens after the node answered.
func startLogs(ctx context.Context, nodes adminv1connect.NodeServiceClient, req *adminv1.StreamLogsRequest) <-chan *connect.ServerStreamForClient[adminv1.StreamLogsResponse] {
	out := make(chan *connect.ServerStreamForClient[adminv1.StreamLogsResponse], 1)
	go func() {
		st, _ := nodes.StreamLogs(ctx, connect.NewRequest(req))
		out <- st
	}()
	return out
}

func logChunk(reqID string, eof bool, lines ...*agentv1.LogLine) *agentv1.ConnectRequest {
	return &agentv1.ConnectRequest{Message: &agentv1.ConnectRequest_LogChunk{LogChunk: &agentv1.LogChunk{RequestId: reqID, Eof: eof, Lines: lines}}}
}

func TestStreamLogsRelay(t *testing.T) {
	e := newEnv(t)
	a := e.enroll("nodea")
	e.fixture(a.nodeID)
	nodes, _ := e.adminClients()
	if st := <-startLogs(e.ctx, nodes, &adminv1.StreamLogsRequest{NodeId: a.nodeID}); st == nil || st.Receive() || code(st.Err()) != connect.CodeFailedPrecondition {
		t.Errorf("offline node: %v", st)
	}
	c, _, _ := connectFull(a, "inst1")
	wantReq := func() *agentv1.LogRequest {
		return c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetLogRequest() != nil }).GetLogRequest()
	}

	// Tail is capped at 1000, follow at 600 s; chunks are relayed until eof.
	ch := startLogs(e.ctx, nodes, &adminv1.StreamLogsRequest{NodeId: a.nodeID, TailLines: 5000, Follow: true, Sources: []string{"agent"}})
	lr := wantReq()
	if lr.TailLines != 1000 || !lr.Follow || lr.FollowMaxSeconds != 600 || len(lr.Sources) != 1 {
		t.Errorf("LogRequest %+v", lr)
	}
	c.send(0, logChunk(lr.RequestId, false, &agentv1.LogLine{TimeUnixMs: 5, Level: agentv1.Severity_SEVERITY_ERROR, Source: "agent", Message: "boom"}))
	stream := <-ch
	if !stream.Receive() || len(stream.Msg().Lines) != 1 || stream.Msg().Lines[0].Message != "boom" || stream.Msg().Lines[0].Level != adminv1.LogLevel_LOG_LEVEL_ERROR {
		t.Fatalf("first chunk: %v %v", stream.Msg(), stream.Err())
	}
	c.send(0, logChunk(lr.RequestId, true))
	if !stream.Receive() || !stream.Msg().Eof {
		t.Fatalf("eof: %v %v", stream.Msg(), stream.Err())
	}
	if stream.Receive() {
		t.Error("data after eof")
	}

	// A client that goes away makes the panel tell the node to stop.
	ctx, cancel := context.WithCancel(e.ctx)
	startLogs(ctx, nodes, &adminv1.StreamLogsRequest{NodeId: a.nodeID, Follow: true})
	lr = wantReq()
	c.send(0, logChunk(lr.RequestId, false, &agentv1.LogLine{Message: "x"})) // lets the client call return
	time.Sleep(100 * time.Millisecond)
	cancel()
	if lc := c.wait(func(m *agentv1.ConnectResponse) bool { return m.GetLogCancel() != nil }).GetLogCancel(); lc.RequestId != lr.RequestId {
		t.Error("LogCancel for another request")
	}

	// A dropped node link ends the stream with an error.
	ch = startLogs(e.ctx, nodes, &adminv1.StreamLogsRequest{NodeId: a.nodeID, Follow: true})
	lr = wantReq()
	c.send(0, logChunk(lr.RequestId, false, &agentv1.LogLine{Message: "y"}))
	stream = <-ch
	stream.Receive()
	c.st.CloseRequest()
	if !stream.Receive() || !stream.Msg().Eof || stream.Msg().Error == "" {
		t.Errorf("link loss: %v %v", stream.Msg(), stream.Err())
	}
}
