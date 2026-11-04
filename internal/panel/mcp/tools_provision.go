package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"connectrpc.com/connect"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/provision"
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
	Retired         bool   `json:"retired,omitempty"`
	PasswordSaved   bool   `json:"password_saved"`
	PasswordLogin   bool   `json:"password_login"`
	PanelKeyFP      string `json:"panel_key_fingerprint"`
}

func nodeProvisionTools() []toolDef {
	list := readTool("node_server_access_list", ProfileAdmin,
		procs(adminv1connect.ProvisioningServiceListNodeServerAccessProcedure),
		"List SSH connection metadata for installed nodes, retired ones marked (their access stays until the owner forgets it). Passwords are never returned or revealed.",
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
					RotationPending: a.GetRotationPending(), Retired: a.GetNodeRetired(),
					PasswordSaved: a.GetPasswordSaved(), PasswordLogin: a.GetPasswordLogin(), PanelKeyFP: a.GetPanelKeyFingerprint(),
				})
			}
			return out, nil
		})
	return []toolDef{
		list, nodeInstallPlanTool(), nodeInstallApplyTool(), passwordRotatePlanTool(), passwordRotateApplyTool(),
		nodeServerKeysListTool(), nodeServerKeyAddPlanTool(), nodeServerKeyAddApplyTool(),
		nodeServerKeyRemovePlanTool(), nodeServerKeyRemoveApplyTool(),
	}
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
		name: base + "_plan", min: ProfileAdmin, procs: procs(
			adminv1connect.ProvisioningServiceGetSSHFingerprintProcedure,
			adminv1connect.ProvisioningServiceGetPanelSSHKeyProcedure),
		free: true, isPlan: true, danger: true,
		desc: "Prepare installation of a Mistgate node over SSH. This reads the public SSH host key and panel public key. Never ask for or pass a server password: " +
			"the owner compares the host key fingerprint and chooses Password or Panel key on the approval screen in the admin panel. Tell the user to approve the plan there.",
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
						!nodeNameShape.MatchString(in.Name) || in.Address == "" || len(in.Address) > 253 || utf8.RuneCountInString(in.Location) > 100 || utf8.RuneCountInString(in.Provider) > 100 {
						return nil, nil, errors.New("invalid SSH target, login or node metadata")
					}
					if in.CountryCode != "" && (len(in.CountryCode) != 2 || !allASCIIAlpha(in.CountryCode)) {
						return nil, nil, errors.New("country_code must be two letters")
					}
					// Both reads run under planning grants; neither sends an SSH login to the target.
					fingerprint, err := c.cl.Provisioning.GetSSHFingerprint(c.e.cfg.Auth.WithPlanning(c.ctx), connect.NewRequest(&adminv1.GetSSHFingerprintRequest{Host: in.Host, Port: in.Port}))
					if err != nil {
						return nil, nil, scrubError(apiError(err))
					}
					panelKey, err := c.cl.Provisioning.GetPanelSSHKey(c.e.cfg.Auth.WithPlanning(c.ctx), connect.NewRequest(&adminv1.GetPanelSSHKeyRequest{}))
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
					// The owner compares host_key and chooses a login method on the approval screen. The public panel key is
					// safe to show there; no password or private key passes through MCP.
					planned := &planned{
						Summary: "Install a new Mistgate agent on one server. The owner confirms the SSH host key and chooses Password or Panel key on the approval screen; no password passes through MCP or reaches the agent.",
						Facts: []Fact{{Key: "node", Value: nm(resolved.Name), Untrusted: true}, {Key: "ssh", Value: net.JoinHostPort(resolved.Host, strconv.FormatUint(uint64(resolved.Port), 10)), Untrusted: true},
							{Key: "username", Value: nm(resolved.Username), Untrusted: true}, {Key: "host_key", Value: resolved.Fingerprint},
							{Key: "host_key_algorithm", Value: clean(fingerprint.Msg.GetAlgorithm(), 64)},
							{Key: "panel_ssh_key", Value: panelKey.Msg.GetPublicKey()},
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

func nodeInstallApplyTool() toolDef {
	const base = "node_install"
	return toolDef{
		name: base + "_apply", min: ProfileAdmin, procs: procs(adminv1connect.ProvisioningServiceStartNodeProvisionProcedure),
		danger: true, desc: "Apply an owner-approved node_install plan. Takes only the confirm_token: the owner chose Password or Panel key and confirmed the host key on the approval screen. A password install also installs the panel key by default.",
		add: func(s *mcp.Server, e *env, desc string) {
			mcp.AddTool(s, &mcp.Tool{Name: base + "_apply", Description: desc, Annotations: &mcp.ToolAnnotations{DestructiveHint: ptr(true), OpenWorldHint: ptr(false)}},
				func(ctx context.Context, req *mcp.CallToolRequest, in applyIn) (*mcp.CallToolResult, any, error) {
					c, err := e.begin(ctx, req, applyTimeout)
					if err != nil {
						return nil, nil, err
					}
					defer c.done()
					out, err := e.apply(c, base, in.ConfirmToken, func(c *call, pl Plan) (done, error) {
						var p nodeInstallParams
						if err := strictJSON([]byte(pl.ParamsJSON), &p); err != nil {
							return done{}, failure("plan_unreadable", "make a new node installation plan")
						}
						// No password here: plan_id lets the panel take the one the owner entered when approving this plan.
						started, err := c.cl.Provisioning.StartNodeProvision(c.ctx, connect.NewRequest(&adminv1.StartNodeProvisionRequest{
							ConfirmInstall: true, Name: p.Name, Address: p.Address, CountryCode: p.CountryCode, Location: p.Location, Provider: p.Provider,
							SshHost: p.Host, SshPort: p.Port, SshUsername: p.Username, Fingerprint: p.Fingerprint, PlanId: pl.ID,
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
					// A name means the live node of that name (retired nodes keep their access, and names are reused).
					var found *adminv1.NodeServerAccess
					for _, item := range r.Msg.GetAccess() {
						if item.GetNodeId() == in.Node || item.GetNodeName() == in.Node && !item.GetNodeRetired() {
							if found != nil {
								return nil, nil, errors.New("several nodes match; use the node id")
							}
							found = item
						}
					}
					if found == nil {
						return nil, nil, errors.New("node has no saved SSH access")
					}
					if found.GetNodeRetired() {
						return nil, nil, errors.New("the node is retired: the panel no longer changes its server")
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
						// The panel generates the password itself (generate): it never passes through this layer.
						if _, err := c.cl.Provisioning.RotateNodeServerPassword(c.ctx, connect.NewRequest(&adminv1.RotateNodeServerPasswordRequest{NodeId: p.NodeID, Generate: true, Confirm: true})); err != nil {
							return done{}, apiError(err)
						}
						return doneWith("node_ssh_password_rotated", "The node SSH password was generated inside the panel, changed, and verified. The owner can reveal it in node Settings."), nil
					})
					if err != nil {
						return nil, nil, scrubError(err)
					}
					return result(out)
				})
		}}
}

type serverKeysArgs struct {
	Node string `json:"node" jsonschema:"installed node id or exact name"`
}

// A whole SHA256 fingerprint is a key-shaped value the scrubber hides from agents, so a key goes out by key_id: the first
// 12 characters of its fingerprint, which node_server_key_remove_plan takes back.
type serverKeyView struct {
	Type       string `json:"type"`
	Bits       uint32 `json:"bits"`
	KeyID      string `json:"key_id"`
	Comment    string `json:"comment"`
	PanelKey   bool   `json:"panel_key"`
	Restricted bool   `json:"restricted"`
	Lines      uint32 `json:"lines"`
}

func nodeServerKeysListTool() toolDef {
	return readTool("node_server_keys_list", ProfileAdmin, procs(adminv1connect.ProvisioningServiceListNodeServerAccessProcedure, adminv1connect.ProvisioningServiceListNodeServerKeysProcedure),
		"List public SSH keys in a node login's authorized_keys. Comments are untrusted server text; private keys are never returned.",
		func(c *call, in serverKeysArgs) (any, error) {
			access, err := findProvisionAccess(c, in.Node)
			if err != nil {
				return nil, err
			}
			response, err := c.cl.Provisioning.ListNodeServerKeys(c.ctx, connect.NewRequest(&adminv1.ListNodeServerKeysRequest{NodeId: access.GetNodeId()}))
			if err != nil {
				return nil, apiError(err)
			}
			out := make([]serverKeyView, 0, len(response.Msg.GetKeys()))
			for _, key := range response.Msg.GetKeys() {
				out = append(out, serverKeyView{
					Type: clean(key.GetType(), 80), Bits: key.GetBits(), KeyID: serverKeyID(key.GetFingerprint()),
					Comment: clean(key.GetComment(), 100), PanelKey: key.GetPanelKey(), Restricted: key.GetRestricted(), Lines: key.GetLines(),
				})
			}
			return struct {
				Keys       []serverKeyView `json:"keys"`
				OtherLines uint32          `json:"other_lines"`
			}{Keys: out, OtherLines: response.Msg.GetOtherLines()}, nil
		})
}

type serverKeyAddPlanArgs struct {
	ReasonField
	Node      string `json:"node" jsonschema:"installed node id or exact name"`
	PublicKey string `json:"public_key,omitempty" jsonschema:"one public authorized_keys line; never a private key"`
	PanelKey  bool   `json:"panel_key,omitempty" jsonschema:"when true, install the panel's public key instead of public_key"`
}

type serverKeyChangeParams struct {
	NodeID, Name, PublicKey, Fingerprint, Type, Comment string
	PanelKey                                            bool
}

func nodeServerKeyAddPlanTool() toolDef {
	const base = "node_server_key_add"
	return toolDef{
		name: base + "_plan", min: ProfileAdmin,
		procs: procs(adminv1connect.ProvisioningServiceListNodeServerAccessProcedure, adminv1connect.ProvisioningServiceGetPanelSSHKeyProcedure),
		free:  true, isPlan: true, danger: true,
		desc: "Prepare to add one public SSH key to a node. Set panel_key=true to add the panel-owned key, or provide one public_key line. Never provide a private key. The owner approves the plan before the server changes.",
		add: func(s *mcp.Server, e *env, desc string) {
			mcp.AddTool(s, &mcp.Tool{Name: base + "_plan", Description: desc, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr(false)}},
				func(ctx context.Context, req *mcp.CallToolRequest, in serverKeyAddPlanArgs) (*mcp.CallToolResult, any, error) {
					c, err := e.begin(ctx, req, readTimeout)
					if err != nil {
						return nil, nil, err
					}
					defer c.done()
					reason := (&in.ReasonField).take()
					if in.PanelKey == (in.PublicKey != "") {
						return nil, nil, errors.New("choose panel_key=true or provide one public_key")
					}
					access, err := findProvisionAccess(c, in.Node)
					if err != nil {
						return nil, nil, err
					}
					if access.GetNodeRetired() {
						return nil, nil, errors.New("the node is retired")
					}
					var canonical string
					var key *adminv1.NodeServerKey
					if in.PanelKey {
						panelKey, err := c.cl.Provisioning.GetPanelSSHKey(c.e.cfg.Auth.WithPlanning(c.ctx), connect.NewRequest(&adminv1.GetPanelSSHKeyRequest{}))
						if err != nil {
							return nil, nil, scrubError(apiError(err))
						}
						canonical, key, err = provision.CanonicalPublicServerKey(panelKey.Msg.GetPublicKey())
					} else {
						if strings.Contains(strings.ToUpper(in.PublicKey), "PRIVATE KEY") {
							return nil, nil, errors.New("private keys are never accepted; provide the .pub line")
						}
						canonical, key, err = provision.CanonicalPublicServerKey(in.PublicKey)
					}
					if err != nil {
						return nil, nil, errors.New("public_key must be one supported SSH public key line")
					}
					panelKeyUsed := key.GetFingerprint() != "" && in.PanelKey
					if !in.PanelKey {
						// Pasting the panel's public key by hand has the same semantics as choosing panel_key.
						panelKey, panelErr := c.cl.Provisioning.GetPanelSSHKey(c.e.cfg.Auth.WithPlanning(c.ctx), connect.NewRequest(&adminv1.GetPanelSSHKeyRequest{}))
						if panelErr != nil {
							return nil, nil, scrubError(apiError(panelErr))
						}
						panelKeyUsed = panelKey.Msg.GetFingerprint() == key.GetFingerprint()
					}
					login := serverAccessLogin(access)
					p := serverKeyChangeParams{
						NodeID: access.GetNodeId(), Name: access.GetNodeName(), PublicKey: canonical,
						Fingerprint: key.GetFingerprint(), Type: key.GetType(), Comment: key.GetComment(), PanelKey: panelKeyUsed,
					}
					params, err := json.Marshal(p)
					if err != nil {
						return nil, nil, errors.New("could not store the plan")
					}
					danger := []string{dangerStepUp, "The panel edits only this login user's ~/.ssh/authorized_keys and verifies the result."}
					planned := &planned{
						Summary: "Add one public SSH key to the selected node after owner approval.",
						Facts: []Fact{{Key: "node", Value: nm(p.Name), Untrusted: true}, {Key: "login", Value: login},
							{Key: "key_type", Value: clean(p.Type, 80)}, {Key: "fingerprint", Value: clean(p.Fingerprint, 128)},
							{Key: "comment", Value: nm(p.Comment), Untrusted: true}},
						Danger: danger, Params: p,
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

func nodeServerKeyAddApplyTool() toolDef {
	const base = "node_server_key_add"
	return toolDef{name: base + "_apply", min: ProfileAdmin, procs: procs(adminv1connect.ProvisioningServiceAddNodeServerKeyProcedure), danger: true,
		desc: "Apply an owner-approved public SSH key addition. Takes only confirm_token; private keys are never accepted or returned.",
		add: func(s *mcp.Server, e *env, desc string) {
			mcp.AddTool(s, &mcp.Tool{Name: base + "_apply", Description: desc, Annotations: &mcp.ToolAnnotations{DestructiveHint: ptr(true), OpenWorldHint: ptr(false)}},
				func(ctx context.Context, req *mcp.CallToolRequest, in applyIn) (*mcp.CallToolResult, any, error) {
					c, err := e.begin(ctx, req, applyTimeout)
					if err != nil {
						return nil, nil, err
					}
					defer c.done()
					out, err := e.apply(c, base, in.ConfirmToken, func(c *call, pl Plan) (done, error) {
						var p serverKeyChangeParams
						if err := strictJSON([]byte(pl.ParamsJSON), &p); err != nil {
							return done{}, failure("plan_unreadable", "make a new SSH key addition plan")
						}
						added, err := c.cl.Provisioning.AddNodeServerKey(c.ctx, connect.NewRequest(&adminv1.AddNodeServerKeyRequest{
							NodeId: p.NodeID, PublicKey: func() string {
								if p.PanelKey {
									return ""
								}
								return p.PublicKey
							}(), PanelKey: p.PanelKey, Confirm: true,
						}))
						if err != nil {
							return done{}, apiError(err)
						}
						return doneWith("node_ssh_key_added", "Added SSH key "+clean(added.Msg.GetKey().GetFingerprint(), 128)+" to node "+clean(p.Name, 64)+"."), nil
					})
					if err != nil {
						return nil, nil, scrubError(err)
					}
					return result(out)
				})
		},
	}
}

func nodeServerKeyRemovePlanTool() toolDef {
	const base = "node_server_key_remove"
	return toolDef{
		name: base + "_plan", min: ProfileAdmin,
		procs: procs(adminv1connect.ProvisioningServiceListNodeServerAccessProcedure, adminv1connect.ProvisioningServiceListNodeServerKeysProcedure),
		free:  true, isPlan: true, danger: true,
		desc: "Prepare to remove an SSH key by its SHA256 fingerprint. The owner approves before the server changes; removing the panel key requires a saved password that the panel proves first.",
		add: func(s *mcp.Server, e *env, desc string) {
			mcp.AddTool(s, &mcp.Tool{Name: base + "_plan", Description: desc, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr(false)}},
				func(ctx context.Context, req *mcp.CallToolRequest, in serverKeyRemovePlanArgs) (*mcp.CallToolResult, any, error) {
					c, err := e.begin(ctx, req, readTimeout)
					if err != nil {
						return nil, nil, err
					}
					defer c.done()
					reason := (&in.ReasonField).take()
					if len(in.Key) < 8 || len(in.Key) > 128 || strings.ContainsAny(in.Key, "\r\n\x00") {
						return nil, nil, errors.New("key must be a key_id from node_server_keys_list or a SHA256 fingerprint")
					}
					access, err := findProvisionAccess(c, in.Node)
					if err != nil {
						return nil, nil, err
					}
					if access.GetNodeRetired() {
						return nil, nil, errors.New("the node is retired")
					}
					listed, err := c.cl.Provisioning.ListNodeServerKeys(c.ctx, connect.NewRequest(&adminv1.ListNodeServerKeysRequest{NodeId: access.GetNodeId()}))
					if err != nil {
						return nil, nil, scrubError(apiError(err))
					}
					var found *adminv1.NodeServerKey
					for _, key := range listed.Msg.GetKeys() {
						if key.GetFingerprint() != in.Key && !strings.HasPrefix(strings.TrimPrefix(key.GetFingerprint(), "SHA256:"), strings.TrimPrefix(in.Key, "SHA256:")) {
							continue
						}
						if found != nil {
							return nil, nil, errors.New("more than one SSH key matches; give the whole key_id")
						}
						found = key
					}
					if found == nil {
						return nil, nil, errors.New("that SSH key is not on the node")
					}
					if found.GetPanelKey() && !access.GetPasswordSaved() {
						return nil, nil, errors.New("the panel key is the only saved login; add password-based access before removing it")
					}
					p := serverKeyChangeParams{NodeID: access.GetNodeId(), Name: access.GetNodeName(), Fingerprint: found.GetFingerprint(), Type: found.GetType(), Comment: found.GetComment(), PanelKey: found.GetPanelKey()}
					params, err := json.Marshal(p)
					if err != nil {
						return nil, nil, errors.New("could not store the plan")
					}
					danger := []string{dangerStepUp, "The panel edits only this login user's ~/.ssh/authorized_keys."}
					if p.PanelKey {
						danger = append(danger, "The panel will log in with the saved password after removing its key; the panel proves that password first.")
					}
					planned := &planned{
						Summary: "Remove one public SSH key from the selected node after owner approval.",
						Facts: []Fact{{Key: "node", Value: nm(p.Name), Untrusted: true}, {Key: "login", Value: serverAccessLogin(access)},
							{Key: "key_type", Value: clean(p.Type, 80)}, {Key: "fingerprint", Value: clean(p.Fingerprint, 128)},
							{Key: "comment", Value: nm(p.Comment), Untrusted: true}, {Key: "panel_key", Value: strconv.FormatBool(p.PanelKey)}},
						Danger: danger, Params: p,
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

type serverKeyRemovePlanArgs struct {
	ReasonField
	Node string `json:"node" jsonschema:"installed node id or exact name"`
	Key  string `json:"key" jsonschema:"key_id returned by node_server_keys_list (or a whole SHA256 fingerprint)"`
}

func nodeServerKeyRemoveApplyTool() toolDef {
	const base = "node_server_key_remove"
	return toolDef{name: base + "_apply", min: ProfileAdmin, procs: procs(adminv1connect.ProvisioningServiceRemoveNodeServerKeyProcedure), danger: true,
		desc: "Apply an owner-approved SSH key removal plan. Takes only confirm_token; panel-key removal requires the saved password to work before removal.",
		add: func(s *mcp.Server, e *env, desc string) {
			mcp.AddTool(s, &mcp.Tool{Name: base + "_apply", Description: desc, Annotations: &mcp.ToolAnnotations{DestructiveHint: ptr(true), OpenWorldHint: ptr(false)}},
				func(ctx context.Context, req *mcp.CallToolRequest, in applyIn) (*mcp.CallToolResult, any, error) {
					c, err := e.begin(ctx, req, applyTimeout)
					if err != nil {
						return nil, nil, err
					}
					defer c.done()
					out, err := e.apply(c, base, in.ConfirmToken, func(c *call, pl Plan) (done, error) {
						var p serverKeyChangeParams
						if err := strictJSON([]byte(pl.ParamsJSON), &p); err != nil {
							return done{}, failure("plan_unreadable", "make a new SSH key removal plan")
						}
						if _, err := c.cl.Provisioning.RemoveNodeServerKey(c.ctx, connect.NewRequest(&adminv1.RemoveNodeServerKeyRequest{
							NodeId: p.NodeID, Fingerprint: p.Fingerprint, Confirm: true, ConfirmPanelKey: p.PanelKey,
						})); err != nil {
							return done{}, apiError(err)
						}
						return doneWith("node_ssh_key_removed", "Removed SSH key "+clean(p.Fingerprint, 128)+" from node "+clean(p.Name, 64)+"."), nil
					})
					if err != nil {
						return nil, nil, scrubError(err)
					}
					return result(out)
				})
		},
	}
}

func findProvisionAccess(c *call, node string) (*adminv1.NodeServerAccess, error) {
	if node == "" {
		return nil, errors.New("node is required")
	}
	response, err := c.cl.Provisioning.ListNodeServerAccess(c.ctx, connect.NewRequest(&adminv1.ListNodeServerAccessRequest{}))
	if err != nil {
		return nil, apiError(err)
	}
	var found *adminv1.NodeServerAccess
	for _, item := range response.Msg.GetAccess() {
		if item.GetNodeId() == node || item.GetNodeName() == node && !item.GetNodeRetired() {
			if found != nil {
				return nil, errors.New("several nodes match; use the node id")
			}
			found = item
		}
	}
	if found == nil {
		return nil, errors.New("node has no saved SSH access")
	}
	return found, nil
}

func serverAccessLogin(access *adminv1.NodeServerAccess) string {
	if access.GetPanelKeyFingerprint() != "" {
		return "panel key"
	}
	if access.GetPasswordSaved() {
		if access.GetPasswordLogin() {
			return "saved password"
		}
		return "saved password; SSH login not verified"
	}
	return "no saved password or panel key"
}

func serverKeyID(fingerprint string) string {
	id := strings.TrimPrefix(fingerprint, "SHA256:")
	if len(id) > 12 {
		id = id[:12]
	}
	return clean(id, 12)
}

func validFingerprint(value string) bool {
	return strings.HasPrefix(value, "SHA256:") && len(value) >= 20 && len(value) <= 128 && !strings.ContainsAny(value, "\r\n\x00")
}

func allASCIIAlpha(s string) bool {
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}
