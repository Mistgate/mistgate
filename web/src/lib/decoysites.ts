import { ConnectError } from "@connectrpc/connect";
import { queryOptions } from "@tanstack/react-query";
import type { DecoyCheck, DecoyFileInfo, DecoyPanelState, DecoySite, DecoyTemplate, DecoyVersion } from "@/gen/mistgate/decoysite/v1/decoysite_pb";
import type { Node as NodeMsg } from "@/gen/mistgate/admin/v1/node_pb";
import { dictionaries, type T } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { decoySites } from "./api";
import { asDecoyMode, isDecoyReason } from "./decoy";
import { errorCode, errorText, errorVars, inUseWhere } from "./errors";
import { plain, type Plain } from "./plain";
import { pollMs } from "./queries";

// The library of decoy sites (docs: the "Decoy sites" page; proto: decoysite.proto). The server decides every rule; this names what the
// screens need: the queries, the filters of the list, the delivery words of a node, the tree of files and the task texts.

export type Site = Plain<DecoySite>;
export type SiteVersion = Plain<DecoyVersion>;
export type SiteCheck = Plain<DecoyCheck>;
export type SiteFile = Plain<DecoyFileInfo>;
export type PanelState = Plain<DecoyPanelState>;
export type SiteTemplate = Plain<DecoyTemplate>;
export type Node = Plain<NodeMsg>;

/** The two entries of the library that are not sites: they have no id of their own in the route, these are their words. */
export const builtinId = "builtin";
export const panelDirId = "panel_dir";
export const isBuiltinId = (id: string) => id === builtinId || id === panelDirId;

/** An archive (and a site) is refused above this; the name takes 60 characters, the note 200 (the server's rules, checked here first). */
export const maxNameLen = 60;
export const maxNoteLen = 200;
/** What the panel's own site is: "builtin", "dir" (its --decoy-dir) or "site:<id>" (a site of the library). */
export function panelSourceOf(source: string): { kind: "builtin" | "dir" | "site"; siteId: string } {
  if (source.startsWith("site:")) return { kind: "site", siteId: source.slice(5) };
  return { kind: source === "dir" ? "dir" : "builtin", siteId: "" };
}

// ---- queries (the panel is polled like the rest of the admin; a new draft from the assistant or an agent shows up by itself)

/** How long what the panel read of a version is kept: a version never changes; the panel's directory changes on its server. */
const keptFor = (id: string) => (id === panelDirId ? 10_000 : Infinity);

export const sitesQuery = queryOptions({
  queryKey: ["decoy-sites"],
  queryFn: async ({ signal }) => plain(await decoySites.listDecoySites({}, { signal })),
  refetchInterval: pollMs,
});

export const siteQuery = (id: string) =>
  queryOptions({
    queryKey: ["decoy-site", id],
    queryFn: async ({ signal }) => plain(await decoySites.getDecoySite({ siteId: id }, { signal })),
    refetchInterval: pollMs,
    // a verdict (no such site) does not change by asking again
    retry: false,
  });

/** The pages and files of the current version (or of a built-in entry, by its id); `sha` is part of the key, so a new version is asked for again. */
export const pagesQuery = (id: string, sha: string) =>
  queryOptions({
    queryKey: ["decoy-pages", id, sha],
    queryFn: async ({ signal }) => plain(await decoySites.listDecoyPages({ siteId: id, seq: 0 }, { signal })),
    staleTime: keptFor(id),
    retry: false,
  });

/** One page as a self-contained document for <iframe sandbox="" srcdoc>. A version never changes, so the document is kept by its hash. */
export const previewQuery = (id: string, sha: string, path: string, notFound: boolean) =>
  queryOptions({
    queryKey: ["decoy-preview", id, sha, path, notFound],
    queryFn: async ({ signal }) => (await decoySites.getDecoyPagePreview({ siteId: id, seq: 0, path, notFound }, { signal })).html,
    staleTime: keptFor(id),
    gcTime: 10 * 60_000,
    retry: false,
  });

/** The text of one file (the first 256 KiB). */
export const fileQuery = (id: string, sha: string, path: string) =>
  queryOptions({
    queryKey: ["decoy-file", id, sha, path],
    queryFn: async ({ signal }) => plain(await decoySites.getDecoyFile({ siteId: id, seq: 0, path }, { signal })),
    staleTime: keptFor(id),
    retry: false,
  });

/** The kinds of site the panel builds from its templates (they grow with the releases; the answer never changes while the panel runs). */
export const templatesQuery = queryOptions({
  queryKey: ["decoy-templates"],
  queryFn: async ({ signal }) => plain(await decoySites.listDecoyTemplates({}, { signal })).templates,
  staleTime: Infinity,
});

/** Whether the owner has put a Pexels key in (the key itself never comes back): the photos of generated sites need it. */
export const photosQuery = queryOptions({
  queryKey: ["decoy-photos"],
  queryFn: async ({ signal }) => plain(await decoySites.getDecoyPhotos({}, { signal })),
  staleTime: Infinity,
});

/** The page where a free Pexels API key is made, and the site the credit line points to. */
export const PEXELS_KEY_URL = "https://www.pexels.com/api/";
export const PEXELS_URL = "https://www.pexels.com";

/** What a preview says about its photos (PreviewDecoyTemplateResponse.photos); a word the panel adds later is not shown. */
export const photoStates = ["used", "no_key", "bad_key", "limit", "unavailable", "none_for_kind"] as const;
export type PhotoState = (typeof photoStates)[number];
export const photoStateKey = (s: string): MessageKey | undefined => ((photoStates as readonly string[]).includes(s) ? (`dz.ph.st.${s}` as MessageKey) : undefined);

/** What an assignment would replace, and the warnings, without doing it (a dry run). `picks` is the sorted node ids and "panel". */
export const assignPreviewQuery = (id: string, picks: readonly string[]) =>
  queryOptions({
    queryKey: ["decoy-assign", id, picks.join(",")],
    queryFn: async ({ signal }) => plain(await decoySites.assignDecoySite({ siteId: id, nodeIds: picks.filter((p) => p !== "panel"), panel: picks.includes("panel"), dryRun: true }, { signal })),
    staleTime: 0,
    gcTime: 0,
    retry: false,
  });

// ---- the sites of the list

export const isDraft = (s: Pick<Site, "state">) => s.state === "draft";
export const isUsed = (s: Pick<Site, "nodeCount" | "onPanel">) => s.nodeCount > 0 || s.onPanel;
export const warningsOf = (checks: readonly SiteCheck[]) => checks.filter((c) => c.level === "warning");
export const requiredFailed = (checks: readonly SiteCheck[]) => checks.filter((c) => c.level === "required" && !c.ok);
/** A site whose current version fails a required check cannot be assigned: the server does not keep one, yet the page words it. */
export const isBad = (s: Pick<Site, "checks">) => requiredFailed(s.checks).length > 0;

export type SiteFilter = "all" | "draft" | "used" | "free";
export const siteFilters: readonly SiteFilter[] = ["all", "draft", "used", "free"];
const filterTest: Record<SiteFilter, (s: Site) => boolean> = {
  all: () => true,
  draft: isDraft,
  used: isUsed,
  free: (s) => !isDraft(s) && !isUsed(s),
};
export const countOfFilter = (sites: readonly Site[], f: SiteFilter) => sites.filter(filterTest[f]).length;

/** The sites a filter and a search keep; the search looks at the name and the note, case aside. */
export function filterSites(sites: readonly Site[], filter: SiteFilter, query: string): Site[] {
  const q = query.trim().toLowerCase();
  return sites.filter((s) => filterTest[filter](s) && (!q || `${s.name} ${s.note}`.toLowerCase().includes(q)));
}

// ---- sites made from a template

/** The icon of each kind of template the screens know (a kind added by a later release shows the generic one until the screens learn it). */
export const templateIcons = { status: "pulse", files: "upload", login: "key", blog: "note", portfolio: "palette", business: "tag", docs: "list", card: "person", club: "people", project: "code" } as const;
export const templateIcon = (id: string) => templateIcons[id as keyof typeof templateIcons] ?? "layers";

/** The kind a site was made from ("" for a site that was not): the panel marks a generated site with the source label "template:<kind>". */
export const templateOf = (s: Pick<Site, "sourceLabel">) => (s.sourceLabel.startsWith("template:") ? s.sourceLabel.slice("template:".length) : "");

/** The name and the one-line description of a kind in the admin's language; a kind the dictionary does not know yet keeps the panel's own English name. */
export function templateWords(t: T, id: string, fallbackName = id): { name: string; text: string } {
  const known = `dz.tpl.${id}` in dictionaries.en;
  return { name: known ? t(`dz.tpl.${id}` as MessageKey) : fallbackName, text: known ? t(`dz.tpl.${id}.text` as MessageKey) : "" };
}

// ---- where a node stands: the words of its delivery

export type DeliveryKind = "ok" | "busy" | "bad" | "off" | "part";
export type Delivery = { kind: DeliveryKind; word: MessageKey; hint?: MessageKey; reason?: string };

/** The delivery of the site to a node: "Stands", on its way, not delivered, offline or an agent too old (Node.decoy_reason_code, "" = it stands). */
export function deliveryOf(n: { reasonCode: string; supported?: boolean }): Delivery {
  const code = n.reasonCode;
  if (code === "" && n.supported === false) return { kind: "part", word: "dz.dl.old", hint: "dz.dl.oldHint" };
  switch (code) {
    case "":
      return { kind: "ok", word: "dz.dl.ok" };
    case "pending":
      return { kind: "busy", word: "dz.dl.way" };
    case "node_offline":
      return { kind: "off", word: "dz.dl.off", hint: "dz.dl.offHint" };
    case "agent_too_old":
      return { kind: "part", word: "dz.dl.old", hint: "dz.dl.oldHint" };
    case "apply_failed":
      return { kind: "bad", word: "dz.dl.fail", hint: "dz.dl.failHint" };
    default:
      // a reason the panel names itself (a missing bundle, the panel's directory): the words of the node page tell it
      return { kind: "bad", word: "dz.dl.fail", reason: code };
  }
}

/** The sentence of a delivery reason the panel names itself ("" when the delivery has none of its own). */
export function deliveryHint(t: T, d: Delivery): string {
  if (d.hint) return t(d.hint);
  if (!d.reason) return "";
  if (d.reason.startsWith("panel_")) return t(`err.${d.reason}` as MessageKey);
  return isDecoyReason(d.reason) ? t(`decoy.reason.${d.reason}` as MessageKey) : t("decoy.reason.unknown", { code: d.reason });
}

/** Why the panel serves something other than what was chosen, as the key of its words ("" when it serves the choice). */
export const panelFallbackKey = (p: Pick<PanelState, "reasonCode"> | undefined): MessageKey | "" => (p?.reasonCode ? (`dz.now.fb.${p.reasonCode}` as MessageKey) : "");

/** What a node serves now, for the "Showing now" strip and the assign window: the entry, and the site it stands on. */
export function nodeServes(n: Pick<Node, "decoyMode" | "decoySiteId">): { kind: "builtin" | "dir" | "site"; siteId: string } {
  const mode = asDecoyMode(n.decoyMode);
  if (mode === "own") return { kind: "site", siteId: n.decoySiteId };
  return { kind: mode === "panel" ? "dir" : "builtin", siteId: "" };
}

// ---- the files of a site

/** "index.html" is "/", the rest "/path": how the page switcher names a page. */
export const pageLabel = (path: string) => (path === "index.html" ? "/" : `/${path}`);

const textExts = new Set(["html", "htm", "css", "txt", "svg", "js", "mjs", "json", "xml", "md", "webmanifest"]);
export const isTextPath = (path: string) => textExts.has(path.split(".").pop()?.toLowerCase() ?? "");
const imageExts = new Set(["jpg", "jpeg", "png", "webp", "gif", "avif", "ico"]);
export const isImagePath = (path: string) => imageExts.has(path.split(".").pop()?.toLowerCase() ?? "");
export const isHtmlPath = (path: string) => /\.html?$/i.test(path);

export type TreeRow = { key: string; path: string; name: string; depth: number; kind: "dir" | "file" | "more"; size: number; hidden: number };

/** A directory with more files than this folds the rest into one row ("N more files"), unless it was opened. */
export const foldAfter = 12;
const foldTo = 8;

/** The tree of a version's files as rows: directories first within a directory, a long directory folded. */
export function treeRows(files: readonly SiteFile[], open: ReadonlySet<string> = new Set()): TreeRow[] {
  type Dir = { dirs: Map<string, Dir>; files: SiteFile[] };
  const root: Dir = { dirs: new Map(), files: [] };
  for (const f of files) {
    const parts = f.path.split("/");
    let at = root;
    for (const d of parts.slice(0, -1)) {
      let next = at.dirs.get(d);
      if (!next) at.dirs.set(d, (next = { dirs: new Map(), files: [] }));
      at = next;
    }
    at.files.push(f);
  }
  const rows: TreeRow[] = [];
  const walk = (dir: Dir, prefix: string, depth: number) => {
    const byName = (a: string, b: string) => a.localeCompare(b, "en");
    // the files of the root come first (the page itself), then the folders, as the board lists them
    const own = [...dir.files].sort((a, b) => byName(a.path, b.path));
    const folded = own.length > foldAfter && !open.has(prefix || "/");
    for (const f of folded ? own.slice(0, foldTo) : own) rows.push({ key: f.path, path: f.path, name: f.path.split("/").pop() ?? f.path, depth, kind: "file", size: f.size, hidden: 0 });
    if (folded) rows.push({ key: `${prefix}\u0000more`, path: prefix || "/", name: "", depth: depth + 1, kind: "more", size: 0, hidden: own.length - foldTo });
    for (const name of [...dir.dirs.keys()].sort(byName)) {
      rows.push({ key: `${prefix}${name}/`, path: `${prefix}${name}/`, name: `${name}/`, depth, kind: "dir", size: 0, hidden: 0 });
      walk(dir.dirs.get(name)!, `${prefix}${name}/`, depth + 1);
    }
  };
  walk(root, "", 0);
  return rows;
}

// ---- the names

/** A name for a new site from a file's name: no extension, trimmed to the limit, and "2", "3" … after it while it is taken. */
export function nameFromFile(file: string, taken: readonly string[]): string {
  const base = (file.replace(/\.(tar\.gz|tgz|tar|zip|gz)$/i, "").replace(/[_-]+/g, " ").trim() || "site").slice(0, maxNameLen);
  const used = new Set(taken.map((n) => n.toLowerCase()));
  if (!used.has(base.toLowerCase())) return base;
  for (let i = 2; ; i++) {
    const suffix = ` ${i}`;
    const name = `${base.slice(0, maxNameLen - suffix.length)}${suffix}`;
    if (!used.has(name.toLowerCase())) return name;
  }
}

/** The name of a copy: "<name> (copy)" trimmed to the limit, numbered while it is taken. */
export function copyName(t: T, name: string, taken: readonly string[]): string {
  const used = new Set(taken.map((n) => n.toLowerCase()));
  for (let i = 1; ; i++) {
    const mark = t("dz.copyName", { name: "" }).trim();
    const tail = i === 1 ? mark : `${mark} ${i}`;
    const candidate = `${name.slice(0, maxNameLen - tail.length - 1).trimEnd()} ${tail}`;
    if (!used.has(candidate.toLowerCase())) return candidate.slice(0, maxNameLen);
  }
}

// ---- the task texts

/** The task for an external agent that builds a new site: the rules, and the subjects that are taken (the notes of the library). */
export function createTask(t: T, sites: readonly Pick<Site, "note">[]): string {
  const themes = sites.map((s) => s.note.trim()).filter(Boolean);
  return t("dz.task.create", { themes: themes.length ? themes.map((n) => `— ${n}`).join("\n") : t("dz.task.none") });
}

/** The task that continues one site: its name and id, so the agent reads it first. */
export const continueTask = (t: T, site: Pick<Site, "id" | "name">) => t("dz.task.continue", { name: site.name, id: site.id });

// ---- errors

/** The words of a refusal; a site that stands on nodes is worded with the node names the page knows (the server sends their ids). */
export function decoyErrorText(e: unknown, t: T, nodeName: (id: string) => string = (id) => id): string {
  const c = ConnectError.from(e);
  if (errorCode(c.rawMessage) === "decoy_site_in_use") {
    const v = errorVars(c.rawMessage);
    return t("err.decoy_site_in_use", { where: inUseWhere(t, (v.nodes ?? "").split(",").filter(Boolean).map(nodeName), v.panel === "1") });
  }
  return errorText(e, t);
}

const uploadCodes = [
  "decoy_bundle_too_large",
  "decoy_bundle_too_many_files",
  "decoy_bundle_bad_path",
  "decoy_bundle_not_static",
  "decoy_bundle_bad_archive",
  "decoy_bundle_no_index",
  "decoy_bundle_empty",
  "decoy_bundle_duplicate",
  "decoy_name_taken",
  "decoy_name_invalid",
  "decoy_note_invalid",
  "decoy_library_full",
] as const;

/** The words of one refusal code of the upload: a title and a line under it (`path` is the entry the archive rules name). */
export function uploadErrorFor(t: T, code: string, path = "?"): { title: string; text: string; code: string } {
  if (!(uploadCodes as readonly string[]).includes(code)) return { code, title: t("dz.er.other"), text: "" };
  const key = code as (typeof uploadCodes)[number];
  return { code, title: t(`dz.er.${key}`), text: t(`dz.er.${key}.s`, { path }) };
}

/** What the upload window says of a refusal: the codes of the bundle rules and the library have their own words; anything else reads as the generic sentence. */
export function uploadError(e: unknown, t: T): { title: string; text: string; code: string } {
  const c = ConnectError.from(e);
  const code = errorCode(c.rawMessage);
  const known = uploadErrorFor(t, code, errorVars(c.rawMessage).path);
  return known.text ? known : { ...known, text: errorText(e, t) };
}

// ---- a download

/** Saves bytes as a file through a link to a blob (the panel's own origin, no other host). */
export function saveBytes(filename: string, bytes: Uint8Array, type = "application/zip") {
  const url = URL.createObjectURL(new Blob([bytes as BlobPart], { type }));
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  document.body.append(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 30_000);
}
