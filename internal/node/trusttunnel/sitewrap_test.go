package trusttunnel

import (
	"crypto/tls"
	"net"
	"sync/atomic"
	"testing"
	"time"

	nodeengine "github.com/mistgate/mistgate/internal/node/engine"
)

type countingListener struct {
	net.Listener
	accepted *atomic.Int32
}

func (l countingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		l.accepted.Add(1)
	}
	return c, err
}

// A subscription host on the inbound's port takes the subdomain off the listener (Env.SiteListener), as from every site:
// the engine serves only what the wrapper hands on.
func TestListenerGoesThroughTheSiteWrapper(t *testing.T) {
	var accepted atomic.Int32
	var gotPort atomic.Int32
	var gotProxied atomic.Bool
	h := newHarnessEnv(t, nil, nil, timings{}, func(env *nodeengine.Env) {
		env.SiteListener = func(ln net.Listener, port uint16, proxied bool) net.Listener {
			gotPort.Store(int32(port))
			gotProxied.Store(proxied)
			return countingListener{Listener: ln, accepted: &accepted}
		}
	})
	if gotPort.Load() != int32(h.spec.Listen.Port) || gotProxied.Load() {
		t.Fatalf("wrapped for port %d (proxied %v), want %d unproxied", gotPort.Load(), gotProxied.Load(), h.spec.Listen.Port)
	}
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 2 * time.Second}, "tcp", h.addr, &tls.Config{InsecureSkipVerify: true, ServerName: "example.com"})
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if accepted.Load() != 1 {
		t.Fatalf("the wrapper accepted %d connections, want 1", accepted.Load())
	}
}
