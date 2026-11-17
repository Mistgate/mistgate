// The new-profile wizard's model (PnProfileNew): the tasks of step 1 and what each starts from, the name a profile gets, and the
// rows of "All settings". Pure functions over the protocol's own schema and settings, so a protocol the wizard does not know still
// works through "I'll pick the protocol myself".

import type { ProtocolInfo } from "@/gen/mistgate/admin/v1/profile_pb";
import type { Tx } from "@/screens/users/t";
import { allFields, getAt, makeSecret, parseSchema, parseSettings, setAt, withGeneratedSecrets, type Field, type Settings } from "./schema";
import { vlessEdited } from "./vless-form";

export type TaskId = "fast" | "phone" | "router" | "cdn" | "tg" | "tt" | "own";
export type Tone = "hy" | "vl" | "aw" | "tg" | "tt" | "";

/** The board's 24-grid glyphs of the protocols (the same paths the board draws). */
export const glyphs = {
  hy: "M13 3L5 14h6l-1 7 8-11h-6z",
  vl: "M12 21a9 9 0 1 0 0-18 9 9 0 0 0 0 18zM3 12h18M12 3c2.5 2.7 2.5 15.3 0 18M12 3c-2.5 2.7-2.5 15.3 0 18",
  aw: "M4 15.5a3.5 3.5 0 1 0 7 0 3.5 3.5 0 0 0-7 0zM11 15.5h9M17 15.5v-3",
  tg: "M20 4L3 11l6 2.5L11 20l3-4.5 4 3z",
  tt: "M4.5 19v-6.5a7.5 7.5 0 0 1 15 0V19M9.5 19v-6a2.5 2.5 0 0 1 5 0v6",
  own: "M12 3l9 5-9 5-9-5zM3 13l9 5 9-5M3 17.5l9 5 9-5",
} as const;

/** The apps of a task's card, the board's letters: a round mark each, the name in its tooltip (brand names, not translated). */
export const appNames: Record<string, string> = { H: "Happ", K: "Klick", M: "Mihomo", S: "sing-box", V: "v2rayNG", A: "AmneziaVPN", T: "Telegram", R: "TrustTunnel", G: "Mistgate" };

export type Task = {
  id: TaskId;
  /** The protocol the task makes ("" for "I'll pick it myself"). */
  protocol: string;
  tone: Tone;
  glyph: string;
  /** The letters of the apps that reach people with it. */
  apps: string;
  /** The one quiet word at the card's foot: its tone ("ok" accent, "part" warning, "" plain). */
  mark?: { tone: "ok" | "part" | ""; key: "wiz.mark.start" | "wiz.mark.cdn" | "wiz.mark.exp" };
};

export const tasks: Task[] = [
  { id: "fast", protocol: "hysteria2", tone: "hy", glyph: glyphs.hy, apps: "HKMS", mark: { tone: "ok", key: "wiz.mark.start" } },
  { id: "phone", protocol: "vless", tone: "vl", glyph: glyphs.vl, apps: "HKMSV" },
  { id: "router", protocol: "awg", tone: "aw", glyph: glyphs.aw, apps: "AK" },
  { id: "cdn", protocol: "vless", tone: "vl", glyph: glyphs.vl, apps: "HKMV", mark: { tone: "", key: "wiz.mark.cdn" } },
  { id: "tg", protocol: "mtproto", tone: "tg", glyph: glyphs.tg, apps: "T", mark: { tone: "part", key: "wiz.mark.exp" } },
  { id: "tt", protocol: "trusttunnel", tone: "tt", glyph: glyphs.tt, apps: "RG", mark: { tone: "part", key: "wiz.mark.exp" } },
  { id: "own", protocol: "", tone: "", glyph: glyphs.own, apps: "" },
];

export const taskById = (id: TaskId): Task => tasks.find((x) => x.id === id) ?? tasks[0]!;

/** The task that makes a protocol (?protocol=vless opens the wizard on its settings): a protocol with no task of its own is picked by hand. */
export const taskOfProtocol = (protocol: string): TaskId => tasks.find((x) => x.protocol === protocol && x.id !== "cdn")?.id ?? "own";

/** The tone and glyph of a protocol (the "I'll pick it myself" task wears the one of the protocol it picked). */
export const toneOf = (protocol: string): Tone => (tasks.find((x) => x.protocol === protocol)?.tone ?? "");
export const glyphOf = (protocol: string): string => tasks.find((x) => x.protocol === protocol)?.glyph ?? glyphs.own;

/** The network of a protocol's port, for "UDP port" / "TCP port" (the schema's own title says it, the board says it in capitals). */
export const netOf = (protocol: string): "udp" | "tcp" => (protocol === "hysteria2" || protocol === "awg" ? "udp" : "tcp");

/** A new profile starts from the plugin defaults with every secret made here; AmneziaWG leaves its client networks to the server (a free pair). */
export function freshSettings(info: Pick<ProtocolInfo, "id" | "settingsSchemaJson" | "defaultSettingsJson">): Settings {
  const fields = allFields(parseSchema(info.settingsSchemaJson));
  const settings = withGeneratedSecrets(parseSettings(info.defaultSettingsJson), fields, makeSecret);
  if (info.id === "awg") {
    delete settings.subnet4;
    delete settings.subnet6;
  }
  return settings;
}

/** What a task starts from: the protocol's defaults, and for "through a CDN" VLESS behind TLS with the CDN's host. */
export function taskSettings(task: TaskId, info: Pick<ProtocolInfo, "id" | "settingsSchemaJson" | "defaultSettingsJson">, cdnHost = ""): Settings {
  const fresh = freshSettings(info);
  if (task !== "cdn") return fresh;
  return withCdnHost(vlessEdited(fresh, setAt(fresh, ["security"], "tls")), cdnHost);
}

/** The CDN host typed into the task's field (vlessEdited makes the port and the XHTTP mode the first time one is there). */
export function withCdnHost(settings: Settings, host: string): Settings {
  return vlessEdited(settings, setAt(settings, ["cdn", "host"], host.trim()));
}

/** The name a profile gets until one is typed: the board's own words, with the port after them (AmneziaWG: its version). */
export function autoNameOf(task: TaskId, info: Pick<ProtocolInfo, "id" | "displayName">, settings: Settings, old: boolean): string {
  const port = getAt(settings, ["port"]);
  const withPort = (base: string) => (typeof port === "number" && port > 0 ? `${base} · ${port}` : base);
  if (task === "own") return withPort(info.displayName);
  if (info.id === "awg") return old ? "AWG 2.0" : "AWG 3.1";
  if (task === "cdn") return withPort("VLESS · CDN");
  if (info.id === "mtproto") return withPort("Telegram");
  return withPort(info.displayName);
}

export const portValid = (v: string) => /^\d{1,5}$/.test(v) && Number(v) >= 1 && Number(v) <= 65535;

/** The settings the summary of "All settings" reads, per protocol: schema field ids, in the order the board lists them. */
const summaryIds: Record<string, string[]> = {
  hysteria2: ["tls_mode", "obfs.type", "hop", "masquerade.type", "egress"],
  vless: ["security", "transport", "reality.target_kind", "reality.fingerprint", "tls_mode", "egress"],
  awg: ["version", "obfuscation.preset", "obfuscation.per_device_signature", "mtu"],
  mtproto: ["fake_tls.kind", "egress"],
  trusttunnel: ["tls_mode", "h3", "sni", "egress"],
};

/** One value of a field as a short word: the choice's label, "on"/"off", the number or text, a range, "generated" for a secret. */
function valueOf(f: Field, settings: Settings, protocol: string, t: Tx): string {
  const raw = f.kind === "range" && f.parts ? undefined : getAt(settings, f.path);
  const copy = (part: string) => t.opt(`profiles.f.${protocol}.${f.id}.${part}`);
  if (f.kind === "range" && f.parts) {
    const from = getAt(settings, f.parts[0]);
    const to = getAt(settings, f.parts[1]);
    return from && to ? `${String(from)}-${String(to)}` : t("wiz.off");
  }
  if (f.kind === "toggle") return raw ? t("wiz.on") : t("wiz.off");
  if (f.kind === "choice") {
    const c = f.choices.find((x) => x.raw === raw || x.value === String(raw));
    return c ? (copy(`enum.${c.value}`) ?? c.label) : String(raw ?? "—");
  }
  if (f.kind === "secret" || f.kind === "generate") return raw ? t("wiz.generated") : "—";
  if (f.kind === "list") return Array.isArray(raw) && raw.length > 0 ? String(raw.length) : "—";
  return raw === undefined || raw === "" || raw === null ? "—" : String(raw);
}

/** The rows of "All settings": what the task set, as the protocol's own fields say it (their titles and labels are the editor's). */
export function summaryRows(info: Pick<ProtocolInfo, "id" | "settingsSchemaJson">, settings: Settings, t: Tx): { id: string; label: string; value: string }[] {
  const fields = allFields(parseSchema(info.settingsSchemaJson));
  const ids = summaryIds[info.id] ?? fields.slice(0, 5).map((f) => f.id);
  return ids.flatMap((id) => {
    const f = fields.find((x) => x.id === id);
    if (!f) return [];
    // a field the settings do not use (a CDN-less VLESS has no certificate mode) is not listed
    if (f.kind !== "toggle" && f.kind !== "range" && getAt(settings, f.path) === undefined) return [];
    return [{ id, label: t.opt(`profiles.f.${info.id}.${f.id}.title`) ?? f.title, value: valueOf(f, settings, info.id, t) }];
  });
}
