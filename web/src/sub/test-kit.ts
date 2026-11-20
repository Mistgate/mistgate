import { deviceModal, deviceModalLabel, deviceModalStage } from "./add";
import { cases } from "./dev-data";
import { dict } from "./i18n";
import { normalize } from "./logic";
import { newAmzState, newDnsState, type Actions, type State } from "./state";
import type { Lang, MgData } from "./types";

// Shared by the tests of the page: sample data, a state, and actions that only note what was asked.

export const noop = () => {};

export const amzNoop = {
  add: noop,
  pane: noop,
  form: noop,
  create: noop,
  show: noop,
  renew: noop,
  stale: noop,
  node: noop,
  ask: noop,
  rotate: noop,
  remove: noop,
  menu: noop,
  renameStart: noop,
  renameInput: noop,
  renameSave: noop,
  renameCancel: noop,
};

/** Actions that do nothing, with the given ones replaced. `log` collects what the copy and platform calls were. */
export function actions(over: Partial<Actions> = {}): Actions & { log: string[] } {
  const log: string[] = [];
  return {
    log,
    lang: (l) => log.push(`lang ${l}`),
    tab: (t) => log.push(`tab ${t}`),
    platform: (p) => log.push(`platform ${p}`),
    app: (k) => log.push(`app ${k}`),
    theme: (t) => log.push(`theme ${t}`),
    devicesOpen: (o) => log.push(`devices-open ${o}`),
    closeAnn: () => log.push("closed"),
    mark: () => log.push("mark"),
    copy: (text, _m, done) => {
      log.push(`copy ${text}`);
      done?.();
    },
    download: (f) => log.push(`download ${f}`),
    amz: { ...amzNoop, add: (o, pane) => log.push(pane ? `add ${o} ${pane}` : `add ${o}`), pane: (p) => log.push(`pane ${p}`) },
    dns: { open: (s) => log.push(`dns-open ${s}`), pick: (p) => log.push(`dns-pick ${p}`), apply: () => log.push("dns-apply"), retry: () => log.push("dns-retry"), scope: (all) => log.push(`dns-scope ${all}`) },
    ...over,
  };
}

/** The state of a page opened on `platform` (the platform is also what the browser says it is). */
export function state(platform: State["platform"] = "ios", over: Partial<State> = {}, lang: Lang = "ru"): State {
  return {
    lang,
    tab: "servers",
    platform,
    detected: platform,
    app: "",
    theme: "auto",
    devicesOpen: false,
    annClosed: false,
    returning: false,
    amz: newAmzState(platform, "p31", platform),
    dns: newDnsState(),
    ...over,
  };
}

/** A sample case, as the page reads it; `f` may change it. */
export const data = (name: keyof typeof cases | string, f: (d: MgData) => void = () => {}): MgData => {
  const d = normalize(structuredClone(cases[name]!));
  f(d);
  return d;
};

/** A string with the unbreakable spaces the page sets before a dash turned into plain ones. */
export const plain = (s: string | undefined | null) => (s ?? "").replace(/ /g, " ");

export const text = (el: Element | null | undefined) => plain(el?.textContent);

/**
 * The window "New device" for a person on `platform`: what the page hands the modal (`box`), its name and step. `amz` and `over`
 * change the state of the keys and of the page; `support` is the link of the page ("" = none).
 */
export function dialog(
  d: MgData,
  o: { platform?: State["platform"]; lang?: Lang; amz?: Partial<State["amz"]>; over?: Partial<State>; a?: ReturnType<typeof actions>; support?: string } = {},
) {
  const platform = o.platform ?? "ios";
  const a = o.a ?? actions();
  const s = state(platform, { ...o.over }, o.lang ?? "ru");
  Object.assign(s.amz, { adding: true, pane: "pick" }, o.amz);
  const c = { d, s, a, t: dict[s.lang], support: o.support ?? "https://t.me/example_support" };
  const box = document.createElement("div");
  box.append(...(deviceModal(c) as Node[]));
  return { box, a, c, label: deviceModalLabel(c), stage: deviceModalStage(c) };
}
