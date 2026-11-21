import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState } from "react";
import { AddNodeProvider } from "@/components/add-node";
import { ChatButton } from "@/components/assistant/chat";
import { LangToggle, ThemeToggle } from "@/components/prefs";
import { PageTitle } from "@/components/ui/bits";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { isLang, setLang, useLang } from "@/i18n";
import { decoySites } from "@/lib/api";
import { accessQuery } from "@/lib/assistant";
import { pagesQuery, siteQuery, sitesQuery, templatesQuery, type Site } from "@/lib/decoysites";
import { setEffectsEnabled } from "@/lib/effects";
import { nodesQuery } from "@/lib/queries";
import { meQuery } from "@/lib/session";
import { setTheme, useTheme } from "@/lib/theme";
import { Shot } from "@/screens/dev-kit-redesign";
import { demoNodes, demoSites, fileText, fillerSites, looks, pageDoc, type DemoSite } from "./dev-kit-decoys-data";
import type { UploadDemo } from "./decoys/create";
import type { TemplateDemo } from "./decoys/template";
import { DecoysScreen, type DecoysDemo } from "./decoys/list";
import { DecoySiteScreen, type SiteDemo } from "./decoys/site";
import { ShellLayout } from "./shell";

// Development only. /dev-kit?only=decoys: the Decoy sites screens of the redesign in every state, each in a frame of its own width
// (an iframe, so the window's breakpoints answer to the frame); each frame is a page of its own, for the screenshot script and for the
// side-by-side with the boards (PnDecoys, PnDecoy):
//   /dev-kit?only=decoys-page&state=<state>[&lang=en|ru|zh|fa|tr][&theme=light|dark][&fx=1]
// list states: list | list-delivery | list-panel-fb | list-empty | list-many | list-full | list-load | list-err | list-deny | list-edge | list-draft |
//   list-used | list-free | list-find | list-nomatch
// the windows of the list: dlg-new | dlg-tpl | dlg-tpl-wait | dlg-tpl-view | dlg-tpl-run | dlg-tpl-err | dlg-up | dlg-up-drag | dlg-up-run | dlg-up-ready | dlg-up-draft | dlg-up-taken | dlg-up-big |
//   dlg-up-many | dlg-up-unsafe | dlg-up-fmt | dlg-up-noindex | dlg-mcp
// page states: site-used | site-ready | site-template | site-draft | site-bad | site-builtin | site-dir | site-dir-off | site-way | site-fail | site-off |
//   site-old | site-phone | site-404 | site-page | site-edit | site-menu | site-checks | site-copied | site-css | site-img | site-deny |
//   site-load | site-nf | site-edge
// the windows of the page: dlg-assign | dlg-assign-two | dlg-assign-panel | dlg-unassign | dlg-unassign-panel | dlg-delete-draft |
//   dlg-delete-used | dlg-replace | dlg-replace-file | dlg-revert | dlg-continue
// Made-up data of the boards: the bakery, the photo studio, the joinery …, the nodes de1, nl1, fi1, msk1 on documentation addresses.
// The panel's calls are answered here (the scene installs a small stand-in for the library service), nothing leaves the page.

const now = Math.floor(Date.now() / 1000);
// the index.html of the made-up panel directory
const PANEL_DIR_INDEX = ["<!doctype html>", "<html>", "<title>Welcome</title>", "<body>", "  <h1>Welcome</h1>", "  <span>This site is under construction</span>", "</body>", "</html>"].join("\n");
const kb = 1024;

type World = {
  sites: DemoSite[];
  panelSource: string;
  /** What each node serves: a site id, "builtin" or "panel". */
  serves: Record<string, string>;
  reasons: Record<string, string>;
  dirOk: boolean;
  role: Role;
  edge: boolean;
  hang: "" | "list" | "site";
  fail: "" | "list" | "site-nf" | "preview" | "generate";
  /** Twelve nodes in all (the grid of "showing now" with a last row that is not full). */
  many: boolean;
};

const moreNodes = ["be1", "se1", "pl1", "fr1", "uk1", "at1", "cz1", "no1"].map((name, i) => ({ id: `nod_${name}`, name, cc: ["BE", "SE", "PL", "FR", "GB", "AT", "CZ", "NO"][i]!, address: `${name}.example.com` }));

function worldOf(state: string): World {
  const w: World = {
    sites: demoSites,
    panelSource: "site:dsi_wd",
    serves: { nod_de1: "builtin", nod_nl1: "dsi_bk", nod_fi1: "dsi_ph", nod_msk1: "dsi_bk" },
    reasons: {},
    dirOk: true,
    role: Role.OWNER,
    edge: state === "list-edge" || state === "site-edge",
    hang: state === "list-load" ? "list" : state === "site-load" ? "site" : "",
    fail: state === "list-err" ? "list" : state === "site-nf" ? "site-nf" : "",
    many: state === "list-many",
  };
  if (state === "list-empty" || state === "dlg-new" || state === "dlg-mcp") {
    if (state === "list-empty") {
      w.sites = [];
      w.serves = { nod_de1: "builtin", nod_nl1: "builtin", nod_fi1: "builtin", nod_msk1: "builtin" };
      w.panelSource = "builtin";
    }
  }
  if (state === "list-full") w.sites = [...demoSites, ...fillerSites(9, 100)];
  if (state === "list-delivery") w.reasons = { nod_nl1: "pending", nod_fi1: "apply_failed", nod_msk1: "node_offline" };
  if (w.edge) {
    w.serves = { nod_de1: "builtin", nod_nl1: "builtin", nod_fi1: "builtin", nod_msk1: "builtin" };
    w.panelSource = "builtin";
  }
  if (state === "list-deny" || state === "site-deny") w.role = Role.HELPER;
  if (state === "site-way") w.reasons = { nod_nl1: "pending" };
  if (state === "site-fail") w.reasons = { nod_nl1: "apply_failed" };
  if (state === "site-off") w.reasons = { nod_nl1: "node_offline" };
  if (state === "site-old") w.reasons = { nod_nl1: "agent_too_old" };
  if (state === "site-dir" || state === "site-dir-off") w.serves = { ...w.serves, nod_de1: "panel" };
  if (state === "site-dir-off") w.dirOk = false;
  if (state === "site-builtin") w.panelSource = "builtin";
  // the panel was told to show a site that is a draft again: it shows its directory, and says so
  if (state === "list-panel-fb") w.panelSource = "site:dsi_bike";
  return w;
}

const checks = (s: DemoSite) => [
  { code: "index_html", level: "required", ok: !s.bad, paths: [] },
  { code: "size", level: "required", ok: true, paths: [] },
  { code: "file_count", level: "required", ok: true, paths: [] },
  { code: "safe_paths", level: "required", ok: true, paths: [] },
  ...s.warn.map((x) => ({ code: x.code, level: "warning", ok: false, paths: x.paths ?? [] })),
];

function siteMsg(s: DemoSite, w: World): Site {
  return {
    id: s.id, name: s.name, note: s.note, source: s.source, sourceLabel: s.template ? `template:${s.template}` : (s.token ?? (s.source === "upload" || s.source === "assistant" ? s.by : "")), state: s.state, bundleSha256: s.sha.padEnd(64, "0"),
    createdUnix: now - s.createdAgoS, createdBy: s.by, updatedUnix: now - s.updatedAgoS, updatedBy: s.by, version: s.versions.length, sizeBytes: s.bytes, fileCount: s.files, title: s.title,
    checks: checks(s), nodeCount: Object.values(w.serves).filter((v) => v === s.id).length, onPanel: w.panelSource === `site:${s.id}`,
  } as Site;
}

/** The panel's row as the panel answers it: its address, what its listener serves, and why that is not the choice. */
function panelOf(w: World) {
  const chosen = w.panelSource;
  const draft = chosen.startsWith("site:") && w.sites.find((s) => s.id === chosen.slice(5))?.state === "draft";
  if (draft) return { address: "panel.example.com", serving: w.dirOk ? "dir" : "builtin", reasonCode: "decoy_site_draft" };
  if (chosen === "dir" && !w.dirOk) return { address: "panel.example.com", serving: "builtin", reasonCode: "panel_unavailable" };
  return { address: "panel.example.com", serving: chosen, reasonCode: "" };
}

/** The warning the panel computes for the nodes a site stands on now: more than one registrable domain among them. */
function sharedOf(standing: string[]) {
  const domain = (id: string) => demoNodes.find((n) => n.id === id)?.address.split(".").slice(-2).join(".");
  if (new Set(standing.map(domain)).size < 2) return undefined;
  return { code: "shared_domains", level: "warning", ok: false, paths: standing.map((id) => demoNodes.find((n) => n.id === id)?.name ?? id).sort() };
}

function libraryOf(w: World) {
  return {
    panel: panelOf(w),
    sites: w.sites.map((s) => siteMsg(s, w)),
    builtin: [
      { id: "builtin", available: true, reasonCode: "", nodeCount: Object.values(w.serves).filter((v) => v === "builtin").length, onPanel: w.panelSource === "builtin" },
      { id: "panel_dir", available: w.dirOk, reasonCode: w.dirOk ? "" : "panel_unavailable", nodeCount: Object.values(w.serves).filter((v) => v === "panel").length, onPanel: w.panelSource === "dir" },
    ],
    panelSource: w.panelSource,
    siteLimit: 100,
    versionLimit: 5,
  };
}

function detailOf(s: DemoSite, w: World) {
  const standing = Object.entries(w.serves).filter(([, v]) => v === s.id).map(([k]) => k);
  return {
    site: siteMsg(s, w),
    versions: s.versions.map((v, i) => ({ seq: s.versions.length - i, bundleSha256: s.sha, createdUnix: now - v.agoS, createdBy: v.by, sizeBytes: v.bytes, fileCount: s.files, title: s.title, checks: checks(s), current: i === 0 })),
    files: s.tree.map(([path, k]) => ({ path, size: Math.round(k * kb) })),
    panel: panelOf(w),
    sharedDomains: sharedOf(standing),
    nodes: standing.map((id) => ({ nodeId: id, nodeName: demoNodes.find((n) => n.id === id)?.name ?? id, reasonCode: w.reasons[id] ?? "", supported: w.reasons[id] !== "agent_too_old", agentSha256: "" })),
  };
}

const nodeRow = (n: (typeof demoNodes)[number], w: World) => {
  const to = w.serves[n.id] ?? "builtin";
  return {
    id: n.id, name: n.name, countryCode: n.cc, location: "", provider: "Example Cloud", notes: "", address: n.address, status: NodeStatus.ONLINE, protocols: ["hysteria2", "vless"], online: [],
    decoyMode: to === "builtin" ? "builtin" : to === "panel" ? "panel" : "own", decoySiteId: to.startsWith("dsi_") ? to : "", decoySupported: w.reasons[n.id] !== "agent_too_old", decoyReasonCode: w.reasons[n.id] ?? "",
    decoyBundleSha256: "", decoyPanelAvailable: w.dirOk, decoyOwnAvailable: true, agentVersion: "0.1.71", warp: { state: 0 },
  };
};

/** The stand-in for the library service the scene installs on the page: the panel's answers for the made-up library. */
function installFake(w: World) {
  const find = (id: string) => w.sites.find((s) => s.id === id);
  const never = () => new Promise<never>(() => {});
  const pagesOf = (s: DemoSite) => (s.bad ? [] : ["index.html", ...s.tree.map((x) => x[0]).filter((p) => /\.html$/.test(p) && p !== "index.html" && p !== "404.html")]);
  const fake = {
    listDecoySites: async () => {
      if (w.hang === "list") return never();
      if (w.fail === "list") throw new ConnectError("unavailable", Code.Unavailable);
      return libraryOf(w);
    },
    getDecoySite: async ({ siteId }: { siteId: string }) => {
      if (w.hang === "site") return never();
      const s = find(siteId);
      if (!s || w.fail === "site-nf") throw new ConnectError("decoy_site_not_found", Code.NotFound);
      return detailOf(s, w);
    },
    listDecoyPages: async ({ siteId }: { siteId: string }) => {
      if (siteId === "builtin") return { pages: ["index.html"], files: [{ path: "index.html", size: 392 }], has404: false };
      if (siteId === "panel_dir") {
        if (!w.dirOk) throw new ConnectError("panel_unavailable", Code.FailedPrecondition);
        return { pages: ["index.html"], files: [{ path: "index.html", size: PANEL_DIR_INDEX.length }], has404: false };
      }
      const s = find(siteId)!;
      return { pages: pagesOf(s), files: s.tree.map(([path, k]) => ({ path, size: Math.round(k * kb) })), has404: s.has404 };
    },
    getDecoyPagePreview: async ({ siteId, path, notFound }: { siteId: string; path: string; notFound: boolean }) => {
      if (siteId === "builtin" || siteId === "panel_dir") {
        if (siteId === "panel_dir" && !w.dirOk) throw new ConnectError("panel_unavailable", Code.FailedPrecondition);
        // a stand-in for the panel's answer: the built-in page is the panel's own text, which this scene does not carry
        const [title, text] = siteId === "builtin" ? ["Coming soon", "Something new is on its way."] : ["Welcome", "This site is under construction"];
        return { html: `<!doctype html><meta charset="utf-8"><body style="margin:0;height:100vh;display:grid;place-items:center;font:16px system-ui;color:#444;background:#fafafa;text-align:center"><main><h1 style="font-weight:500">${notFound ? "Not Found" : title}</h1><p style="color:#888">${notFound ? "" : text}</p></main>`, path: path || "index.html" };
      }
      const s = find(siteId)!;
      const pages = pagesOf(s);
      const i = Math.max(0, pages.indexOf(path || "index.html"));
      const page = notFound ? (s.has404 ? 404 : "nf") : i === 0 ? 0 : i === pages.length - 1 ? 2 : 1;
      return { html: pageDoc(siteId.replace("dsi_", ""), page, 0), path };
    },
    listDecoyTemplates: async () => ({ templates: templateKinds.map(([id, nameEn]) => ({ id, nameEn, nameRu: nameEn })) }),
    previewDecoyTemplate: async ({ kind }: { kind: string; seed: string }) => {
      if (w.fail === "preview") throw new ConnectError("unavailable", Code.Unavailable);
      variant += 1;
      return { html: pageDoc(templateLook[kind] ?? "tpl", 0, variant), seed: `${(variant + 0x1000).toString(16)}0000000000000000`.slice(0, 16), name: looks[templateLook[kind] ?? "tpl"]!.brand };
    },
    generateDecoySite: async ({ kind }: { kind: string }) => {
      if (w.fail === "generate") throw new ConnectError("decoy_library_full", Code.FailedPrecondition);
      const s = { ...demoSites.find((x) => x.id === "dsi_tpl")!, id: `dsi_gen${w.sites.length}`, name: looks[templateLook[kind] ?? "tpl"]!.brand, template: kind };
      w.sites = [...w.sites, s];
      return { site: siteMsg(s, w) };
    },
    getDecoyFile: async ({ siteId, path }: { siteId: string; path: string }) =>
      siteId === "panel_dir" ? { text: PANEL_DIR_INDEX, size: PANEL_DIR_INDEX.length, truncated: false } : { text: fileText(siteId.replace("dsi_", ""), path), size: 1024, truncated: false },
    assignDecoySite: async ({ siteId, nodeIds, panel }: { siteId: string; nodeIds: string[]; panel: boolean }) => {
      const s = find(siteId)!;
      const replaced = nodeIds.map((id) => {
        const to = w.serves[id] ?? "builtin";
        return { target: id, previous: to === "builtin" ? "builtin" : to === "panel" ? "panel" : "own", previousSiteId: to.startsWith("dsi_") ? to : "", previousSiteName: find(to)?.name ?? "" };
      });
      if (panel) {
        const src = w.panelSource;
        replaced.push({ target: "panel", previous: src === "builtin" ? "builtin" : src === "dir" ? "dir" : "site", previousSiteId: src.startsWith("site:") ? src.slice(5) : "", previousSiteName: find(src.slice(5))?.name ?? "" });
      }
      const standing = Object.entries(w.serves).filter(([, v]) => v === s.id).map(([k]) => k);
      const shared = nodeIds.length ? sharedOf([...new Set([...standing, ...nodeIds])]) : undefined;
      const warnings = [...checks(s).filter((c) => c.level === "warning"), ...(shared ? [shared] : [])];
      return { warnings, replaced, nodes: [], applied: false };
    },
  };
  Object.assign(decoySites, fake);
}

/** The made-up library the node page's card chooses from (the stand-in for the panel installed, the library it answers with). */
export function installLibrary() {
  const w = worldOf("list");
  installFake(w);
  return libraryOf(w);
}

function seeded(w: World) {
  const c = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false, refetchOnWindowFocus: false, refetchInterval: false } } });
  c.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", displayName: "owner", role: w.role }, version: "0.1.71", sourceUrl: "https://github.com/example/mistgate", sourceStars: 298, stepUpUntilUnix: 0, ...(w.edge ? { edition: "edge", unavailable: ["decoy_own"] } : {}) } as never);
  c.setQueryData(accessQuery.queryKey, { available: true, unread: 0, readOnly: false, model: "", provider: undefined } as never);
  c.setQueryData(nodesQuery.queryKey, { nodes: (w.many ? [...demoNodes, ...moreNodes] : demoNodes).map((n) => nodeRow(n, w)), expectedAgentVersion: "0.1.71" } as never);
  if (!w.hang && !w.fail) {
    c.setQueryData(sitesQuery.queryKey, libraryOf(w) as never);
    c.setQueryData(templatesQuery.queryKey, templateKinds.map(([id, nameEn]) => ({ id, nameEn, nameRu: nameEn })) as never);
    for (const s of w.sites.slice(0, 12)) {
      const d = detailOf(s, w);
      c.setQueryData(siteQuery(s.id).queryKey, d as never);
      c.setQueryData(pagesQuery(s.id, s.sha.padEnd(64, "0")).queryKey, { pages: ["index.html", ...s.tree.map((x) => x[0]).filter((p) => /\.html$/.test(p) && p !== "index.html" && p !== "404.html")], files: d.files, has404: s.has404 } as never);
    }
  }
  return c;
}

// ---- the made-up uploads of the window "Upload an archive"
const bkSite = (over: Partial<Site> = {}) => siteMsg({ ...demoSites[0]!, id: "dsi_new", name: "Holzofen", note: "", updatedAgoS: 5, createdAgoS: 5, versions: [{ agoS: 5, by: "owner", bytes: 412 * kb }] }, worldOf("list")) as Site & typeof over;
const uploads: Record<string, UploadDemo> = {
  "dlg-up": { phase: "pick" },
  "dlg-up-drag": { phase: "drag" },
  "dlg-up-run": { phase: "run", file: { name: "holzofen.zip", size: 412 * kb }, pct: 34 },
  "dlg-up-ready": { phase: "res", file: { name: "holzofen.zip", size: 412 * kb }, site: bkSite() },
  "dlg-up-taken": { phase: "res", file: { name: "holzofen.zip", size: 412 * kb }, site: { ...bkSite(), name: "Пекарня Хольцофен" } },
  "dlg-up-draft": { phase: "res", file: { name: "site-backup.tar.gz", size: 880 * kb }, site: { ...bkSite(), name: "backup", state: "draft", sizeBytes: 880 * kb, fileCount: 31, checks: checks({ ...demoSites[6]!, bad: true }) } },
  "dlg-up-big": { phase: "err", file: { name: "holzofen.zip", size: 3.4 * 1024 * kb }, error: { code: "decoy_bundle_too_large" } },
  "dlg-up-many": { phase: "err", file: { name: "holzofen.tar.gz", size: 1.1 * 1024 * kb }, error: { code: "decoy_bundle_too_many_files" } },
  "dlg-up-unsafe": { phase: "err", file: { name: "holzofen.zip", size: 412 * kb }, error: { code: "decoy_bundle_bad_path", path: "img/latest" } },
  "dlg-up-fmt": { phase: "err", file: { name: "holzofen.rar", size: 412 * kb }, error: { code: "decoy_bundle_bad_archive" } },
  "dlg-up-noindex": { phase: "err", file: { name: "site-backup.tar.gz", size: 880 * kb }, error: { code: "decoy_bundle_no_index" } },
};
// ---- the made-up window "From a template": the kinds the panel names, and which made-up look stands for each in the preview
const templateKinds: [string, string][] = [["status", "Service status page"], ["files", "File transfer (upload page)"], ["login", "Sign-in page"], ["blog", "Personal blog"], ["portfolio", "Photo / illustration portfolio"], ["business", "Small business site"], ["docs", "Project documentation"], ["card", "Personal card page"], ["club", "Club / society / event"], ["project", "Open-source project page"]];
const templateLook: Record<string, string> = { blog: "tpl", business: "bk", portfolio: "ph", card: "wd", club: "boat", status: "bike", docs: "gd" };
let variant = 0;
const templates: Record<string, TemplateDemo> = {
  "dlg-tpl": { phase: "pick" },
  "dlg-tpl-wait": { phase: "view", kind: "blog" },
  "dlg-tpl-view": { phase: "view", kind: "blog", html: pageDoc("tpl", 0), name: "Rainy Reader" },
  "dlg-tpl-run": { phase: "run", kind: "blog", html: pageDoc("tpl", 0), name: "Rainy Reader" },
  "dlg-tpl-err": { phase: "err", kind: "blog", html: pageDoc("tpl", 0), name: "Rainy Reader", error: "decoy_library_full" },
};
const listDemos = (state: string): DecoysDemo | undefined => {
  if (templates[state]) return { dialog: "tpl", template: templates[state] };
  if (state === "dlg-new") return { dialog: "new" };
  if (state === "dlg-mcp") return { dialog: "mcp" };
  if (uploads[state]) return { dialog: "up", upload: uploads[state] };
  if (state === "list-draft") return { filter: "draft" };
  if (state === "list-used") return { filter: "used" };
  if (state === "list-free") return { filter: "free" };
  if (state === "list-find") return { q: "пекар" };
  if (state === "list-nomatch") return { q: "zzz" };
  return undefined;
};

// ---- the pages of one site
const siteOf: Record<string, string> = { "site-used": "dsi_bk", "site-ready": "dsi_boat", "site-template": "dsi_tpl", "site-draft": "dsi_bike", "site-bad": "dsi_arc", "site-builtin": "builtin", "site-dir": "panel_dir", "site-dir-off": "panel_dir" };
const siteDemos: Record<string, SiteDemo> = {
  "site-phone": { preview: { width: "ph" } },
  "site-404": { preview: { notFound: true } },
  "site-page": { preview: { page: 1 } },
  "site-edit": { edit: true },
  "site-menu": { menu: true },
  "site-checks": { checksOpen: true },
  "site-copied": { copied: true },
  "site-css": { files: { file: "css/style.css" } },
  "site-img": { files: { file: "img/ofen.jpg" } },
  "dlg-assign": { dialog: "as", assign: { picks: ["nod_fi1"] } },
  "dlg-assign-two": { dialog: "as", assign: { picks: ["nod_de1", "nod_fi1"] } },
  "dlg-assign-panel": { dialog: "as", assign: { picks: ["nod_de1", "panel"] } },
  "dlg-unassign": { dialog: "un", unTarget: "nod_nl1" },
  "dlg-unassign-panel": { dialog: "un", unTarget: "panel", unTo: "panel" },
  "dlg-delete-draft": { dialog: "del" },
  "dlg-delete-used": { dialog: "del" },
  "dlg-replace": { dialog: "rep" },
  "dlg-replace-file": { dialog: "rep", repFile: { name: "holzofen-new.zip", size: 428 * kb } },
  "dlg-revert": { dialog: "ver", verSeq: 2 },
  "dlg-continue": { dialog: "mcp" },
};
/** The site a page scene shows: the bakery (it stands on two nodes) by default, a draft for the draft window, the joinery (on the panel) for taking it off the panel. */
const siteFor = (state: string) => siteOf[state] ?? (state === "dlg-delete-draft" ? "dsi_bike" : state === "dlg-unassign-panel" ? "dsi_wd" : "dsi_bk");

const isSiteState = (state: string) => state.startsWith("site-") || ["dlg-assign", "dlg-assign-two", "dlg-assign-panel", "dlg-unassign", "dlg-unassign-panel", "dlg-delete-draft", "dlg-delete-used", "dlg-replace", "dlg-replace-file", "dlg-revert", "dlg-continue"].includes(state);

/** What a scene starts the screen with: its private query cache, and the demo of the list or of the page (exported for its smoke test). */
export function sceneOf(state: string) {
  const w = worldOf(state);
  installFake(w);
  const site = isSiteState(state);
  return { qc: seeded(w), site, id: site ? siteFor(state) : "", listDemo: site ? undefined : listDemos(state), siteDemo: site ? siteDemos[state] : undefined };
}

/** One whole page of the scene: the screen in the real shell, at the window's own width. */
export function DecoysPage() {
  const params = new URLSearchParams(location.search);
  const state = params.get("state") ?? "list";
  const [scene] = useState(() => {
    const lang = params.get("lang");
    if (isLang(lang)) setLang(lang);
    const theme = params.get("theme");
    if (theme === "light" || theme === "dark") setTheme(theme);
    setEffectsEnabled(params.get("fx") === "1", false);
    return sceneOf(state);
  });
  return (
    <QueryClientProvider client={scene.qc}>
      <ToastProvider>
        <AddNodeProvider>
          <ShellLayout current="decoys" fab={<ChatButton onClick={() => {}} running={false} unread={0} />}>
            {scene.site ? <DecoySiteScreen id={scene.id} demo={scene.siteDemo} /> : <DecoysScreen demo={scene.listDemo} />}
          </ShellLayout>
        </AddNodeProvider>
      </ToastProvider>
    </QueryClientProvider>
  );
}

export const frames: { id: string; state: string; title: string; w: number; h: number; params?: string }[] = [
  { id: "decoys-list", state: "list", title: "The list: showing now, the built-in entries, the own sites (1440)", w: 1440, h: 1500 },
  { id: "decoys-list-light", state: "list", title: "The same on the light theme", w: 1440, h: 1500, params: "&theme=light" },
  { id: "decoys-list-rail", state: "list", title: "A 900 window: the rail, two columns of cards", w: 900, h: 1700 },
  { id: "decoys-list-phone", state: "list", title: "A phone, 400 wide", w: 400, h: 2400 },
  { id: "decoys-list-fa", state: "list", title: "Right to left (fa)", w: 1440, h: 1500, params: "&lang=fa" },
  { id: "decoys-list-many", state: "list-many", title: "Showing now: twelve nodes and the panel, the last row is not full", w: 1440, h: 1500 },
  { id: "decoys-list-many-phone", state: "list-many", title: "The same on a phone, 400 wide", w: 400, h: 2000 },
  { id: "decoys-list-delivery", state: "list-delivery", title: "Showing now with a delivery on its way, failed, offline", w: 1440, h: 900 },
  { id: "decoys-list-panel-fb", state: "list-panel-fb", title: "Showing now: the site chosen for the panel is a draft, the directory is shown", w: 1440, h: 900 },
  { id: "decoys-list-empty", state: "list-empty", title: "No own site yet: the three ways", w: 1440, h: 900 },
  { id: "decoys-list-empty-phone", state: "list-empty", title: "No own site yet, a phone", w: 400, h: 1500 },
  { id: "decoys-list-full", state: "list-full", title: "The library is full: 100 of 100, the button is off", w: 1440, h: 1200 },
  { id: "decoys-list-load", state: "list-load", title: "Loading", w: 1440, h: 900 },
  { id: "decoys-list-err", state: "list-err", title: "The panel did not answer", w: 1440, h: 700 },
  { id: "decoys-list-deny", state: "list-deny", title: "Not the owner", w: 1440, h: 700 },
  { id: "decoys-list-edge", state: "list-edge", title: "The Cloudflare edition", w: 1440, h: 800 },
  { id: "decoys-list-draft", state: "list-draft", title: "Filter: drafts", w: 1440, h: 1100 },
  { id: "decoys-list-used", state: "list-used", title: "Filter: in use", w: 1440, h: 1100 },
  { id: "decoys-list-free", state: "list-free", title: "Filter: not in use", w: 1440, h: 1100 },
  { id: "decoys-list-find", state: "list-find", title: "A search that finds a site", w: 1440, h: 1100 },
  { id: "decoys-list-nomatch", state: "list-nomatch", title: "A search that finds nothing", w: 1440, h: 1000 },
  { id: "decoys-dlg-new", state: "dlg-new", title: "Window: new site", w: 1440, h: 900 },
  { id: "decoys-dlg-tpl", state: "dlg-tpl", title: "Window: from a template, the kinds", w: 1440, h: 900 },
  { id: "decoys-dlg-tpl-light", state: "dlg-tpl", title: "Window: from a template, the same on the light theme", w: 1440, h: 900, params: "&theme=light" },
  { id: "decoys-dlg-tpl-rail", state: "dlg-tpl", title: "Window: from a template, a 820 window", w: 820, h: 1000 },
  { id: "decoys-dlg-tpl-phone", state: "dlg-tpl", title: "Window: from a template, a phone (a sheet)", w: 400, h: 1100 },
  { id: "decoys-dlg-tpl-fa", state: "dlg-tpl", title: "Window: from a template, right to left (fa)", w: 1440, h: 900, params: "&lang=fa" },
  { id: "decoys-dlg-tpl-wait", state: "dlg-tpl-wait", title: "Window: from a template, the page is being made", w: 1440, h: 1000 },
  { id: "decoys-dlg-tpl-view", state: "dlg-tpl-view", title: "Window: from a template, a preview", w: 1440, h: 1100 },
  { id: "decoys-dlg-tpl-view-light", state: "dlg-tpl-view", title: "Window: from a template, a preview on the light theme", w: 1440, h: 1100, params: "&theme=light" },
  { id: "decoys-dlg-tpl-view-rail", state: "dlg-tpl-view", title: "Window: from a template, a preview in a 820 window", w: 820, h: 1100 },
  { id: "decoys-dlg-tpl-view-phone", state: "dlg-tpl-view", title: "Window: from a template, a preview on a phone", w: 400, h: 1100 },
  { id: "decoys-dlg-tpl-view-fa", state: "dlg-tpl-view", title: "Window: from a template, a preview, right to left (fa)", w: 1440, h: 1100, params: "&lang=fa" },
  { id: "decoys-dlg-tpl-run", state: "dlg-tpl-run", title: "Window: from a template, the site is being saved", w: 1440, h: 1100 },
  { id: "decoys-dlg-tpl-err", state: "dlg-tpl-err", title: "Window: from a template, the library is full", w: 1440, h: 1100 },
  { id: "decoys-dlg-up", state: "dlg-up", title: "Window: upload an archive", w: 1440, h: 900 },
  { id: "decoys-dlg-up-drag", state: "dlg-up-drag", title: "Window: a file is over the drop zone", w: 1440, h: 900 },
  { id: "decoys-dlg-up-run", state: "dlg-up-run", title: "Window: the upload runs", w: 1440, h: 900 },
  { id: "decoys-dlg-up-ready", state: "dlg-up-ready", title: "Window: the site is ready", w: 1440, h: 900 },
  { id: "decoys-dlg-up-draft", state: "dlg-up-draft", title: "Window: saved as a draft (a required check failed)", w: 1440, h: 900 },
  { id: "decoys-dlg-up-taken", state: "dlg-up-taken", title: "Window: the name is taken", w: 1440, h: 900 },
  { id: "decoys-dlg-up-big", state: "dlg-up-big", title: "Window: over 2 MiB", w: 1440, h: 900 },
  { id: "decoys-dlg-up-many", state: "dlg-up-many", title: "Window: too many files", w: 1440, h: 900 },
  { id: "decoys-dlg-up-unsafe", state: "dlg-up-unsafe", title: "Window: a link or an unsafe path", w: 1440, h: 900 },
  { id: "decoys-dlg-up-fmt", state: "dlg-up-fmt", title: "Window: format not recognised", w: 1440, h: 900 },
  { id: "decoys-dlg-up-noindex", state: "dlg-up-noindex", title: "Window: no index.html in the root", w: 1440, h: 900 },
  { id: "decoys-dlg-up-phone", state: "dlg-up-ready", title: "Window: the site is ready, a phone (a sheet)", w: 400, h: 900 },
  { id: "decoys-dlg-mcp", state: "dlg-mcp", title: "Window: the task for an external agent", w: 1440, h: 900 },
  { id: "decoys-site-used", state: "site-used", title: "A site that stands on two nodes (1440)", w: 1440, h: 1700 },
  { id: "decoys-site-used-light", state: "site-used", title: "The same on the light theme", w: 1440, h: 1700, params: "&theme=light" },
  { id: "decoys-site-used-rail", state: "site-used", title: "A 900 window: one column", w: 900, h: 2500 },
  { id: "decoys-site-used-phone", state: "site-used", title: "A phone, 400 wide (the phone width only)", w: 400, h: 3200 },
  { id: "decoys-site-used-fa", state: "site-used", title: "Right to left (fa)", w: 1440, h: 1700, params: "&lang=fa" },
  { id: "decoys-site-ready", state: "site-ready", title: "A ready site that stands nowhere", w: 1440, h: 1500 },
  { id: "decoys-site-template", state: "site-template", title: "A site made from a template", w: 1440, h: 1500 },
  { id: "decoys-site-draft", state: "site-draft", title: "A draft", w: 1440, h: 1500 },
  { id: "decoys-site-bad", state: "site-bad", title: "A site that fails a required check", w: 1440, h: 1300 },
  { id: "decoys-site-builtin", state: "site-builtin", title: "The built-in entry", w: 1440, h: 1000 },
  { id: "decoys-site-dir", state: "site-dir", title: "The panel's directory", w: 1440, h: 1000 },
  { id: "decoys-site-dir-off", state: "site-dir-off", title: "The panel's directory that cannot be used", w: 1440, h: 1000 },
  { id: "decoys-site-way", state: "site-way", title: "Delivery: on its way", w: 1440, h: 1200 },
  { id: "decoys-site-fail", state: "site-fail", title: "Delivery: not delivered", w: 1440, h: 1200 },
  { id: "decoys-site-off", state: "site-off", title: "Delivery: the node is offline", w: 1440, h: 1200 },
  { id: "decoys-site-old", state: "site-old", title: "Delivery: the agent is too old", w: 1440, h: 1200 },
  { id: "decoys-site-phone-view", state: "site-phone", title: "Preview: the phone width", w: 1440, h: 1700 },
  { id: "decoys-site-404", state: "site-404", title: "Preview: the 404 page", w: 1440, h: 1500 },
  { id: "decoys-site-page", state: "site-page", title: "Preview: another page", w: 1440, h: 1500 },
  { id: "decoys-site-css", state: "site-css", title: "Files: a style sheet with an external address", w: 1440, h: 1700 },
  { id: "decoys-site-img", state: "site-img", title: "Files: a picture", w: 1440, h: 1700 },
  { id: "decoys-site-edit", state: "site-edit", title: "Renaming", w: 1440, h: 900 },
  { id: "decoys-site-menu", state: "site-menu", title: "The \"More\" menu", w: 1440, h: 900 },
  { id: "decoys-site-checks", state: "site-checks", title: "Checks: the passed ones open", w: 1440, h: 1700 },
  { id: "decoys-site-copied", state: "site-copied", title: "A copy was made", w: 1440, h: 900 },
  { id: "decoys-site-deny", state: "site-deny", title: "Not the owner", w: 1440, h: 700 },
  { id: "decoys-site-load", state: "site-load", title: "Loading", w: 1440, h: 700 },
  { id: "decoys-site-nf", state: "site-nf", title: "No such site", w: 1440, h: 700 },
  { id: "decoys-site-edge", state: "site-edge", title: "The Cloudflare edition", w: 1440, h: 700 },
  { id: "decoys-dlg-assign", state: "dlg-assign", title: "Window: assign (fi1 ticked, what it replaces)", w: 1440, h: 1100 },
  { id: "decoys-dlg-assign-two", state: "dlg-assign-two", title: "Window: assign to two nodes of different domains (a warning)", w: 1440, h: 1200 },
  { id: "decoys-dlg-assign-panel", state: "dlg-assign-panel", title: "Window: assign to a node and the panel", w: 1440, h: 1200 },
  { id: "decoys-dlg-assign-phone", state: "dlg-assign-two", title: "Window: assign, a phone (a sheet)", w: 400, h: 1100 },
  { id: "decoys-dlg-unassign", state: "dlg-unassign", title: "Window: take it off a node", w: 1440, h: 1000 },
  { id: "decoys-dlg-unassign-panel", state: "dlg-unassign-panel", title: "Window: take it off the panel (the directory chosen)", w: 1440, h: 1000 },
  { id: "decoys-dlg-delete-draft", state: "dlg-delete-draft", title: "Window: delete a draft", w: 1440, h: 900 },
  { id: "decoys-dlg-delete-used", state: "dlg-delete-used", title: "Window: a site that stands cannot be deleted", w: 1440, h: 1000 },
  { id: "decoys-dlg-replace", state: "dlg-replace", title: "Window: replace the files", w: 1440, h: 900 },
  { id: "decoys-dlg-replace-file", state: "dlg-replace-file", title: "Window: replace the files, an archive chosen", w: 1440, h: 900 },
  { id: "decoys-dlg-revert", state: "dlg-revert", title: "Window: revert a version", w: 1440, h: 900 },
  { id: "decoys-dlg-continue", state: "dlg-continue", title: "Window: continue over MCP", w: 1440, h: 900 },
];

export function DecoysKit() {
  // the frames read the theme and the language when they load: a change at the top reloads them
  const key = `${useTheme()}-${useLang()}`;
  const path = location.pathname;
  return (
    <div className="flex flex-col gap-8">
      <div className="flex items-center gap-3">
        <PageTitle>Decoy sites</PageTitle>
        <div className="flex-1" />
        <LangToggle />
        <ThemeToggle />
      </div>
      {frames.map((f) => (
        <Shot key={f.id} id={f.id} title={f.title}>
          <div className="max-w-full overflow-x-auto rounded-card border border-line">
            {/* the frame is this same app (a made-up page of it), so a sandbox would only break it */}
            {/* oxlint-disable-next-line react/iframe-missing-sandbox */}
            <iframe key={key} title={f.title} src={`${path}?only=decoys-page&state=${f.state}${f.params ?? ""}`} width={f.w} height={f.h} className="block max-w-none border-0" />
          </div>
        </Shot>
      ))}
    </div>
  );
}
