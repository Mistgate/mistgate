// protobuf-es v2 turns every 64-bit field into a `bigint`: it honours `jstype = JS_STRING` but not the
// `JS_NUMBER` the admin protos ask for (common.proto says the counters "use number"). Counters here stay far
// below 2^53 (9 PB), so the screens work with plain numbers: every response goes through `plain()` at the
// boundary (queries.ts and the mutations), which converts the bigints and drops the `$typeName` bookkeeping.
// `Plain<T>` is the matching type. Requests are the other way round: a 64-bit request field takes `BigInt(n)`.

type Bookkeeping = "$typeName" | "$unknown";

export type Plain<T> = T extends bigint
  ? number
  : T extends readonly (infer U)[]
    ? Plain<U>[]
    : T extends object
      ? { [K in keyof T as K extends Bookkeeping ? never : K]: Plain<T[K]> }
      : T;

export function plain<T>(value: T): Plain<T> {
  if (typeof value === "bigint") return Number(value) as Plain<T>;
  if (Array.isArray(value)) return value.map((v) => plain(v)) as Plain<T>;
  if (value && typeof value === "object") {
    const out: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(value)) if (k !== "$typeName" && k !== "$unknown") out[k] = plain(v);
    return out as Plain<T>;
  }
  return value as Plain<T>;
}
