// Small pure helpers of the AmneziaWG screens (profile editor, devices): what the server does not hand out as data.
// The tables mirror Go (internal/panel/protocols/awg): keep them in step when those change. (Warnings and the score are not
// mirrored: the server hands them out with every preview.)

export type AwgVersion = "3.1" | "2.0";
export const versions: AwgVersion[] = ["3.1", "2.0"];

/** Minimum client releases per protocol version (minclient.go: repository tags, not store versions). */
export const minClients: Record<AwgVersion, { app: string; min: string }[]> = {
  "3.1": [
    { app: "AmneziaVPN", min: "5.0.1.5" },
    { app: "AmneziaWG Android", min: "v3.1.20260814" },
    { app: "AmneziaWG Windows", min: "3.1.0" },
    { app: "AmneziaWG Apple", min: "v3.1.3" },
    { app: "Mihomo", min: "v1.19.30" },
  ],
  "2.0": [
    { app: "AmneziaVPN", min: "4.8.12.9" },
    { app: "AmneziaWG", min: "2.0.0" },
    { app: "Mihomo", min: "v1.19.14" },
  ],
};

export const asVersion = (v: unknown): AwgVersion => (v === "2.0" ? "2.0" : "3.1");

/** Platforms the "add a device" dialogs offer (device.proto: CreateAwgDeviceRequest.platform). */
export const platforms = ["ios", "android", "windows", "macos", "linux", "other"] as const;
export type DevicePlatform = (typeof platforms)[number];

/** The platform a browser runs on, for a sensible default in "add a device". */
export function guessPlatform(ua: string, touchPoints = 0): DevicePlatform {
  if (/iPhone|iPad|iPod/i.test(ua)) return "ios";
  if (/Android/i.test(ua)) return "android";
  if (/Windows/i.test(ua)) return "windows";
  if (/Macintosh|Mac OS X/i.test(ua)) return touchPoints > 1 ? "ios" : "macos";
  if (/Linux|X11|CrOS/i.test(ua)) return "linux";
  return "other";
}

// ---- values by path (the obfuscation block) ----

type Json = Record<string, unknown>;
const isObj = (v: unknown): v is Json => !!v && typeof v === "object" && !Array.isArray(v);

/** "jc" -> value for every leaf of an object, with dotted paths. */
export function leaves(obj: unknown, prefix = ""): Map<string, unknown> {
  const out = new Map<string, unknown>();
  if (!isObj(obj)) return out;
  for (const [k, v] of Object.entries(obj)) {
    if (isObj(v)) for (const [p, x] of leaves(v, `${prefix}${k}.`)) out.set(p, x);
    else out.set(`${prefix}${k}`, v);
  }
  return out;
}

/** How many fields of the obfuscation block differ between two versions of it (the "N changed" after Generate). */
export function changedCount(before: unknown, after: unknown): number {
  const a = leaves(before);
  const b = leaves(after);
  let n = 0;
  for (const k of new Set([...a.keys(), ...b.keys()])) if (JSON.stringify(a.get(k) ?? "") !== JSON.stringify(b.get(k) ?? "")) n++;
  return n;
}

// ---- import of a client .conf ----

/** The 3.x-only keys of the obfuscation block: a 2.0 profile refuses them. */
export const v31Only = [
  "header_protection_key", "random_trailers", "disable_cookies", "content_padding_addition",
  "rekey_after_time", "rekey_timeout", "reject_after_time", "keepalive_timeout", "max_handshake_attempts",
] as const;

export type ConfKind = "3.1" | "2.0" | "1.5" | "1.0" | "wg" | "none";

export type ConfImport = {
  kind: ConfKind;
  /** The profile version the form switches to; null when the text holds nothing of AmneziaWG. */
  version: AwgVersion | null;
  mtu?: number;
  /** Keys of the profile's `obfuscation` block (not the whole block: the rest of the form stays). */
  obfuscation: Record<string, unknown>;
  /** Names of the .conf keys that went into the form, of those it knew but did not take, and of the strangers. */
  filled: string[];
  ignored: string[];
  unknown: string[];
  /** "custom": the packets came from the file, so the look is "custom"; "legacy": a 1.x file has no S3 and S4. */
  notes: ("custom" | "legacy")[];
};

type ConfKey = { to: string; as: "int" | "str" | "bool" | "h" };
const confKeys: Record<string, ConfKey> = Object.fromEntries(
  [
    ...["Jc", "Jmin", "Jmax", "S1", "S2", "S3", "S4"].map((k) => [k, { to: k.toLowerCase(), as: "int" }]),
    ...["H1", "H2", "H3", "H4"].map((k) => [k, { to: k.toLowerCase(), as: "h" }]),
    ...["I1", "I2", "I3", "I4", "I5"].map((k) => [k, { to: k.toLowerCase(), as: "str" }]),
    ["HeaderProtectionKey", { to: "header_protection_key", as: "str" }],
    ["RandomTrailers", { to: "random_trailers", as: "bool" }],
    ["ContentPaddingAddition", { to: "content_padding_addition", as: "str" }],
    ["RekeyAfterTime", { to: "rekey_after_time", as: "str" }],
    ["RekeyTimeout", { to: "rekey_timeout", as: "str" }],
    ["RejectAfterTime", { to: "reject_after_time", as: "str" }],
    ["KeepaliveTimeout", { to: "keepalive_timeout", as: "str" }],
    ["MaxHandshakeAttempts", { to: "max_handshake_attempts", as: "str" }],
    ["PersistentKeepalive", { to: "persistent_keepalive", as: "str" }],
  ].map(([k, v]) => [(k as string).toLowerCase(), { ...(v as ConfKey), name: k }]),
);
// Known keys the profile does not take: per-device, per-server or secret (never read into the result).
// DisableCookies is the client's own switch here; the profile's one is the node's.
const skipKeys = ["privatekey", "publickey", "presharedkey", "address", "dns", "allowedips", "endpoint", "listenport", "table", "fwmark", "preup", "postup", "predown", "postdown", "saveconfig", "disablecookies"];
const v31Keys = new Set(["headerprotectionkey", "randomtrailers", "contentpaddingaddition", "rekeyaftertime", "rekeytimeout", "rejectaftertime", "keepalivetimeout", "maxhandshakeattempts", "disablecookies"]);
const canon = (k: string) => confKeys[k] as (ConfKey & { name: string }) | undefined;

/**
 * Reads an AmneziaWG client .conf (AmneziaVPN, awg-multi-script, ...) the way the editor needs it: the version by the keys
 * present (3.x keys > S3/S4 or an H range > I1-I5 > the old five > plain WireGuard), the MTU and the obfuscation keys.
 * Nothing leaves the browser, and the key material of the file (PrivateKey, PresharedKey) is skipped, not copied.
 */
export function parseAwgConf(text: string): ConfImport {
  const out: ConfImport = { kind: "none", version: null, obfuscation: {}, filled: [], ignored: [], unknown: [], notes: [] };
  const seen = new Set<string>();
  let wgish = false;
  for (const raw of text.split(/\r?\n/)) {
    // wg-quick drops everything after a "#" on any line (base64 keys and CPS tags never hold one)
    const line = raw.split("#", 1)[0]!.trim();
    if (!line || line[0] === ";") continue;
    if (/^\[(interface|peer)\]$/i.test(line)) {
      wgish = true;
      continue;
    }
    const eq = line.indexOf("=");
    if (eq < 1) continue;
    const key = line.slice(0, eq).trim().toLowerCase();
    const val = line.slice(eq + 1).trim();
    const k = canon(key);
    if (key === "mtu") {
      if (/^\d+$/.test(val)) {
        out.mtu = Number(val);
        out.filled.push("MTU");
      } else out.ignored.push("MTU");
      continue;
    }
    if (skipKeys.includes(key)) {
      wgish = true;
      if (v31Keys.has(key)) seen.add(key);
      out.ignored.push(line.slice(0, eq).trim());
      continue;
    }
    if (!k) {
      out.unknown.push(line.slice(0, eq).trim().slice(0, 40));
      continue;
    }
    seen.add(key);
    const v: unknown =
      k.as === "int" ? (/^\d+$/.test(val) ? Number(val) : undefined)
      : k.as === "h" ? (/^\d{1,10}(-\d{1,10})?$/.test(val) ? val : undefined)
      : k.as === "bool" ? ({ on: true, true: true, 1: true, off: false, false: false, 0: false } as Record<string, boolean>)[val.toLowerCase()]
      : val;
    if (v === undefined) out.ignored.push(k.name);
    else {
      out.obfuscation[k.to] = v;
      out.filled.push(k.name);
    }
  }
  const has = (...ks: string[]) => ks.some((k) => seen.has(k));
  if ([...seen].some((k) => v31Keys.has(k))) out.kind = "3.1";
  else if (has("s3", "s4") || ["h1", "h2", "h3", "h4"].some((h) => String(out.obfuscation[h] ?? "").includes("-"))) out.kind = "2.0";
  else if (has("i1", "i2", "i3", "i4", "i5")) out.kind = "1.5";
  else if (has("jc", "jmin", "jmax", "s1", "s2", "h1", "h2", "h3", "h4")) out.kind = "1.0";
  else if (wgish) out.kind = "wg";
  if (out.kind === "none") return { ...out, mtu: undefined, obfuscation: {}, filled: [], ignored: [] };
  if (out.kind === "wg") return { ...out, obfuscation: {}, filled: out.mtu ? ["MTU"] : [] };
  out.version = out.kind === "3.1" ? "3.1" : "2.0";
  // our own render omits RandomTrailers when it is off, so a 3.x file without the line means off, not "keep the form's value"
  // (the server would then send trailers the imported clients do not expect: no handshake). 2.0 has no such key and
  // the validator refuses it there, so nothing to say for 2.0.
  if (out.kind === "3.1" && !seen.has("randomtrailers")) out.obfuscation.random_trailers = false;
  if (has("i1", "i2", "i3", "i4", "i5")) {
    for (const i of ["i1", "i2", "i3", "i4", "i5"]) out.obfuscation[i] ??= "";
    out.obfuscation.preset = "custom";
    out.notes.push("custom");
  }
  if (out.kind === "1.5" || out.kind === "1.0") {
    out.obfuscation.s3 = 0;
    out.obfuscation.s4 = 0;
    out.notes.push("legacy");
  }
  return out;
}
// ---- generators ----

/** A random UDP port the way the panel picks one for a new profile: 10000-60000, not 51820 or 55424. */
export function randomPort(rand: (n: number) => number = (n) => crypto.getRandomValues(new Uint32Array(1))[0]! % n): number {
  for (;;) {
    const p = 10000 + rand(50001);
    if (p !== 51820 && p !== 55424) return p;
  }
}

/** 32 random bytes in standard base64: the shape of the header protection key (validate.go: hpkBytes). */
export function randomKey32(): string {
  const b = crypto.getRandomValues(new Uint8Array(32));
  let bin = "";
  for (const x of b) bin += String.fromCharCode(x);
  return btoa(bin);
}

/** Settings fields that hold a 32-byte base64 key rather than a free-form password (id = dotted path). */
export const key32Fields = new Set(["obfuscation.header_protection_key"]);

// ---- devices ----

/** Saves text as a file in the browser (no server round trip: the config is already here). */
export function downloadText(filename: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: "text/plain" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  document.body.append(a);
  a.click();
  a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
