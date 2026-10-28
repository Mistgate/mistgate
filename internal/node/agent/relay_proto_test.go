package agent

import (
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

// The wire contract of the relay forwards (agent.proto "RELAY FORWARDS"): the fields survive both encodings the agent uses
// (binary on the stream, protojson in the state file), and an empty list stays different from no list.
func TestRelayMessagesRoundTrip(t *testing.T) {
	ds := &pb.DesiredState{Revision: 7, Relays: &pb.RelayForwards{Items: []*pb.RelayForward{
		{RelayId: "rly_a", Network: "tcp", Port: 8443, ToHost: "203.0.113.7", ToPort: 443},
		{RelayId: "rly_b", Network: "udp", Port: 443, ToHost: "exit.example.com", ToPort: 8443},
	}}}
	res := &pb.ApplyResult{Revision: 7, Relays: []*pb.RelayResult{
		{RelayId: "rly_a", Ok: true, ToIp: "203.0.113.7"},
		{RelayId: "rly_b", Error: "forward_drop", ToIp: "198.51.100.9"},
	}}
	for name, m := range map[string]proto.Message{"desired state": ds, "apply result": res} {
		wire, err := proto.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		back := m.ProtoReflect().New().Interface()
		if err := proto.Unmarshal(wire, back); err != nil || !proto.Equal(m, back) {
			t.Errorf("%s: binary round trip: %v", name, err)
		}
		text, err := protojson.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		back = m.ProtoReflect().New().Interface()
		if err := protojson.Unmarshal(text, back); err != nil || !proto.Equal(m, back) {
			t.Errorf("%s: json round trip: %v (%s)", name, err, text)
		}
	}

	// The field numbers are the contract.
	wire, _ := proto.Marshal(&pb.DesiredState{Relays: &pb.RelayForwards{Items: []*pb.RelayForward{{RelayId: "x", Port: 1}}}})
	if wire[0] != 8<<3|2 { // field 8, length-delimited
		t.Errorf("DesiredState.relays is not field 8: first byte %#x", wire[0])
	}
	wire, _ = proto.Marshal(&pb.ApplyResult{Relays: []*pb.RelayResult{{RelayId: "x"}}})
	if wire[0] != 7<<3|2 { // field 7, length-delimited
		t.Errorf("ApplyResult.relays is not field 7: first byte %#x", wire[0])
	}

	// Absent is not empty: the wrapper is what lets a delta say "none any more".
	for name, enc := range map[string]func(proto.Message) ([]byte, error){
		"binary": proto.Marshal,
		"json":   func(m proto.Message) ([]byte, error) { return protojson.Marshal(m) },
	} {
		dec := func(b []byte) *pb.DesiredState {
			out := &pb.DesiredState{}
			var err error
			if name == "binary" {
				err = proto.Unmarshal(b, out)
			} else {
				err = protojson.Unmarshal(b, out)
			}
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			return out
		}
		absent, _ := enc(&pb.DesiredState{Revision: 1})
		empty, _ := enc(&pb.DesiredState{Revision: 1, Relays: &pb.RelayForwards{}})
		if dec(absent).Relays != nil {
			t.Errorf("%s: an absent list came back present", name)
		}
		if got := dec(empty).Relays; got == nil || len(got.Items) != 0 {
			t.Errorf("%s: an empty list came back as %v", name, got)
		}
	}
}
