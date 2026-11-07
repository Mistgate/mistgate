import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { setLang } from "@/i18n";
import { FleetShowcase } from "./dev-kit-fleet-map";

// The fleet map on the dev kit's 11-node fleet (ru and ru2 are entries relaying to be and tr): the cards, the drawers and
// the relay drawing, with the calls the owner would make. What a pointer does on the canvas is checked in a real browser.

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, params, search, className }: { children?: React.ReactNode; to: string; params?: { id: string }; search?: Record<string, string>; className?: string }) => (
    <a href={`${to.replace("$id", params?.id ?? "")}${search ? `?${new URLSearchParams(search)}` : ""}`} className={className}>
      {children}
    </a>
  ),
  useSearch: () => ({}),
  useNavigate: () => vi.fn(),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  Element.prototype.scrollIntoView ??= () => {};
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => {
  setLang("en");
  localStorage.clear();
  window.history.replaceState({}, "", "/");
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  window.history.replaceState({}, "", "/");
});

const settle = async () => {
  for (let i = 0; i < 10; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};
async function mount(search = "") {
  window.history.replaceState({}, "", `/${search}`);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(<FleetShowcase />));
  await settle();
}
const card = (id: string) => document.querySelector<HTMLElement>(`[data-gnode="${id}"]`);
const text = (id: string) => card(id)?.textContent ?? "";
const click = (el: Element | undefined | null) => act(async () => void el?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const aside = () => document.querySelector("aside");
const dialog = () => [...document.querySelectorAll<HTMLElement>("[role=dialog]")].at(-1) ?? null;
const drop = (el: Element, id: string) =>
  act(async () => {
    const ev = new Event("drop", { bubbles: true, cancelable: true });
    Object.defineProperty(ev, "dataTransfer", { value: { types: ["application/x-mg-node"], getData: (t: string) => (t === "application/x-mg-node" ? id : "") } });
    el.dispatchEvent(ev);
  });
const body = (id: string) => card(id)!.querySelector<HTMLElement>(".p-3\\.5")!;

describe("the fleet map", () => {
  it("a card for every node, People on the left, the internet and WARP on the right", async () => {
    await mount();
    const names = [...document.querySelectorAll("[data-gnode^='node:']")].map((n) => n.getAttribute("data-gnode")!.slice(9));
    expect(names.sort()).toEqual(["be", "de", "de2", "fresh", "mass", "nl", "ro", "ru", "ru2", "tll", "tr"]);
    expect(text("people")).toContain("People · Russia");
    expect(text("exit:internet")).toContain("Internet");
    expect(text("exit:warp")).toContain("Cloudflare WARP");
  });

  it("a card shows the status, the load, the people online and the servers; an entry-only node says so", async () => {
    await mount();
    expect(text("node:nod_mass")).toContain("online");
    expect(text("node:nod_mass")).toContain("load 72% · 41 online");
    expect(text("node:nod_mass")).toContain("HY2");
    expect(text("node:nod_ro")).toContain("unstable");
    expect(text("node:nod_tll")).toContain("offline");
    expect(text("node:nod_ru")).toContain("entry only");
    expect(text("node:nod_ru2")).toContain("entry only");
    expect(text("node:nod_be")).not.toContain("entry only");
    expect(text("node:nod_fresh")).toContain("nothing deployed yet");
  });

  it("draws a wire per way and per pair a relay joins, with the number of relays", async () => {
    await mount();
    const ids = [...document.querySelectorAll("[data-gedge]")].map((e) => e.getAttribute("data-gedge"));
    for (const id of ["relay:nod_ru:nod_be", "relay:nod_ru:nod_tr", "relay:nod_ru2:nod_be", "relay:nod_ru2:nod_tr", "people:nod_de", "exit:nod_de:internet", "exit:nod_de:warp"]) expect(ids).toContain(id);
    expect(ids.filter((i) => i!.startsWith("relay:"))).toHaveLength(4);
    const labels = [...document.querySelectorAll(".mg-glabel")].map((l) => l.textContent);
    expect(labels).toContain("2 relays");
    expect(labels).toContain("1 relay");
  });

  it("selecting a node opens its drawer: the servers with their switches, the ways in, the relays, and the links", async () => {
    await mount();
    await act(async () => card("node:nod_be")!.focus());
    await settle();
    const t = aside()!.textContent!;
    expect(t).toContain("Servers");
    expect(t).toContain("Ways in");
    expect(t).toContain("Through ru");
    expect(t).toContain("Relays to this node");
    expect(t).toContain("HY2 · ru");
    expect(aside()!.querySelectorAll('[role="switch"]').length).toBe(3);
    const hrefs = [...aside()!.querySelectorAll("a")].map((a) => a.getAttribute("href"));
    expect(hrefs).toContain("/nodes/nod_be?from=map");
    expect(hrefs).toContain("/nodes/nod_be?tab=ways&view=graph&from=map");
    expect(card("node:nod_be")!.hasAttribute("data-selected")).toBe(true);
  });

  it("selecting a wire opens the relay group's drawer, with the relays' actions and Add a server", async () => {
    await mount();
    await click(document.querySelector('[data-gedge="relay:nod_ru:nod_be"]'));
    await settle();
    expect(aside()!.textContent).toContain("ru → be");
    expect(aside()!.textContent).toContain("2 relays");
    expect(aside()!.textContent).toContain("Turn off");
    await click([...aside()!.querySelectorAll("button")].find((b) => b.textContent === "Add a server…"));
    await settle();
    expect(dialog()!.textContent).toContain("Relay through ru");
  });

  it("the search box finds a node by name and selects it", async () => {
    await mount();
    const input = document.querySelector<HTMLInputElement>('input[aria-label="Find a node"]')!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "ma");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await click([...document.querySelectorAll("ul button")].find((b) => b.textContent === "mass"));
    await settle();
    expect(card("node:nod_mass")!.hasAttribute("data-selected")).toBe(true);
    expect(aside()!.textContent).toContain("mass");
  });

  it("?focus=<id> opens with that node selected", async () => {
    await mount("?only=graph&focus=tr");
    expect(card("node:nod_tr")!.hasAttribute("data-selected")).toBe(true);
    expect(aside()!.textContent).toContain("tr");
  });

  it("double-clicking a node opens its page on the route map", async () => {
    await mount();
    await act(async () => void body("node:nod_de2").dispatchEvent(new MouseEvent("dblclick", { bubbles: true })));
    expect(document.querySelector("[data-testid=opened]")?.textContent).toContain("de2");
  });
});

describe("the owner draws and detaches relays", () => {
  it("dragging Relay from here onto another node opens the relay dialog with the entry and that exit's servers", async () => {
    await mount();
    await drop(body("node:nod_nl"), "nod_ru");
    await settle();
    expect(dialog()!.textContent).toContain("Relay through ru");
    expect(dialog()!.textContent).toContain("nl");
  });

  it("refuses the same node, a node with nothing deployed, and says why", async () => {
    await mount();
    await drop(body("node:nod_ru"), "nod_ru");
    expect(document.body.textContent).toContain("A relay goes to another node.");
    await drop(body("node:nod_fresh"), "nod_ru");
    expect(document.body.textContent).toContain("Nothing is deployed on that node yet.");
    expect(dialog()?.textContent ?? "").not.toContain("Relay through ru");
  });

  it("the drawer's Add a relay from this node opens the dialog for it, a relay is removed after a confirm", async () => {
    await mount();
    await act(async () => card("node:nod_nl")!.focus());
    await settle();
    await click([...aside()!.querySelectorAll("button")].find((b) => b.textContent === "Add a relay from this node…"));
    await settle();
    expect(dialog()!.textContent).toContain("Relay through nl");
    await click([...dialog()!.querySelectorAll("button")].find((b) => b.textContent === "Cancel" || b.textContent === "Close"));
    await settle();
    await click(document.querySelector('[data-gedge="relay:nod_ru2:nod_tr"]'));
    await settle();
    await click([...aside()!.querySelectorAll("button")].find((b) => b.textContent === "Remove…"));
    await settle();
    expect(dialog()!.textContent).toContain("ru2");
  });
});

describe("another role", () => {
  it("a helper sees the map read-only: no Relay from here, no relays, no actions that change anything", async () => {
    await mount("?role=helper");
    expect(card("node:nod_be")).not.toBeNull();
    expect(document.querySelector("[draggable=true]")).toBeNull();
    expect([...document.querySelectorAll("[data-gedge]")].some((e) => e.getAttribute("data-gedge")!.startsWith("relay:"))).toBe(false);
    await act(async () => card("node:nod_be")!.focus());
    await settle();
    expect(aside()!.textContent).not.toContain("Add a relay from this node");
    expect(aside()!.querySelectorAll('[role="switch"]').length).toBe(0);
  });
});
