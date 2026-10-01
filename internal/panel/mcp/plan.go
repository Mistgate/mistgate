package mcp

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Plan and apply. Nothing changes in one call. <tool>_plan reads what it needs to describe the change in
// the panel's own words, stores the validated arguments and hands out a one-time confirm token; <tool>_apply takes only that
// token, so the arguments cannot change between the two. The plan is bound to the token, the tool and the arguments, lives
// ten minutes, and for a dangerous change waits for the owner (ApprovalService, in the admin panel) before apply runs.

const (
	planTTL         = 10 * time.Minute
	maxOpenPerToken = 20
	maxAwaiting     = 50
)

// env is what the tools share.
type env struct {
	cfg  Config
	now  func() time.Time
	sems sync.Map // token id -> chan struct{}: concurrent tool calls
}

// Fact is one line of "what this will do", built by the panel from structured parameters and what it read. A value that
// came from data somebody could have written (a user or node name, a note, a node's own text) is untrusted. Code and
// Params say the same as Value for the owner's UI, which words them itself (integrations.proto ApprovalFact); the
// params named in UntrustedParams come from data and are shown quoted, apart from the panel's wording.
type Fact struct {
	Key             string            `json:"key"`
	Value           string            `json:"value"`
	Untrusted       bool              `json:"untrusted,omitempty"`
	Code            string            `json:"code,omitempty"`
	Params          map[string]string `json:"params,omitempty"`
	UntrustedParams []string          `json:"untrusted_params,omitempty"`
}

// Outcome is what came of a change, as a code the owner's UI words itself ("users_disabled" {n}), next to the English
// line the agent reads. A failure's code is the panel's own refusal where there is one ("no_trusted_bundle").
type Outcome struct {
	Code   string
	Params map[string]string
}

// outcomeError is an error that also says what it was as an Outcome; Error() is the agent's English text.
type outcomeError struct {
	msg string
	out Outcome
}

func (e *outcomeError) Error() string { return e.msg }

// failure is an error for the agent (msg) that the owner's UI words by code.
func failure(code, msg string, kv ...string) error {
	return &outcomeError{msg: msg, out: outcomeOf(code, kv...)}
}

func outcomeOf(code string, kv ...string) Outcome {
	o := Outcome{Code: code}
	if len(kv) > 0 {
		o.Params = map[string]string{}
		for i := 0; i+1 < len(kv); i += 2 {
			o.Params[kv[i]] = kv[i+1]
		}
	}
	return o
}

// outcomeOfError is the Outcome an error carries; a plain error has none.
func outcomeOfError(err error) Outcome {
	var oe *outcomeError
	if errors.As(err, &oe) {
		return oe.out
	}
	return Outcome{}
}

// done is what an apply step returns: the agent's English line and the outcome the owner's UI words itself.
type done struct {
	text string
	out  Outcome
}

func doneWith(code, text string, kv ...string) done {
	return done{text: text, out: outcomeOf(code, kv...)}
}

// paramsJSON is Params as stored ("" when there are none).
func (o Outcome) paramsJSON() string {
	if len(o.Params) == 0 {
		return ""
	}
	b, _ := json.Marshal(o.Params)
	return string(b)
}

// planned is what a tool's plan step finds out.
type planned struct {
	Summary  string // one English paragraph; never embeds untrusted text (that goes in Facts)
	Facts    []Fact
	Danger   []string // step_up | fleet | bulk: any entry makes the plan wait for the owner
	InnerRef string   // node_fix: the dry run's own plan id, never shown to the agent
	// Params, when set, are the arguments as the plan resolved them (a node name became its id, "every outdated node" became
	// the list the owner sees); they are what gets stored and applied. Nil: the arguments as given.
	Params any
}

// PlanOut is the result of a _plan tool.
type PlanOut struct {
	PlanID        string   `json:"plan_id"`
	Summary       string   `json:"summary"`
	Facts         []Fact   `json:"facts"`
	NeedsApproval bool     `json:"needs_approval"`
	Danger        []string `json:"danger,omitempty"`
	ExpiresInS    int      `json:"expires_in_s"`
	ConfirmToken  string   `json:"confirm_token"`
	Next          string   `json:"next"`
}

// ApplyOut is the result of an _apply tool.
type ApplyOut struct {
	PlanID string `json:"plan_id"`
	Status string `json:"status"`
	Result string `json:"result"`
}

// reasoned is implemented by every plan's arguments (they embed ReasonField): the agent's own words for the owner.
type reasoned[P any] interface {
	*P
	take() string
}

// ReasonField is embedded in the arguments of every _plan tool.
type ReasonField struct {
	Reason string `json:"reason,omitempty" jsonschema:"why you want this change, in a sentence; the owner sees it as a quote (at most 300 characters)"`
}

func (r *ReasonField) take() string {
	s := clean(r.Reason, maxReason)
	r.Reason = ""
	return s
}

type applyIn struct {
	ConfirmToken string `json:"confirm_token" jsonschema:"the confirm_token returned by the matching _plan tool"`
}

// changeSpec describes one change: its plan step, which reads and validates, and its apply step, which makes exactly one
// call that changes something.
type changeSpec[P any] struct {
	name   string
	min    Profile
	procs  []string
	desc   string
	danger bool // may need the owner (a danger rule can fire)
	plan   func(c *call, p P) (*planned, error)
	apply  func(c *call, p P, pl Plan) (done, error)
}

// change builds the two tools of a change.
func change[P any, PP reasoned[P]](s changeSpec[P]) []toolDef {
	planTool := toolDef{
		name: s.name + "_plan", min: s.min, procs: s.procs, free: true, isPlan: true, danger: s.danger,
		desc: s.desc + " Changes nothing yet: it returns a plan and a confirm_token for " + s.name + "_apply.",
		add: func(srv *mcp.Server, e *env, desc string) {
			mcp.AddTool(srv, &mcp.Tool{Name: s.name + "_plan", Description: desc, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr(false)}},
				func(ctx context.Context, req *mcp.CallToolRequest, in P) (*mcp.CallToolResult, any, error) {
					c, err := e.begin(ctx, req, readTimeout)
					if err != nil {
						return nil, nil, err
					}
					defer c.done()
					reason := PP(&in).take()
					pl, err := s.plan(c, in)
					if err != nil {
						return nil, nil, scrubError(err)
					}
					var stored any = in
					if pl.Params != nil {
						stored = pl.Params
					}
					params, err := json.Marshal(stored)
					if err != nil || len(params) > maxArgsJSON {
						return nil, nil, errors.New("the arguments are too large")
					}
					out, err := e.createPlan(c, s.name, params, reason, pl)
					if err != nil {
						return nil, nil, scrubError(err)
					}
					return planResult(out)
				})
		},
	}
	applyTool := toolDef{
		name: s.name + "_apply", min: s.min, procs: s.procs, danger: s.danger,
		desc: "Executes the plan made by " + s.name + "_plan, once. Takes only the confirm_token. Call it only after the user agreed to the plan" +
			" (and, when the plan says it needs the owner, after the owner approved it in the admin panel).",
		add: func(srv *mcp.Server, e *env, desc string) {
			mcp.AddTool(srv, &mcp.Tool{Name: s.name + "_apply", Description: desc, Annotations: &mcp.ToolAnnotations{DestructiveHint: ptr(s.danger), OpenWorldHint: ptr(false)}},
				func(ctx context.Context, req *mcp.CallToolRequest, in applyIn) (*mcp.CallToolResult, any, error) {
					c, err := e.begin(ctx, req, applyTimeout)
					if err != nil {
						return nil, nil, err
					}
					defer c.done()
					out, err := e.apply(c, s.name, in.ConfirmToken, func(c *call, pl Plan) (done, error) {
						var p P
						if err := strictJSON([]byte(pl.ParamsJSON), &p); err != nil {
							return done{}, failure("plan_unreadable", "the stored plan is unreadable: make a new plan")
						}
						return s.apply(c, p, pl)
					})
					if err != nil {
						return nil, nil, scrubError(err)
					}
					return result(out)
				})
		},
	}
	return []toolDef{planTool, applyTool}
}

func newPlanID() string {
	var b [16]byte
	rand.Read(b[:])
	return "pln_" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b[:]))
}

func newConfirm() string {
	var b [32]byte
	rand.Read(b[:])
	return "cf_" + base64.RawURLEncoding.EncodeToString(b[:])
}

func hashOf(s string) []byte {
	h := sha256.Sum256([]byte(s))
	return h[:]
}

// createPlan stores the plan and returns what the agent gets.
func (e *env) createPlan(c *call, tool string, params []byte, reason string, pl *planned) (*PlanOut, error) {
	now := e.now()
	confirm := newConfirm()
	needs := len(pl.Danger) > 0
	status := StatusPlanned
	if needs {
		status = StatusAwaiting
	}
	facts := pl.Facts
	if facts == nil {
		facts = []Fact{}
	}
	factsJSON, err := json.Marshal(facts)
	if err != nil {
		return nil, errors.New("internal error")
	}
	danger := pl.Danger
	if danger == nil {
		danger = []string{}
	}
	dangerJSON, _ := json.Marshal(danger)
	p := Plan{
		ID: newPlanID(), TokenID: c.tokenID, Tool: tool, ParamsJSON: string(params), ParamsHash: hashOf(string(params)),
		ConfirmHash: hashOf(confirm), FactsJSON: string(factsJSON), Summary: pl.Summary, Danger: string(dangerJSON),
		NeedsApproval: needs, Reason: reason, InnerRef: pl.InnerRef, Status: status,
		CreatedAt: now, ExpiresAt: now.Add(planTTL),
	}
	if err := e.cfg.Plans.CreateMCPPlan(c.ctx, p, maxOpenPerToken, maxAwaiting); err != nil {
		if errors.Is(err, ErrTooManyPlans) {
			return nil, errors.New("too many open plans: apply or let the old ones expire first")
		}
		e.cfg.Log.Error("mcp: store plan", "err", err)
		return nil, errors.New("could not store the plan")
	}
	e.audit(c, "mcp_plan", map[string]any{"plan_id": p.ID, "tool": tool, "needs_approval": needs, "danger": danger}, "ok")
	out := &PlanOut{
		PlanID: p.ID, Summary: pl.Summary, Facts: facts, NeedsApproval: needs, Danger: pl.Danger,
		ExpiresInS: int(planTTL / time.Second), ConfirmToken: confirm,
	}
	if needs {
		out.Next = fmt.Sprintf("Waiting for the owner to approve plan %s in the admin panel (Settings, Approvals). Tell the user. "+
			"Do not poll more than once every 30 seconds; call %s_apply with the confirm_token after the owner approved.", p.ID, tool)
	} else {
		out.Next = "Show the plan to the user and, once they agree, call " + tool + "_apply with the confirm_token."
	}
	return out, nil
}

func (e *env) audit(c *call, action string, params map[string]any, result string) {
	if e.cfg.Audit != nil {
		e.cfg.Audit(context.WithoutCancel(c.ctx), AuditEntry{Actor: "mcp:" + c.tokenID, Action: action, Params: params, Result: result})
	}
}

var errUnknownConfirm = errors.New("unknown confirm token")

// stateError is what an apply says for a plan that cannot run: one message per state. It returns
// nil for a plan that may run (planned or approved, and not expired). A replay of an applied plan is handled by the caller.
func stateError(p Plan, now time.Time) error {
	switch p.Status {
	case StatusPlanned, StatusApproved:
		if !now.Before(p.ExpiresAt) {
			return errors.New("expired: make a new plan")
		}
		return nil
	case StatusAwaiting:
		if !now.Before(p.ExpiresAt) {
			return errors.New("expired: make a new plan")
		}
		return fmt.Errorf("waiting for the owner to approve plan %s; it expires at %s", p.ID, p.ExpiresAt.UTC().Format(time.RFC3339))
	case StatusRejected:
		return errors.New("rejected by the owner")
	case StatusExpired:
		return errors.New("expired: make a new plan")
	case StatusApplying:
		return errors.New("already running")
	case StatusFailed:
		return errors.New("failed: " + clean(p.Error, maxError) + ". Make a new plan.")
	case StatusCancelled:
		return errors.New("the token was revoked")
	}
	return errUnknownConfirm
}

// apply runs the plan behind a confirm token. run makes the one procedure call.
func (e *env) apply(c *call, tool, confirm string, run func(c *call, pl Plan) (done, error)) (*ApplyOut, error) {
	if len(confirm) > 100 || !strings.HasPrefix(confirm, "cf_") {
		return nil, errUnknownConfirm
	}
	confirmHash := hashOf(confirm)
	pl, err := e.cfg.Plans.MCPPlanByConfirm(c.ctx, c.tokenID, confirmHash)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, errUnknownConfirm
		}
		e.cfg.Log.Error("mcp: find plan", "err", err)
		return nil, errors.New("could not look the plan up")
	}
	// One message for a token of another token, a plan of another tool and a token that never existed: no oracle.
	if pl.Tool != tool || pl.TokenID != c.tokenID || subtle.ConstantTimeCompare(pl.ConfirmHash, confirmHash) != 1 {
		return nil, errUnknownConfirm
	}
	if pl.Status == StatusApplied {
		return &ApplyOut{PlanID: pl.ID, Status: StatusApplied, Result: clean(pl.Result, maxError)}, nil // a replay is safe
	}
	if err := stateError(pl, e.now()); err != nil {
		return nil, err
	}
	// The hash is of the stored arguments as they are now: a row edited behind our back refuses to start.
	started, err := e.cfg.Plans.BeginApply(c.ctx, pl.ID, c.tokenID, tool, hashOf(pl.ParamsJSON), confirmHash, e.now())
	if err != nil {
		// lost a race, expired in between, or the arguments no longer match their hash
		if cur, gerr := e.cfg.Plans.GetMCPPlan(c.ctx, pl.ID); gerr == nil {
			if cur.Status == StatusApplied {
				return &ApplyOut{PlanID: cur.ID, Status: StatusApplied, Result: clean(cur.Result, maxError)}, nil
			}
			if serr := stateError(cur, e.now()); serr != nil {
				return nil, serr
			}
		}
		return nil, errors.New("the plan could not be started: make a new plan")
	}
	rc := *c
	if started.NeedsApproval {
		// The owner's approval is what lets a step-up procedure run for this one call; the grant dies with the apply.
		rc.ctx = e.cfg.Auth.WithApprovedStepUp(c.ctx, started.ID)
	}
	res, runErr := run(&rc, started)
	fctx := context.WithoutCancel(c.ctx) // a cancelled request must not leave the plan "applying"
	if runErr != nil {
		msg, out := clean(runErr.Error(), maxError), outcomeOfError(runErr)
		if errors.Is(runErr, context.DeadlineExceeded) || errors.Is(c.ctx.Err(), context.DeadlineExceeded) {
			msg, out = "timeout, outcome unknown: check before retrying", Outcome{Code: "timeout"}
		}
		if ferr := e.cfg.Plans.FinishApply(fctx, started.ID, false, "", msg, out.Code, out.paramsJSON(), e.now()); ferr != nil {
			e.cfg.Log.Error("mcp: finish apply", "err", ferr)
		}
		e.audit(c, "mcp_apply", map[string]any{"plan_id": started.ID, "tool": tool, "result": "failed: " + msg}, "fail")
		return nil, errors.New(msg)
	}
	text := clean(res.text, maxError)
	if ferr := e.cfg.Plans.FinishApply(fctx, started.ID, true, text, "", res.out.Code, res.out.paramsJSON(), e.now()); ferr != nil {
		e.cfg.Log.Error("mcp: finish apply", "err", ferr)
	}
	e.audit(c, "mcp_apply", map[string]any{"plan_id": started.ID, "tool": tool, "result": StatusApplied}, "ok")
	return &ApplyOut{PlanID: started.ID, Status: StatusApplied, Result: text}, nil
}

// strictJSON decodes into v and refuses unknown fields (used for stored arguments).
func strictJSON(b []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}
