import { label } from "./amnezia";
import { keyDnsServers } from "./dns-actions";
import { errLine, foot, frame, spin } from "./dlg-kit";
import { h, type Kid } from "./dom";
import { icon } from "./icons";
import { countryName, dnsChoices, dnsDefault, dnsPresetOf, hasKeys, keyAppName, LINK, linkAppName, linkChoices, presetDescription, presetName, serversOf, serverText, type DnsChoice } from "./logic";
import { flag } from "./servers";
import type { Ctx } from "./state";
import type { AwgDevice, Platform, ServerEntry } from "./types";

// The DNS windows after the boards V2DlgDns / V2DlgDnsdone: the choice (which servers, which DNS) and, when it is saved, the result:
// what applies now and which key devices must get their key again. One window for the keys of a server (or of all servers) and
// the same for the apps that use the link (LINK), which have one DNS for all servers and no keys. State: s.dns (open, pick, all,
// busy, error, skipped, done); the window does not close when the choice is saved, it turns into the result.

/** What a server is called in the window: the place ("Германия · Франкфурт"), and its number among the twins of its country ("Германия DE-2") when the table says so too. */
function placeOf(c: Ctx, x: ServerEntry): string {
  const cc = x.country_code.trim().toUpperCase();
  const twins = cc && !x.place ? serversOf(c.d).filter((y) => y.country_code.trim().toUpperCase() === cc && !y.place) : [];
  // the panel names the twins "Germany 2": its number is the server's; else the order in the subscription
  const n = /\s(\d{1,2})$/.exec(x.label.trim());
  return twins.length > 1 ? `${serverText(x, c.s.lang)} ${cc}-${n ? n[1] : twins.indexOf(x) + 1}` : serverText(x, c.s.lang);
}

const server = (c: Pick<Ctx, "d">, id: string | null) => (id && id !== LINK ? c.d.servers.find((x) => x.id === id) : undefined);

/** The window can be shown: the link apps' DNS exists, or the server is there with a DNS to choose. */
export const dnsHas = (c: Ctx): boolean => {
  const id = c.s.dns.open;
  if (!id) return false;
  return id === LINK ? !!c.d.dns : !!server(c, id)?.dns;
};

/** "The result": the choice was saved (the window then says what happens next). */
export const dnsDone = (c: Ctx): boolean => !!c.s.dns.open && c.s.dns.done[c.s.dns.open] === true;

/** The link app the window names: the one the page puts first on this device. */
const linkApp = (c: Ctx) => linkAppName(c.d, (c.s.amz.here || null) as Platform | null);

export function dnsLabel(c: Ctx): string {
  const { t } = c;
  if (dnsDone(c)) return t.dgDoneT;
  return c.s.dns.open === LINK ? t.dnsLinkT(linkApp(c)) : t.dgDnsT;
}

/** The step of the window: the choice, or its result. */
export const dnsStage = (c: Ctx): string => (dnsDone(c) ? "dns:done" : "dns:pick");

// ---- the choice ----

/** The preset that applies now at the open target. */
function nowOf(c: Ctx, srv?: ServerEntry): string {
  const { d } = c;
  if (c.s.dns.open === LINK) return d.dns!.link.effective || d.dns!.link.default;
  return srv!.dns!.effective || dnsDefault(srv!.dns!);
}

/** The radio list of presets (the board's .a-opts): the default first, "now" on the one that applies. */
function options(c: Ctx, choices: DnsChoice[], now: string, sameDefault: boolean): HTMLElement {
  const { s, a, t } = c;
  return h(
    "div",
    { class: "a-opts", role: "radiogroup", "aria-labelledby": "dlg-dns" },
    ...choices.map((o) => {
      const value = o.isDefault ? "" : o.id;
      const on = s.dns.pick === value;
      // the default of the servers may differ: only the open server's own default is named
      const plain = o.isDefault && !sameDefault;
      return h(
        "button",
        { class: `a-opt${on ? " on" : ""}`, type: "button", role: "radio", "aria-checked": on, "data-k": `dns-opt-${value || "default"}`, "data-autofocus": on ? "" : false, on: { click: () => a.dns.pick(value) } },
        h("span", { class: "a-rd" }),
        h(
          "span",
          { class: "tx" },
          h("span", { class: "t" }, o.isDefault ? (plain ? t.dnsDefaultPlain : t.dnsDefault(o.name.split(/\s+—\s+/)[0] ?? o.name)) : o.name, o.id === now && h("span", { class: "tag" }, t.dnsNow)),
          o.description && !plain && h("span", { class: "d" }, o.description),
        ),
      );
    }),
  );
}

function pick(c: Ctx): Kid[] {
  const { d, s, a, t } = c;
  const link = s.dns.open === LINK;
  const srv = server(c, s.dns.open);
  const targets = link ? [] : keyDnsServers(d, s.dns.open!).map((x) => server(c, x)!);
  const many = targets.length > 1;
  const all = many && s.dns.all;
  const choices = link ? linkChoices(d, s.lang) : dnsChoices(d, srv!, s.lang);
  const sameDefault = !all || targets.every((x) => x.dns && dnsDefault(x.dns) === dnsDefault(srv!.dns!));
  const busy = s.dns.busy !== "";
  const e = s.dns.error;
  const fail = e && (e.server === LINK || !all ? t.dnsErr(e.code, e.retryMin) : `${placeOf(c, server(c, e.server) ?? srv!)}: ${t.dnsErr(e.code, e.retryMin)}`);
  const chip = (on: boolean, k: string, text: string, click: () => void) => h("button", { class: `a-chip${on ? " on" : ""}`, type: "button", role: "radio", "aria-checked": on, "data-k": k, on: { click } }, text);
  const body = h(
    "div",
    { class: "a-b" },
    h("p", { class: "a-lead" }, link ? t.dgLinkLead(d.dns!.refresh_hours) : t.dgDnsLead(keyAppName(d, null))),
    many &&
      h(
        "div",
        { class: "a-fld" },
        h("p", { class: "a-lbl", id: "dlg-sc" }, t.dgDnsScope),
        h("div", { class: "a-seg", role: "radiogroup", "aria-labelledby": "dlg-sc" }, chip(all, "dlg-sc-all", t.dgDnsAll, () => a.dns.scope(true)), chip(!all, "dlg-sc-one", t.dgDnsOne(placeOf(c, srv!)), () => a.dns.scope(false))),
      ),
    h("div", { class: "a-fld" }, h("p", { class: "a-lbl", id: "dlg-dns" }, t.dgDnsField), options(c, choices, nowOf(c, srv), sameDefault)),
    fail && errLine(fail),
  );
  // after the change the keys are fetched again: said where there are keys
  const keys = !link && (all ? targets.some(hasKeys) : hasKeys(srv!));
  return frame(c, {
    label: dnsLabel(c),
    title: link ? t.dnsLinkT(linkApp(c)) : t.dgDnsT,
    close: () => a.dns.open(null),
    body,
    foot: foot(
      keys && h("p", { class: "u-hint" }, t.dgDnsHint),
      h("button", { class: "u-btn", type: "button", "data-k": "dns-cancel", on: { click: () => a.dns.open(null) } }, t.awgCancel),
      e
        ? h("button", { class: "u-btn pri", type: "button", "data-k": "dns-apply", disabled: busy, "aria-busy": busy ? "true" : false, on: { click: () => a.dns.retry() } }, busy ? spin() : icon("refresh"), busy ? t.dnsSaving : t.dnsRetry)
        : h("button", { class: "u-btn pri", type: "button", "data-k": "dns-apply", disabled: busy, "aria-busy": busy ? "true" : false, on: { click: () => a.dns.apply() } }, busy && spin(), busy ? t.dnsSaving : t.save),
    ),
  });
}

// ---- the result ----

/** The server a key belongs to: the one just changed when it carries the key's profile, else the first with a DNS of its keys that does, else any. */
function serverOfKey(c: Ctx, x: AwgDevice): ServerEntry | undefined {
  const carries = (s: ServerEntry) => s.connections.some((k) => k.way === "key" && k.profile_id === x.profile_id);
  const open = server(c, c.s.dns.open);
  return (open && !c.s.dns.all && carries(open) ? open : undefined) ?? c.d.servers.find((s) => s.dns && carries(s)) ?? c.d.servers.find(carries);
}

/** The key devices that hold the old DNS: each is a row with "Get". */
function rows(c: Ctx, list: AwgDevice[]): HTMLElement {
  const { s, a, t } = c;
  return h(
    "div",
    { class: "a-opts" },
    ...list.map((x) => {
      const srv = serverOfKey(c, x);
      const country = srv ? countryName(srv.country_code, s.lang) : "";
      const name = label(x, t);
      const busy = s.amz.busy === `renew:${x.id}`;
      return h(
        "div",
        { class: "a-opt" },
        srv && flag(c, srv, true),
        h("span", { class: "tx" }, h("span", { class: "t" }, name), h("span", { class: "d" }, country ? t.dgDoneRow(country) : t.dgDoneRowNo)),
        h("button", { class: "u-btn", type: "button", "data-k": `dlg-get-${x.id}`, disabled: busy, "aria-busy": busy ? "true" : false, "aria-label": t.dgGetAria(name), on: { click: () => !busy && a.amz.renew(x.id) } }, busy && spin(), busy ? t.awgBusy : t.dgGet),
      );
    }),
  );
}

function done(c: Ctx): Kid[] {
  const { d, s, a, t } = c;
  const link = s.dns.open === LINK;
  const srv = server(c, s.dns.open);
  const stale = (d.amnezia?.devices ?? []).filter((x) => x.stale && x.stale_reason === "dns");
  const all = !link && s.dns.all && keyDnsServers(d, s.dns.open!).length > 1;
  const p = link ? undefined : dnsPresetOf(d, nowOf(c, srv));
  const name = p ? presetName(p, s.lang) : "";
  const desc = p ? presetDescription(p, s.lang) : "";
  let lead: string;
  if (link) lead = t.dgDoneLink(d.dns!.refresh_hours);
  else if (stale.length === 0) lead = t.dgDoneNone;
  else lead = all ? t.dgDoneAll(name, desc) : t.dgDoneOne(placeOf(c, srv!), name, desc);
  const skipped = s.dns.skipped.map((id) => server(c, id)).filter((x): x is ServerEntry => !!x);
  const renewFail = s.amz.error && s.amz.errorAt.startsWith("renew:") && stale.some((x) => `renew:${x.id}` === s.amz.errorAt) ? errLine(t.err(s.amz.error, s.amz.retryMin)) : null;
  return frame(c, {
    label: dnsLabel(c),
    title: t.dgDoneT,
    close: () => a.dns.open(null),
    body: h(
      "div",
      { class: "a-b" },
      h("p", { class: "a-lead" }, lead),
      all && skipped.length > 0 && h("p", { class: "u-hint" }, t.dgDoneSkip(skipped.map((x) => placeOf(c, x)).join(", "))),
      !link && stale.length > 0 && rows(c, stale),
      renewFail,
    ),
    foot: foot(h("button", { class: "u-btn", type: "button", "data-k": "dlg-done", "data-autofocus": "", on: { click: () => a.dns.open(null) } }, t.awgDone)),
  });
}

export const dnsWindow = (c: Ctx): Kid[] => (dnsDone(c) ? done(c) : pick(c));
