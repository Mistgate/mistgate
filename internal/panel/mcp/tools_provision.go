package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
)

var (
	sshUserShape  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.-]{0,31}$`)
	nodeNameShape = regexp.MustCompile(`^[a-zA-Z0-9-]{2,24}$`)
)

type accessListArgs struct{}

type serverAccessView struct {
	NodeID          string `json:"node_id"`
	Name            string `json:"name"`
	Host            string `json:"host"`
	Username        string `json:"username"`
	Fingerprint     string `json:"fingerprint"`
	ConfiguredUnix  int64  `json:"configured_unix"`
	RotationPending bool   `json:"rotation_pending"`
}

func nodeProvisionTools() []toolDef {
	list := readTool("node_server_access_list", ProfileAdmin,
		procs(adminv1connect.ProvisioningServiceListNodeServerAccessProcedure),
		"List SSH connection metadata for installed nodes. Passwords are never returned or revealed.",
		func(c *call, _ accessListArgs) (any, error) {
			r, err := c.cl.Provisioning.ListNodeServerAccess(c.ctx, connect.NewRequest(&adminv1.ListNodeServerAccessRequest{}))
			if err != nil {
				return nil, apiError(err)
			}
			out := make([]serverAccessView, 0, len(r.Msg.GetAccess()))
			for _, a := range r.Msg.GetAccess() {
				out = append(out, serverAccessView{
					NodeID: a.GetNodeId(), Name: a.GetNodeName(), Host: net.JoinHostPort(a.GetHost(), strconv.FormatUint(uint64(a.GetPort()), 10)),
					Username: a.GetUsername(), Fingerprint: a.GetFingerprint(), ConfiguredUnix: a.GetConfiguredUnix(),
					RotationPending: a.GetRotationPending(),
				})
			}
			return out, nil
		})
	return []toolDef{list, nodeInstallPlanTool(), nodeInstallApplyTool(), passwordRotatePlanTool(), passwordRotateApplyTool()}
}

type nodeInstallArgs struct {
	ReasonField
	Host        string `json:"host" jsonschema:"public SSH hostname or IP of a server you control"`
	Port        uint32 `json:"port,omitempty" jsonschema:"SSH port; defaults to 22"`
	Username    string `json:"username" jsonschema:"SSH login; root or a user with non-interactive sudo"`
	Name        string `json:"name" jsonschema:"unique node name, 2-24 letters, digits or hyphens"`
	Address     string `json:"address" jsonschema:"public address clients use to reach the node"`
	CountryCode string `json:"country_code,omitempty" jsonschema:"two-letter country code"`
	Location    string `json:"location,omitempty" jsonschema:"city or region"`
	Provider    string `json:"provider,omitempty" jsonschema:"hosting provider"`
}

type nodeInstallParams struct {
	Host, Username, Name, Address, CountryCode, Location, Provider, Fingerprint string
	Port                                                                        uint32
}

func nodeInstallPlanTool() toolDef {
	const base = "node_install"
	return toolDef{
		name: base + "_plan", min: ProfileAdmin, procs: procs(adminv1connect.ProvisioningServiceGetSSHFingerprintProcedure),
		free: true, isPlan: true, danger: true,
		desc: "Prepare installation of a Mistgate node over SSH. This only reads the public SSH host key. The agent must show the fingerprint and wait for explicit confirmation before applying.",
		add: func(s *mcp.Server, e *env, desc string) {
			mcp.AddTool(s, &mcp.Tool{Name: base + "_plan", Description: desc, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr(false)}},
				func(ctx context.Context, req *mcp.CallToolRequest, in nodeInstallArgs) (*mcp.CallToolResult, any, error) {
					c, err := e.begin(ctx, req, readTimeout)
					if err != nil {
						return nil, nil, err
					}
					defer c.done()
					reason := (&in.ReasonField).take()
					if in.Port == 0 {
						in.Port = 22
					}
					if in.Host == "" || len(in.Host) > 253 || in.Port > 65535 || !sshUserShape.MatchString(in.Username) ||
						!nodeNameShape.MatchString(in.Name) || in.Address == "" || len(in.Address) > 253 || len(in.Location) > 100 || len(in.Provider) > 100 {
						return nil, nil, errors.New("invalid SSH target, login or node metadata")
					}
					if in.CountryCode != "" && (len(in.CountryCode) != 2 || !allASCIIAlpha(in.CountryCode)) {
						return nil, nil, errors.New("country_code must be two letters")
					}
					fingerprint, err := c.cl.Provisioning.GetSSHFingerprint(c.ctx, connect.NewRequest(&adminv1.GetSSHFingerprintRequest{Host: in.Host, Port: in.Port}))
					if err != nil {
						return nil, nil, scrubError(apiError(err))
					}
					resolved := nodeInstallParams{Host: fingerprint.Msg.GetHost(), Port: fingerprint.Msg.GetPort(), Username: in.Username,
						Name: strings.ToLower(in.Name), Address: in.Address, CountryCode: strings.ToUpper(in.CountryCode),
						Location: in.Location, Provider: in.Provider, Fingerprint: fingerprint.Msg.GetFingerprint()}
					params, err := json.Marshal(resolved)
					if err != nil {
						return nil, nil, errors.New("could not store the plan")
					}
					planned := &planned{
						Summary: "Install a new Mistgate agent on one server after the owner approves the SSH host key and preflight checks.",
						Facts: []Fact{{Key: "node", Value: nm(resolved.Name), Untrusted: true}, {Key: "ssh", Value: net.JoinHostPort(resolved.Host, strconv.FormatUint(uint64(resolved.Port), 10)), Untrusted: true},
							{Key: "username", Value: nm(resolved.Username), Untrusted: true}, {Key: "host_key", Value: resolved.Fingerprint},
							{Key: "address", Value: nm(resolved.Address), Untrusted: true}, {Key: "installation", Value: "signed current agent bundle; waits for the new node to connect"}},
						Danger: []string{dangerStepUp, dangerFleet}, Params: resolved,
					}
					out, err := e.createPlan(c, base, params, reason, planned)
					if err != nil {
						return nil, nil, scrubError(err)
					}
					return planResult(out)
				})
		},
	}
}

type nodeInstallApplyArgs struct {
	ConfirmToken         string `json:"confirm_token" jsonschema:"token returned by node_install_plan"`
	Password             string `json:"password" jsonschema:"SSH password; supplied only to this call and never added to the plan"`
	ConfirmedFingerprint string `json:"confirmed_fingerprint" jsonschema:"the exact SHA-256 host key shown by node_install_plan, after the owner confirms it"`
}

func nodeInstallApplyTool() toolDef {
	const base = "node_install"
	return toolDef{
		name: base + "_apply", min: ProfileAdmin, procs: procs(adminv1connect.ProvisioningServiceStartNodeProvisionProcedure),
		danger: true, desc: "Apply an owner-approved node_install plan. Provide the SSH password only here, after the owner confirmed the exact host fingerprint in the plan.",
		add: func(s *mcp.Server, e *env, desc string) {
			mcp.AddTool(s, &mcp.Tool{Name: base + "_apply", Description: desc, Annotations: &mcp.ToolAnnotations{DestructiveHint: ptr(true), OpenWorldHint: ptr(false)}},
				func(ctx context.Context, req *mcp.CallToolRequest, in nodeInstallApplyArgs) (*mcp.CallToolResult, any, error) {
					c, err := e.begin(ctx, req, applyTimeout)
					if err != nil {
						return nil, nil, err
					}
					defer c.done()
					if in.Password == "" || in.ConfirmedFingerprint == "" {
						return nil, nil, errors.New("password and confirmed_fingerprint are required")
					}
					out, err := e.apply(c, base, in.ConfirmToken, func(c *call, pl Plan) (done, error) {
						var p nodeInstallParams
						if err := strictJSON([]byte(pl.ParamsJSON), &p); err != nil {
							return done{}, failure("plan_unreadable", "make a new node installation plan")
						}
						if in.ConfirmedFingerprint != p.Fingerprint {
							return done{}, errors.New("confirmed_fingerprint does not match the plan; stop and review the host key")
						}
						started, err := c.cl.Provisioning.StartNodeProvision(c.ctx, connect.NewRequest(&adminv1.StartNodeProvisionRequest{
							ConfirmInstall: true, Name: p.Name, Address: p.Address, CountryCode: p.CountryCode, Location: p.Location, Provider: p.Provider,
							SshHost: p.Host, SshPort: p.Port, SshUsername: p.Username, Fingerprint: p.Fingerprint, Password: in.Password,
						}))
						if err != nil {
							return done{}, apiError(err)
						}
						job := started.Msg.GetJob()
						return doneWith("node_install_queued", fmt.Sprintf("Node installation %s queued; the panel will verify the agent connection before saving SSH access.", clean(job.GetId(), 64)), "job_id", clean(job.GetId(), 64)), nil
					})
					if err != nil {
						return nil, nil, scrubError(err)
					}
					in.Password = ""
					return result(out)
				})
		},
	}
}

type passwordRotatePlanArgs struct {
	ReasonField
	Node string `json:"node" jsonschema:"installed node id or exact name"`
}

type passwordRotateParams struct{ NodeID, Name, Host, Username string }

func passwordRotatePlanTool() toolDef {
	const base = "node_server_password_rotate"
	return toolDef{
		name: base + "_plan", min: ProfileAdmin, procs: procs(adminv1connect.ProvisioningServiceListNodeServerAccessProcedure,
			adminv1connect.ProvisioningServiceRotateNodeServerPasswordProcedure),
		free: true, isPlan: true, danger: true,
		desc: "Prepare to generate and install a new random SSH password for one installed node. This plan contains only the node id and public login metadata; the panel generates the password only after owner approval.",
		add: func(s *mcp.Server, e *env, desc string) {
			mcp.AddTool(s, &mcp.Tool{Name: base + "_plan", Description: desc, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr(false)}},
				func(ctx context.Context, req *mcp.CallToolRequest, in passwordRotatePlanArgs) (*mcp.CallToolResult, any, error) {
					c, err := e.begin(ctx, req, readTimeout)
					if err != nil {
						return nil, nil, err
					}
					defer c.done()
					reason := (&in.ReasonField).take()
					r, err := c.cl.Provisioning.ListNodeServerAccess(c.ctx, connect.NewRequest(&adminv1.ListNodeServerAccessRequest{}))
					if err != nil {
						return nil, nil, scrubError(apiError(err))
					}
					var found *adminv1.NodeServerAccess
					for _, item := range r.Msg.GetAccess() {
						if item.GetNodeId() == in.Node || item.GetNodeName() == in.Node {
							if found != nil {
								return nil, nil, errors.New("several nodes match; use the node id")
							}
							found = item
						}
					}
					if found == nil {
						return nil, nil, errors.New("node has no saved SSH access")
					}
					p := passwordRotateParams{NodeID: found.GetNodeId(), Name: found.GetNodeName(), Host: net.JoinHostPort(found.GetHost(), strconv.FormatUint(uint64(found.GetPort()), 10)), Username: found.GetUsername()}
					params, err := json.Marshal(p)
					if err != nil {
						return nil, nil, errors.New("could not store the plan")
					}
					pl := &planned{Summary: "Change the SSH login password for one installed node and verify the new login before saving it.", Facts: []Fact{{Key: "node", Value: nm(p.Name), Untrusted: true}, {Key: "ssh", Value: p.Host, Untrusted: true}, {Key: "username", Value: nm(p.Username), Untrusted: true}, {Key: "recovery", Value: "the old encrypted password remains available until the new login is verified"}}, Danger: []string{dangerStepUp, dangerFleet}, Params: p}
					out, err := e.createPlan(c, base, params, reason, pl)
					if err != nil {
						return nil, nil, scrubError(err)
					}
					return planResult(out)
				})
		},
	}
}

type passwordRotateApplyArgs struct {
	ConfirmToken string `json:"confirm_token" jsonschema:"token returned by node_server_password_rotate_plan"`
}

func passwordRotateApplyTool() toolDef {
	const base = "node_server_password_rotate"
	return toolDef{name: base + "_apply", min: ProfileAdmin, procs: procs(adminv1connect.ProvisioningServiceListNodeServerAccessProcedure, adminv1connect.ProvisioningServiceRotateNodeServerPasswordProcedure), danger: true,
		desc: "Apply an owner-approved SSH password rotation plan. The panel generates and installs a strong random password; it is never returned to the agent. The owner can reveal it later in node Settings after step-up verification.",
		add: func(s *mcp.Server, e *env, desc string) {
			mcp.AddTool(s, &mcp.Tool{Name: base + "_apply", Description: desc, Annotations: &mcp.ToolAnnotations{DestructiveHint: ptr(true), OpenWorldHint: ptr(false)}},
				func(ctx context.Context, req *mcp.CallToolRequest, in passwordRotateApplyArgs) (*mcp.CallToolResult, any, error) {
					c, err := e.begin(ctx, req, applyTimeout)
					if err != nil {
						return nil, nil, err
					}
					defer c.done()
					out, err := e.apply(c, base, in.ConfirmToken, func(c *call, pl Plan) (done, error) {
						var p passwordRotateParams
						if err := strictJSON([]byte(pl.ParamsJSON), &p); err != nil {
							return done{}, failure("plan_unreadable", "make a new SSH password rotation plan")
						}
						return rotateGeneratedNodePassword(c.ctx, func(ctx context.Context, password string) error {
							_, err := c.cl.Provisioning.RotateNodeServerPassword(ctx, connect.NewRequest(&adminv1.RotateNodeServerPasswordRequest{NodeId: p.NodeID, NewPassword: password, Confirm: true}))
							return err
						})
					})
					if err != nil {
						return nil, nil, scrubError(err)
					}
					return result(out)
				})
		}}
}

func rotateGeneratedNodePassword(ctx context.Context, rotate func(context.Context, string) error) (done, error) {
	raw := make([]byte, 32)
	defer clear(raw)
	if _, err := rand.Read(raw); err != nil {
		return done{}, errors.New("could not generate a node password")
	}
	password := hex.EncodeToString(raw)
	defer func() { password = "" }()
	if err := rotate(ctx, password); err != nil {
		return done{}, apiError(err)
	}
	return doneWith("node_ssh_password_rotated", "The node SSH password was generated inside the panel, changed, and verified. The owner can reveal it in node Settings."), nil
}

func allASCIIAlpha(s string) bool {
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}
