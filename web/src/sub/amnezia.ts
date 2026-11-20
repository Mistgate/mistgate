import { qrLogoSrc } from "./brand";
import { chip } from "./connect";
import { h, type Kid } from "./dom";
import type { Dict } from "./i18n";
import { icon, type IconName } from "./icons";
import { addPlatforms, deviceIcon, keyAppName, keyVersions, platformWord, serverMark, serverName, serversOf, versionsFor, type KeySet, type KeyVersion } from "./logic";
import { qrCard } from "./qr";
import { flag } from "./servers";
import type { AmzActions, AmzState, Tools } from "./state";
import type { AwgConfig, AwgDevice, Lang, MgData, Platform } from "./types";

export { newAmzState } from "./state";
export type { AmzActions, AmzState, Tools } from "./state";

// The AmneziaVPN way of the user page: the form that makes a device (step "key" of the "new device" window) and the key of a
// device (step "ready": its QR code, file and vpn:// text). The list of devices is devices.ts. State and actions are the page's
// (view.ts, main.ts); nothing here talks to the network. Keys hold a private key: they exist only in `AmzState.configs`, in
// memory, and are asked for one device at a time.

/** What these parts need of the page: the data, the state they read, what they may do, the words, the support link. */
export type AmzCtx = { d: MgData; s: { lang: Lang; amz: AmzState }; a: { amz: AmzActions } & Tools; t: Dict; support?: string };

/** The name of a device: its label, else its platform's word, else "Device". */
export const label = (x: AwgDevice, t: Dict) => x.label || platformWord(x.platform, t) || t.devGeneric;


// ---- step "key": the form of a new device ----

/** The flags a key's card draws: a computer eight, a phone four; the rest is a count ("+3"). sub-b.css shows the one end that fits (.only-w, .only-m). */
function flagRow(c: AmzCtx, ccs: string[]): HTMLElement {
  const one = (cc: string, i: number) => {
    const f = flag(c, { country_code: cc, place: "", label: "" }, true);
    if (i >= 4) f.classList.add("only-w");
    return f;
  };
  const more = (n: number, cls: string) => n > 0 && h("span", { class: `x ${cls}` }, `+${n}`);
  return h("span", { class: "a-fls", "aria-hidden": "true" }, ...ccs.slice(0, 8).map(one), more(ccs.length - 8, "only-w"), more(ccs.length - 4, "only-m"));
}

/** The form: what device, its name, the version of the app (when the profiles differ), the key to make and the servers it holds. */
export function keyBody(c: AmzCtx): HTMLElement {
  const { d, s, a, t } = c;
  const app = keyAppName(d, null);
  const f = s.amz.form;
  const busy = s.amz.busy === "add";
  const versions = keyVersions(d, s.lang);
  const cur = versions.find((v) => v.sets.some((x) => x.id === f.profile)) ?? versions[0];
  const set = cur?.sets.find((x) => x.id === f.profile) ?? cur?.sets[0];
  const err = s.amz.error && s.amz.errorAt === "add" ? s.amz.error : "";
  const word = (v: KeyVersion) => (v.kind === "old" ? t.adVerOld : t.adVerNew);
  const badge = (v: KeyVersion) => h("span", { class: `pr${v.kind === "new" ? " new" : ""}` }, `AWG ${v.awg}`);
  // the servers of a key said in a line; a key no server is known on says its countries
  const about = (x: KeySet) => (x.servers.length > 0 ? t.adSetD(x.servers.length, x.countries.length, x.servers[0]!.country) : x.countries.length > 0 ? t.adInclT(x.countries.length) : "");

  const ver = (v: KeyVersion) => {
    const on = v === cur;
    return h(
      "button",
      { class: `a-ver${on ? " on" : ""}`, type: "button", role: "radio", "aria-checked": on, "data-k": `amz-ver-${v.kind}`, on: { click: () => a.amz.form({ profile: on ? f.profile : v.sets[0]!.id }, true) } },
      h("span", { class: "a-rd" }),
      h("span", { class: "v" }, word(v), badge(v)),
      h("span", { class: "d" }, v.kind === "old" ? t.adVerOldD : t.adVerNewD),
    );
  };
  const versionField =
    cur &&
    h(
      "div",
      { class: "a-fld" },
      h("p", { class: "a-lbl", id: "nd-v" }, t.adVerT(app), h("span", null, t.adVerH)),
      versions.length > 1
        ? h("div", { class: "a-vers", role: "radiogroup", "aria-labelledby": "nd-v" }, ...versions.map(ver))
        : h("p", { class: "a-vone", role: "group", "aria-labelledby": "nd-v" }, h("span", { class: "v" }, word(cur)), badge(cur), h("span", { class: "d" }, cur.kind === "old" ? t.adVerOldD : t.adVerOnly)),
    );

  const multi = !!cur && cur.sets.length > 1;
  const card = (x: KeySet) => {
    const on = x.id === set?.id;
    const tag = x.warp && h("span", { class: "u-tag warp" }, t.adVerWarp);
    const body = [h("span", { class: "tx" }, h("span", { class: "t" }, multi ? x.name || about(x) : about(x), tag), multi && h("span", { class: "d" }, about(x))), flagRow(c, x.countries)];
    return multi
      ? h("button", { class: `a-set${on ? " on" : ""}`, type: "button", role: "radio", "aria-checked": on, "data-k": `amz-set-${x.id}`, on: { click: () => a.amz.form({ profile: x.id }, true) } }, h("span", { class: "a-rd" }), ...body)
      : h("div", { class: "a-set" }, ...body);
  };
  const more = !!set && set.servers.length > 0;
  const setsField =
    cur &&
    set &&
    (multi || set.countries.length > 0) &&
    h(
      "div",
      { class: "a-fld" },
      h(
        "p",
        { class: "a-lbl", id: "nd-s" },
        multi ? t.adSetsT : t.adSrvT,
        more && h("button", { class: `a-lnk a-more${f.list ? " on" : ""}`, type: "button", "aria-expanded": f.list, "data-k": "amz-srv-more", on: { click: () => a.amz.form({ list: !f.list }, true) } }, f.list ? t.adLess : t.adMore, icon("chev")),
      ),
      h("div", { class: `a-sets${multi ? "" : " solo"}`, role: multi ? "radiogroup" : "group", "aria-labelledby": "nd-s" }, ...cur.sets.map(card)),
      more &&
        f.list &&
        h(
          "ul",
          { class: "a-srv" },
          ...set.servers.map((x) => h("li", null, flag(c, { country_code: x.cc, place: "", label: x.country }, true), h("span", { class: "nm" }, h("bdi", null, x.country), x.mark && h("span", { class: "id" }, x.mark)), x.host && h("span", { class: "dom" }, x.host))),
        ),
    );
  return h(
    "form",
    {
      class: "a-b",
      id: "a-key-form",
      on: {
        submit: (e) => {
          e.preventDefault();
          if (!busy) a.amz.create();
        },
      },
    },
    h(
      "div",
      { class: "a-fld" },
      h("p", { class: "a-lbl", id: "nd-p" }, t.awgPlatform),
      h("div", { class: "a-seg", role: "radiogroup", "aria-labelledby": "nd-p" }, ...addPlatforms.map((p) => chip({ ico: p === "other" ? "router" : deviceIcon(p), label: t.platforms[p], on: f.platform === p, k: `amz-platform-${p}`, click: () => a.amz.form({ platform: p }, true) }))),
    ),
    h(
      "div",
      { class: "a-fld" },
      h("label", { class: "a-lbl", for: "a-nm" }, t.awgName, h("span", null, t.optional)),
      h("input", { class: "a-inp", id: "a-nm", "data-k": "amz-label", type: "text", dir: "auto", maxlength: 40, autocomplete: "off", placeholder: t.awgNamePh[f.platform as keyof typeof t.awgNamePh], value: f.label, on: { input: (e) => a.amz.form({ label: (e.target as HTMLInputElement).value }) } }),
    ),
    versionField,
    setsField,
    err && h("p", { class: "a-err", role: "alert" }, icon("warn"), t.err(err, s.amz.retryMin)),
  );
}

export function keyFoot(c: AmzCtx): HTMLElement {
  const { s, a, t } = c;
  const busy = s.amz.busy === "add";
  return h(
    "footer",
    { class: "a-f" },
    h("button", { class: "u-btn pri", type: "button", "data-k": "modal-create", disabled: busy, on: { click: () => !busy && a.amz.create() } }, busy ? h("span", { class: "spin", "aria-hidden": "true" }) : icon("key"), busy ? t.awgBusy : t.awgCreate),
  );
}

// ---- step "ready": the key of one device ----

export type KeyMode = "shown" | "created" | "renew";

/** The device whose key the window shows: the new key of a stale one, the one just made, or the one asked for ("Show key"). */
export function shownKey(c: Pick<AmzCtx, "d" | "s">): { x: AwgDevice; mode: KeyMode } | null {
  const { d, s } = c;
  const find = (id: string | null) => (id ? d.amnezia?.devices.find((x) => x.id === id) : undefined);
  const r = find(s.amz.renew);
  if (r) return { x: r, mode: "renew" };
  const m = find(s.amz.created);
  if (m) return { x: m, mode: "created" };
  const o = find(s.amz.open);
  return o ? { x: o, mode: "shown" } : null;
}

const metaRow = (ico: IconName, ...kids: Kid[]) => h("p", null, icon(ico), h("span", null, ...kids));

/**
 * The key: which server of the key (when it holds several), the three ways to take it (the file, the text of the file, the key
 * line) beside the code a phone scans, and what the person must know. The same for every device and every place it is added.
 */
export function readyBody(c: AmzCtx, x: AwgDevice): HTMLElement {
  const { d, s, a, t } = c;
  const app = keyAppName(d, x.platform as Platform);
  const list = s.amz.configs[x.id];
  if (!list || list.length === 0) return h("div", { class: "a-b" }, h("p", { class: "u-hint" }, list ? t.noProfile : t.awgBusy));

  const i = Math.min(s.amz.node[x.id] ?? 0, list.length - 1);
  const cfg = list[i]!;
  const filename = cfg.filename || "amnezia.conf";

  // one chip a server of the key: its flag, its country and what tells it from the others of its country (the Servers table's mark);
  // a path through an entry or the IPv6 one is a connection of its own, named as the page names it
  const all = serversOf(d);
  const chipOf = (l: AwgConfig, k: number) => {
    const srv = l.via_node_id || l.ipv6 ? undefined : all.find((y) => y.id === l.node_id);
    const mark = srv ? serverMark(all, srv) : "";
    return chip({
      pre: flag({ t, s }, srv ?? { country_code: l.country_code, place: "", label: l.label }, true),
      label: srv ? serverName(srv, s.lang).country : l.label || String(k + 1),
      post: mark && h("span", { class: "id" }, mark),
      on: i === k,
      k: `amz-node-${x.id}-${k}`,
      click: () => a.amz.node(x.id, k),
    });
  };
  const servers =
    list.length > 1 &&
    h(
      "div",
      { class: "a-fld" },
      h("p", { class: "a-lbl", id: `k-c-${x.id}` }, t.adSrvL, h("span", null, t.adKeyHas(list.length))),
      h("div", { class: "a-seg a-rsv", role: "radiogroup", "aria-labelledby": `k-c-${x.id}` }, ...list.map(chipOf)),
    );

  // the board's .a-act: a tile with the icon, the name of the way and a line under it
  const act = (ico: IconName, k: string, title: string, line: Kid[], click: (btn: HTMLButtonElement) => void) => {
    const btn = h("button", { class: "a-act", type: "button", "data-k": k }, h("span", { class: "ic", "aria-hidden": "true" }, icon(ico)), h("span", { class: "tx" }, h("span", { class: "t" }, title), h("span", { class: "d" }, ...line)));
    btn.addEventListener("click", () => click(btn));
    return btn;
  };
  // "The line vpn:// for AmneziaVPN": the scheme in the file's own face, wherever the language puts it
  const [lead, tail] = t.adActKeyD(app).split("vpn://");
  const acts = h(
    "div",
    { class: "a-acts3" },
    act("file", `amz-dl-${x.id}`, t.adActFile, [h("span", { class: "m" }, filename)], () => a.download(filename, cfg.conf)),
    act("copy", `amz-conf-${x.id}`, t.adActConf, [t.adActConfD], (btn) => a.copy(cfg.conf, t.confCopied, undefined, btn)),
    act("key", `amz-key-${x.id}`, t.copyKey, [lead ?? "", h("span", { class: "m" }, "vpn://"), tail ?? ""], (btn) => a.copy(cfg.vpn_key, t.keyCopied, undefined, btn)),
  );
  const qr = qrCard(cfg.qr, t.qrKeyAlt(app), qrLogoSrc(d));

  const meta: Kid[] = [metaRow("shield", t.adSecret)];
  const versions = versionsFor(x.platform, cfg.min_clients.length ? cfg.min_clients : x.min_clients);
  if (cfg.version === "2.0") meta.push(metaRow("info", t.adNeedOld(app)));
  else if (versions.length > 0) meta.push(metaRow("info", t.needApp(versions)));
  // the key through an entry is another server in the app: said once, where it is picked
  if (cfg.via_node_id || cfg.ipv6) meta.push(metaRow("info", t.viaKeyNote(cfg.via_node_id ? t.chipVia(cfg.via_country) : "IPv6", app)));
  if (!qr) meta.push(metaRow("info", t.qrTooBig));
  return h(
    "div",
    { class: "a-b" },
    servers,
    qr ? h("div", { class: "a-two a-got" }, acts, h("div", { class: "a-side" }, qr, h("p", { class: "u-hint" }, t.adScan(app)))) : acts,
    h("div", { class: "a-meta" }, ...meta),
  );
}

export const readyFoot = (c: AmzCtx, mode: KeyMode): HTMLElement =>
  h("footer", { class: "a-f" }, h("p", { class: "u-hint" }, mode === "renew" ? c.t.renewS : c.t.keyAgain), h("button", { class: "u-btn", type: "button", "data-k": "modal-done", on: { click: () => c.a.amz.add(false) } }, c.t.awgDone));

