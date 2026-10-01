// Package mcp is the panel's MCP server for agents: the tools of the token profiles over
// Streamable HTTP, and the stdio proxy that `mistgate mcp` runs.
//
// The tools do not reach into stores. They call the admin API the way any client does, in process, with the Bearer header
// the agent sent, so the role policy, the token allow-list, the handlers' own validation and the audit rows are the
// panel's own (inproc.go). A change is two tools, <tool>_plan and <tool>_apply (plan.go); a dangerous one also waits for
// the owner's click in the admin panel. What an agent reads is built from explicit projections (views.go), cut to size and
// scrubbed (limits.go, scrub.go): names, notes and logs are untrusted text and say so in every tool description.
package mcp

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

// Profile is what a token may do: readonly (reads), operator (+ day-to-day changes), admin (+ the dangerous ones, through
// the owner's approval). The roles they map to (readonly, helper, owner) are the auth package's business.
type Profile string

const (
	ProfileReadonly Profile = "readonly"
	ProfileOperator Profile = "operator"
	ProfileAdmin    Profile = "admin"
)

var profiles = []Profile{ProfileReadonly, ProfileOperator, ProfileAdmin}

// rank orders the profiles; an unknown one ranks below readonly and sees nothing.
func (p Profile) rank() int {
	for i, q := range profiles {
		if p == q {
			return i + 1
		}
	}
	return 0
}

// Principal is the token behind a request, as the bearer layer resolved it.
type Principal struct {
	TokenID string
	Profile Profile
}

// Auth is what the MCP layer needs from the panel's auth (internal/panel/auth, wired in cmd/mistgate). The bearer
// middleware in front of the MCP handler has already authenticated the request and put the principal in its context.
type Auth interface {
	// Principal is the token of the request (the handler's request context, as the bearer middleware left it).
	Principal(ctx context.Context) (Principal, bool)
	// WithChannel marks an in-process call as the MCP layer's own: audited as mcp:<id>, not rate limited a second time.
	WithChannel(ctx context.Context) context.Context
	// WithPlanning lets a token-approved procedure through for a call that changes nothing (the dry run of node_fix).
	WithPlanning(ctx context.Context) context.Context
	// WithApprovedStepUp lets one token-approved procedure through, and its step-up check, while plan planID is
	// applying, belongs to the calling token and has the owner's recorded approval. Checked in the database on every use.
	WithApprovedStepUp(ctx context.Context, planID string) context.Context
}

// Plan states, the values of mcp_plan.status.
const (
	StatusPlanned   = "planned"
	StatusAwaiting  = "awaiting"
	StatusApproved  = "approved"
	StatusRejected  = "rejected"
	StatusExpired   = "expired"
	StatusApplying  = "applying"
	StatusApplied   = "applied"
	StatusFailed    = "failed"
	StatusCancelled = "cancelled"
)

// Plan is one change an agent asked for. The fields (names, types, order) are those of store.MCPPlan, so the wiring in
// cmd/mistgate converts one to the other with a plain conversion.
type Plan struct {
	ID, TokenID, TokenName, TokenProfile, Tool               string
	ParamsJSON, FactsJSON, Summary, Danger, Reason, InnerRef string
	ParamsHash, ConfirmHash                                  []byte
	NeedsApproval                                            bool
	Status                                                   string
	CreatedAt, ExpiresAt, DecidedAt, AppliedAt               time.Time
	DecidedBy, DecidedByName, Result, Error                  string
	OutcomeCode, OutcomeParams                               string // what came of it, for the owner's UI (Outcome)
}

// Errors the Plans implementation returns (the wiring maps the store's own to these).
var (
	// ErrNotFound: no such plan (for this token).
	ErrNotFound = errors.New("not found")
	// ErrTooManyPlans: the token or the panel has too many open plans.
	ErrTooManyPlans = errors.New("too many open plans")
)

// Plans is the storage of plans: *store.Store through the wiring in cmd/mistgate.
type Plans interface {
	CreateMCPPlan(ctx context.Context, p Plan, maxOpenPerToken, maxAwaiting int) error
	// MCPPlanByConfirm finds the plan of this token with this confirm-token hash; ErrNotFound otherwise.
	MCPPlanByConfirm(ctx context.Context, tokenID string, confirmHash []byte) (Plan, error)
	GetMCPPlan(ctx context.Context, id string) (Plan, error)
	// BeginApply is the compare-and-swap planned|approved -> applying (same token, tool, hashes, not expired). Any error
	// means "not started"; the caller reads the plan again to say why.
	BeginApply(ctx context.Context, id, tokenID, tool string, paramsHash, confirmHash []byte, now time.Time) (Plan, error)
	// FinishApply ends the apply with the agent's English result or error and the outcome (code and JSON params).
	FinishApply(ctx context.Context, id string, ok bool, result, errText, outcomeCode, outcomeParams string, now time.Time) error
}

// AuditEntry is a row for the audit log; the source is always "mcp".
type AuditEntry struct {
	Actor  string // "mcp:<token id>"
	Action string // "mcp_plan", "mcp_apply"
	Params map[string]any
	Result string // "ok" | "fail"
}

// Config wires the MCP layer.
type Config struct {
	Plans Plans
	Auth  Auth
	// API is the in-process admin API: auth.RequireSession over the services, request paths without the "/api" prefix
	// ("/mistgate.admin.v1.UserService/GetUser").
	API http.Handler
	// Audit writes a row; nil drops them (tests).
	Audit func(ctx context.Context, e AuditEntry)
	Now   func() time.Time // default time.Now
	Log   *slog.Logger
}
