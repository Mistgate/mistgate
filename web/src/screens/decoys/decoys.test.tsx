import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { dictionaries, setLang } from "@/i18n";
import { en } from "@/i18n/en";
import { accessQuery } from "@/lib/assistant";
import { pagesQuery, siteQuery, sitesQuery } from "@/lib/decoysites";
import { nodesQuery } from "@/lib/queries";
import { meQuery } from "@/lib/session";
import { DecoysScreen, type DecoysDemo } from "./list";
import { DecoySiteScreen, type SiteDemo } from "./site";

// The Decoy sites screens: the list in its states, filters and search, the upload window and its refusals, the page of a site with
// its actions, and the rules the buttons keep (a draft cannot be assigned, a site that stands cannot be deleted, only the owner
// sees the page). The panel's calls are mocks; nothing is drawn from a real panel.

const api = {
  listDecoySites: vi.fn(),
  getDecoySite: vi.fn(),
  createDecoySite: vi.fn(),
  updateDecoySite: vi.fn(),
  assignDecoySite: vi.fn(),
  unassignDecoySite: vi.fn(),
  deleteDecoySite: vi.fn(),
  setDecoySiteReady: vi.fn(),
  copyDecoySite: vi.fn(),
  revertDecoySite: vi.fn(),
  addDecoySiteVersion: vi.fn(),
  listDecoyPages: vi.fn(),
  getDecoyPagePreview: vi.fn(),
  getDecoyFile: vi.fn(),
  listDecoyTemplates: vi.fn(),
  previewDecoyTemplate: vi.fn(),
  generateDecoySite: vi.fn(),
  getDecoyPhotos: vi.fn(),
  setDecoyPhotosKey: vi.fn(),
  setDecoyPhotosSource: vi.fn(),
};
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<typeof import("@/lib/api")>()),
  decoySites: new Proxy({}, { get: (_t, k: string) => (...a: unknown[]) => (api as Record<string, (...x: unknown[]) => unknown>)[k]!(...a) }),
  nodes: { setNodesDecoyMode: () => Promise.resolve({}) },
}));
const navigate = vi.fn();
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, params, className }: { children?: ReactNode; to: string; params?: Record<string, string>; className?: string }) => (
    <a href={params ? to.replace("$id", params.id!) : to} className={className}>
      {children}
    </a>
  ),
  useNavigate: () => navigate,
  useParams: () => ({}),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});
// Every root a test mounts, so none outlives its test: a root left mounted keeps polling the panel (the queries refetch every
// 10 s) and calls the mocks of a later test. A test that mounts again unmounts the previous ones with unmountAll().
const mounted: { root: Root; host: HTMLDivElement }[] = [];
const unmountAll = () => {
  act(() => mounted.forEach((m) => m.root.unmount()));
  mounted.forEach((m) => m.host.remove());
  mounted.length = 0;
};
beforeEach(() => {
  setLang("en");
  for (const f of Object.values(api)) f.mockReset();
  navigate.mockReset();
  api.getDecoyPhotos.mockResolvedValue({ hasKey: false, hasPixabayKey: false, source: "pixabay" });
  api.getDecoyPagePreview.mockResolvedValue({ html: "<p>home</p>", path: "" });
  api.listDecoyPages.mockResolvedValue({ pages: ["index.html", "about.html"], files: [], has404: true });
  api.getDecoyFile.mockResolvedValue({ text: "<h1>Hi</h1>", size: 11, truncated: false });
});
afterEach(unmountAll);

const checks = (over: Record<string, unknown>[] = []) => [
  { code: "index_html", level: "required", ok: true, paths: [] },
  { code: "size", level: "required", ok: true, paths: [] },
  { code: "file_count", level: "required", ok: true, paths: [] },
  { code: "safe_paths", level: "required", ok: true, paths: [] },
  ...over,
];
const site = (id: string, over: Record<string, unknown> = {}) => ({
  id: `dsi_${id}`, name: `Site ${id}`, note: `theme ${id}`, source: "upload", sourceLabel: "owner", state: "ready", bundleSha256: id.padEnd(64, "0"), createdUnix: 1000, createdBy: "owner", updatedUnix: 2000, updatedBy: "owner", version: 1,
  sizeBytes: 12_000, fileCount: 3, title: "T", checks: checks(), nodeCount: 0, onPanel: false, ...over,
});
const node = (name: string, over: Record<string, unknown> = {}) => ({
  id: `nod_${name}`, name, countryCode: "DE", location: "", provider: "", notes: "", address: `${name}.example.com`, status: NodeStatus.ONLINE, protocols: [], online: [], decoyMode: "builtin", decoySiteId: "", decoySupported: true, decoyReasonCode: "",
  decoyPanelAvailable: true, decoyOwnAvailable: true, ...over,
});
const library = (sites: ReturnType<typeof site>[], over: Record<string, unknown> = {}) => ({
  sites,
  builtin: [{ id: "builtin", available: true, reasonCode: "", nodeCount: 1, onPanel: false }, { id: "panel_dir", available: true, reasonCode: "", nodeCount: 0, onPanel: false }],
  // the panel's row: what it was told to show and, unless a test says otherwise, that is what it shows
  panel: { address: "panel.example.com", serving: (over.panelSource as string | undefined) ?? "builtin", reasonCode: "" },
  panelSource: "builtin", siteLimit: 100, versionLimit: 5, ...over,
});

type Mount = { role?: Role; edge?: boolean; nodes?: ReturnType<typeof node>[]; lib?: ReturnType<typeof library>; detail?: Record<string, unknown> & { site: ReturnType<typeof site> } };
async function mountUi(ui: ReactNode, o: Mount = {}) {
  const host = document.createElement("div");
  document.body.append(host);
  const root = createRoot(host);
  mounted.push({ root, host });
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, staleTime: Infinity, gcTime: Infinity } } });
  qc.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", role: o.role ?? Role.OWNER }, ...(o.edge ? { edition: "edge", unavailable: ["decoy_own"] } : {}) } as never);
  qc.setQueryData(accessQuery.queryKey, { available: true } as never);
  qc.setQueryData(nodesQuery.queryKey, { nodes: o.nodes ?? [node("de1"), node("nl1")] } as never);
  if (o.lib) qc.setQueryData(sitesQuery.queryKey, o.lib as never);
  if (o.detail) {
    const id = o.detail.site.id;
    qc.setQueryData(siteQuery(id).queryKey, { versions: [{ seq: 1, current: true, createdUnix: 2000, createdBy: "owner", sizeBytes: 12_000, fileCount: 3, checks: [], title: "", bundleSha256: "" }], files: [{ path: "index.html", size: 100 }, { path: "about.html", size: 90 }], nodes: [], ...o.detail } as never);
    qc.setQueryData(pagesQuery(id, o.detail.site.bundleSha256).queryKey, { pages: ["index.html", "about.html"], files: [], has404: true } as never);
  }
  await act(async () =>
    root.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>{ui}</ToastProvider>
      </QueryClientProvider>,
    ),
  );
  await settle();
}
const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 0))));
const text = () => document.body.textContent ?? "";
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const click = async (el: Element | null | undefined) => {
  await act(async () => void el?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
  await settle();
};
const type = async (el: HTMLInputElement, value: string) => {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
};
const list = (demo?: DecoysDemo) => <DecoysScreen demo={demo} />;
const page = (id: string, demo?: SiteDemo) => <DecoySiteScreen id={id} demo={demo} />;
const cards = () => [...document.querySelectorAll("a.dz-c")];

describe("the list", () => {
  const sites = [
    site("bk", { name: "Bakery", note: "bakery, German", nodeCount: 2 }),
    site("ph", { name: "Studio", note: "photo studio, English", state: "draft", source: "assistant" }),
    site("wd", { name: "Joinery", note: "furniture, Dutch", onPanel: true }),
    site("bt", { name: "Boats", note: "boat rental", source: "mcp", sourceLabel: "claude-desktop" }),
  ];
  const nodes = [node("de1"), node("nl1", { decoyMode: "own", decoySiteId: "dsi_bk" }), node("fi1", { decoyMode: "own", decoySiteId: "dsi_bk", decoyReasonCode: "pending" })];

  it("says when the panel shows something else than it was told to: the chosen site is a draft again", async () => {
    await mountUi(list(), { lib: library(sites, { panelSource: "site:dsi_ph", panel: { address: "panel.example.com", serving: "dir", reasonCode: "decoy_site_draft" } }), nodes });
    const panel = [...document.querySelectorAll("[data-shot=decoy-now] .dz-nw")].find((c) => c.textContent?.includes(en["dz.now.panel"]))!;
    expect(panel.textContent).toContain(en["dz.now.dir"]);
    expect(panel.textContent).toContain(en["dz.now.fb.decoy_site_draft"]);
    expect(panel.textContent).not.toContain("Studio");
  });

  it("fills the last row of the grid of nodes up to five cells, for any count of nodes and the panel", async () => {
    for (const count of [1, 4, 5, 9, 12, 13]) {
      unmountAll();
      await mountUi(list(), { lib: library(sites), nodes: Array.from({ length: count }, (_, i) => node(`n${i + 1}`)) });
      const now = document.querySelector("[data-shot=decoy-now]")!;
      const real = now.querySelectorAll(".dz-nw:not(.dz-nb)").length;
      expect(real, `${count} nodes`).toBe(count + 1); // the nodes and the panel
      expect(now.querySelectorAll(".dz-nw").length % 5, `${count} nodes`).toBe(0);
      expect(now.querySelectorAll(".dz-nb").length, `${count} nodes`).toBeLessThan(5);
    }
  });

  it("shows what stands on each node, the built-in entries and the own sites", async () => {
    await mountUi(list(), { lib: library(sites, { panelSource: "site:dsi_wd" }), nodes });
    expect(text()).toContain(en["dz.now"]);
    const now = document.querySelector("[data-shot=decoy-now]")!;
    expect(now.textContent).toContain("Bakery"); // nl1 and fi1 show it
    expect(now.textContent).toContain("Joinery"); // the panel shows it
    expect(now.textContent).toContain(en["dz.now.builtinSub"]); // de1 keeps the built-in page
    expect(now.querySelector(".dz-gl.busy")).not.toBeNull(); // fi1: on its way
    expect(text()).toContain("3 nodes and the panel");
    expect(text()).toContain(en["dz.bi.builtin"]);
    expect(text()).toContain(en["dz.bi.dir"]);
    expect(cards()).toHaveLength(4);
    expect(text()).toContain("4");
    expect(text()).toContain("of 100");
  });

  it("tells the state of each card: draft tag, source with the token, size, where it stands", async () => {
    await mountUi(list(), { lib: library(sites, { panelSource: "site:dsi_wd" }), nodes });
    const byName = (n: string) => cards().find((c) => c.textContent?.includes(n))!;
    expect(byName("Studio").textContent).toContain(en["dz.card.draft"]);
    expect(byName("Boats").textContent).toContain("claude-desktop");
    expect(byName("Bakery").textContent).toContain("2 nodes");
    expect(byName("Joinery").textContent).toContain(en["dz.use.panel"]);
    expect(byName("Studio").textContent).toContain(en["dz.use.none"]);
    expect(byName("Studio").textContent).toContain("11.7 KB");
  });

  it("counts the filters and narrows the cards, a search too, and says when nothing is found", async () => {
    await mountUi(list(), { lib: library(sites), nodes });
    const chip = (label: string) => document.querySelector<HTMLButtonElement>(`.dz-ch button[aria-pressed]:has(.dz-m)`) && [...document.querySelectorAll<HTMLButtonElement>(".dz-ch button")].find((b) => b.textContent?.startsWith(label))!;
    expect(chip(en["dz.f.all"])!.textContent).toBe(`${en["dz.f.all"]}4`);
    expect(chip(en["dz.f.draft"])!.textContent).toBe(`${en["dz.f.draft"]}1`);
    expect(chip(en["dz.f.used"])!.textContent).toBe(`${en["dz.f.used"]}2`); // the bakery stands on nodes, the joinery on the panel
    expect(chip(en["dz.f.free"])!.textContent).toBe(`${en["dz.f.free"]}1`); // the boats; a draft is neither in use nor free
    await click(chip(en["dz.f.draft"]));
    expect(cards().map((c) => c.textContent).join()).toContain("Studio");
    expect(cards()).toHaveLength(1);
    await click(chip(en["dz.f.all"]));
    const q = document.querySelector<HTMLInputElement>(`input[aria-label="${en["dz.qLabel"]}"]`)!;
    await type(q, "bakery");
    expect(cards()).toHaveLength(1);
    await type(q, "zzz");
    expect(cards()).toHaveLength(0);
    expect(text()).toContain(en["dz.none.search"]);
    await click(button(en["dz.none.reset"]));
    expect(cards()).toHaveLength(4);
  });

  it("opens with the filter and the search the dev kit asks for", async () => {
    await mountUi(list({ filter: "draft" }), { lib: library(sites), nodes });
    expect(cards()).toHaveLength(1);
  });

  it("shows the empty first look with the four ways", async () => {
    await mountUi(list(), { lib: library([]), nodes: [node("de1")] });
    expect(text()).toContain(en["dz.first.title"]);
    expect(document.querySelectorAll(".dz-way")).toHaveLength(4);
    expect(document.querySelector(".dz-bar")).toBeNull();
    expect(text()).toContain(en["dz.now.sumBi"].replace("{nodes}", "1 node"));
  });

  it("closes the new button when the library is full, and says why", async () => {
    const many = Array.from({ length: 100 }, (_, i) => site(`s${i}`));
    await mountUi(list(), { lib: library(many), nodes });
    const add = button(en["dz.new"])!;
    expect(add.hasAttribute("disabled") || add.getAttribute("data-disabled") !== null).toBe(true);
    expect(add.closest("span")?.getAttribute("title")).toBe(en["dz.newFull"].replace("{n}", "100"));
  });

  it("says on a card and on the page where a site came from: a template shows its kind", async () => {
    const made = site("tp", { name: "Rainy Reader", sourceLabel: "template:blog" });
    await mountUi(list(), { lib: library([made, site("up")]), nodes });
    const meta = cards().map((c) => c.querySelector(".dz-src")?.textContent);
    expect(meta).toEqual([`${en["dz.src.template"]} · ${en["dz.tpl.blog"]}`, en["dz.src.upload"]]);
    unmountAll();
    await mountUi(page("dsi_tp"), { lib: library([made]), detail: { site: made } });
    expect(document.querySelector(".dz-meta .dz-src")?.textContent).toBe(`${en["dz.src.template"]} · ${en["dz.tpl.blog"]}`);
    expect(text()).toContain(en["dz.fact.template"].replace("{kind}", en["dz.tpl.blog"]));
    // a kind that a later release adds keeps the name the panel gave it
    unmountAll();
    await mountUi(list(), { lib: library([site("tp", { sourceLabel: "template:newkind" })]), nodes });
    expect(cards()[0]!.querySelector(".dz-src")?.textContent).toBe(`${en["dz.src.template"]} · newkind`);
  });

  it("opens the three ways of the new-site window: archive, task for an agent", async () => {
    await mountUi(list(), { lib: library(sites), nodes });
    await click(button(en["dz.new"]));
    expect(text()).toContain(en["dz.d.new"]);
    expect(text()).toContain(en["dz.way.mcp"]);
    await click([...document.querySelectorAll(".dz-way")].find((w) => w.textContent?.includes(en["dz.way.mcp"])));
    expect(text()).toContain(en["dz.d.mcp"]);
    const task = document.querySelector(".dz-task")!.textContent!;
    expect(task).toContain("decoy_sites_list");
    expect(task).toContain("— bakery, German"); // the subjects that are taken
    expect(task).toContain("— boat rental");
  });

  it("the assistant way opens the chat with a request, and closes the window", async () => {
    const heard = vi.fn();
    window.addEventListener("mistgate:assistant-open", heard);
    await mountUi(list(), { lib: library(sites), nodes });
    await click(button(en["dz.new"]));
    await click([...document.querySelectorAll(".dz-way")].find((w) => w.textContent?.includes(en["dz.way.as"])));
    window.removeEventListener("mistgate:assistant-open", heard);
    expect(heard).toHaveBeenCalledTimes(1);
    expect((heard.mock.calls[0]![0] as CustomEvent).detail.text).toContain("decoy site");
    expect(text()).not.toContain(en["dz.d.new"]);
  });
});

// the panel lists the kinds by id; the window puts them in its own order. The Netherlands has only two of them.
const countries = ["us", "de", "nl"];
const kindOf = (id: string) => ({ id, nameEn: `Kind ${id}`, nameRu: `Вид ${id}`, countries: id === "files" || id === "login" ? countries : ["us", "de"] });
const listed = (templates: ReturnType<typeof kindOf>[]) => ({ templates, countries });

describe("a site from a template", () => {
  const kinds = ["blog", "business", "card", "club", "docs", "files", "login", "portfolio", "project", "status"].map(kindOf);
  const shown = (n: number) => ({ html: `<p>page ${n}</p>`, seed: `${n}`.padStart(16, "0"), name: `Brand ${n}` });
  const open = async (lib = library([site("a")])) => {
    api.listDecoyTemplates.mockResolvedValue(listed(kinds));
    await mountUi(list(), { lib, nodes: [node("de1")] });
    await click(button(en["dz.new"]));
    await click([...document.querySelectorAll(".dz-way")].find((w) => w.textContent?.includes(en["dz.way.tpl"])));
  };
  const frame = () => document.querySelector(".dz-fr iframe")?.getAttribute("srcdoc");

  it("offers the ten kinds in the window's order with a name and a line each, and asks for no preview before one is chosen", async () => {
    await open();
    expect(text()).toContain(en["dz.d.tpl"]);
    const cardsOf = [...document.querySelectorAll(".dz-ways.tpl .dz-way")];
    expect(cardsOf).toHaveLength(10);
    expect(cardsOf.map((c) => c.querySelector("b")?.textContent)).toEqual(
      ["status", "login", "files", "docs", "project", "blog", "club", "portfolio", "business", "card"].map((k) => en[`dz.tpl.${k}` as keyof typeof en]),
    );
    expect(cardsOf[0]!.textContent).toBe(`${en["dz.tpl.status"]}${en["dz.tpl.status.text"]}`);
    expect(api.previewDecoyTemplate).not.toHaveBeenCalled();
    expect(button(en["dz.tpl.create"])).toBeUndefined();
  });

  it("starts with the country most of the nodes that show a shared page stand in, with its flag, and the first country when there is none", async () => {
    api.listDecoyTemplates.mockResolvedValue(listed(kinds));
    const open2 = async (nodes: ReturnType<typeof node>[]) => {
      unmountAll();
      await mountUi(list(), { lib: library([site("a")]), nodes });
      await click(button(en["dz.new"]));
      await click([...document.querySelectorAll(".dz-way")].find((w) => w.textContent?.includes(en["dz.way.tpl"])));
      return document.querySelector(".dz-cc");
    };
    let row = await open2([node("nl1", { countryCode: "NL" }), node("de1"), node("de2"), node("fi1", { countryCode: "FI" })]);
    expect(row?.textContent).toContain(en["dz.tpl.country"]);
    expect(row?.querySelector("[role=combobox]")?.textContent).toContain("Germany");
    expect(row?.querySelector("svg")).not.toBeNull(); // the flag
    // a node with a site of its own does not vote, and a country the panel has no template for does not either
    row = await open2([node("nl1", { countryCode: "NL" }), node("de1", { decoyMode: "own", decoySiteId: "dsi_a" }), node("de2", { decoyMode: "own", decoySiteId: "dsi_a" }), node("fi1", { countryCode: "FI" })]);
    expect(row?.querySelector("[role=combobox]")?.textContent).toContain("Netherlands");
    row = await open2([node("fi1", { countryCode: "FI" })]);
    expect(row?.querySelector("[role=combobox]")?.textContent).toContain("United States");
  });

  it("shows only the kinds that have texts for the country, and asks for the previews of that country", async () => {
    api.listDecoyTemplates.mockResolvedValue(listed(kinds));
    api.previewDecoyTemplate.mockResolvedValue(shown(1));
    await mountUi(list({ dialog: "tpl", template: { phase: "pick", country: "nl" } }), { lib: library([site("a")]), nodes: [node("de1")] });
    const names = () => [...document.querySelectorAll(".dz-ways.tpl .dz-way b")].map((b) => b.textContent);
    expect(names()).toEqual([en["dz.tpl.login"], en["dz.tpl.files"]]);
    expect(document.querySelector(".dz-cc [role=combobox]")?.textContent).toContain("Netherlands");
    await click([...document.querySelectorAll(".dz-way")].find((w) => w.textContent?.includes(en["dz.tpl.files"])));
    expect(api.previewDecoyTemplate).toHaveBeenLastCalledWith({ kind: "files", seed: "", country: "nl" });
    expect(document.querySelector(".dz-uf")?.textContent).toContain("Netherlands");
  });

  it("previews the chosen kind, another variant asks for a new one, and Create saves the variant in view", async () => {
    api.previewDecoyTemplate.mockResolvedValueOnce(shown(1)).mockResolvedValueOnce(shown(2));
    api.generateDecoySite.mockResolvedValue({ site: site("new", { id: "dsi_new" }) });
    await open();
    await click([...document.querySelectorAll(".dz-way")].find((w) => w.textContent?.includes(en["dz.tpl.blog"])));
    expect(api.previewDecoyTemplate).toHaveBeenLastCalledWith({ kind: "blog", seed: "", country: "de" });
    expect(frame()).toBe("<p>page 1</p>");
    expect(text()).toContain(en["dz.tpl.footView"].replace("{name}", "Brand 1"));
    expect(api.generateDecoySite).not.toHaveBeenCalled(); // a preview saves nothing
    await click(button(en["dz.tpl.another"]));
    expect(api.previewDecoyTemplate).toHaveBeenCalledTimes(2);
    expect(api.previewDecoyTemplate).toHaveBeenLastCalledWith({ kind: "blog", seed: "", country: "de" });
    expect(frame()).toBe("<p>page 2</p>");
    await click(button(en["dz.tpl.create"]));
    expect(api.generateDecoySite).toHaveBeenCalledTimes(1);
    // the note says what it is made of: "Template · kind · country"
    expect(api.generateDecoySite).toHaveBeenCalledWith({ kind: "blog", seed: shown(2).seed, note: `${en["dz.tpl.noteLead"]} · ${en["dz.tpl.blog"]} · Germany`, country: "de" });
    expect(navigate).toHaveBeenCalledWith({ to: "/decoys/$id", params: { id: "dsi_new" } });
  });

  it("goes back to the kinds, and says in words when the library is full", async () => {
    api.previewDecoyTemplate.mockResolvedValue(shown(1));
    api.generateDecoySite.mockRejectedValue(new ConnectError("decoy_library_full", Code.FailedPrecondition));
    await open();
    await click([...document.querySelectorAll(".dz-way")].find((w) => w.textContent?.includes(en["dz.tpl.card"])));
    await click(button(en["dz.tpl.create"]));
    expect(document.querySelector("[role=alert]")?.textContent).toContain(en["err.decoy_library_full"]);
    expect(text()).toContain(en["dz.tpl.footErr"]);
    expect(navigate).not.toHaveBeenCalled();
    await click(button(en["dz.tpl.back"]));
    expect(document.querySelectorAll(".dz-ways.tpl .dz-way")).toHaveLength(10);
    expect(button(en["dz.tpl.create"])).toBeUndefined();
  });

  it("keeps Create closed while the page is being made, and says when the preview failed", async () => {
    api.previewDecoyTemplate.mockReturnValue(new Promise(() => {}));
    await open();
    await click([...document.querySelectorAll(".dz-way")].find((w) => w.textContent?.includes(en["dz.tpl.docs"])));
    expect(document.querySelector(".dz-fr")?.textContent).toContain(en["dz.tpl.loading"]);
    expect(button(en["dz.tpl.create"])!.getAttribute("aria-disabled")).toBe("true");
    await click(button(en["dz.tpl.create"]));
    expect(api.generateDecoySite).not.toHaveBeenCalled();
    unmountAll();
    api.previewDecoyTemplate.mockReset().mockRejectedValue(new ConnectError("decoy_template_unknown", Code.InvalidArgument));
    await open();
    await click([...document.querySelectorAll(".dz-way")].find((w) => w.textContent?.includes(en["dz.tpl.docs"])));
    expect(document.querySelector(".dz-fr [role=alert]")?.textContent).toContain(en["err.decoy_template_unknown"]);
    expect(button(en["dz.tpl.create"])!.getAttribute("aria-disabled")).toBe("true");
  });

  it("names a kind this screen does not know yet by the panel's own name, and shows it in every language", async () => {
    api.listDecoyTemplates.mockResolvedValue(listed([...kinds, { ...kindOf("newkind"), nameEn: "A new kind", nameRu: "Новый вид" }]));
    await mountUi(list(), { lib: library([]), nodes: [node("de1")] });
    await click(button(en["dz.new"]));
    await click([...document.querySelectorAll(".dz-way")].find((w) => w.textContent?.includes(en["dz.way.tpl"])));
    expect(document.querySelectorAll(".dz-ways.tpl .dz-way")).toHaveLength(11);
    expect(text()).toContain("A new kind");
    for (const lang of ["ru", "zh", "fa", "tr"] as const) {
      setLang(lang);
      unmountAll();
      await mountUi(list(), { lib: library([]), nodes: [node("de1")] });
      await click(button(dictionaries[lang]["dz.new"]));
      await click([...document.querySelectorAll(".dz-way")].find((w) => w.textContent?.includes(dictionaries[lang]["dz.way.tpl"])));
      expect(document.querySelectorAll(".dz-ways.tpl .dz-way"), lang).toHaveLength(11);
      expect(text(), lang).toContain(dictionaries[lang]["dz.tpl.blog"]);
    }
  });
});

describe("the photos of a generated site", () => {
  const kinds = ["blog", "card"].map(kindOf);
  const nothing = { hasKey: false, hasPixabayKey: false, source: "pixabay" };
  const seeded = (n: number, photos: string, photosSource = "pixabay") => ({ html: `<p>page ${n}</p>`, seed: `${n}`.padStart(16, "0"), name: `Brand ${n}`, photos, photosSource });
  const SECRET = "pixabay-test-key-0123456789ab";
  const view = async (photos = "no_key", photosSource = "pixabay") => {
    api.listDecoyTemplates.mockResolvedValue(listed(kinds));
    api.previewDecoyTemplate.mockResolvedValue(seeded(7, photos, photosSource));
    await mountUi(list(), { lib: library([site("a")]), nodes: [node("de1")] });
    await click(button(en["dz.new"]));
    await click([...document.querySelectorAll(".dz-way")].find((w) => w.textContent?.includes(en["dz.way.tpl"])));
    await click([...document.querySelectorAll(".dz-way")].find((w) => w.textContent?.includes(en["dz.tpl.blog"])));
  };
  const row = () => document.querySelector(".dz-ph");
  const radio = (name: string) => [...document.querySelectorAll(".dz-ph .src [role=radio]")].find((r) => r.textContent?.includes(name));

  it("names the source and says whether it has a key on the page of the kinds as well, with the line both services ask for", async () => {
    api.listDecoyTemplates.mockResolvedValue(listed(kinds));
    await mountUi(list(), { lib: library([site("a")]), nodes: [node("de1")] });
    await click(button(en["dz.new"]));
    await click([...document.querySelectorAll(".dz-way")].find((w) => w.textContent?.includes(en["dz.way.tpl"])));
    expect(row()?.textContent).toContain(en["dz.ph.title"]);
    expect(row()?.textContent).toContain(en["dz.ph.off"]);
    expect(button(en["dz.ph.add"])).toBeDefined();
    expect(button(en["dz.ph.remove"])).toBeUndefined();
    // nothing is stored: Pixabay, the source that still gives keys, is the one in view
    expect(document.querySelectorAll(".dz-ph .src [role=radio]")).toHaveLength(2);
    expect(radio("Pixabay")?.getAttribute("data-checked")).not.toBeNull();
    expect(radio("Pexels")?.getAttribute("data-checked")).toBeNull();
    const credit = row()?.querySelector(".ft a");
    expect(credit?.getAttribute("href")).toBe("https://pixabay.com");
    expect(credit?.getAttribute("rel")).toBe("noopener noreferrer");
    expect(credit?.textContent).toBe("Photos provided by Pixabay");
    expect(row()?.querySelector(".hint")).toBeNull();
  });

  it("says one line about the preview's photos, naming the source it was made from, for every answer the panel gives", async () => {
    for (const source of ["pixabay", "pexels"]) {
      const name = source === "pexels" ? "Pexels" : "Pixabay";
      for (const photos of ["used", "no_key", "bad_key", "limit", "unavailable", "none_for_kind"]) {
        unmountAll();
        await view(photos, source);
        const line = row()?.querySelector(".ft")?.textContent;
        expect(line, `${source} ${photos}`).toContain(en[`dz.ph.st.${photos}` as "dz.ph.st.used"].replace("{name}", name));
        expect(line, `${source} ${photos}`).not.toContain("{name}");
        // the credit follows the preview, not the pick: the photos in view are from this source
        expect(row()?.querySelector(".ft a")?.getAttribute("href")).toBe(`https://${source === "pexels" ? "www.pexels.com" : "pixabay.com"}`);
      }
    }
    // a word this screen does not know is not shown as a key of the dictionary
    unmountAll();
    await view("something_new");
    expect(text()).not.toContain("dz.ph.st.");
  });

  it("takes the key in a password field, builds the same site again with it, and never shows the key again", async () => {
    api.getDecoyPhotos.mockResolvedValue(nothing);
    api.setDecoyPhotosKey.mockResolvedValue({ hasKey: false, hasPixabayKey: true, source: "pixabay" });
    await view();
    await click(button(en["dz.ph.add"]));
    const field = row()?.querySelector("input[type=password]") as HTMLInputElement;
    expect(field.type).toBe("password");
    expect(field.autocomplete).toBe("off");
    expect(row()?.textContent).toContain(en["dz.ph.lead"]);
    expect(row()?.textContent).toContain(en["dz.ph.howPixabay"]);
    expect(row()?.textContent).toContain(en["dz.ph.field"].replace("{name}", "Pixabay"));
    expect(row()?.querySelector(".frm a")?.getAttribute("href")).toBe("https://pixabay.com/api/docs/");
    expect(button(en["dz.ph.save"])!.disabled).toBe(true); // nothing typed
    await type(field, `  ${SECRET} `);
    api.previewDecoyTemplate.mockResolvedValue(seeded(7, "used"));
    await click(button(en["dz.ph.save"]));
    expect(api.setDecoyPhotosKey).toHaveBeenCalledWith({ key: SECRET, source: "pixabay" });
    // the site in view is made again from its own seed, so that it comes back with its photos
    expect(api.previewDecoyTemplate).toHaveBeenLastCalledWith({ kind: "blog", seed: seeded(7, "").seed, country: "de" });
    expect(row()?.querySelector("input[type=password]")).toBeNull();
    expect(text()).not.toContain(SECRET);
    expect(row()?.textContent).toContain(en["dz.ph.on"]);
    expect(row()?.querySelector(".ft")?.textContent).toContain(en["dz.ph.st.used"]);
    expect(button(en["dz.ph.remove"])).toBeDefined();
    // with a key set the button that opens the form says so
    expect(button(en["dz.ph.add"])).toBeUndefined();
    expect(button(en["dz.ph.change"])).toBeDefined();
    expect(radio("Pixabay")?.querySelector(".tone-ok")).not.toBeNull(); // the source with a key wears the dot
    expect(radio("Pexels")?.querySelector(".tone-ok")).toBeNull();
  });

  it("removes the key with one press, and a refused key is said in words and stays in the field to be fixed", async () => {
    api.getDecoyPhotos.mockResolvedValue({ hasKey: false, hasPixabayKey: true, source: "pixabay" });
    api.setDecoyPhotosKey.mockResolvedValue(nothing);
    await view("used");
    expect(row()?.textContent).toContain(en["dz.ph.on"]);
    await click(button(en["dz.ph.remove"]));
    expect(api.setDecoyPhotosKey).toHaveBeenCalledWith({ key: "", source: "pixabay" });
    expect(row()?.textContent).toContain(en["dz.ph.off"]);
    expect(button(en["dz.ph.remove"])).toBeUndefined();
    expect(button(en["dz.ph.add"])).toBeDefined(); // the key is gone: the button asks for one again

    api.setDecoyPhotosKey.mockReset().mockRejectedValue(new ConnectError("decoy_photos_key_invalid", Code.InvalidArgument));
    await click(button(en["dz.ph.add"]));
    await type(row()?.querySelector("input[type=password]") as HTMLInputElement, "bad key");
    await click(button(en["dz.ph.save"]));
    expect(row()?.textContent).toContain(en["err.decoy_photos_key_invalid"]);
    expect((row()?.querySelector("input[type=password]") as HTMLInputElement | null)?.value).toBe("bad key");
    await click(button(en["dz.ph.cancel"]));
    expect(row()?.querySelector("input[type=password]")).toBeNull();
  });

  it("looking at a source without a key, a preview without photos says nothing of the other source", async () => {
    api.getDecoyPhotos.mockResolvedValue({ hasKey: false, hasPixabayKey: false, source: "pixabay" });
    await view("no_key", "pixabay");
    expect(row()?.querySelector(".ft")?.textContent).toContain("Pixabay");
    await click(radio("Pexels"));
    const foot = row()?.querySelector(".ft");
    expect(foot?.textContent).not.toContain("Pixabay");
    expect(foot?.querySelector("a")?.getAttribute("href")).toBe("https://www.pexels.com");
  });

  it("looks at Pexels without a key: a calm hint to Pixabay, nothing is asked of the panel, and the key typed there is Pexels'", async () => {
    api.getDecoyPhotos.mockResolvedValue({ hasKey: false, hasPixabayKey: true, source: "pixabay" });
    api.setDecoyPhotosKey.mockResolvedValue({ hasKey: true, hasPixabayKey: true, source: "pexels" });
    await view("used");
    expect(row()?.querySelector(".hint")).toBeNull();
    await click(radio("Pexels"));
    expect(api.setDecoyPhotosSource).not.toHaveBeenCalled(); // no key there: the pick stays in this window
    expect(radio("Pexels")?.getAttribute("data-checked")).not.toBeNull();
    expect(row()?.textContent).toContain(en["dz.ph.off"]);
    expect(row()?.querySelector(".hint")?.textContent).toContain(en["dz.ph.pexelsHint"]);
    const hint = row()?.querySelector(".hint a");
    expect(hint?.getAttribute("href")).toBe("https://pixabay.com/api/docs/");
    expect(hint?.getAttribute("target")).toBe("_blank");
    await click(button(en["dz.ph.add"]));
    expect(row()?.querySelector(".hint")).toBeNull(); // the form takes its place
    expect(row()?.textContent).toContain(en["dz.ph.field"].replace("{name}", "Pexels"));
    expect(row()?.querySelector(".frm a")?.getAttribute("href")).toBe("https://www.pexels.com/api/");
    expect(row()?.textContent).not.toContain(en["dz.ph.howPixabay"]);
    await type(row()?.querySelector("input[type=password]") as HTMLInputElement, "pexels-test-key-0123456789abcdef");
    await click(button(en["dz.ph.save"]));
    expect(api.setDecoyPhotosKey).toHaveBeenCalledWith({ key: "pexels-test-key-0123456789abcdef", source: "pexels" });
    expect(radio("Pexels")?.getAttribute("data-checked")).not.toBeNull(); // the key just stored is the one in use
  });

  it("picks between two keys with one press and builds the same site again from the other source", async () => {
    api.getDecoyPhotos.mockResolvedValue({ hasKey: true, hasPixabayKey: true, source: "pixabay" });
    api.setDecoyPhotosSource.mockResolvedValue({ hasKey: true, hasPixabayKey: true, source: "pexels" });
    await view("used");
    expect(radio("Pixabay")?.querySelector(".tone-ok")).not.toBeNull();
    expect(radio("Pexels")?.querySelector(".tone-ok")).not.toBeNull();
    api.previewDecoyTemplate.mockResolvedValue(seeded(7, "limit", "pexels"));
    await click(radio("Pexels"));
    expect(api.setDecoyPhotosSource).toHaveBeenCalledWith({ source: "pexels" });
    expect(api.previewDecoyTemplate).toHaveBeenLastCalledWith({ kind: "blog", seed: seeded(7, "").seed, country: "de" });
    expect(radio("Pexels")?.getAttribute("data-checked")).not.toBeNull();
    expect(row()?.querySelector(".ft")?.textContent).toContain(en["dz.ph.st.limit"].replace("{name}", "Pexels"));
    expect(row()?.querySelector(".ft a")?.getAttribute("href")).toBe("https://www.pexels.com");
    // pressing the source already in use asks for nothing
    api.setDecoyPhotosSource.mockClear();
    await click(radio("Pexels"));
    expect(api.setDecoyPhotosSource).not.toHaveBeenCalled();
  });

  it("is worded in every language, and every language keeps the name of the source in the lines that carry it", () => {
    const keys = Object.keys(en).filter((k) => k.startsWith("dz.ph.") || /^err\.decoy_photos_/.test(k) || k === "err.decoy_template_country" || /^dz\.tpl\.(country|countryText|noteLead)$/.test(k));
    expect(keys.length).toBeGreaterThanOrEqual(30);
    for (const lang of ["ru", "zh", "fa", "tr"] as const) {
      for (const k of keys) {
        const w = dictionaries[lang][k as keyof typeof en];
        expect(w, `${lang} ${k}`).toBeTruthy();
        if (!/^(dz\.ph\.credit|dz\.ph\.title)$/.test(k)) expect(w, `${lang} ${k} is the English text`).not.toBe(en[k as keyof typeof en]);
        if (en[k as keyof typeof en].includes("{name}")) expect(w, `${lang} ${k} lost {name}`).toContain("{name}");
      }
    }
  });
});

describe("what the list says when it cannot list", () => {
  it("loads: skeleton cards, no data", async () => {
    api.listDecoySites.mockReturnValue(new Promise(() => {}));
    await mountUi(list());
    expect(document.querySelector("[aria-busy=true]")).not.toBeNull();
    expect(document.querySelectorAll(".dz-sk")).toHaveLength(6);
  });

  it("fails: the card with 'Try again', and the nodes are said to go on", async () => {
    api.listDecoySites.mockRejectedValue(new ConnectError("down", Code.Unavailable));
    await mountUi(list());
    await settle();
    expect(document.querySelector("[role=alert]")?.textContent).toContain(en["dz.err.title"]);
    expect(text()).toContain(en["dz.err.text"]);
    api.listDecoySites.mockResolvedValue(library([site("a")]));
    await click(button(en["common.retry"]));
    await settle();
    expect(cards()).toHaveLength(1);
  });

  it("is for the owner only: a helper sees the card, and the panel is never asked", async () => {
    await mountUi(list(), { role: Role.HELPER });
    expect(text()).toContain(en["dz.deny.title"]);
    expect(api.listDecoySites).not.toHaveBeenCalled();
    expect(button(en["dz.new"])).toBeUndefined();
  });

  it("the Cloudflare edition keeps no sites: it says so, lists the built-in page only, and asks for no library", async () => {
    await mountUi(list(), { edge: true });
    expect(text()).toContain(en["dz.edge.title"]);
    expect(text()).toContain(en["dz.bi.builtin"]);
    expect(text()).not.toContain(en["dz.bi.dir"]);
    expect(text()).not.toContain(en["dz.mine"]);
    expect(button(en["dz.new"])).toBeUndefined();
    expect(api.listDecoySites).not.toHaveBeenCalled();
  });
});

describe("uploading an archive", () => {
  const file = (name: string, size = 1000) => {
    const f = new File([new Uint8Array(Math.min(size, 10))], name);
    Object.defineProperty(f, "size", { value: size });
    return f;
  };
  const choose = async (f: File) => {
    const input = document.querySelector<HTMLInputElement>(`input[type=file][aria-label="${en["dz.d.up"]}"]`)!;
    await act(async () => {
      Object.defineProperty(input, "files", { value: [f], configurable: true });
      input.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await settle();
  };
  const open = (lib = library([site("a", { name: "Bakery" })])) => mountUi(list({ dialog: "up" }), { lib });

  it("turns a file over 2 MiB back at once, without sending it", async () => {
    await open();
    await choose(file("big.zip", 3 * 1024 * 1024 + 1));
    expect(text()).toContain(en["dz.er.decoy_bundle_too_large"]);
    expect(api.createDecoySite).not.toHaveBeenCalled();
    expect(text()).toContain(en["dz.up.footErr"]);
  });

  for (const [code, path] of [
    ["decoy_bundle_too_many_files", ""],
    ["decoy_bundle_bad_path", "img%2Flatest"],
    ["decoy_bundle_no_index", ""],
    ["decoy_bundle_bad_archive", ""],
  ] as const) {
    it(`says ${code} in words`, async () => {
      api.createDecoySite.mockRejectedValue(new ConnectError(path ? `${code}: path=${path}` : code, Code.InvalidArgument));
      await open();
      await choose(file("site.zip"));
      expect(document.querySelector("[role=alert]")?.textContent).toContain(en[`dz.er.${code}` as keyof typeof en]);
      if (path) expect(document.querySelector("[role=alert]")?.textContent).toContain("img/latest");
      expect(button(en["dz.up.again"])).toBeDefined();
    });
  }

  it("saves the archive as a ready site named after the file, then asks the name and the note", async () => {
    api.createDecoySite.mockResolvedValue({ site: site("new", { name: "holzofen", note: "" }) });
    await open();
    await choose(file("holzofen.zip", 412_000));
    expect(api.createDecoySite).toHaveBeenCalledTimes(1);
    const req = api.createDecoySite.mock.calls[0]![0] as { name: string; state: string; source: string; bundleArchive: Uint8Array };
    expect(req).toMatchObject({ name: "holzofen", state: "ready", source: "upload" });
    expect(req.bundleArchive).toBeInstanceOf(Uint8Array);
    expect(text()).toContain(en["dz.res.ready"]);
    const name = document.querySelector<HTMLInputElement>(`input[aria-label="${en["dz.fld.name"]}"]`)!;
    expect(name.value).toBe("holzofen");
    expect(text()).toContain(en["dz.fld.noteHint"]);
  });

  it("keeps the name taken: the site cannot be opened until the name is another", async () => {
    api.createDecoySite.mockResolvedValue({ site: site("new", { name: "holzofen", note: "" }) });
    await open(library([site("a", { name: "Bakery" })]));
    await choose(file("holzofen.zip"));
    const name = document.querySelector<HTMLInputElement>(`input[aria-label="${en["dz.fld.name"]}"]`)!;
    await type(name, "bakery");
    expect(text()).toContain(en["dz.nameTaken"]);
    expect(text()).toContain(en["dz.up.footTaken"]);
    const openBtn = button(en["dz.up.open"])!;
    expect(openBtn.hasAttribute("aria-disabled") || openBtn.getAttribute("aria-disabled") === "true").toBe(true);
    await click(openBtn);
    expect(api.updateDecoySite).not.toHaveBeenCalled();
    await type(name, "Holz ofen");
    expect(text()).toContain(en["dz.up.footName"]);
  });

  it("saves the new name and the note when the site is opened", async () => {
    api.createDecoySite.mockResolvedValue({ site: site("new", { name: "holzofen", note: "" }) });
    api.updateDecoySite.mockResolvedValue({});
    await open();
    await choose(file("holzofen.zip"));
    await type(document.querySelector<HTMLInputElement>(`input[aria-label="${en["dz.fld.name"]}"]`)!, "Holzofen bakery");
    await type(document.querySelector<HTMLInputElement>(`input[aria-label="${en["dz.fld.note"]}"]`)!, "bakery, German");
    await click(button(en["dz.up.open"]));
    expect(api.updateDecoySite).toHaveBeenCalledWith({ siteId: "dsi_new", name: "Holzofen bakery", note: "bakery, German" });
    expect(navigate).toHaveBeenCalledWith({ to: "/decoys/$id", params: { id: "dsi_new" } });
  });
});

describe("the page of a site", () => {
  const nodes = [node("de1"), node("nl1", { decoyMode: "own", decoySiteId: "dsi_a" }), node("fi1", { decoyMode: "own", decoySiteId: "dsi_a", address: "fi1.example.org" })];
  const lib = (extra: ReturnType<typeof site>[] = []) => library([site("a", { name: "Bakery", nodeCount: 2 }), site("b", { name: "Studio", state: "draft" }), ...extra], { panelSource: "builtin" });
  const used = { site: site("a", { name: "Bakery", nodeCount: 2, checks: checks([{ code: "external_urls", level: "warning", ok: false, paths: ["css/style.css"] }]) }), nodes: [
    { nodeId: "nod_nl1", nodeName: "nl1", reasonCode: "", supported: true, agentSha256: "" },
    { nodeId: "nod_fi1", nodeName: "fi1", reasonCode: "pending", supported: true, agentSha256: "" },
  ], sharedDomains: { code: "shared_domains", level: "warning", ok: false, paths: ["fi1", "nl1"] } };
  const free = { site: site("c", { name: "Boats" }) };
  const draft = { site: site("b", { name: "Studio", state: "draft" }) };
  const bad = { site: site("d", { name: "Backup", state: "draft", checks: checks().map((c) => (c.code === "index_html" ? { ...c, ok: false } : c)) }) };

  it("heads the page with the name, the state and where it stands, and lists the nodes with their delivery", async () => {
    await mountUi(page("dsi_a"), { lib: lib(), nodes, detail: used });
    const h1 = document.querySelector("h1")!;
    expect(h1.textContent).toBe("Bakery");
    expect(text()).toContain(en["dz.st.ready"]);
    expect(text()).toContain("stands on 2 nodes");
    const placement = document.querySelector("[data-shot=decoy-placement]")!;
    expect(placement.textContent).toContain("nl1");
    expect(placement.textContent).toContain(en["dz.dl.ok"]);
    expect(placement.textContent).toContain(en["dz.dl.way"]); // fi1 is on its way
    expect(placement.querySelectorAll("button")).toHaveLength(2); // "Take off" on every row
    const checksCard = document.querySelector("[data-shot=decoy-checks]")!;
    expect(checksCard.textContent).toContain(en["dz.ck.external_urls"]);
    expect(checksCard.textContent).toContain("css/style.css"); // the file that fired it
    expect(checksCard.textContent).toContain(en["dz.ck.shared_domains"]); // the panel's own warning for the nodes it stands on
    expect(checksCard.textContent).toContain("fi1, nl1");
  });

  it("does not count domains itself: no warning from the panel, none on the page, whatever the addresses are", async () => {
    // fi1 is on another domain than nl1, but the panel says nothing
    await mountUi(page("dsi_a"), { lib: lib(), nodes, detail: { ...used, sharedDomains: undefined } });
    expect(document.querySelector("[data-shot=decoy-checks]")!.textContent).not.toContain(en["dz.ck.shared_domains"]);
    expect(document.querySelector("[data-shot=decoy-checks]")!.textContent).not.toContain("fi1, nl1");
    expect(document.querySelector("[data-shot=decoy-checks]")!.querySelectorAll("li.part")).toHaveLength(1); // only the external address
  });

  it("puts the panel's address on its row in the window that assigns", async () => {
    await mountUi(page("dsi_c", { dialog: "as" }), { lib: library([site("c", { name: "Boats" })]), nodes, detail: free });
    expect(document.querySelector(".dz-asl")!.textContent).toContain("panel.example.com");
  });

  it("puts the panel's address on its row", async () => {
    await mountUi(page("dsi_w"), { lib: library([site("w", { name: "Joinery", onPanel: true })], { panelSource: "site:dsi_w" }), nodes, detail: { site: site("w", { name: "Joinery", onPanel: true }) } });
    const row = [...document.querySelectorAll("[data-shot=decoy-placement] .dz-plr")].find((r) => r.textContent?.includes(en["dz.pl.panel"]))!;
    expect(row.textContent).toContain("panel.example.com");
  });

  it("a draft cannot be assigned: no Assign button, 'Ready to install' instead, and the place says so", async () => {
    await mountUi(page("dsi_b"), { lib: lib(), nodes, detail: draft });
    expect(button(en["dz.act.assign"])).toBeUndefined();
    expect(button(en["dz.act.ready"])).toBeDefined();
    expect(text()).toContain(en["dz.pl.noneDraft"]);
    expect(text()).toContain(en["dz.ban.draft"]);
  });

  it("a site that fails a required check cannot be marked ready, and the banner says what to do", async () => {
    await mountUi(page("dsi_d"), { lib: lib(), nodes, detail: bad });
    const ready = button(en["dz.act.ready"])!;
    expect(ready.hasAttribute("disabled") || ready.getAttribute("data-disabled") !== null).toBe(true);
    expect(text()).toContain(en["dz.ban.bad"]);
    expect(text()).toContain(en["dz.ban.badText"]);
    expect(text()).toContain(en["dz.ck.indexBad"]);
    await click(ready);
    expect(api.setDecoySiteReady).not.toHaveBeenCalled();
  });

  it("marks a draft ready", async () => {
    api.setDecoySiteReady.mockResolvedValue({});
    await mountUi(page("dsi_b"), { lib: lib(), nodes, detail: draft });
    await click(button(en["dz.act.ready"]));
    expect(api.setDecoySiteReady).toHaveBeenCalledWith({ siteId: "dsi_b", ready: true });
  });

  it("assigns: asks the panel what each pick replaces (a dry run), shows it, and applies after the click", async () => {
    api.assignDecoySite.mockImplementation(async (req: { dryRun: boolean }) =>
      req.dryRun
        ? { warnings: [{ code: "shared_domains", level: "warning", ok: false, paths: ["fi1", "nl1"] }], replaced: [{ target: "nod_de1", previous: "builtin", previousSiteId: "", previousSiteName: "" }], nodes: [], applied: false }
        : { warnings: [], replaced: [], nodes: [], applied: true },
    );
    await mountUi(page("dsi_c"), { lib: library([site("c", { name: "Boats" })]), nodes, detail: free });
    await click(button(en["dz.act.assign"]));
    expect(text()).toContain(en["dz.d.as"]);
    const go = () => [...document.querySelectorAll("button")].filter((b) => b.textContent?.trim() === en["dz.act.assign"]).at(-1)!;
    expect(go().hasAttribute("disabled") || go().getAttribute("data-disabled") !== null).toBe(true); // nothing picked
    expect(text()).toContain(en["dz.as.footNone"]);
    await click(document.querySelector('[role=checkbox]:not([aria-disabled])')); // de1
    expect(api.assignDecoySite).toHaveBeenCalledWith({ siteId: "dsi_c", nodeIds: ["nod_de1"], panel: false, dryRun: true }, expect.anything());
    expect(text()).toContain(en["dz.as.sum"]);
    expect(text()).toContain(en["dz.as.shared"]); // the warning of the dry run
    expect(text()).toContain("fi1, nl1");
    expect(text()).toContain("Replace on 1 node".replace("Replace", "Will replace"));
    await click(go());
    expect(api.assignDecoySite).toHaveBeenLastCalledWith({ siteId: "dsi_c", nodeIds: ["nod_de1"], panel: false });
  });

  it("locks the nodes the site already stands on in the assign window", async () => {
    api.assignDecoySite.mockResolvedValue({ warnings: [], replaced: [], nodes: [], applied: false });
    await mountUi(page("dsi_a"), { lib: lib(), nodes, detail: used });
    await click(button(en["dz.act.assign"]));
    const rows = [...document.querySelectorAll("[role=checkbox]")];
    expect(rows.filter((r) => r.getAttribute("aria-disabled") === "true").map((r) => r.textContent)).toHaveLength(2);
    expect(rows.find((r) => r.textContent?.includes("nl1"))!.textContent).toContain(en["dz.as.has"]);
  });

  it("will not delete a site that stands on nodes: it lists them and offers 'Take it off first'", async () => {
    await mountUi(page("dsi_a", { dialog: "del" }), { lib: lib(), nodes, detail: used });
    expect(text()).toContain(en["dz.d.delUsed"]);
    expect(text()).toContain(en["dz.del.usedTitle"]);
    expect(document.querySelectorAll(".dz-asl .dz-plr")).toHaveLength(2);
    expect(button(en["dz.del.go"])).toBeUndefined();
    expect(button(en["dz.del.first"])).toBeDefined();
    expect(api.deleteDecoySite).not.toHaveBeenCalled();
  });

  it("deletes a site that stands nowhere, after the question, and goes back to the list", async () => {
    api.deleteDecoySite.mockResolvedValue({});
    await mountUi(page("dsi_c", { dialog: "del" }), { lib: library([site("c")]), nodes, detail: free });
    expect(text()).toContain(en["dz.d.delSite"]);
    await click(button(en["dz.del.go"]));
    expect(api.deleteDecoySite).toHaveBeenCalledWith({ siteId: "dsi_c" });
    expect(navigate).toHaveBeenCalledWith({ to: "/decoys" });
  });

  it("takes a site off a node and says what it gets instead (the built-in page by default)", async () => {
    api.unassignDecoySite.mockResolvedValue({});
    await mountUi(page("dsi_a", { dialog: "un", unTarget: "nod_nl1" }), { lib: lib(), nodes, detail: used });
    expect(text()).toContain("Take it off nl1");
    await click([...document.querySelectorAll("button")].filter((b) => b.textContent?.trim() === en["dz.un.go"]).at(-1));
    expect(api.unassignDecoySite).toHaveBeenCalledWith({ siteId: "dsi_a", nodeIds: ["nod_nl1"], panel: false, fallback: "builtin" });
  });

  it("warns that a new version reaches the nodes at once, and reverts a version through the panel", async () => {
    await mountUi(page("dsi_a", { dialog: "rep" }), { lib: lib(), nodes, detail: used });
    expect(text()).toContain(en["dz.rep.used"].replace("{where}", "2 nodes"));
    expect(button(en["dz.rep.go"])!.hasAttribute("disabled") || button(en["dz.rep.go"])!.getAttribute("data-disabled") !== null).toBe(true); // no file yet
  });

  it("reverts a version", async () => {
    api.revertDecoySite.mockResolvedValue({});
    const withVersions = { ...used, versions: [{ seq: 2, current: true, createdUnix: 3000, createdBy: "owner", sizeBytes: 5, fileCount: 1, checks: [], title: "", bundleSha256: "" }, { seq: 1, current: false, createdUnix: 2000, createdBy: "owner", sizeBytes: 5, fileCount: 1, checks: [], title: "", bundleSha256: "" }] };
    await mountUi(page("dsi_a", { dialog: "ver", verSeq: 1 }), { lib: lib(), nodes, detail: withVersions });
    expect(text()).toContain(en["dz.ver.used"].replace("{where}", "2 nodes"));
    await click(button(en["dz.ver.go"]));
    expect(api.revertDecoySite).toHaveBeenCalledWith({ siteId: "dsi_a", seq: 1 });
  });

  it("hands a task over MCP with the site's name and id", async () => {
    await mountUi(page("dsi_a", { dialog: "mcp" }), { lib: lib(), nodes, detail: used });
    const task = document.querySelector(".dz-task")!.textContent!;
    expect(task).toContain("“Bakery”");
    expect(task).toContain("id: dsi_a");
    expect(text()).toContain(en["dz.cmcp.footUsed"]); // a site that stands needs the owner's approval
  });

  it("is for the owner only", async () => {
    await mountUi(page("dsi_a"), { role: Role.READONLY, lib: lib(), nodes, detail: used });
    expect(text()).toContain(en["dz.deny.title"]);
    expect(api.getDecoySite).not.toHaveBeenCalled();
  });

  it("says when there is no such site", async () => {
    api.getDecoySite.mockRejectedValue(new ConnectError("decoy_site_not_found", Code.NotFound));
    await mountUi(page("dsi_zz"), { lib: lib(), nodes });
    await settle();
    expect(text()).toContain(en["dz.nf.title"]);
  });

  it("shows the built-in entry without an Edit button or a menu, and lets it be assigned", async () => {
    await mountUi(page("builtin"), { lib: lib(), nodes });
    expect(document.querySelector("h1")!.textContent).toBe(en["dz.bi.builtin"]);
    expect(text()).toContain(en["dz.st.builtin"]);
    expect(button(en["dz.act.assign"])).toBeDefined();
    expect(button(en["dz.act.more"])).toBeUndefined();
    expect(document.querySelector(`[aria-label="${en["dz.edit"]}"]`)).toBeNull();
    expect(api.getDecoySite).not.toHaveBeenCalled();
    // the page comes from the panel under the id of the entry: the web keeps no copy of it
    expect(api.getDecoyPagePreview).toHaveBeenCalledWith({ siteId: "builtin", seq: 0, path: "", notFound: false }, expect.anything());
    expect(document.querySelector("iframe")?.getAttribute("srcdoc")).toBe("<p>home</p>");
    expect(document.querySelector("[data-shot=decoy-files]")).toBeNull(); // one page: nothing to look through
  });

  it("shows the panel's directory for real: its pages, its files and the text of one, from the panel", async () => {
    api.listDecoyPages.mockResolvedValue({ pages: ["index.html"], files: [{ path: "index.html", size: 138 }, { path: "css/site.css", size: 40 }], has404: false });
    api.getDecoyPagePreview.mockResolvedValue({ html: "<p>Welcome</p>", path: "index.html" });
    api.getDecoyFile.mockResolvedValue({ text: "<h1>Welcome</h1>", size: 138, truncated: false });
    await mountUi(page("panel_dir"), { lib: lib(), nodes });
    await settle();
    expect(document.querySelector("iframe")?.getAttribute("srcdoc")).toBe("<p>Welcome</p>");
    const files = document.querySelector("[data-shot=decoy-files]")!;
    expect(files.textContent).toContain("css/site.css");
    expect(files.textContent).toContain("<h1>Welcome</h1>");
    expect(api.getDecoyFile).toHaveBeenCalledWith({ siteId: "panel_dir", seq: 0, path: "index.html" }, expect.anything());
    expect(files.textContent).not.toContain("sha256"); // the directory has no hash of its own
    expect(button(en["dz.v.replace"])).toBeUndefined(); // and cannot be changed here
    expect(api.getDecoySite).not.toHaveBeenCalled();
  });

  it("says why the panel's directory cannot be shown, in the frame", async () => {
    api.listDecoyPages.mockRejectedValue(new ConnectError("panel_too_large", Code.FailedPrecondition));
    api.getDecoyPagePreview.mockRejectedValue(new ConnectError("panel_too_large", Code.FailedPrecondition));
    await mountUi(page("panel_dir"), { lib: library([site("a")], { builtin: [{ id: "builtin", available: true, reasonCode: "", nodeCount: 0, onPanel: false }, { id: "panel_dir", available: false, reasonCode: "panel_too_large", nodeCount: 0, onPanel: false }] }), nodes });
    await settle();
    expect(document.querySelector(".dz-fr")!.textContent).toContain(en["dz.pv.failed"]);
    expect(document.querySelector(".dz-fr")!.textContent).toContain(en["err.panel_too_large"]);
    expect(document.querySelector("[data-shot=decoy-files]")).toBeNull();
  });

  it("draws the preview in a frame with no permissions at all", async () => {
    await mountUi(page("dsi_a"), { lib: lib(), nodes, detail: used });
    await settle();
    const frame = document.querySelector<HTMLIFrameElement>(".dz-fr iframe")!;
    expect(frame).not.toBeNull();
    expect(frame.getAttribute("sandbox")).toBe("");
    expect(frame.getAttribute("srcdoc")).toBe("<p>home</p>");
  });
});
