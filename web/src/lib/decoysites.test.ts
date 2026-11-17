import { Code, ConnectError } from "@connectrpc/connect";
import { beforeEach, describe, expect, it } from "vitest";
import { dictionaries, fill, pickForm, setLang, type Lang, type T } from "@/i18n";
import * as decoysites from "@/i18n/decoysites";
import { en } from "@/i18n/en";
import { errorCodes, errorText } from "./errors";
import {
  continueTask,
  copyName,
  countOfFilter,
  createTask,
  decoyErrorText,
  deliveryOf,
  filterSites,
  isBad,
  linkKey,
  nameFromFile,
  nodeServes,
  pageLabel,
  panelSourceOf,
  treeRows,
  uploadError,
  type Site,
} from "./decoysites";

const t: T = Object.assign((key: keyof typeof en, vars?: Record<string, string | number>) => fill(en[key], vars), {
  n: (key: keyof typeof en, count: number, vars?: Record<string, string | number>) => fill(pickForm(en[key], count, "en"), { n: count, ...vars }),
}) as T;

const site = (over: Partial<Site> = {}): Site =>
  ({
    id: "dsi_a", name: "Bakery", note: "bakery, German", source: "upload", sourceLabel: "owner", state: "ready", bundleSha256: "ab".repeat(32), createdUnix: 1, createdBy: "owner", updatedUnix: 2, updatedBy: "owner", version: 1,
    sizeBytes: 1000, fileCount: 3, title: "Holz", checks: [], nodeCount: 0, onPanel: false, ...over,
  }) as Site;

describe("the filters of the list", () => {
  const sites = [
    site({ id: "dsi_1", name: "Bakery", note: "bakery, German" }),
    site({ id: "dsi_2", name: "Studio", note: "photo studio", state: "draft" }),
    site({ id: "dsi_3", name: "Joinery", note: "furniture", nodeCount: 2 }),
    site({ id: "dsi_4", name: "Nursery", note: "plants", onPanel: true }),
  ];
  it("counts every filter the way the chips show it", () => {
    expect(["all", "draft", "used", "free"].map((f) => countOfFilter(sites, f as never))).toEqual([4, 1, 2, 1]);
  });
  it("a draft is neither in use nor free, a site on the panel alone is in use", () => {
    expect(filterSites(sites, "used", "").map((s) => s.id)).toEqual(["dsi_3", "dsi_4"]);
    expect(filterSites(sites, "free", "").map((s) => s.id)).toEqual(["dsi_1"]);
    expect(filterSites(sites, "draft", "").map((s) => s.id)).toEqual(["dsi_2"]);
  });
  it("searches the name and the note, case aside", () => {
    expect(filterSites(sites, "all", "  PHOTO ").map((s) => s.id)).toEqual(["dsi_2"]);
    expect(filterSites(sites, "all", "bak").map((s) => s.id)).toEqual(["dsi_1"]);
    expect(filterSites(sites, "used", "bakery")).toEqual([]);
  });
});

describe("a site that fails a required check", () => {
  it("is bad only when a required check failed", () => {
    expect(isBad(site())).toBe(false);
    expect(isBad(site({ checks: [{ code: "index_html", level: "required", ok: false, paths: [] }] }))).toBe(true);
    expect(isBad(site({ checks: [{ code: "no_404", level: "warning", ok: false, paths: [] }] }))).toBe(false);
  });
});

describe("the delivery of a site to a node", () => {
  it("reads the node's reason code as the board's five words", () => {
    expect(deliveryOf({ reasonCode: "" })).toMatchObject({ kind: "ok", word: "dz.dl.ok" });
    expect(deliveryOf({ reasonCode: "pending" })).toMatchObject({ kind: "busy", word: "dz.dl.way" });
    expect(deliveryOf({ reasonCode: "apply_failed" })).toMatchObject({ kind: "bad", word: "dz.dl.fail", hint: "dz.dl.failHint" });
    expect(deliveryOf({ reasonCode: "node_offline" })).toMatchObject({ kind: "off", word: "dz.dl.off" });
    expect(deliveryOf({ reasonCode: "agent_too_old" })).toMatchObject({ kind: "part", word: "dz.dl.old" });
  });
  it("an agent without decoy/1 is too old even when no reason came", () => {
    expect(deliveryOf({ reasonCode: "", supported: false })).toMatchObject({ kind: "part", word: "dz.dl.old" });
  });
  it("a reason the panel names itself is not delivered, and keeps its code", () => {
    expect(deliveryOf({ reasonCode: "own_bundle_missing" })).toMatchObject({ kind: "bad", word: "dz.dl.fail", reason: "own_bundle_missing" });
  });
});

describe("what a node or the panel shows", () => {
  it("names the entry or the site", () => {
    expect(nodeServes({ decoyMode: "builtin", decoySiteId: "" })).toEqual({ kind: "builtin", siteId: "" });
    expect(nodeServes({ decoyMode: "panel", decoySiteId: "" })).toEqual({ kind: "dir", siteId: "" });
    expect(nodeServes({ decoyMode: "own", decoySiteId: "dsi_a" })).toEqual({ kind: "site", siteId: "dsi_a" });
    expect(panelSourceOf("site:dsi_a")).toEqual({ kind: "site", siteId: "dsi_a" });
    expect(panelSourceOf("dir").kind).toBe("dir");
    expect(panelSourceOf("").kind).toBe("builtin");
  });
  it("ties nodes by the last two labels of an address, an IP stands alone", () => {
    expect(linkKey("de1.example.com", "a")).toBe(linkKey("nl1.example.com", "b"));
    expect(linkKey("de1.example.com", "a")).not.toBe(linkKey("de1.example.net", "a"));
    expect(linkKey("203.0.113.7", "a")).toBe("node:a");
    expect(linkKey("[2001:db8::1]:443", "a")).toBe("node:a");
  });
});

describe("the tree of files", () => {
  const files = (paths: string[]) => paths.map((path) => ({ path, size: 10 }));
  it("lists the root files first, then every folder with its files under it", () => {
    const rows = treeRows(files(["index.html", "css/style.css", "img/a.jpg", "404.html"]));
    expect(rows.map((r) => `${r.depth}${r.kind[0]}${r.name}`)).toEqual(["0f404.html", "0findex.html", "0dcss/", "1fstyle.css", "0dimg/", "1fa.jpg"]);
  });
  it("folds a directory with many files into one row, which a click opens", () => {
    const many = files(Array.from({ length: 18 }, (_, i) => `img/p${String(i).padStart(2, "0")}.jpg`));
    const folded = treeRows(many);
    expect(folded.filter((r) => r.kind === "file")).toHaveLength(8);
    expect(folded.find((r) => r.kind === "more")).toMatchObject({ hidden: 10, path: "img/" });
    expect(treeRows(many, new Set(["img/"])).filter((r) => r.kind === "file")).toHaveLength(18);
  });
  it("names a page as the visitor sees its address", () => {
    expect(pageLabel("index.html")).toBe("/");
    expect(pageLabel("about/index.html")).toBe("/about/index.html");
  });
});

describe("names", () => {
  it("takes a name from the archive, numbered while it is taken, within 60 characters", () => {
    expect(nameFromFile("holz-ofen.zip", [])).toBe("holz ofen");
    expect(nameFromFile("holzofen.tar.gz", ["Holzofen", "holzofen 2"])).toBe("holzofen 3");
    expect(nameFromFile(`${"x".repeat(80)}.zip`, [])).toHaveLength(60);
    expect(nameFromFile(`${"x".repeat(80)}.zip`, ["x".repeat(60)]).length).toBeLessThanOrEqual(60);
  });
  it("names a copy after the site and numbers it", () => {
    expect(copyName(t, "Bakery", [])).toBe("Bakery (copy)");
    expect(copyName(t, "Bakery", ["bakery (copy)"])).toBe("Bakery (copy) 2");
    expect(copyName(t, "x".repeat(60), [])).toHaveLength(60);
  });
});

describe("the task texts", () => {
  it("asks for a subject and a look the library does not have, and names the taken subjects", () => {
    const task = createTask(t, [{ note: "bakery, German" }, { note: "  " }, { note: "boat rental" }]);
    expect(task).toContain("decoy_sites_list");
    expect(task).toContain("decoy_site_draft_save");
    expect(task).toContain("differ from all of them");
    expect(task).toContain("Always fill the note");
    expect(task).toContain("— bakery, German\n— boat rental");
    expect(createTask(t, [])).toContain(en["dz.task.none"]);
  });
  it("names the site by its id and name, so the agent reads it first", () => {
    const task = continueTask(t, { id: "dsi_a1", name: "Bakery" });
    expect(task).toContain("“Bakery”");
    expect(task).toContain("id: dsi_a1");
    expect(task).toContain("decoy_site_version_plan");
  });
});

describe("the words of a refusal", () => {
  beforeEach(() => setLang("en"));
  const refuse = (code: Code, message: string) => new ConnectError(message, code);
  it("words every refusal of the archive rules with a title and a line, and the entry that is in the way", () => {
    const e = uploadError(refuse(Code.InvalidArgument, "decoy_bundle_bad_path: path=img%2Flatest"), t);
    expect(e.title).toBe(en["dz.er.decoy_bundle_bad_path"]);
    expect(e.text).toContain("img/latest");
    for (const reason of ["too_large", "too_many_files", "bad_archive", "no_index", "empty", "duplicate", "not_static"]) {
      const r = uploadError(refuse(Code.InvalidArgument, `decoy_bundle_${reason}`), t);
      expect(r.title).not.toBe(en["dz.er.other"]);
      expect(r.text).not.toBe("");
    }
  });
  it("words the refusals of the library", () => {
    for (const code of ["decoy_name_taken", "decoy_name_invalid", "decoy_note_invalid", "decoy_library_full"]) {
      expect(uploadError(refuse(Code.FailedPrecondition, code), t).title).toBe(en[`dz.er.${code}` as keyof typeof en]);
    }
  });
  it("reads anything else as the generic sentence", () => {
    const e = uploadError(refuse(Code.Internal, "boom"), t);
    expect(e.title).toBe(en["dz.er.other"]);
    expect(e.text).not.toBe("");
  });
  it("names where a site that cannot be deleted stands, by the names the page knows", () => {
    const e = refuse(Code.FailedPrecondition, "decoy_site_in_use: nodes=nod_a%2Cnod_b&panel=1");
    const names = new Map([["nod_a", "de1"], ["nod_b", "nl1"]]);
    expect(decoyErrorText(e, t, (id) => names.get(id) ?? id)).toBe("The site stands on nodes de1, nl1 and the panel: take it off first.");
    expect(decoyErrorText(refuse(Code.FailedPrecondition, "decoy_site_in_use: nodes=nod_a"), t)).toContain("nodes nod_a");
    expect(decoyErrorText(refuse(Code.FailedPrecondition, "decoy_site_in_use: nodes="), t, undefined)).toContain("take it off first");
  });
});

describe("every code of the library reads in words in every language", () => {
  const codes = [
    "decoy_site_not_found", "decoy_version_not_found", "decoy_name_taken", "decoy_name_invalid", "decoy_note_invalid", "decoy_library_full", "decoy_site_draft", "decoy_site_not_draft", "decoy_site_changed",
    "decoy_checks_failed", "decoy_site_in_use", "node_not_assigned", "panel_not_assigned", "node_retired", "decoy_file_not_found", "decoy_file_binary", "decoy_page_not_found", "decoy_preview_too_large",
    "panel_unavailable", "panel_too_large", "panel_no_index", "panel_invalid",
    "decoy_bundle_empty", "decoy_bundle_bad_archive", "decoy_bundle_too_large", "decoy_bundle_too_many_files", "decoy_bundle_bad_path", "decoy_bundle_not_static", "decoy_bundle_no_index", "decoy_bundle_duplicate",
  ];
  it("is a listed code of the page, with a sentence of its own", () => {
    for (const c of codes) expect(errorCodes as readonly string[], c).toContain(c);
  });
  for (const lang of ["en", "ru", "zh", "fa", "tr"] as Lang[]) {
    it(`speaks ${lang}`, () => {
      setLang(lang);
      const d = dictionaries[lang];
      const tl = Object.assign((k: keyof typeof en, v?: Record<string, string | number>) => fill(d[k], v), { n: (k: keyof typeof en, n: number, v?: Record<string, string | number>) => fill(pickForm(d[k], n, lang), { n, ...v }) }) as T;
      for (const c of codes) {
        const text = errorText(new ConnectError(`${c}: path=x&nodes=nod_a&feature=decoy_own`, Code.FailedPrecondition), tl);
        // a code the page words reads as a sentence, not as the code the server sent
        expect(text, `${lang}/${c}`).not.toBe(c);
        expect(text, `${lang}/${c}`).not.toMatch(/^decoy_|^panel_|^node_/);
      }
      setLang("en");
    });
  }
  it("names the feature the Cloudflare edition lacks", () => {
    expect(errorText(new ConnectError("unavailable_in_edition: feature=decoy_own", Code.FailedPrecondition), t)).toContain(en["edition.feature.decoy_own"]);
  });
});

describe("the copy of the page in the five languages", () => {
  // words that are the same in every language: a protocol, a hash, a file name
  const same = new Set(["dz.src.mcp", "dz.sha", "dz.fact.sha", "dz.fact.by", "dz.now.dirSub", "dz.bi.dirNote", "dz.lim.sizeV", "dz.new", "dz.now.panel", "dz.use.panel", "dz.pl.panel"]);
  const own = Object.keys(decoysites.en) as (keyof typeof decoysites.en)[];
  for (const lang of ["ru", "zh", "fa", "tr"] as const) {
    it(`${lang} has every key, translated, with the same placeholders`, () => {
      const d = decoysites[lang];
      const holes = (s: string) => [...new Set([...s.matchAll(/\{(\w+)\}/g)].map((m) => m[1]))].sort().join(",");
      expect(own.filter((k) => typeof d[k] !== "string")).toEqual([]);
      expect(own.filter((k) => !same.has(k) && d[k] === decoysites.en[k]), "still English").toEqual([]);
      expect(own.filter((k) => holes(d[k]) !== holes(decoysites.en[k])), "placeholders differ").toEqual([]);
    });
  }
});
