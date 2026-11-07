import { Code, ConnectError } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";
import type { RetentionItem } from "@/gen/mistgate/ops/v1/ops_pb";
import { en } from "@/i18n/en";
import { ru } from "@/i18n/ru";
import { fill } from "@/i18n";
import { errorText, requestIdOf } from "./errors";
import { changedRetention, exportFileName, isLogLevel, isUnavailable, retentionProblem } from "./ops";

const t = ((d: Record<string, string>) => Object.assign((key: string, vars?: Record<string, string | number>) => fill(d[key]!, vars), { n: () => "" }))(en) as never;
const tRu = ((d: Record<string, string>) => Object.assign((key: string, vars?: Record<string, string | number>) => fill(d[key]!, vars), { n: () => "" }))(ru) as never;

const item = (key: string, value: number, min: number, max: number) => ({ key, value, defaultValue: value, min, max, unit: "days" }) as RetentionItem;

describe("retention drafts", () => {
  const items = [item("audit_days", 400, 30, 1095), item("journal_days", 7, 1, 7)];
  it("sends only what changed, as whole numbers", () => {
    expect(changedRetention(items, {})).toEqual({});
    expect(changedRetention(items, { audit_days: "400", journal_days: "3" })).toEqual({ journal_days: 3 });
    expect(changedRetention(items, { audit_days: "", journal_days: "1.5" })).toEqual({});
  });
  it("names a value outside the limits, or not a whole number", () => {
    expect(retentionProblem(items[0]!, "29")).toBe(true);
    expect(retentionProblem(items[0]!, "30")).toBe(false);
    expect(retentionProblem(items[1]!, "8")).toBe(true);
    expect(retentionProblem(items[1]!, "1e2")).toBe(true);
    expect(retentionProblem(items[1]!, "")).toBe(false); // not edited
  });
});

describe("small helpers", () => {
  it("knows the four log levels and the edition that has none", () => {
    expect(["debug", "info", "warn", "error"].every(isLogLevel)).toBe(true);
    expect(isLogLevel("trace")).toBe(false);
    expect(isUnavailable(new ConnectError("x", Code.Unimplemented))).toBe(true);
    expect(isUnavailable(new ConnectError("x", Code.Internal))).toBe(false);
  });
  it("names the export file by day", () => {
    expect(exportFileName(new Date("2026-11-07T12:00:00Z"))).toBe("mistgate-export-2026-11-07.ndjson");
  });
});

describe("the request code on a failed call", () => {
  const failed = (id: string | null) => new ConnectError("internal error", Code.Internal, id === null ? undefined : new Headers({ "X-Request-Id": id }));
  it("is read from the response header", () => {
    expect(requestIdOf(failed("abc12345"))).toBe("abc12345");
    expect(requestIdOf(failed(null))).toBe("");
    expect(requestIdOf(failed("not an id <script>"))).toBe("");
  });
  it("follows the generic line, in the language of the page", () => {
    expect(errorText(failed("abc12345"), t)).toBe(`${en["err.generic"]} Request code: abc12345`);
    expect(errorText(failed("abc12345"), tRu)).toBe(`${ru["err.generic"]} Код запроса: abc12345`);
    expect(errorText(failed(null), t)).toBe(en["err.generic"]);
  });
  it("never decorates a refusal that names its own rule", () => {
    expect(errorText(new ConnectError("name_taken", Code.AlreadyExists, new Headers({ "X-Request-Id": "abc12345" })), t)).not.toContain("abc12345");
  });
});
