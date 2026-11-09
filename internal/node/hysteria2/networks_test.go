package hysteria2

import (
	"bytes"
	"log/slog"
	"net"
	"net/netip"
	"runtime"
	"testing"

	agentpb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/nets"
	"github.com/mistgate/mistgate/internal/node/netstest"
	"github.com/mistgate/mistgate/internal/plugin"
	"google.golang.org/protobuf/proto"
)

func TestClientConnectionCountsNetwork(t *testing.T) {
	r := newRig(t, "")
	agg := nets.New(2)
	db, err := netstest.LoopbackDB(64500)
	if err != nil {
		t.Fatal(err)
	}
	if got := db.Lookup(netip.MustParseAddr("127.0.0.1")); got != 64500 {
		t.Fatalf("loopback fixture lookup ASN = %d, want 64500", got)
	}
	agg.SetTable(db, "fixture-sha")
	r.e.(*eng).env.Nets = agg
	r.apply(cred("alice"))
	client := r.mustDial("alice")
	if err := echoOnce(client, r.echo, []byte("network count")); err != nil {
		t.Fatal(err)
	}

	networks, people := agg.Take()
	if len(networks) != 1 || networks[0].Asn != 0 || networks[0].Attempts != 1 || networks[0].Connects != 1 || networks[0].People != 1 {
		t.Fatalf("network counters = %+v", networks)
	}
	if len(people) != 1 || people[0].CredId != "crd_alice" || people[0].Asn != 64500 || people[0].Connects != 1 {
		t.Fatalf("person counters = %+v", people)
	}
}

func TestThreeHysteriaClientsKeepRealASNAndNoAddressLeavesEngine(t *testing.T) {
	logs := &netstest.LogBuffer{}
	r := newRigWithLogger(t, "", slog.New(slog.NewTextHandler(logs, nil)))
	agg := nets.New(1)
	db, err := netstest.LoopbackDB(64500)
	if err != nil {
		t.Fatal(err)
	}
	agg.SetTable(db, "fixture-sha")
	r.e.(*eng).env.Nets = agg
	ids := []string{"alice", "bob", "de1"}
	creds := make([]plugin.UserCred, 0, len(ids))
	for _, id := range ids {
		creds = append(creds, cred(id))
	}
	r.apply(creds...)
	factory := sourceLoopFactory{ip: net.ParseIP("127.0.0.2")}
	for _, id := range ids {
		client, err := r.dial(id, factory)
		if err != nil {
			if runtime.GOOS == "windows" {
				t.Skip("127.0.0.2 source binding is unavailable on Windows; this check runs in WSL")
			}
			t.Fatal("dial Hysteria from the loopback source failed")
		}
		if err := echoOnce(client, r.echo, []byte("network count")); err != nil {
			if runtime.GOOS == "windows" {
				t.Skip("127.0.0.2 source binding is unavailable on Windows; this check runs in WSL")
			}
			t.Fatal("Hysteria loopback-source exchange failed")
		}
		_ = client.Close()
	}

	networks, people := agg.Take()
	if len(networks) != 1 || networks[0].Asn != 64500 || networks[0].Connects != 3 || networks[0].People != 3 || len(people) != 0 {
		t.Fatalf("Hysteria network counters = %+v", networks)
	}
	wire, err := proto.Marshal(&agentpb.StatsBatch{Networks: networks})
	if err != nil {
		t.Fatal(err)
	}
	forms, err := netstest.AddressForms("127.0.0.2")
	if err != nil {
		t.Fatal(err)
	}
	logText := []byte(logs.String())
	for _, form := range forms {
		if bytes.Contains(wire, form) || bytes.Contains(logText, form) {
			t.Fatal("Hysteria output contains a client address form")
		}
	}
}

type sourceLoopFactory struct{ ip net.IP }

func (f sourceLoopFactory) New(net.Addr) (net.PacketConn, error) {
	return net.ListenUDP("udp", &net.UDPAddr{IP: f.ip})
}
