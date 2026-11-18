//go:build !js

package assistant

import (
	"context"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/mistgate/mistgate/internal/panel/mcp"
)

// The assistant's interface tools (ADR 0014): ui_open(page, id) and ui_secret_form(kind). Neither runs anything: the chat shows a
// card (a page with an Open button, or a secure field) and the person's click or typing does the rest. Changes are not made
// here: a node, a user, a profile or a relay is a plan the assistant prepares with the MCP plan tools and the owner approves
// on the plan's own card, where the secrets are typed. The pages are a whitelist; web/src/lib/ui-actions.ts holds the same
// whitelist and maps a page to the router's route.

// uiPages: the pages ui_open knows, and the kind of thing each one needs (a node, a person, a profile, a group, a decoy site)
// named by id or by its name; "" is a page without a target.
var uiPages = map[string]string{
	"overview": "", "nodes": "", "node": "node", "node_ways": "node", "node_profiles": "node", "node_history": "node", "node_server": "node",
	"users": "", "groups": "", "group": "group", "user": "user", "profiles": "", "profile": "profile", "subscriptions": "",
	"decoys": "", "decoy": "decoy",
	"health": "", "health_people": "", "reachability": "", "updates": "",
	"ai": "", "ai_memory": "", "ai_mcp": "", "ai_waiting": "",
	"settings_interface": "", "settings_system": "", "settings_security": "", "settings_sessions": "", "settings_audit": "",
	"settings_admins": "", "settings_domains": "", "settings_backups": "",
}

// uiTargetTable: where each kind of target lives; every one has an id and a unique name.
var uiTargetTable = map[string]string{"node": "node", "user": "user", "profile": "profile", "group": "user_group", "decoy": "decoy_site"}

// uiBuiltinDecoys: the two entries of the decoy library that are not rows (the panel's built-in site and its directory).
var uiBuiltinDecoys = map[string]bool{"builtin": true, "panel_dir": true}

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
			Description: "Offer to open a page of the panel. The person sees a card with a button and clicks it; nothing opens by itself. Use it when asked where something is. The node, node_ways, node_profiles, node_history, node_server, user, group, profile and decoy pages need the target: its id or its exact name (the card opens the real one; a target that does not exist is refused, so never guess).",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"page":{"type":"string","enum":` + string(pagesJSON) + `},"id":{"type":"string","maxLength":64,"description":"the id or the exact name of the node, person, group, profile or decoy site"}},"required":["page"],"additionalProperties":false}`),
		},
	}
}

// uiOpenTool checks a ui_open call and answers with its canonical arguments (what the card is drawn from). A page that needs a
// target is given its id or its name: the panel looks the target up and the card carries the real id (and the name to show),
// so the Open button never leads to "no such node"; a target that does not exist is a failed call, not a card.
func (s *Service) uiOpenTool(ctx context.Context, raw []byte) toolExecution {
	var args struct {
		Page string `json:"page"`
		ID   string `json:"id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return toolExecution{output: toolOutput{err: true, text: "invalid tool input JSON"}}
	}
	kind, known := uiPages[args.Page]
	if !known {
		return toolExecution{output: toolOutput{err: true, text: "unknown page"}}
	}
	ref := strings.TrimSpace(args.ID)
	if kind != "" && (ref == "" || utf8.RuneCountInString(ref) > 64) {
		return toolExecution{output: toolOutput{err: true, text: "this page needs the id or the exact name of the node, person, group, profile or decoy site"}}
	}
	if kind == "" && ref != "" {
		return toolExecution{output: toolOutput{err: true, text: "this page takes no id"}}
	}
	canonical := map[string]string{"page": args.Page}
	if kind != "" {
		id, name, found := s.uiTarget(ctx, kind, ref)
		if !found {
			return toolExecution{output: toolOutput{err: true, text: "not found: no " + kind + " has this id or name, so nothing is opened. Look it up with a read tool and try again; do not guess"}}
		}
		canonical["id"] = id
		if name != "" {
			canonical["name"] = name
		}
	}
	b, _ := json.Marshal(canonical)
	return toolExecution{output: toolOutput{text: `{"shown":true,"note":"a card with an Open button is shown; the page opens only when the person clicks it"}`}, args: string(b)}
}

// uiTarget finds a node, person, group, profile or decoy site by id, else by exact name (names are unique, case-blind).
func (s *Service) uiTarget(ctx context.Context, kind, ref string) (id, name string, found bool) {
	if kind == "decoy" && uiBuiltinDecoys[ref] {
		return ref, "", true
	}
	err := s.st.R.QueryRowContext(ctx, `SELECT id,name FROM `+uiTargetTable[kind]+` WHERE id=?1 OR name=?1 ORDER BY id=?1 DESC LIMIT 1`, ref).Scan(&id, &name)
	return id, name, err == nil
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
