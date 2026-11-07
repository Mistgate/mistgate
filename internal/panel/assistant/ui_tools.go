//go:build !js

package assistant

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	"github.com/mistgate/mistgate/internal/panel/mcp"
)

// The assistant's interface tools (ADR 0014): ui_open(page, id) and ui_secret_form(kind). Neither runs anything: the chat shows a
// card (a page with an Open button, or a secure field) and the person's click or typing does the rest. Changes are not made
// here: a node, a user, a profile or a relay is a plan the assistant prepares with the MCP plan tools and the owner approves
// on the plan's own card, where the secrets are typed. The pages are a whitelist; web/src/lib/ui-actions.ts holds the same
// whitelist and maps a page to the router's route.

// uiPages: the pages ui_open knows, and whether each needs an id.
var uiPages = map[string]bool{
	"overview": false, "nodes": false, "node": true, "node_ways": true, "node_profiles": true, "node_history": true, "node_server": true,
	"users": false, "groups": false, "user": true, "profiles": false, "profile": true, "subscriptions": false,
	"health": false, "health_people": false, "reachability": false, "updates": false,
	"ai": false, "ai_memory": false, "ai_mcp": false, "ai_waiting": false,
	"settings_interface": false, "settings_system": false, "settings_security": false, "settings_sessions": false,
	"settings_audit": false, "settings_admins": false, "settings_domains": false, "settings_backups": false,
}

var uiIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func uiToolSchemas() []mcp.AssistantTool {
	pages := make([]string, 0, len(uiPages))
	for p := range uiPages {
		pages = append(pages, p)
	}
	sort.Strings(pages)
	pagesJSON, _ := json.Marshal(pages)
	return []mcp.AssistantTool{
		{
			Name:        "ui_open",
			Description: "Offer to open a page of the panel. The person sees a card with a button and clicks it; nothing opens by itself. Use it when asked where something is. The node, node_ways, node_profiles, node_history, node_server, user and profile pages need their id.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"page":{"type":"string","enum":` + string(pagesJSON) + `},"id":{"type":"string","maxLength":64}},"required":["page"],"additionalProperties":false}`),
		},
	}
}

// uiOpenTool checks a ui_open call and answers with its canonical arguments (what the card is drawn from).
func uiOpenTool(raw []byte) toolExecution {
	var args struct {
		Page string `json:"page"`
		ID   string `json:"id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return toolExecution{output: toolOutput{err: true, text: "invalid tool input JSON"}}
	}
	needsID, known := uiPages[args.Page]
	if !known {
		return toolExecution{output: toolOutput{err: true, text: "unknown page"}}
	}
	id := strings.TrimSpace(args.ID)
	if needsID && !uiIDPattern.MatchString(id) {
		return toolExecution{output: toolOutput{err: true, text: "this page needs the id of the node, user or profile"}}
	}
	if !needsID && id != "" {
		return toolExecution{output: toolOutput{err: true, text: "this page takes no id"}}
	}
	canonical := map[string]string{"page": args.Page}
	if id != "" {
		canonical["id"] = id
	}
	b, _ := json.Marshal(canonical)
	return toolExecution{output: toolOutput{text: `{"shown":true,"note":"a card with an Open button is shown; the page opens only when the person clicks it"}`}, args: string(b)}
}

// uiSecretKinds: the secrets the panel can take in a secure field in the chat, with the context each needs (never the value).
// A secure form submits straight to the panel's own call (the provider key, the Telegram bot, the WARP import); the model
// chooses only the kind and the context, and never receives the value. Owner only, like the calls behind them.
var uiSecretKinds = map[string]string{"provider_key": "provider", "telegram_token": "", "warp_license": "node"}

var uiSecretProviders = map[string]bool{"anthropic": true, "deepseek": true, "zai": true, "ollama_cloud": true}

// uiSecretToolSchema is offered to the owner only.
func uiSecretToolSchema() mcp.AssistantTool {
	return mcp.AssistantTool{
		Name:        "ui_secret_form",
		Description: "Offer a secure field in the chat for a secret the owner must enter: an AI provider's API key (kind provider_key, with provider), the Telegram bot token (telegram_token), the WARP account of a node (warp_license, with node). The value goes straight to the panel; you never see it and cannot pass it on. Use it instead of asking the person to type a key or token to you. A node install's password is typed on its plan's approval card, not here.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"kind":{"type":"string","enum":["provider_key","telegram_token","warp_license"]},"provider":{"type":"string","enum":["anthropic","deepseek","zai","ollama_cloud"]},"node":{"type":"string","maxLength":64}},"required":["kind"],"additionalProperties":false}`),
	}
}

// uiSecretTool checks a ui_secret_form call and answers with its canonical arguments: the kind and its context, no value.
func uiSecretTool(raw []byte) toolExecution {
	var args struct {
		Kind     string `json:"kind"`
		Provider string `json:"provider"`
		Node     string `json:"node"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return toolExecution{output: toolOutput{err: true, text: "invalid tool input JSON"}}
	}
	context, known := uiSecretKinds[args.Kind]
	if !known {
		return toolExecution{output: toolOutput{err: true, text: "unknown kind"}}
	}
	canonical := map[string]string{"kind": args.Kind}
	switch context {
	case "provider":
		if !uiSecretProviders[args.Provider] || args.Node != "" {
			return toolExecution{output: toolOutput{err: true, text: "provider_key needs the provider (anthropic, deepseek, zai or ollama_cloud)"}}
		}
		canonical["provider"] = args.Provider
	case "node":
		if !uiIDPattern.MatchString(args.Node) || args.Provider != "" {
			return toolExecution{output: toolOutput{err: true, text: "warp_license needs the node id"}}
		}
		canonical["node"] = args.Node
	default:
		if args.Provider != "" || args.Node != "" {
			return toolExecution{output: toolOutput{err: true, text: "this kind takes no context"}}
		}
	}
	b, _ := json.Marshal(canonical)
	return toolExecution{output: toolOutput{text: `{"shown":true,"note":"a secure field is shown to the owner; the value goes straight to the panel and you will not see it"}`}, args: string(b)}
}
