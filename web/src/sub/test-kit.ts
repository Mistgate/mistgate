import { cases } from "./dev-data";
import { normalize } from "./logic";
import { newAmzState, newDnsState, type Actions, type State } from "./state";
import type { MgData } from "./types";

// Shared by the tests of the page: sample data, a state, and actions that only note what was asked.

export const noop = () => {};

export const amzNoop = {
  add: noop,
  form: noop,
  create: noop,
  show: noop,
  renew: noop,
  node: noop,
  where: noop,
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
    platform: (p) => log.push(`platform ${p}`),
    app: (k) => log.push(`app ${k}`),
    theme: (t) => log.push(`theme ${t}`),
    qrOpen: (o) => log.push(`qr ${o}`),
    more: (o) => log.push(`more ${o}`),
    stepsAgain: () => log.push("steps-again"),
    closeAnn: () => log.push("closed"),
    mark: () => log.push("mark"),
    copy: (text, _m, done) => {
      log.push(`copy ${text}`);
      done?.();
    },
    download: (f) => log.push(`download ${f}`),
    amz: { ...amzNoop, add: (o) => log.push(`add ${o}`) },
    dns: { open: (s) => log.push(`dns-open ${s}`), pick: (p) => log.push(`dns-pick ${p}`), apply: () => log.push("dns-apply"), retry: () => log.push("dns-retry") },
    ...over,
  };
}

/** The state of a page opened on `platform` (the platform is also what the browser says it is). */
export function state(platform: State["platform"] = "ios", over: Partial<State> = {}, lang: "ru" | "en" = "ru"): State {
  return {
    lang,
    platform,
    detected: platform,
    app: "",
    theme: "auto",
    qrOpen: false,
    more: false,
    stepsAgain: false,
    annClosed: false,
    returning: false,
    marked: false,
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
