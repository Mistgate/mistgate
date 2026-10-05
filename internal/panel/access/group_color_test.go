package access

import (
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func TestGroupColor(t *testing.T) {
	e := newEnv(t)
	a := must(e.s.CreateGroup(e.ctx, req(&adminv1.CreateGroupRequest{Name: "a"}))).Msg.Group
	b := must(e.s.CreateGroup(e.ctx, req(&adminv1.CreateGroupRequest{Name: "b"}))).Msg.Group
	if a.Color != "lavender" || b.Color != "sand" {
		t.Errorf("new groups wear %q and %q: the least used tone each, palette order", a.Color, b.Color)
	}
	c := must(e.s.CreateGroup(e.ctx, req(&adminv1.CreateGroupRequest{Name: "c", Color: "rose"}))).Msg.Group
	if c.Color != "rose" {
		t.Errorf("asked for rose, got %q", c.Color)
	}

	// a name outside the palette is refused, on create and on update; "" is allowed (none picked)
	_, err := e.s.CreateGroup(e.ctx, req(&adminv1.CreateGroupRequest{Name: "d", Color: "purple"}))
	wantCode(t, err, connect.CodeInvalidArgument)
	bad := "#ff0000"
	_, err = e.s.UpdateGroup(e.ctx, req(&adminv1.UpdateGroupRequest{GroupId: a.Id, Color: &bad}))
	wantCode(t, err, connect.CodeInvalidArgument)
	if got := must(e.s.ListGroups(e.ctx, req(&adminv1.ListGroupsRequest{}))).Msg.Groups; len(got) != 3 {
		t.Errorf("a refused create left %d groups", len(got))
	}

	before := len(must(e.st.ListAudit(e.ctx, "", 0, 100)))
	mint := "mint"
	g := must(e.s.UpdateGroup(e.ctx, req(&adminv1.UpdateGroupRequest{GroupId: a.Id, Color: &mint, DryRun: true}))).Msg.Group
	if g.Color != "lavender" || len(must(e.st.ListAudit(e.ctx, "", 0, 100))) != before {
		t.Errorf("a dry run changed or audited the colour: %q", g.Color)
	}
	g = must(e.s.UpdateGroup(e.ctx, req(&adminv1.UpdateGroupRequest{GroupId: a.Id, Color: &mint}))).Msg.Group
	if g.Color != "mint" {
		t.Errorf("colour after the update: %q", g.Color)
	}
	rows := must(e.st.ListAudit(e.ctx, "", 0, 100))
	if len(rows) != before+1 || rows[0].Action != "group_update" {
		t.Errorf("a colour change is audited as group_update: %+v", rows)
	}
	// the other fields leave it alone, and "" clears it
	name := "a2"
	if g = must(e.s.UpdateGroup(e.ctx, req(&adminv1.UpdateGroupRequest{GroupId: a.Id, Name: &name}))).Msg.Group; g.Color != "mint" {
		t.Errorf("a rename changed the colour to %q", g.Color)
	}
	none := ""
	if g = must(e.s.UpdateGroup(e.ctx, req(&adminv1.UpdateGroupRequest{GroupId: a.Id, Color: &none}))).Msg.Group; g.Color != "" {
		t.Errorf("cleared colour = %q", g.Color)
	}
	for _, tone := range store.GroupTones {
		if _, err := e.s.UpdateGroup(e.ctx, req(&adminv1.UpdateGroupRequest{GroupId: a.Id, Color: &tone})); err != nil {
			t.Errorf("%s refused: %v", tone, err)
		}
	}
}
