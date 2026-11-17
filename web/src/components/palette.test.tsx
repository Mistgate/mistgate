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
vi.mock("@/lib/node-status", () => ({ nodeKind: () => "ok", useNodeStatus: () => ({ word: () => "Healthy" }) }));
vi.mock("@/lib/session", () => ({ useSignOut: () => signOut, meQuery: { queryKey: ["me"], queryFn: async () => ({}), staleTime: Infinity } }));
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
async function mount(me: object = {}) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  qc.setQueryData(["me"], me);
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
    expect(found("доступность")).toContain("reachability");
    expect(found("ркн")).toContain("reachability");
    expect(found("tspu")).toContain("reachability");
    expect(found("可达性")).toContain("reachability");
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
    expect(navigate).toHaveBeenCalledWith({ to: "/nodes/$id", params: { id: "n1" }, search: { tab: "history", view: "logs" } });
  });

  it("finds the new tabs, the journal and the people of a node", async () => {
    for (const [query, label, target] of [
      ["de1 журнал", "de1 · Journal", { search: { tab: "history", view: "journal" } }],
      ["de1 journal", "de1 · Journal", { search: { tab: "history", view: "journal" } }],
      ["de1 события", "de1 · History", { search: { tab: "history" } }],
      ["de1 warp", "de1 · Exit", { search: { tab: "exit" } }],
      ["de1 ipv6", "de1 · Exit", { search: { tab: "exit" } }],
      ["de1 настройки", "de1 · Server", { search: { tab: "server" } }],
      ["de1 ssh", "de1 · Server", { search: { tab: "server" } }],
      ["de1 пользователи", "de1 · On this node now", { search: {}, hash: "people" }],
      ["de1 входы", "de1 · Ways in", { search: { tab: "ways" } }],
      ["de1 ретрансляция", "de1 · Ways in", { search: { tab: "ways" } }],
      ["de1 relay", "de1 · Ways in", { search: { tab: "ways" } }],
      ["de1 порты", "de1 · Ports", { search: { tab: "ways", way: "direct" } }],
      ["обход", "de1 · Ways in", { search: { tab: "ways" } }],
    ] as const) {
      navigate.mockReset();
      await mount();
      await type(query);
      await wait(250);
      expect(text(), query).toContain(label);
      await enter();
      expect(navigate, query).toHaveBeenCalledWith({ to: "/nodes/$id", params: { id: "n1" }, ...target });
      act(() => root?.unmount());
      host?.remove();
    }
  });

  it("runs the actions: a new API token opens its form, sign out signs out", async () => {
    await mount();
    await type("токен");
    await wait(250);
    const item = [...document.querySelectorAll("[role=option]")].find((o) => o.textContent?.includes(en["palette.newToken"]));
    await act(async () => void item?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
    expect(navigate).toHaveBeenCalledWith({ to: "/ai", search: { tab: "mcp", new: "token" } });

    await type("выйти");
    await wait(250);
    await enter();
    expect(signOut).toHaveBeenCalledTimes(1);
  });

  it("does not offer a new API token where the edition cannot create one", async () => {
    await mount({ edition: "edge", unavailable: ["mcp"] });
    await type("токен");
    await wait(250);
    const options = [...document.querySelectorAll("[role=option]")].map((o) => o.textContent ?? "");
    expect(options.some((o) => o.includes(en["palette.newToken"]))).toBe(false);
    expect(options.some((o) => o.includes(en["int.tok.title"]))).toBe(true); // the section itself is still found
  });
});

describe("the window of the board (PnSearch)", () => {
  const options = () => [...document.querySelectorAll<HTMLElement>("[role=option]")];
  const selected = () => options().findIndex((o) => o.getAttribute("aria-selected") === "true");
  const arrow = (key: string) => act(async () => void field().dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true, cancelable: true })));

  it("starts with the actions and the nodes, four rows of an action at most, and no count", async () => {
    await mount();
    const groups = [...document.querySelectorAll("[role=group]")];
    expect(groups.map((g) => g.getAttribute("aria-label"))).toEqual([en["palette.actions"], en["palette.nodes"]]);
    expect(groups[0]!.querySelectorAll("[role=option]")).toHaveLength(4);
    expect(groups[0]!.textContent).toContain(en["win.search.addSsh"]);
    expect(groups[0]!.textContent).toContain(en["win.search.addCmd"]);
    expect(groups[0]!.textContent).toContain(en["palette.createUser"]);
    expect(groups[0]!.textContent).toContain(en["win.search.themeLight"]);
    // a node: its name, where it is, and how it is
    expect(groups[1]!.textContent).toContain("de1");
    expect(groups[1]!.textContent).toContain("Frankfurt");
    expect(groups[1]!.textContent).toContain("Healthy");
    expect(document.body.textContent).not.toContain(" of "); // no count until something is typed
  });

  it("counts the rows of each group and says how many of how many at the foot", async () => {
    await mount();
    await type("backup");
    await wait(250);
    const heads = [...document.querySelectorAll("[role=group] > div:first-child")].map((h) => h.textContent);
    expect(heads).toContain(`${en["palette.places"]}1`);
    expect(text()).toContain(en["win.search.count"].replace("{n}", "1").replace("{total}", "1"));
  });

  it("marks what was typed inside a name", async () => {
    await mount();
    await type("ack");
    await wait(250);
    expect(document.querySelector("mark")?.textContent).toBe("ack");
  });

  it("shows a person with the node they are on and that they are online, and opens them", async () => {
    userSearch = async () => ({ users: [{ id: "u1", name: "alice", groupName: "family", online: true, currentNodeName: "de1" } as never] });
    await mount();
    await type("ali");
    await wait(250);
    await wait(0);
    const row = options().find((o) => o.textContent?.includes("alice"))!;
    expect(row.textContent).toContain("de1");
    expect(row.textContent).toContain(en["win.search.online"]);
    expect(row.querySelector("span")?.textContent).toBe("A"); // the round initial
    await act(async () => void row.dispatchEvent(new MouseEvent("click", { bubbles: true })));
    expect(navigate).toHaveBeenCalledWith({ to: "/users/$id", params: { id: "u1" } });
  });

  it("opens the new-node window by SSH or by a command, as the two actions say", async () => {
    await mount();
    await type("node");
    await wait(250);
    await act(async () => void options().find((o) => o.textContent?.includes(en["win.search.addCmd"]))?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
    expect(addNode).toHaveBeenLastCalledWith(undefined, "cmd");
    await act(async () => void options().find((o) => o.textContent?.includes(en["win.search.addSsh"]))?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
    expect(addNode).toHaveBeenLastCalledWith(undefined, "ssh");
  });

  it("walks the rows with the arrows and stops at both ends", async () => {
    await mount();
    expect(selected()).toBe(0);
    await arrow("ArrowUp");
    expect(selected()).toBe(0);
    for (let i = 0; i < 20; i++) await arrow("ArrowDown");
    expect(selected()).toBe(options().length - 1);
  });

  it("says what to search by when nothing was found", async () => {
    await mount();
    await type("qwertyuiop");
    await wait(250);
    await wait(0);
    expect(text()).toContain(en["palette.empty"]);
    expect(text()).toContain(en["win.search.emptyHint"]);
  });

  it("has the keys at the foot and closes with the Esc keycap", async () => {
    await mount();
    expect(text()).toContain(en["win.search.pick"]);
    expect(text()).toContain(en["win.search.open"]);
    expect(document.querySelector("[role=listbox]")).not.toBeNull();
    expect([...document.querySelectorAll("button")].some((b) => b.textContent === "Esc")).toBe(true);
  });
});
