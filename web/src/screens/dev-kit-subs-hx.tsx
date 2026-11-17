import { Code, ConnectError } from "@connectrpc/connect";
import { subscriptions, users } from "@/lib/api";
import { pickerUsersQuery } from "./subscriptions/queries";
import { setHxDemo, type HxDemo } from "./subscriptions/demo";
import type { MihomoTemplate, ResponseHeader } from "./subscriptions/model";
import { fakeHeaderPreview, fakeMihomoPreview, hdrBad, hdrHas, hdrMany, hdrMax, hdrOver, mtBig, mtDup, mtErr, mtFree, mtHas, mtMax, mtOver, reservedNames } from "./dev-kit-subs-hx-data";
import type { SceneDef, World } from "./dev-kit-subs-page";

// Development only. The scenes of the tabs "Response headers" and "Mihomo templates" (screens/subscriptions/headers.tsx, mihomo.tsx):
// every state of the board's switches "Заголовки" (есть, пусто, ошибки, предел) and "Шаблоны" (есть, пусто, ошибки, без групп,
// наблюдатель: a state ending in -helper is the screen as a helper sees it), then what the product has beyond the board (a save in
// flight, the panel's refusal, the question before a delete, the question before leaving, a file over the text, a preview of every
// outcome, the preview that is loading or has failed), and the same page on the phone, in the light theme and in Persian.
// Registered in dev-kit-subs-page.tsx. The panel's two previews are answered here (dev-kit-subs-hx-data.ts); nothing leaves the page.

type Make = { demo?: HxDemo; tune?: (w: World) => void; w?: number; h?: number; params?: string };

const pickerUsersQueryKey = pickerUsersQuery.queryKey; // the people of the picker, as the screen reads them
const hung = () => new Promise<never>(() => {});
const unavailable = () => Promise.reject(new ConnectError("the panel does not answer", Code.Unavailable));

/** A scene of the headers tab: the list the board starts with, what the scene asks the tab to start with, what it changes in the world. */
function headers(state: string, title: string, rows: ResponseHeader[], o: Make = {}): SceneDef {
  return {
    state,
    tab: "headers",
    title,
    h: 1200,
    ...(o.w && { w: o.w }),
    ...(o.h && { h: o.h }),
    ...(o.params && { params: o.params }),
    setup: (w) => {
      setHxDemo(o.demo ?? {});
      w.settings.responseHeaders = rows;
      w.extra.reservedHeaderNames = reservedNames;
      w.patch(subscriptions, { previewResponseHeaders: fakeHeaderPreview as (r: never) => unknown });
      o.tune?.(w);
    },
  };
}

/** A scene of the Mihomo tab. */
function templates(state: string, title: string, rows: MihomoTemplate[], o: Make & { warnings?: { code: string; arg: string }[] } = {}): SceneDef {
  return {
    state,
    tab: "mihomo",
    title,
    h: 1300,
    ...(o.w && { w: o.w }),
    ...(o.h && { h: o.h }),
    ...(o.params && { params: o.params }),
    setup: (w) => {
      setHxDemo(o.demo ?? {});
      w.settings.mihomoTemplates = rows;
      w.patch(subscriptions, { previewMihomoTemplate: fakeMihomoPreview(o.warnings) as (r: never) => unknown });
      o.tune?.(w);
    },
  };
}

/** The people's list that does not arrive: still loading, or failed (the screen's own queries are told, and the client too). */
const peopleStuck = (kind: "load" | "error") => (w: World) => {
  w.patch(users, { listUsers: kind === "load" ? hung : unavailable });
  void w.qc.prefetchQuery({ queryKey: pickerUsersQuery.queryKey, queryFn: kind === "load" ? hung : unavailable });
};
const twelve = Array.from({ length: 12 }, (_, i) => ({ id: `usr_p${i + 1}`, name: ["alice", "bob", "carol", "dave", "erin", "frank", "grace", "heidi", "ivan", "judy", "mallory", "niaj"][i]!, groupId: i % 2 ? "grp_fr" : "grp_fm" }));

/** The same scene on the phone, in the light theme and in Persian (right to left). */
const looks = (s: SceneDef): SceneDef[] => [
  { ...s, state: `${s.state}-light`, title: `${s.title}, light`, params: "&theme=light" },
  { ...s, state: `${s.state}-phone`, title: `${s.title}, phone 400`, w: 400, h: 1700 },
  { ...s, state: `${s.state}-fa`, title: `${s.title}, Persian`, params: "&lang=fa" },
];

const hdrOpen = headers("hdr-open", "Headers: the board's «есть», the first row open", hdrHas, { demo: { open: 0, who: "usr_bob" } });
const mtHasScene = templates("mt", "Mihomo templates: the board's «есть», the first open, the preview for a person of its group", mtHas);

export const hxScenes: SceneDef[] = [
  // ---- the switch "Заголовки"
  headers("hdr", "Headers: the board's «есть» (three headers, the third covered by the second)", hdrHas),
  headers("hdr-empty", "Headers: the board's «пусто»", []),
  headers("hdr-bad", "Headers: the board's «ошибки» (the first row open)", hdrBad, { demo: { open: 0 } }),
  headers("hdr-max", "Headers: the board's «предел» (20 of 20)", hdrMax),
  // ---- the product's, beyond the board
  hdrOpen,
  headers("hdr-open-b64", "Headers: a row with base64 and chosen groups open", hdrMany, { demo: { open: 3 } }),
  headers("hdr-add", "Headers: a new row, empty, open (what is missing is said at once)", [...hdrHas, { name: "", value: "", base64: false, formats: [], onlyGroups: false, groupIds: [] }], { demo: { open: 3 } }),
  headers("hdr-dirty", "Headers: changed, not saved (Discard beside Save)", hdrHas, { demo: { open: 0, headers: hdrHas.map((r, i) => (i === 0 ? { ...r, value: "Плановые работы в воскресенье" } : r)) } }),
  headers("hdr-covered", "Headers: the covered row open (its note)", hdrHas, { demo: { open: 2 } }),
  headers("hdr-over", "Headers: all together over 4096 bytes", hdrOver),
  headers("hdr-busy", "Headers: the save is on its way", hdrHas, { demo: { busy: true } }),
  headers("hdr-saved", "Headers: saved", hdrHas, { demo: { saved: true } }),
  headers("hdr-fail", "Headers: the panel did not take the save", hdrHas, { demo: { fail: "the panel does not answer" } }),
  headers("hdr-names", "Headers: the names the panel keeps, all of them", hdrHas, { demo: { names: true } }),
  headers("hdr-pv-page", "Headers: the preview for a browser (the page, none of the headers)", hdrHas, { demo: { ua: "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) Safari/604.1" } }),
  headers("hdr-pv-noaccess", "Headers: the preview for a person out of access", hdrHas, { demo: { who: "usr_carol" } }),
  headers("hdr-pv-none", "Headers: the preview where none of the headers goes", [hdrHas[1]!], { demo: { who: "usr_dave", ua: "sing-box 1.14.3" } }),
  headers("hdr-pv-load", "Headers: the preview is being asked for", hdrHas, { tune: (w) => w.patch(subscriptions, { previewResponseHeaders: hung }) }),
  headers("hdr-pv-err", "Headers: the panel does not answer the preview", hdrHas, { tune: (w) => w.patch(subscriptions, { previewResponseHeaders: unavailable }) }),
  headers("hdr-users-load", "Headers: the people are loading", hdrHas, { tune: peopleStuck("load") }),
  headers("hdr-users-err", "Headers: the people did not load", hdrHas, { tune: peopleStuck("error") }),
  headers("hdr-nobody", "Headers: no people to preview for", hdrHas, { tune: (w) => w.seed(pickerUsersQueryKey, []) }),
  headers("hdr-many", "Headers: twelve people (a list to pick from)", hdrHas, { tune: (w) => w.seed(pickerUsersQueryKey, twelve) }),
  ...looks(hdrOpen),
  ...looks(headers("hdr-bad-look", "Headers: errors", hdrBad, { demo: { open: 0 } })),
  // ---- the switch "Шаблоны"
  mtHasScene,
  templates("mt-empty", "Mihomo templates: the board's «пусто»", [], { demo: { open: null } }),
  templates("mt-err", "Mihomo templates: the board's «ошибки» (the panel refuses the first text, its line)", mtErr),
  templates("mt-free", "Mihomo templates: the board's «без групп»", mtFree),
  templates("mt-ro-helper", "Mihomo templates: the board's «наблюдатель» (a helper reads, changes nothing)", mtHas),
  // ---- the product's, beyond the board
  templates("mt-closed", "Mihomo templates: all folded (who gets which template)", mtHas, { demo: { open: null } }),
  templates("mt-second", "Mihomo templates: the second open (the group the first has is taken)", mtHas, { demo: { open: 1 } }),
  templates("mt-three", "Mihomo templates: three, one with no group", mtDup, { demo: { open: 2 } }),
  templates("mt-warn", "Mihomo templates: the notes the panel makes (a save is not stopped)", mtHas, { warnings: [{ code: "own_dns", arg: "" }, { code: "no_server_group", arg: "" }, { code: "unknown_rule_target", arg: "Прямой" }, { code: "preset_rules_skipped", arg: "" }], demo: { open: 0 } }),
  templates("mt-dirty", "Mihomo templates: changed, not saved", mtHas, { demo: { open: 0, templates: mtHas.map((r, i) => (i === 0 ? { ...r, name: "Для семьи и гостей" } : r)) } }),
  templates("mt-busy", "Mihomo templates: the save is on its way", mtHas, { demo: { busy: true, templates: mtHas.map((r, i) => (i === 0 ? { ...r, name: "Для семьи и гостей" } : r)) } }),
  templates("mt-saved", "Mihomo templates: saved", mtHas, { demo: { saved: true } }),
  templates("mt-ask", "Mihomo templates: the question before a delete", mtHas, { demo: { open: 1, ask: 1 } }),
  templates("mt-ask-free", "Mihomo templates: the question before a delete, a template nobody has", mtFree, { demo: { open: 0, ask: 0 } }),
  templates("mt-leave", "Mihomo templates: the question «Уйти без сохранения?» (a change, then another tab)", mtHas, { demo: { open: 0, leave: true, templates: mtHas.map((r, i) => (i === 0 ? { ...r, name: "Для семьи и гостей" } : r)) } }),
  templates("mt-refused-row", "Mihomo templates: the panel refuses the save at the first text", mtHas, { demo: { open: 0, refusal: { text: 'line 11: mapping values are not allowed in this context', line: 11, k: 0 }, templates: mtHas.map((r, i) => (i === 0 ? { ...r, name: "Для семьи и гостей" } : r)) } }),
  templates("mt-refused-all", "Mihomo templates: the panel refuses the whole list", mtHas, { demo: { refusal: { text: "mihomo_templates: 300000 bytes of text in all, at most 262144" }, templates: mtHas.map((r, i) => (i === 0 ? { ...r, name: "Для семьи и гостей" } : r)) } }),
  templates("mt-over", "Mihomo templates: all together over 256 KiB", mtOver, { demo: { open: null } }),
  templates("mt-big", "Mihomo templates: one text over 64 KiB", mtBig),
  templates("mt-file-big", "Mihomo templates: a file over 1 MiB was dropped", mtHas, { demo: { open: 0, fileErr: true } }),
  templates("mt-drag", "Mihomo templates: a file is over the text", mtHas, { demo: { open: 0, drag: true } }),
  templates("mt-max", "Mihomo templates: 20 of 20", mtMax, { demo: { open: 0 } }),
  templates("mt-pv-bob", "Mihomo templates: the preview for a person of the other group (the profile on request)", mtHas, { demo: { open: 0, who: "usr_bob" } }),
  templates("mt-pv-carol", "Mihomo templates: the preview for a person out of access", mtHas, { demo: { open: 0, who: "usr_carol" } }),
  templates("mt-pv-dave", "Mihomo templates: the preview for a person with no server", mtHas, { demo: { open: 0, who: "usr_dave" } }),
  templates("mt-pv-load", "Mihomo templates: the profile is being built", mtHas, { tune: (w) => w.patch(subscriptions, { previewMihomoTemplate: hung }) }),
  templates("mt-pv-err", "Mihomo templates: the panel does not answer the preview", mtHas, { tune: (w) => w.patch(subscriptions, { previewMihomoTemplate: unavailable }) }),
  templates("mt-users-load", "Mihomo templates: the people are loading", mtHas, { tune: peopleStuck("load") }),
  templates("mt-users-err", "Mihomo templates: the people did not load", mtHas, { tune: peopleStuck("error") }),
  templates("mt-nobody", "Mihomo templates: no people to preview for", mtHas, { tune: (w) => w.seed(pickerUsersQueryKey, []) }),
  templates("mt-many", "Mihomo templates: twelve people (a list to pick from)", mtHas, { tune: (w) => w.seed(pickerUsersQueryKey, twelve) }),
  ...looks(mtHasScene),
  ...looks(templates("mt-err-look", "Mihomo templates: errors", mtErr)),
];
