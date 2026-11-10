//go:build !js

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// AssistantIdentity is minted by the signed-in AssistantService and is scoped to one MCP session. A follow-up keeps its
// administrator's read profile while the session caps its change tools at the operator profile.
type AssistantIdentity struct {
	TokenID, Profile, AdminID, AdminName string
	Scheduled, FollowUp                  bool
}

// AssistantTool is one role-filtered tool from the live MCP registry.
type AssistantTool struct {
	Name, Description string
	InputSchema       json.RawMessage
}

// AssistantToolResult is the scrubbed text the provider may receive from a tool.
type AssistantToolResult struct {
	Text    string
	IsError bool
}

// AssistantSession exposes the normal MCP tools for one assistant turn.
type AssistantSession interface {
	ListTools(context.Context) ([]AssistantTool, error)
	CallTool(context.Context, string, json.RawMessage) (AssistantToolResult, error)
	Close() error
}

// AssistantRuntime is backed by the same registry, server descriptions and in-process admin API as external MCP.
type AssistantRuntime interface {
	OpenAssistant(context.Context, AssistantIdentity) (AssistantSession, error)
}

type assistantCallContextKey struct{}

// assistantCallIdentity is available to registry handlers on a server.Connect context.
func assistantCallIdentity(ctx context.Context) (AssistantIdentity, bool) {
	i, ok := ctx.Value(assistantCallContextKey{}).(AssistantIdentity)
	return i, ok
}

type assistantAuth interface {
	WithAssistant(context.Context, AssistantIdentity) context.Context
}

// AssistantServer owns the standard MCP registry configured for the panel. It deliberately does not add tools: the
// local memory tools belong to AssistantService and every panel tool comes from registry.go at runtime.
type AssistantServer struct {
	e       *env
	servers map[Profile]*sdk.Server
}

// NewAssistant creates the in-process assistant MCP server using the same configuration as the HTTP MCP endpoint.
func NewAssistant(c Config) (*AssistantServer, error) {
	if c.Plans == nil || c.Auth == nil || c.API == nil {
		return nil, errors.New("mcp assistant: Plans, Auth and API are required")
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Log == nil {
		c.Log = slog.Default()
	}
	e := &env{cfg: c, now: c.Now}
	servers := make(map[Profile]*sdk.Server, len(profiles))
	for _, p := range profiles {
		servers[p] = e.newServer(p)
	}
	return &AssistantServer{e: e, servers: servers}, nil
}

// OpenAssistant starts an MCP SDK in-memory client/server session for one admin turn.
func (s *AssistantServer) OpenAssistant(ctx context.Context, identity AssistantIdentity) (AssistantSession, error) {
	profile := Profile(identity.Profile)
	server := s.servers[profile]
	if identity.TokenID == "" || identity.AdminID == "" || server == nil || profile.rank() == 0 {
		return nil, errors.New("mcp assistant: invalid identity")
	}
	callCtx := context.WithValue(ctx, assistantCallContextKey{}, identity)
	clientTransport, serverTransport := sdk.NewInMemoryTransports()
	serverSession, err := server.Connect(callCtx, serverTransport, nil)
	if err != nil {
		return nil, errors.New("mcp assistant: could not open tool session")
	}
	client := sdk.NewClient(&sdk.Implementation{Name: "mistgate-assistant", Version: "1"}, nil)
	clientSession, err := client.Connect(callCtx, clientTransport, nil)
	if err != nil {
		_ = serverSession.Close()
		return nil, errors.New("mcp assistant: could not connect tool client")
	}
	return &assistantSession{ctx: callCtx, identity: identity, client: clientSession, server: serverSession}, nil
}

type assistantSession struct {
	ctx      context.Context
	identity AssistantIdentity
	client   *sdk.ClientSession
	server   *sdk.ServerSession
}

func (s *assistantSession) ListTools(ctx context.Context) ([]AssistantTool, error) {
	var out []AssistantTool
	cursor := ""
	for {
		page, err := s.client.ListTools(ctx, &sdk.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, errors.New("could not list panel tools")
		}
		for _, tool := range page.Tools {
			if strings.HasSuffix(tool.Name, "_apply") || !assistantAllowsTool(s.identity, tool.Name) {
				continue
			}
			schema, err := json.Marshal(tool.InputSchema)
			if err != nil {
				return nil, errors.New("panel tool schema is invalid")
			}
			out = append(out, AssistantTool{Name: tool.Name, Description: tool.Description, InputSchema: schema})
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	return out, nil
}

func (s *assistantSession) CallTool(ctx context.Context, name string, args json.RawMessage) (AssistantToolResult, error) {
	if !assistantAllowsTool(s.identity, name) {
		return AssistantToolResult{Text: "this tool is outside the follow-up profile; tell the person to ask for it in a live turn", IsError: true}, nil
	}
	var input any
	if len(args) == 0 {
		input = map[string]any{}
	} else if err := json.Unmarshal(args, &input); err != nil {
		return AssistantToolResult{Text: "invalid tool input JSON", IsError: true}, nil
	}
	result, err := s.client.CallTool(ctx, &sdk.CallToolParams{Name: name, Arguments: input})
	if err != nil {
		return AssistantToolResult{Text: "tool call failed", IsError: true}, nil
	}
	var parts []string
	for _, item := range result.Content {
		if text, ok := item.(*sdk.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	if len(parts) == 0 && result.StructuredContent != nil {
		if b, err := json.Marshal(result.StructuredContent); err == nil {
			parts = append(parts, string(b))
		}
	}
	return AssistantToolResult{Text: strings.Join(parts, "\n"), IsError: result.IsError}, nil
}

func assistantAllowsTool(identity AssistantIdentity, name string) bool {
	if !identity.FollowUp {
		return true
	}
	for _, tool := range registry {
		if tool.name != name {
			continue
		}
		minimum := tool.min.rank()
		if minimum > Profile(identity.Profile).rank() {
			return false
		}
		return tool.readOnly || minimum <= ProfileOperator.rank()
	}
	return false
}

func (s *assistantSession) Close() error {
	err := s.client.Close()
	return errors.Join(err, s.server.Close())
}

var _ AssistantRuntime = (*AssistantServer)(nil)
