package access

import (
	"context"
	"crypto/sha256"
	"net/http"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// approvedPlanGrant builds an owner-approved MCP plan of tool for token id in status applying, as plan.go apply() does.
func approvedPlanGrant(t *testing.T, c *capEnv, id, tool string) context.Context {
	t.Helper()
	e := c.e
	confirm := sha256.Sum256([]byte(store.NewID("cf_")))
	now := time.Now()
	p := store.MCPPlan{TokenID: id, Tool: tool, ParamsJSON: `{}`, FactsJSON: "[]", Summary: "s", Danger: `["fleet"]`, ConfirmHash: confirm[:],
		NeedsApproval: true, Status: store.PlanAwaiting, CreatedAt: now}
	if err := e.st.CreateMCPPlan(e.ctx, p, 20, 50); err != nil {
		t.Fatal(err)
	}
	got, err := e.st.MCPPlanByConfirm(e.ctx, id, confirm[:])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.DecideMCPPlan(e.ctx, got.ID, c.owner, true, now); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256([]byte(got.ParamsJSON))
	if _, err := e.st.BeginApply(e.ctx, got.ID, id, tool, hash[:], got.ConfirmHash, now); err != nil {
		t.Fatal(err)
	}
	return c.auth.WithApprovedStepUp(auth.WithChannel(e.ctx, auth.ChannelMCP), got.ID)
}

func groupsAs(c *capEnv, id string, grant context.Context) adminv1connect.GroupServiceClient {
	var wrap func(context.Context) context.Context
	if grant != nil {
		wrap = func(context.Context) context.Context { return grant }
	}
	return adminv1connect.NewGroupServiceClient(&http.Client{Transport: inProcess{h: c.h, secret: c.secrets[id], wrap: wrap}}, "http://panel.test")
}

// A token reaches UpdateGroup only through a plan the owner approved. Such a plan is applied whatever the size of the
// group: counting its members against the unattended limit would refuse every approved change of a group of more
// than ten people.
func TestOwnerApprovedGroupChangeIsNotStoppedByTheTokenLimit(t *testing.T) {
	for _, tool := range []string{"group_update", "profile_create", "profile_deploy"} {
		t.Run(tool, func(t *testing.T) {
			c := newCapEnv(t)
			e := c.e
			c.users(12)
			id, _ := c.token("agent", false)
			second := e.profile("hy2 8443", `{"port":8443}`)
			_, err := groupsAs(c, id, approvedPlanGrant(t, c, id, tool)).UpdateGroup(e.ctx, connect.NewRequest(&adminv1.UpdateGroupRequest{
				GroupId: c.group, ProfileIds: &adminv1.ProfileIds{Values: []string{c.profile, second.Id}}}))
			if err != nil {
				t.Errorf("owner-approved %s apply on a 12-person group refused: %v", tool, err)
			}
		})
	}
}

// And it spends none of the token's unattended units.
func TestOwnerApprovedGroupChangeSpendsNothing(t *testing.T) {
	c := newCapEnv(t)
	e := c.e
	c.users(4)
	id, _ := c.token("agent", false)
	second := e.profile("hy2 8443", `{"port":8443}`)
	if _, err := groupsAs(c, id, approvedPlanGrant(t, c, id, "group_update")).UpdateGroup(e.ctx, connect.NewRequest(&adminv1.UpdateGroupRequest{
		GroupId: c.group, ProfileIds: &adminv1.ProfileIds{Values: []string{c.profile, second.Id}}})); err != nil {
		t.Fatal(err)
	}
	if got := c.remaining(id); got != TokenUserChangeLimit {
		t.Errorf("approved group_update spent unattended units: %d left, want %d", got, TokenUserChangeLimit)
	}
}
