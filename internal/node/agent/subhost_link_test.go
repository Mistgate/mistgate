package agent

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

// A host's own link to its subscription name goes through its bridge entry, never out to the network; any other address is
// dialled as before.
func TestHostLinkDialsThroughTheBridge(t *testing.T) {
	r := newShareRig(t)
	withSubHostPort(t, r.port)
	a := r.h.a
	a.applySubHost(&pb.NodeSettings{SubHost: &pb.SubHostRole{Names: []string{"sub.example.test"}, Path: "/sub-host-bridge-path", SiteName: "site.example.test"}})
	r.apply()
	var direct []string
	a.directDialContext = func(_ context.Context, _, addr string) (net.Conn, error) {
		direct = append(direct, addr)
		return nil, errors.New("no network in this test")
	}
	client, _ := a.newLinkHTTPClient()
	dial := client.Transport.(*http.Transport).DialContext
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := dial(ctx, "tcp", "SUB.example.test:443"); err == nil {
		t.Fatal("no panel holds the bridge: the dial must fail")
	}
	if len(direct) != 0 {
		t.Fatalf("the host's own link went out to %v", direct)
	}
	if _, err := dial(context.Background(), "tcp", "other.example.test:443"); err == nil || len(direct) != 1 {
		t.Fatalf("another address must be dialled as before: %v %v", err, direct)
	}
}
