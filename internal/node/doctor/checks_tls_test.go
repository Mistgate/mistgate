package doctor

import (
	"context"
	"crypto/x509"
	"fmt"
	"testing"
	"time"
)

func TestCertIsNotReadFromAForeignListener(t *testing.T) {
	f := newFake(t)
	in := Inbound{ID: "inb_1", Enabled: true, Network: "udp", Port: 443, TLSMode: "acme_domain", ServerName: "example.com", State: "running", TLSPort: 443}
	called := 0
	tweak := func(e *Env) {
		e.Inbounds = func() []Inbound { return []Inbound{in} }
		e.ServedCert = func(context.Context, string, string) (*x509.Certificate, error) {
			called++
			return mkCert(t, t0.Add(60*24*time.Hour), "example.com"), nil
		}
	}
	listen := func(pid int, comm string, inode int) {
		f.put("/proc/net/tcp", tcpTable(fmt.Sprintf("  1: 00000000:01BB 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 %d 1 0\n", inode)))
		f.put("/proc/net/udp", udpTable(""))
		f.put(fmt.Sprintf("/proc/%d/comm", pid), comm+"\n")
		f.links[fmt.Sprintf("/proc/%d/fd/7", pid)] = fmt.Sprintf("socket:[%d]", inode)
		f.put(fmt.Sprintf("/proc/%d/fd/7", pid), "")
	}

	// Caddy holds TCP 443: its certificate says nothing about ours.
	listen(812, "caddy", 5001)
	want(t, run(t, f.doctor(tweak), CheckCertExpiry), Skip, "")
	if called != 0 {
		t.Fatal("read a certificate from a foreign listener")
	}
	r := run(t, f.doctor(tweak), CheckPortConflicts)
	want(t, r, Warn, "")
	param(t, r, "process", "caddy(812)")
	param(t, r, "network", "tcp")

	// Our own listener on the port: read it.
	f.remove("/proc/812")
	listen(100, "mistgate-node", 5002)
	want(t, run(t, f.doctor(tweak), CheckCertExpiry), OK, "")
	if called != 1 {
		t.Fatalf("ServedCert called %d times", called)
	}
	want(t, run(t, f.doctor(tweak), CheckPortConflicts), OK, "")

	// The engine says its HTTPS listener is down: nothing to read either.
	in.TLSDown = "masq_tcp: listen tcp :443: bind: address already in use"
	want(t, run(t, f.doctor(tweak), CheckCertExpiry), Skip, "")
	if called != 1 {
		t.Fatal("read a certificate although the listener is down")
	}
}
