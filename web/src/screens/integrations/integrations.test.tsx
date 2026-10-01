import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { ApprovalState, TokenChannel, TokenProfile } from "@/gen/mistgate/admin/v1/integrations_pb";
import { IntegrationsScreen } from "./index";

const listApiTokens = vi.fn();
const createApiToken = vi.fn();
const revokeApiToken = vi.fn();
const listApprovals = vi.fn();
const approve = vi.fn();
const reject = vi.fn();
let role = Role.OWNER;
vi.mock("@/lib/api", () => ({
  apiTokens: {
    listApiTokens: (...a: unknown[]) => listApiTokens(...a),
    createApiToken: (...a: unknown[]) => createApiToken(...a),
    revokeApiToken: (...a: unknown[]) => revokeApiToken(...a),
  },
  approvals: {
    listApprovals: (...a: unknown[]) => listApprovals(...a),
    approve: (...a: unknown[]) => approve(...a),
    reject: (...a: unknown[]) => reject(...a),
  },
  updates: {},
  auth: { me: () => Promise.resolve({ admin: { id: "adm_1", role } }) },
  isUnauthenticated: () => false,
  webauthnSupported: () => false,
}));
// the screen is tested without a router: a link is just an anchor here, the search is set by a test
let search: Record<string, string> = {};
const navigate = vi.fn();
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, className }: { children?: ReactNode; to: string; className?: string }) => (
    <a href={to} className={className}>
      {children}
    </a>
  ),
  useSearch: () => search,
  useNavigate: () => navigate,
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => {
  role = Role.OWNER;
  search = {};
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const m of [listApiTokens, createApiToken, revokeApiToken, listApprovals, approve, reject, navigate]) m.mockReset();
});

const NOW = Math.floor(Date.now() / 1000);
const token = (over: Record<string, unknown> = {}) => ({
  id: "tok_1",
  name: "claude-ops",
  profile: TokenProfile.OPERATOR,
  createdUnix: NOW - 86400,
  expiresUnix: NOW + 86400 * 30,
  lastUsedUnix: NOW - 180,
  lastUsedIp: "127.0.0.1",
  lastUsedVia: TokenChannel.MCP,
  revokedUnix: 0,
  createdByName: "Owner",
  rateLimitPerMin: 120,
  hint: "ab12",
  ...over,
});
const approval = (over: Record<string, unknown> = {}) => ({
  id: "pln_1",
  tokenId: "tok_1",
  tokenName: "claude-ops",
  tokenProfile: TokenProfile.ADMIN,
  tool: "node_fix",
  facts: [
    { key: "node", value: "de1", untrusted: true },
    { key: "fix", value: "restart_engine", untrusted: false },
    { key: "drops_sessions", value: "true", untrusted: false },
  ],
  danger: ["fleet"],
  reason: "the engine looks stuck",
  state: ApprovalState.AWAITING,
  createdUnix: NOW - 60,
  expiresUnix: NOW + 540,
  decidedByName: "",
  decidedUnix: 0,
  appliedUnix: 0,
  result: "",
  error: "",
  ...over,
});
const tokens = (list: unknown[]) => ({ tokens: list, nowUnix: NOW });
const inbox = (list: unknown[], awaiting = list.filter((a) => (a as { state: number }).state === ApprovalState.AWAITING).length) => ({ approvals: list, awaiting, nowUnix: NOW });

async function mount(t: unknown, a: unknown = inbox([])) {
  listApiTokens.mockResolvedValue(t);
  listApprovals.mockResolvedValue(a);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <IntegrationsScreen />
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 3; i++) await settle();
}
const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 0))));
const text = () => document.body.textContent ?? "";
const buttons = () => [...document.querySelectorAll("button")];
const button = (label: string) => buttons().find((b) => b.textContent?.trim() === label);
const click = (b: Element | undefined | null) => act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const dialog = () => document.querySelector<HTMLElement>("[role=dialog]");
const inDialog = (label: string) => [...(dialog()?.querySelectorAll("button") ?? [])].find((b) => b.textContent?.trim() === label);
function type(input: Element | null, value: string) {
  const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
  return act(async () => {
    set.call(input, value);
    input?.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
const radio = (label: string) => [...document.querySelectorAll("[role=radio]")].find((r) => r.textContent?.includes(label));

describe("the Integrations screen: tokens", () => {
  it("lists live tokens with their access, expiry and last use, and hides the dead ones until asked", async () => {
    await mount(
      tokens([
        token(),
        token({ id: "tok_2", name: "nightly-backup", profile: TokenProfile.READONLY, lastUsedUnix: 0, lastUsedIp: "", lastUsedVia: TokenChannel.UNSPECIFIED, hint: "zz99" }),
        token({ id: "tok_3", name: "old-script", revokedUnix: NOW - 5 }),
        token({ id: "tok_4", name: "lapsed", expiresUnix: NOW - 5 }),
      ]),
    );
    expect(text()).toContain("claude-ops");
    expect(text()).toContain("Operator");
    expect(text()).toContain("last used 3 min ago from 127.0.0.1 via MCP");
    expect(text()).toContain("nightly-backup");
    expect(text()).toContain("not used yet");
    expect(text()).toContain("120/min");
    expect(text()).toContain("…ab12");
    expect(text()).not.toContain("old-script");
    expect(text()).not.toContain("lapsed");
    await click(button("Show expired and revoked (2)"));
    expect(text()).toContain("old-script");
    expect(text()).toContain("Revoked");
    expect(text()).toContain("Expired");
    expect(document.querySelector("button[aria-label='Revoke token old-script']")).toBeNull();
  });

  it("creates a token, shows its secret once and forgets it when the dialog is closed", async () => {
    createApiToken.mockResolvedValue({ token: token({ id: "tok_9", name: "grafana" }), secret: "tk1_exampleexampleexampleexampleexampleexample1" });
    await mount(tokens([]));
    expect(text()).toContain("No tokens yet");
    await click(button("New token"));
    expect(radio("Read only")?.getAttribute("aria-checked")).toBe("true"); // the safest access is preselected
    await type(document.querySelector("input[placeholder^='e.g.']"), "  grafana ");
    await click(radio("Operator"));
    await click([...document.querySelectorAll("[role=radio]")].find((r) => r.textContent === "180 days"));
    await click(button("Create token"));
    await settle();
    expect(createApiToken).toHaveBeenCalledWith({ name: "grafana", profile: TokenProfile.OPERATOR, ttlDays: 180, rateLimitPerMin: 120 });
    expect(dialog()!.textContent).toContain("Copy the token now");
    expect(document.querySelector("[data-testid=token-secret]")?.textContent).toBe("tk1_exampleexampleexampleexampleexampleexample1");

    // Esc does not lose it; only the button closes it
    await act(async () => void dialog()!.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
    expect(dialog()).not.toBeNull();
    await click(inDialog("I have copied it"));
    await settle();
    expect(dialog()).toBeNull();
    expect(text()).not.toContain("tk1_example");
  });

  it("checks the form before it asks the panel, and offers no lifetime beyond a year", async () => {
    await mount(tokens([]));
    await click(button("New token"));
    const labels = [...document.querySelectorAll("[role=radio]")].map((r) => r.textContent);
    expect(labels).toEqual(expect.arrayContaining(["30 days", "90 days", "180 days", "1 year"]));
    expect(labels.filter((l) => /^(never|no expiry|forever|unlimited)/i.test(l ?? ""))).toEqual([]);
    await click(button("Create token"));
    expect(text()).toContain("Give the token a name");
    await type(document.querySelector("input[placeholder^='e.g.']"), "x");
    await type(document.querySelector("input[inputmode=numeric]"), "601");
    await click(button("Create token"));
    expect(text()).toContain("Enter a whole number from 1 to 600");
    expect(createApiToken).not.toHaveBeenCalled();
  });

  it("keeps the form open under a toast when the panel refuses", async () => {
    createApiToken.mockRejectedValue(new ConnectError("name taken", Code.AlreadyExists));
    await mount(tokens([token()]));
    await click(button("New token"));
    await type(document.querySelector("input[placeholder^='e.g.']"), "claude-ops");
    await click(button("Create token"));
    await settle();
    expect(createApiToken).toHaveBeenCalledTimes(1);
    expect(dialog()).toBeNull();
    expect(button("Create token")).toBeDefined();
  });

  it("asks before revoking, then revokes that token", async () => {
    revokeApiToken.mockResolvedValue({});
    await mount(tokens([token()]));
    await click(document.querySelector("button[aria-label='Revoke token claude-ops']"));
    expect(dialog()!.textContent).toContain("Revoke “claude-ops”?");
    expect(revokeApiToken).not.toHaveBeenCalled();
    await click(inDialog("Revoke token"));
    await settle();
    expect(revokeApiToken).toHaveBeenCalledWith({ id: "tok_1" });
    expect(dialog()).toBeNull();
  });

  it("says only the owner manages integrations, and asks the panel for nothing", async () => {
    role = Role.HELPER;
    await mount(tokens([token()]));
    expect(text()).toContain("Only the owner manages integrations");
    expect(button("New token")).toBeUndefined();
    expect(listApiTokens).not.toHaveBeenCalled();
    expect(listApprovals).not.toHaveBeenCalled();
  });

  it("does not offer the Telegram bot or webhooks", async () => {
    await mount(tokens([]));
    expect(text()).not.toMatch(/telegram|webhook/i);
  });

  it("opens the create form for the palette's “New API token” and takes ?new=token out of the address", async () => {
    search = { new: "token" };
    await mount(tokens([token()]));
    expect(button("Create token")).toBeDefined();
    expect(navigate).toHaveBeenCalledWith(expect.objectContaining({ to: "/integrations", search: {}, replace: true }));
  });
});

describe("the Integrations screen: MCP", () => {
  it("shows the address and a snippet per client, with a placeholder instead of a token", async () => {
    await mount(tokens([token()]));
    const url = document.querySelector("[data-testid=mcp-url]")?.textContent ?? "";
    expect(url).toMatch(/\/mcp$/);
    expect(text()).toContain(`claude mcp add --transport http mistgate ${url}`);
    expect(text()).toContain("Bearer <token>");
    await click(radio("Claude Desktop"));
    expect(text()).toContain('"command": "mistgate"');
    expect(text()).toContain("--token-file");
    await click(radio("stdio"));
    expect(text()).toContain("mistgate mcp --url");
    expect(text()).not.toContain("tk1_");
  });
});

describe("the Integrations screen: approvals", () => {
  it("shows what the agent wants in the panel's words, quotes what came from data, and counts down", async () => {
    await mount(tokens([token()]), inbox([approval()]));
    expect(text()).toContain("1 change is waiting for your decision");
    expect(text()).toContain("Waiting for you");
    expect(text()).toContain("Fix a node");
    expect(text()).toContain("by token claude-ops");
    expect(text()).toContain("Admin");
    expect(text()).toContain("Drops connections");
    expect(text()).toMatch(/Drops connections\s*yes/);
    expect(text()).toContain("It changes what runs on your nodes.");
    expect(text()).toMatch(/\d:\d\d left/);
    // the node name and the agent's reason are quoted, not mixed into the panel's wording
    expect(text()).toContain("“de1”");
    expect(text()).toContain("“the engine looks stuck”");
    expect(text()).toContain("Written by the agent");
  });

  it("approves and rejects by id", async () => {
    approve.mockResolvedValue({});
    reject.mockResolvedValue({});
    await mount(tokens([]), inbox([approval(), approval({ id: "pln_2", tool: "rollout_start" })]));
    await click(document.querySelector("button[aria-label='Approve: Fix a node']"));
    await settle();
    expect(approve).toHaveBeenCalledWith({ id: "pln_1" });
    await click(document.querySelector("button[aria-label='Reject: Start an update rollout']"));
    await settle();
    expect(reject).toHaveBeenCalledWith({ id: "pln_2" });
  });

  it("refuses to approve what has run out of time", async () => {
    await mount(tokens([]), inbox([approval({ expiresUnix: NOW - 1 })]));
    expect(text()).toContain("expired");
    expect((document.querySelector("button[aria-label='Approve: Fix a node']") as HTMLButtonElement).hasAttribute("data-disabled")).toBe(true);
    expect((document.querySelector("button[aria-label='Reject: Fix a node']") as HTMLButtonElement).hasAttribute("data-disabled")).toBe(true);
  });

  it("keeps decided changes in a history below, and no queue when nothing waits", async () => {
    await mount(
      tokens([]),
      inbox([
        approval({ id: "pln_3", state: ApprovalState.APPLIED, decidedByName: "Owner", decidedUnix: NOW - 100, appliedUnix: NOW - 90, result: "Fix applied." }),
        approval({ id: "pln_4", tool: "node_rollback", state: ApprovalState.FAILED, error: "node is offline" }),
        approval({ id: "pln_5", tool: "rollout_cancel", state: ApprovalState.REJECTED }),
      ]),
    );
    expect(text()).not.toContain("Waiting for you");
    expect(text()).toContain("Recent decisions");
    expect(text()).toContain("Done");
    expect(text()).toContain("Fix applied.");
    expect(text()).toContain("Error");
    expect(text()).toContain("node is offline");
    expect(text()).toContain("Rejected");
    expect(text()).toContain("API tokens"); // the rest of the screen is still there
  });

  it("words the coded facts, keeps a profile's name on a plate inside the sentence, and words the outcomes", async () => {
    await mount(
      tokens([]),
      inbox([
        approval({
          facts: [
            { key: "node", value: "de1", untrusted: true },
            { key: "fix", value: "restart_inbound", untrusted: false, code: "restart_inbound", params: { profile: "hy2 · 443", port: "443" }, untrustedParams: ["profile"] },
            { key: "duration", value: "3600 s", untrusted: false, code: "seconds", params: { n: "3600" } },
            { key: "quota", value: "93.1 GiB, reset month", untrusted: false, code: "quota", params: { bytes: "100000000000", reset: "month" } },
            { key: "term", value: "never expires", untrusted: false, code: "never" },
            { key: "effect", value: "the used traffic of the current period becomes 0", untrusted: false, code: "traffic_reset" },
          ],
        }),
        approval({ id: "pln_6", tool: "user_disable", state: ApprovalState.APPLIED, result: "3 users disabled.", outcomeCode: "users_disabled", outcomeParams: { n: "3" } }),
        approval({ id: "pln_7", tool: "rollout_start", state: ApprovalState.FAILED, error: "failed_precondition: no trusted bundle", outcomeCode: "no_trusted_bundle", outcomeParams: {} }),
      ]),
    );
    expect(text()).toContain("Restart the profile “hy2 · 443” (port 443)");
    const plate = [...document.querySelectorAll("dd span[title]")].find((s) => s.textContent === "“hy2 · 443”");
    expect(plate).toBeDefined(); // the name is its own plate, not part of the panel's words
    expect(text()).toMatch(/For how long\s*1 h/);
    expect(text()).toContain("100 GB, resets on the 1st");
    expect(text()).toContain("no end date");
    expect(text()).toContain("users stopped by the quota can connect again");
    expect(text()).toContain("3 users disabled");
    expect(text()).not.toContain("3 users disabled.");
    expect(text()).toContain("Error: no trusted update bundle");
    expect(text()).not.toContain("failed_precondition");
  });
});
