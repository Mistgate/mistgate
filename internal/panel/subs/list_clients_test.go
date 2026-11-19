package subs_test

import (
	"context"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/protocols/builtin"
	"github.com/mistgate/mistgate/internal/panel/subs"
)

// Every client a built-in protocol declares has a name for people: ListClients falls back to the raw id ("trusttunnel") only
// for a client nobody named.
func TestListClientsNamesEveryClientOfTheBuiltInProtocols(t *testing.T) {
	svc := subs.NewService(nil, nil, builtin.Registry(), nil, nil, nil)
	r, err := svc.ListClients(context.Background(), connect.NewRequest(&adminv1.ListClientsRequest{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Msg.Clients) == 0 {
		t.Fatal("no clients")
	}
	for _, c := range r.Msg.Clients {
		if c.Name == "" || c.Name == c.Id {
			t.Errorf("client %q is listed under its raw id: add it to clientNames", c.Id)
		}
	}
}
