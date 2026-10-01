// Turns the JSON Schema a protocol plugin publishes into a flat, ordered list of form fields, and holds the small
// helpers the editor needs to read, write and compare values by path. The vocabulary it understands is listed in
// proto/mistgate/admin/v1/profile.proto ("SCHEMA VOCABULARY"); anything else in a schema is ignored.
//
// Nothing here knows a protocol: a second plugin shows up in the form with no change to this file or the UI.

import { key32Fields, randomKey32 } from "@/lib/awg";
import { generateSecret } from "@/lib/secret";

export type Choice ={ value: string; label: string; raw: unknown };

export type FieldKind = "text" | "number" | "toggle" | "choice" | "range" | "generate" | "secret";

export type Field = {
  /** JSON pointer of the field ("/obfs/type"); a port range is the pointer of its object ("/hop"). */
  key: string;
  path: string[];
  /** The path with dots ("obfs.type"): what the translation keys use. */
  id: string;
  kind: FieldKind;
  title: string;
  hint: string;
  unit: string;
  integer: boolean;
  choices: Choice[];
  /** A choice rendered as a segmented control (widget "segmented", or few and short options). */
  segmented: boolean;
  /** Port range: the paths of its two ends. */
  parts: [string[], string[]] | null;
  critical: boolean;
  secret: boolean;
};

export type Group = { id: string; fields: Field[] };

type Node = {
  type?: string;
  title?: string;
  description?: string;
  enum?: unknown[];
  properties?: Record<string, Node>;
  "x-unit"?: string;
  "x-group"?: string;
  "x-widget"?: string;
  "x-secret"?: boolean;
  "x-critical"?: boolean;
  "x-order"?: number;
  "x-enum-labels"?: Record<string, string>;
};

type Ctx = { group?: string; critical?: boolean; parent?: Node };

const ordered = (props: Record<string, Node>): [string, Node][] =>
  Object.entries(props)
    .map(([k, n], i) => ({ k, n, o: typeof n["x-order"] === "number" ? n["x-order"] : 1e6 + i }))
    .sort((a, b) => a.o - b.o)
    .map(({ k, n }) => [k, n]);

export const pointerOf = (path: string[]) => "/" + path.map((p) => p.replaceAll("~", "~0").replaceAll("/", "~1")).join("/");

function collect(node: Node, path: string[], ctx: Ctx, out: Array<Field & { group: string }>) {
  const group = node["x-group"] ?? ctx.group;
  const critical = node["x-critical"] ?? ctx.critical ?? false;
  const widget = node["x-widget"];

  if (node.type === "object" && node.properties) {
    if (widget === "port-range") {
      const ends = ordered(node.properties).slice(0, 2);
      if (ends.length < 2) return;
      out.push({
        ...leaf(node, path, ctx, "range"),
        integer: true,
        parts: [
          [...path, ends[0]![0]],
          [...path, ends[1]![0]],
        ],
        critical,
        group: group ?? "advanced",
      });
      return;
    }
    // An object is a bundle of fields. One that holds a single field lends its title and description to it
    // ("Masquerade" > "Type" is shown as "Masquerade"); several fields keep their own names.
    const kids = ordered(node.properties);
    for (const [k, child] of kids) collect(child, [...path, k], { group, critical, parent: kids.length === 1 ? node : undefined }, out);
    return;
  }

  let kind: FieldKind;
  if (Array.isArray(node.enum) && node.enum.length > 0) kind = "choice";
  else if (node.type === "boolean") kind = "toggle";
  else if (node.type === "integer" || node.type === "number") kind = "number";
  else if (node.type === "string") kind = node["x-secret"] ? (widget === "generate" ? "generate" : "secret") : "text";
  else return; // arrays, null, untyped: not something this form can edit
  out.push({ ...leaf(node, path, ctx, kind), critical, group: group ?? "advanced" });
}

function leaf(node: Node, path: string[], ctx: Ctx, kind: FieldKind): Field {
  const labels = node["x-enum-labels"] ?? {};
  const choices: Choice[] = (node.enum ?? []).map((raw) => ({ value: String(raw), label: labels[String(raw)] ?? String(raw), raw }));
  return {
    key: pointerOf(path),
    path,
    id: path.join("."),
    kind,
    title: ctx.parent?.title ?? node.title ?? path[path.length - 1] ?? "",
    hint: node.description ?? ctx.parent?.description ?? "",
    unit: node["x-unit"] ?? "",
    integer: node.type === "integer",
    choices,
    segmented: node["x-widget"] === "segmented" || (choices.length > 0 && choices.length <= 3 && choices.every((c) => c.label.length <= 22)),
    parts: null,
    critical: false,
    secret: node["x-secret"] === true,
  };
}

const groupOrder = ["basics", "obfuscation", "advanced"];

/** The form: fields in schema order, gathered into groups (basics, obfuscation, advanced, then any other). */
export function parseSchema(schemaJson: string): Group[] {
  let root: Node;
  try {
    root = JSON.parse(schemaJson) as Node;
  } catch {
    return [];
  }
  if (!root || typeof root !== "object" || !root.properties) return [];
  const flat: Array<Field & { group: string }> = [];
  for (const [k, n] of ordered(root.properties)) collect(n, [k], {}, flat);

  const byId = new Map<string, Field[]>();
  for (const { group, ...f } of flat) byId.set(group, [...(byId.get(group) ?? []), f]);
  const rank = (id: string) => (groupOrder.includes(id) ? groupOrder.indexOf(id) : groupOrder.length);
  return [...byId.entries()].sort((a, b) => rank(a[0]) - rank(b[0])).map(([id, fields]) => ({ id, fields }));
}

export const allFields = (groups: Group[]) => groups.flatMap((g) => g.fields);

// ---- values by path ----

export type Settings = Record<string, unknown>;

export function getAt(obj: unknown, path: string[]): unknown {
  let cur = obj;
  for (const p of path) {
    if (cur === null || typeof cur !== "object") return undefined;
    cur = (cur as Record<string, unknown>)[p];
  }
  return cur;
}

/** A copy of `obj` with `value` at `path` (objects on the way are created when missing). */
export function setAt(obj: Settings, path: string[], value: unknown): Settings {
  const [head, ...rest] = path;
  if (head === undefined) return obj;
  if (rest.length === 0) return { ...obj, [head]: value };
  const child = obj[head];
  return { ...obj, [head]: setAt(child && typeof child === "object" ? (child as Settings) : {}, rest, value) };
}

const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b);

export function isChanged(f: Field, cur: Settings, base: Settings): boolean {
  if (f.parts) return f.parts.some((p) => !same(getAt(cur, p), getAt(base, p)));
  return !same(getAt(cur, f.path), getAt(base, f.path));
}

export const changedFields = (fields: Field[], cur: Settings, base: Settings) => fields.filter((f) => isChanged(f, cur, base));

/** The problems that belong to `f`: an error on the field itself or, for a port range, on one of its ends. */
export function errorsOf<E extends { pointer: string }>(f: Field, errors: readonly E[]): E[] {
  return errors.filter((e) => e.pointer === f.key || e.pointer.startsWith(f.key + "/"));
}

export function parseSettings(json: string): Settings {
  try {
    const v = JSON.parse(json || "{}") as unknown;
    return v && typeof v === "object" && !Array.isArray(v) ? (v as Settings) : {};
  } catch {
    return {};
  }
}

// ---- secrets ----

// The constants the API uses for a secret (profile.proto): "a secret exists, keep it" and "make a new one".
export const MASK = "••••";
export const GENERATE = "$generate";

/**
 * Settings for a new profile with every secret that came masked replaced by a fresh random value. The panel
 * never reveals a stored secret, so the form makes the value itself: it is visible (eye) and copyable before the
 * profile is saved, and the field is never empty. `make` is injected so a test can be exact.
 */
export function withGeneratedSecrets(settings: Settings, fields: Field[], make: (f: Field) => string): Settings {
  let out = settings;
  for (const f of fields) {
    if (!f.secret) continue;
    const cur = getAt(out, f.path);
    if (cur === undefined || cur === null || cur === "" || cur === MASK || cur === GENERATE) out = setAt(out, f.path, make(f));
  }
  return out;
}

/** A fresh value for a secret field: a 32-byte base64 key where the protocol needs one, else a random password. */
export const makeSecret = (f: Field) => (key32Fields.has(f.id) ? randomKey32() : generateSecret());
