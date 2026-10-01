package auth

import (
	"context"
	"slices"
	"time"

	"github.com/mistgate/mistgate/internal/panel/store"
)

// Who is calling, as far as API tokens are concerned.
//
// A request made with an API token carries a synthetic store.Admin in the context (AdminFrom): its ID is
// "token:<id>" or "mcp:<id>", so every module that writes AdminFrom(ctx).ID to the audit log records the token
// without knowing tokens exist, and its Role is the role of the token's profile. Principal says it is a token.

// Channel is how a token call arrived.
type Channel string

const (
	// ChannelAPI is a Connect-RPC call with a Bearer header (a script).
	ChannelAPI Channel = "api"
	// ChannelMCP is a call the MCP layer makes in process on behalf of an agent.
	ChannelMCP Channel = "mcp"
)

// Principal describes the token behind a request. The zero value is a signed-in admin (a cookie session).
type Principal struct {
	Token     bool   // false: an admin signed in with a cookie session
	TokenID   string // "tok_..." when Token
	Profile   string // store.ProfileReadonly | ProfileOperator | ProfileAdmin
	Channel   Channel
	Name      string    // the token's name
	ExpiresAt time.Time // when the token stops working
}

// ActorID is the id the audit log shows for the principal: "token:<id>" for the API channel, "mcp:<id>" for MCP.
func (p Principal) ActorID() string {
	if p.Channel == ChannelMCP {
		return "mcp:" + p.TokenID
	}
	return "token:" + p.TokenID
}

type (
	principalKey struct{}
	channelKey   struct{}
	planningKey  struct{}
	procedureKey struct{}
	grantKey     struct{}
)

// PrincipalFrom returns the token principal RequireSession or RequireBearer put into ctx; the zero Principal for
// a cookie session or outside the middleware.
func PrincipalFrom(ctx context.Context) Principal {
	p, _ := ctx.Value(principalKey{}).(Principal)
	return p
}

func channelFrom(ctx context.Context) Channel {
	c, _ := ctx.Value(channelKey{}).(Channel)
	return c
}

// WithChannel marks ctx as belonging to a call the MCP layer makes in process: RequireSession then counts the
// call as "mcp:<id>", audits it with source mcp and does not charge the token's rate limit again (the /mcp
// request already paid it). The token is still verified against the database on every call. Only ChannelMCP
// has an effect, and only code that holds the context can set it: a network request cannot.
func WithChannel(ctx context.Context, c Channel) context.Context {
	if c != ChannelMCP {
		return ctx
	}
	return context.WithValue(ctx, channelKey{}, c)
}

// WithPlanning lets a TokenAccessApproved procedure through for a call that changes nothing: the dry run of a
// node fix, made when the agent asks for the plan. The MCP layer uses it only around HealthService.ApplyFix with
// dry_run = true. It does not open step-up, and it counts only on the MCP channel.
func WithPlanning(ctx context.Context) context.Context {
	return context.WithValue(ctx, planningKey{}, true)
}

// approvedGrant is the value WithApprovedStepUp stores. It names the service so a grant minted by one Service is
// not honoured by another, and carries nothing but the plan id: everything else is read from the database.
type approvedGrant struct {
	s      *Service
	planID string
}

// WithApprovedStepUp returns ctx holding the grant of plan planID. The grant lets a TokenAccessApproved
// procedure through and makes RequireStepUp return nil, but only while the plan, read from the database on every
// use, is applying, belongs to the calling token, needs approval and carries a recorded human decision, and only
// for the procedure its tool is made for (grantProcedures). It therefore dies the moment the apply finishes. The
// MCP layer derives it for exactly one procedure call per apply.
func (s *Service) WithApprovedStepUp(ctx context.Context, planID string) context.Context {
	return context.WithValue(ctx, grantKey{}, approvedGrant{s: s, planID: planID})
}

// grantAllows reports whether the approved grant in ctx is valid for tokenID calling the procedure in ctx.
func (s *Service) grantAllows(ctx context.Context, tokenID, procedure string) bool {
	g, ok := ctx.Value(grantKey{}).(approvedGrant)
	if !ok || g.s != s || g.planID == "" || tokenID == "" || procedure == "" || channelFrom(ctx) != ChannelMCP {
		return false
	}
	plan, err := s.st.GetMCPPlan(ctx, g.planID)
	if err != nil {
		return false
	}
	if plan.TokenID != tokenID || plan.Status != store.PlanApplying || !plan.NeedsApproval ||
		plan.DecidedBy == "" || plan.DecidedAt.IsZero() {
		return false
	}
	return slices.Contains(grantProcedureList(plan.Tool), procedure)
}

func grantProcedureList(tool string) []string {
	if p, ok := grantProcedures[tool]; ok {
		return []string{p}
	}
	return nil
}

// planningAllows reports whether ctx is an MCP planning call (WithPlanning on the MCP channel).
func planningAllows(ctx context.Context) bool {
	on, _ := ctx.Value(planningKey{}).(bool)
	return on && channelFrom(ctx) == ChannelMCP
}

func procedureFrom(ctx context.Context) string {
	p, _ := ctx.Value(procedureKey{}).(string)
	return p
}
