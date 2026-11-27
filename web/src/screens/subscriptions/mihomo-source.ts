const prefix = "# @mistgate-";
export const groupLimit = 100; // subsettings.maxHeaderGroups
const nonIDChar = /[^a-zA-Z0-9_-]/;

export type Group = { id: string; name: string };
export type SourceIssue = { kind: "syntax" | "unknown" | "inactive" | "header" | "selection" | "emptyLine"; line: number; group?: string };
type Result<T> = { value: T; issue?: never } | { value?: never; issue: SourceIssue };
type SourceLine = { start: number; body: string; eol: string; end: number };
type Block = { open: number; close: number; refs: string[]; json: string; before: string };
export type Source = { lines: SourceLine[]; blocks: Block[] };
export type SourceDraft = { text: string; names: ReadonlyMap<string, string> };

function sourceLines(text: string): SourceLine[] {
  const lines: SourceLine[] = [];
  for (let start = 0; start < text.length;) {
    const lf = text.indexOf("\n", start);
    const end = lf < 0 ? text.length : lf + 1;
    const eol = lf < 0 ? "" : text[lf - 1] === "\r" ? "\r\n" : "\n";
    lines.push({ start, body: text.slice(start, end - eol.length), eol, end });
    start = end;
  }
  return lines;
}

function jsonOf(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return undefined;
  }
}

function namesOf(value: unknown, allowEmpty: boolean): string[] | undefined {
  if (!Array.isArray(value) || (!allowEmpty && value.length === 0) || value.length > groupLimit || !value.every((v): v is string => typeof v === "string" && v !== "") || new Set(value).size !== value.length) return;
  return value;
}

export function parseSource(text: string, ids = false): Result<Source> {
  if (!text.startsWith(prefix) && !text.includes(`\n${prefix}`)) return { value: { lines: [], blocks: [] } };
  const lines = sourceLines(text);
  const blocks: Block[] = [];
  let pending: Omit<Block, "close"> | undefined;
  for (let i = 0; i < lines.length; i++) {
    const body = lines[i]!.body;
    if (!body.startsWith(prefix)) continue;
    const issue = { kind: "syntax", line: i + 1 } as const;
    if (/^# @mistgate-end[ \t]*$(?![\s\S])/.test(body)) {
      if (!pending) return { issue };
      blocks.push({ ...pending, close: i });
      pending = undefined;
      continue;
    }
    const token = /^(# @mistgate-if[ \t]+)(\[[^\n]*\][ \t]*)$(?![\s\S])/u.exec(body);
    if (!token || pending) return { issue };
    const refs = namesOf(jsonOf(token[2]!), false);
    if (!refs || (ids && refs.some((id) => id.length > 64 || nonIDChar.test(id)))) return { issue };
    pending = { open: i, refs, json: token[2]!, before: token[1]! };
  }
  return pending ? { issue: { kind: "syntax", line: pending.open + 1 } } : { value: { lines, blocks } };
}

/** Replace only the JSON strings on opening markers. Leave spacing and all other bytes as written. */
function rewrite(text: string, source: Source, remap: (name: string, line: number) => string | SourceIssue): Result<string> {
  let out = "";
  let at = 0;
  for (const block of source.blocks) {
    const mapped: string[] = [];
    for (const v of block.refs) {
      const to = remap(v, block.open + 1);
      if (typeof to !== "string") return { issue: to };
      mapped.push(to);
    }
    const line = source.lines[block.open]!;
    let j = 0;
    const json = block.json.replace(/"(?:[^"\\]|\\.)*"/gu, (literal) => {
      const i = j++;
      return mapped[i] === block.refs[i] ? literal : JSON.stringify(mapped[i]);
    });
    out += text.slice(at, line.start) + block.before + json;
    at = line.end - line.eol.length;
  }
  return { value: out + text.slice(at) };
}

/** Keep aliases already used in a draft. A new ID never inherits a stale alias. */
function groupAlias(names: Map<string, string>, id: string, groups: readonly Group[]): string {
  for (const [alias, bound] of names) if (id === bound) return alias;
  const name = groups.find((g) => g.id === id)?.name;
  const claimed = (s: string) => names.has(s) || groups.some((g) => g.id !== id && g.name === s);
  let alias = name && !claimed(name) ? name : `[${id}]`;
  while (claimed(alias)) alias = `[${alias}]`;
  names.set(alias, id);
  return alias;
}

export function displaySource(text: string, groups: readonly Group[]): SourceDraft {
  const names = new Map<string, string>();
  const source = parseSource(text, true);
  return { text: source.issue ? text : rewrite(text, source.value, (id) => groupAlias(names, id, groups)).value!, names };
}

export function editSource(draft: SourceDraft, text: string, groups: readonly Group[]): SourceDraft {
  const names = new Map(draft.names);
  for (const g of groups) {
    if (g.name !== "" && !names.has(g.name) && groups.filter((other) => other.name === g.name).length === 1) names.set(g.name, g.id);
  }
  return { text, names };
}

export function canonicalSource({ text, names }: SourceDraft): Result<string> {
  const source = parseSource(text);
  return source.issue ? { issue: source.issue } : rewrite(text, source.value, (name, line) => names.get(name) ?? { kind: "unknown", line, group: name });
}

export function inactiveRefs(source: Source, groups: readonly Group[], assigned: readonly string[]): SourceIssue[] {
  return source.blocks.flatMap((b) => b.refs.filter((id) => !assigned.includes(id) || !groups.some((g) => g.id === id)).map((id) => ({ kind: "inactive", line: b.open + 1, group: groups.find((g) => g.id === id)?.name ?? id })));
}

/** One common variant, then each different set of active blocks. Keep parser line coordinates. */
export function* variants(text: string, source: Source, assigned: readonly string[]): Generator<string, void, unknown> {
  if (source.blocks.length === 0) {
    yield text;
    return;
  }
  const seen = new Set<string>();
  for (const id of ["", ...assigned]) {
    const chosen = source.blocks.map((b) => b.refs.includes(id));
    const signature = chosen.map((on) => +on).join("");
    if (seen.has(signature)) continue;
    seen.add(signature);
    let at = 0;
    let out = "";
    for (const [i, b] of source.blocks.entries()) {
      while (at < b.open) {
        const l = source.lines[at++]!;
        out += l.body + l.eol;
      }
      out += source.lines[at++]!.eol;
      while (at < b.close) {
        const l = source.lines[at++]!;
        out += (chosen[i] ? l.body : "") + l.eol;
      }
      out += source.lines[at++]!.eol;
    }
    yield out + text.slice(source.lines[at - 1]!.end);
  }
}

export type Selection = { start: number; end: number; eol: string; block?: Block; lines: SourceLine[] };

/** HTML textareas expose LF offsets even when the underlying draft still contains CRLF. */
export function sourceSelection(text: string, from: number, to: number): Result<Selection> {
  const parsed = parseSource(text);
  if (parsed.issue) return { issue: parsed.issue };
  const lines = parsed.value.lines.length ? parsed.value.lines : sourceLines(text);
  let removed = 0;
  let first = -1;
  let last = -1;
  for (let i = 0; i < lines.length; i++) {
    const l = lines[i]!;
    const start = l.start - removed;
    if (l.eol === "\r\n") removed++;
    const end = l.end - removed;
    if (start <= from && (from < end || (!l.eol && from === end))) first = i;
    if (start < to || start === from) last = i;
  }
  if (first < 0) return { issue: { kind: "emptyLine", line: lines.length + 1 } };
  const a = lines[first]!;
  const b = lines[last]!;
  const block = parsed.value.blocks.find((x) => x.open <= last && x.close >= first);
  if (block && from !== to) return { issue: { kind: "selection", line: first + 1 } };
  if (!block && from === to && !a.body.trim()) return { issue: { kind: "emptyLine", line: first + 1 } };
  return { value: { start: a.start, end: b.end, eol: a.eol || lines.find((l) => l.eol)?.eol || "\n", block, lines } };
}

export function condition(draft: SourceDraft, selection: Selection, ids: string[], groups: readonly Group[]): SourceDraft {
  const names = new Map(draft.names);
  const aliases = ids.map((id) => groupAlias(names, id, groups));
  const list = JSON.stringify(aliases);
  const { text } = draft;
  const { start, end, eol, block, lines } = selection;
  if (block) {
    const a = lines[block.open]!;
    const b = lines[block.close]!;
    if (ids.length) return { text: text.slice(0, a.start) + block.before + list + a.body.match(/[ \t]*$/)![0] + text.slice(a.end - a.eol.length), names };
    // The separator inserted before an EOF closing marker was not part of the original last line.
    const bodyEnd = b.eol || block.close === block.open + 1 ? b.start : b.start - lines[block.close - 1]!.eol.length;
    return { text: text.slice(0, a.start) + text.slice(a.end, bodyEnd) + text.slice(b.end), names };
  }
  const selected = text.slice(start, end);
  const terminated = selected.endsWith("\n");
  return { text: `${text.slice(0, start)}${prefix}if ${list}${eol}${selected}${terminated ? "" : eol}${prefix}end${terminated ? eol : ""}${text.slice(end)}`, names };
}

export type Transfer = { name: string; groups: string[]; text: string };

/** Ordinary files are left alone. Headers are reserved at column zero, just like body directives. */
export function readTransfer(text: string): Result<Transfer | null> {
  if (!text.includes(prefix)) return { value: null };
  const lines = sourceLines(text);
  const header = /^# @mistgate-(?:template|groups)(?:[ \t]|$(?![\s\S]))/;
  const misplaced = lines.findIndex((l, i) => i > 1 && header.test(l.body));
  if (misplaced !== -1) return { issue: { kind: "header", line: misplaced + 1 } };
  if (!lines.slice(0, 2).some((l) => header.test(l.body))) return { value: null };
  const title = /^# @mistgate-template[ \t]+([^\n]+)$(?![\s\S])/.exec(lines[0]?.body ?? "");
  const manifest = /^# @mistgate-groups[ \t]+([^\n]+)$(?![\s\S])/.exec(lines[1]?.body ?? "");
  const name = jsonOf(title?.[1] ?? "");
  if (typeof name !== "string") return { issue: { kind: "header", line: 1 } };
  const groups = namesOf(jsonOf(manifest?.[1] ?? ""), true);
  if (!groups) return { issue: { kind: "header", line: 2 } };
  const body = text.slice(lines[1]!.end);
  const checked = mapTransfer(body, new Map(groups.map((g) => [g, g])));
  if (checked.issue) return { issue: { ...checked.issue, line: checked.issue.line + 2 } };
  return { value: { name, groups, text: body } };
}

export function mapTransfer(text: string, mapping: ReadonlyMap<string, string>): Result<string> {
  const source = parseSource(text);
  return source.issue ? { issue: source.issue } : rewrite(text, source.value, (name, line) => mapping.get(name) ?? { kind: "unknown", line, group: name });
}

export function writeTransfer(name: string, text: string, assigned: readonly string[], groups: readonly Group[], draft?: SourceDraft): Result<string> {
  const source = parseSource(text, true);
  if (source.issue) return { issue: source.issue };
  const inactive = inactiveRefs(source.value, groups, assigned)[0];
  if (inactive) return { issue: inactive };
  const listed = assigned.map((id) => groups.find((g) => g.id === id)?.name);
  if (listed.some((g) => !g) || !namesOf(listed, true)) return { issue: { kind: "header", line: 2 } };
  const mapping = new Map(assigned.map((id, i) => [id, listed[i]!]));
  const body = draft ? mapTransfer(draft.text, new Map([...draft.names].flatMap(([alias, id]) => {
    const current = mapping.get(id);
    return current ? [[alias, current] as const] : [];
  }))) : rewrite(text, source.value, (id) => mapping.get(id)!);
  if (body.issue) return { issue: body.issue };
  const eol = body.value.match(/\r?\n/)?.[0] ?? "\n";
  return { value: `${prefix}template ${JSON.stringify(name)}${eol}${prefix}groups ${JSON.stringify(listed)}${eol}${body.value}` };
}
