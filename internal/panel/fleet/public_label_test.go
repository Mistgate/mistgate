package fleet

import (
	"strings"
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// The public label of a node (migration 00129): the owner's short text for people. Optional, trimmed, at most 40 characters,
// one plain line; unset leaves it alone and "" clears it.
func TestPublicLabelOfANode(t *testing.T) {
	x, a := newL3Env(t)
	service := nodeService{x.f}
	update := func(label *string) error {
		_, err := service.UpdateNode(x.ctx, connect.NewRequest(&adminv1.UpdateNodeRequest{NodeId: a.nodeID, PublicLabel: label}))
		return err
	}
	get := func() string {
		t.Helper()
		resp, err := service.GetNode(x.ctx, connect.NewRequest(&adminv1.GetNodeRequest{NodeId: a.nodeID}))
		if err != nil {
			t.Fatal(err)
		}
		return resp.Msg.Node.PublicLabel
	}
	if got := get(); got != "" {
		t.Fatalf("a new node has the label %q", got)
	}
	if err := update(ptr("  Home route  ")); err != nil {
		t.Fatal(err)
	}
	if got := get(); got != "Home route" {
		t.Errorf("label = %q, want it trimmed", got)
	}
	// A call that does not name the label leaves it as it is (the other forms of the node never send it).
	if _, err := service.UpdateNode(x.ctx, connect.NewRequest(&adminv1.UpdateNodeRequest{NodeId: a.nodeID, Notes: ptr("note")})); err != nil {
		t.Fatal(err)
	}
	if got := get(); got != "Home route" {
		t.Errorf("an update of the notes changed the label to %q", got)
	}
	// Forty characters pass, counted as characters and not bytes; forty-one and anything that is not one plain line do not.
	forty := strings.Repeat("я", 40)
	if err := update(ptr(forty)); err != nil || get() != forty {
		t.Fatalf("40 characters: %v %q", err, get())
	}
	for name, bad := range map[string]string{
		"41 characters": strings.Repeat("я", 41), "newline": "a\nb", "tab": "a\tb", "NUL": "a\x00b", "DEL": "a\x7fb", "C1 control": "a\u0085b",
		"line separator": "a\u2028b", "paragraph separator": "a\u2029b", "invalid UTF-8": "a\xffb",
	} {
		if err := update(ptr(bad)); code(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: %v", name, err)
		}
	}
	if got := get(); got != forty {
		t.Errorf("a refused call changed the label to %q", got)
	}
	if err := update(ptr("")); err != nil || get() != "" {
		t.Errorf("clearing: %v %q", err, get())
	}
	if err := update(ptr("   ")); err != nil || get() != "" {
		t.Errorf("blanks: %v %q", err, get())
	}
}
