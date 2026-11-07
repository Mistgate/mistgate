import { describe, expect, it } from "vitest";
import { actionOf, parseOpen, parseSecret, uiPageIds } from "./ui-actions";

describe("ui_open pages", () => {
  it("maps each whitelisted page to the router's route and its search", () => {
    expect(parseOpen({ page: "nodes" })?.target).toEqual({ to: "/nodes" });
    expect(parseOpen({ page: "node_ways", id: "nod_de1" })?.target).toEqual({ to: "/nodes/$id", params: { id: "nod_de1" }, search: { tab: "ways" } });
    expect(parseOpen({ page: "user", id: "usr_1" })?.target).toEqual({ to: "/users/$id", params: { id: "usr_1" } });
    expect(parseOpen({ page: "groups" })?.target).toEqual({ to: "/users", search: { tab: "groups" } });
    expect(parseOpen({ page: "health_people" })?.target).toEqual({ to: "/health", search: { tab: "people" } });
    expect(parseOpen({ page: "settings_backups" })?.target).toEqual({ to: "/settings/$section", params: { section: "backups" } });
    expect(parseOpen({ page: "ai_waiting" })?.target).toEqual({ to: "/ai", search: { tab: "approvals" } });
    expect(uiPageIds.length).toBeGreaterThan(25);
  });

  it("refuses an unknown page, a missing or odd id, and an id where none belongs", () => {
    expect(parseOpen({ page: "https://evil.example" })).toBeNull();
    expect(parseOpen({ page: "node" })).toBeNull();
    expect(parseOpen({ page: "node", id: "../x" })).toBeNull();
    expect(parseOpen({ page: "nodes", id: "nod_1" })).toBeNull();
    expect(parseOpen(null)).toBeNull();
  });
});

describe("ui_secret_form kinds", () => {
  it("takes only a whitelisted kind with the context it needs, never a value", () => {
    expect(parseSecret({ kind: "provider_key", provider: "deepseek" })).toEqual({ kind: "secret", secret: "provider_key", provider: "deepseek", node: "" });
    expect(parseSecret({ kind: "telegram_token" })?.secret).toBe("telegram_token");
    expect(parseSecret({ kind: "warp_license", node: "nod_de1" })?.node).toBe("nod_de1");
    expect(JSON.stringify(parseSecret({ kind: "telegram_token", value: "123456:ABC-secret" }))).not.toContain("ABC-secret");
    expect(parseSecret({ kind: "root_password" })).toBeNull();
    expect(parseSecret({ kind: "provider_key" })).toBeNull();
    expect(parseSecret({ kind: "provider_key", provider: "skynet" })).toBeNull();
    expect(parseSecret({ kind: "warp_license", node: "../x" })).toBeNull();
    expect(parseSecret({ kind: "telegram_token", provider: "anthropic" })).toBeNull();
  });
});

describe("a persisted tool call as a card", () => {
  it("needs a finished, successful interface call that passes the whitelist", () => {
    const base = { tool: "ui_open", argsJson: '{"page":"nodes"}', done: true, ok: true };
    expect(actionOf(base)?.kind).toBe("open");
    expect(actionOf({ ...base, tool: "ui_secret_form", argsJson: '{"kind":"telegram_token"}' })?.kind).toBe("secret");
    expect(actionOf({ ...base, tool: "ui_prefill", argsJson: '{"form":"add_node","fields":{"host":"h"}}' })).toBeNull();
    expect(actionOf({ ...base, ok: false })).toBeNull();
    expect(actionOf({ ...base, done: false })).toBeNull();
    expect(actionOf({ ...base, tool: "node_journal" })).toBeNull();
    expect(actionOf({ ...base, argsJson: "{nope" })).toBeNull();
  });
});
