import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { setLang } from "@/i18n";
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
let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => {
  setLang("en");
  for (const f of Object.values(api)) f.mockReset();
  navigate.mockReset();
  api.getDecoyPagePreview.mockResolvedValue({ html: "<p>home</p>", path: "" });
  api.listDecoyPages.mockResolvedValue({ pages: ["index.html", "about.html"], files: [], has404: true });
  api.getDecoyFile.mockResolvedValue({ text: "<h1>Hi</h1>", size: 11, truncated: false });
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
});

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
  panelSource: "builtin", siteLimit: 100, versionLimit: 5, ...over,
});

type Mount = { role?: Role; edge?: boolean; nodes?: ReturnType<typeof node>[]; lib?: ReturnType<typeof library>; detail?: Record<string, unknown> & { site: ReturnType<typeof site> } };
async function mountUi(ui: ReactNode, o: Mount = {}) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
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
    root!.render(
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

  it("shows the empty first look with the three ways", async () => {
    await mountUi(list(), { lib: library([]), nodes: [node("de1")] });
    expect(text()).toContain(en["dz.first.title"]);
    expect(document.querySelectorAll(".dz-way")).toHaveLength(3);
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
  ] };
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
    expect(checksCard.textContent).toContain(en["dz.ck.shared_domains"]); // two addresses of two domains
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
    expect(document.querySelector("iframe")?.getAttribute("srcdoc")).toContain("Coming soon");
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
