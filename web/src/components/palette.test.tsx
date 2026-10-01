import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { en } from "@/i18n/en";
import Palette, { placeMatches, places } from "./palette";

// The command palette finds sections and actions by their words (Russian or English), a node's tab by "node + tab",
// and does not say "nothing found" while it is still asking the panel.

const navigate = vi.fn();
const addNode = vi.fn();
const signOut = vi.fn();
let userSearch: (q: string) => Promise<{ users: { id: string; name: string; groupName: string }[] }> = async () => ({ users: [] });
vi.mock("@tanstack/react-router", () => ({ useNavigate: () => navigate }));
vi.mock("@/components/add-node", () => ({ useAddNode: () => addNode }));
vi.mock("@/lib/node-status", () => ({ useNodeStatus: () => ({ word: () => "Healthy" }) }));
vi.mock("@/lib/session", () => ({ useSignOut: () => signOut }));
vi.mock("@/lib/queries", () => ({
  nodesQuery: { queryKey: ["nodes"], queryFn: async () => ({ nodes: [{ id: "n1", name: "de1", countryCode: "DE", location: "Frankfurt", provider: "", address: "" }] }) },
  userSearchQuery: (q: string) => ({ queryKey: ["users", q], queryFn: () => userSearch(q) }),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const m of [navigate, addNode, signOut]) m.mockReset();
  userSearch = async () => ({ users: [] });
});

const wait = (ms: number) => act(async () => void (await new Promise((r) => setTimeout(r, ms))));
const text = () => document.body.textContent ?? "";
const field = () => document.querySelector<HTMLInputElement>("input[role=combobox]")!;
async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () => root!.render(<QueryClientProvider client={qc}><Palette open onOpenChange={() => {}} /></QueryClientProvider>));
  await wait(0);
}
async function type(value: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(field(), value);
    field().dispatchEvent(new Event("input", { bubbles: true }));
  });
}
const enter = () => act(async () => void field().dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })));
const found = (q: string) => places.filter((p) => placeMatches(p, q)).map((p) => p.id);

describe("places", () => {
  it("are found by the words people use, in either language", () => {
    expect(found("бэкап")).toContain("settings-backups");
    expect(found("backup")).toContain("settings-backups");
    expect(found("токен")).toContain("tokens");
    expect(found("mcp")).toContain("mcp");
    expect(found("passkey")).toContain("settings-security");
    expect(found("сессии")).toContain("settings-sessions");
    expect(found("аудит")).toContain("settings-audit");
    expect(found("тема")).toContain("settings-interface");
    expect(found("язык")).toContain("settings-interface");
    expect(found("dns")).toContain("subscriptions-dns");
    expect(found("правила")).toContain("subscriptions-rules");
    expect(found("группы")).toContain("users");
    expect(found("здоровье")).toContain("health");
    expect(found("доктор")).toContain("health-doctor");
    expect(found("qwertyuiop")).toEqual([]);
  });
});

describe("the palette", () => {
  it("takes a word to its section", async () => {
    await mount();
    await type("бэкап");
    await wait(250);
    expect(text()).toContain(en["palette.places"]);
    expect(text()).toContain("Backups");
    await enter();
    expect(navigate).toHaveBeenCalledWith({ to: "/settings/$section", params: { section: "backups" } });
  });

  it("says “Searching…” until the panel has answered for what is typed, then “Nothing found”", async () => {
    let answer: (v: { users: never[] }) => void = () => {};
    userSearch = () => new Promise((r) => (answer = r));
    await mount();
    await type("zzzz");
    expect(text()).toContain(en["palette.searching"]); // the pause before asking
    await wait(250);
    expect(text()).toContain(en["palette.searching"]); // asked, no answer yet
    expect(text()).not.toContain(en["palette.empty"]);
    await act(async () => answer({ users: [] }));
    await wait(0);
    expect(text()).toContain(en["palette.empty"]);
  });

  it("opens a tab of a node when the query names both", async () => {
    await mount();
    await type("de1 логи");
    await wait(250);
    expect(text()).toContain("de1 · Logs");
    await enter();
    expect(navigate).toHaveBeenCalledWith({ to: "/nodes/$id", params: { id: "n1" }, search: { tab: "logs" } });
  });

  it("runs the actions: a new API token opens its form, sign out signs out", async () => {
    await mount();
    await type("токен");
    await wait(250);
    const item = [...document.querySelectorAll("[role=option]")].find((o) => o.textContent?.includes(en["palette.newToken"]));
    await act(async () => void item?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
    expect(navigate).toHaveBeenCalledWith({ to: "/integrations", search: { new: "token" } });

    await type("выйти");
    await wait(250);
    await enter();
    expect(signOut).toHaveBeenCalledTimes(1);
  });
});
