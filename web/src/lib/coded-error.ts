import { Code, ConnectError } from "@connectrpc/connect";
import type { T } from "@/i18n";
import { errorCode, errorText, errorVars } from "./errors";

type Opt = T & { opt: (key: string, vars?: Record<string, string | number>) => string | undefined };

/**
 * A sentence for a failed call whose reason is a code: the dictionary's `<prefix>.<code>` (with the message's values, see
 * errorVars: `{detail}` = the text after the colon), tried for the codes that mean "the rules refuse this" (FAILED_PRECONDITION,
 * RESOURCE_EXHAUSTED, UNAVAILABLE, INVALID_ARGUMENT); everything else, and a code the dictionary does not know, reads as
 * errorText does.
 */
export function codedError(e: unknown, t: Opt, prefix: string): string {
  const c = ConnectError.from(e);
  const coded = c.code === Code.FailedPrecondition || c.code === Code.ResourceExhausted || c.code === Code.Unavailable || c.code === Code.InvalidArgument;
  if (coded) {
    const msg = c.rawMessage;
    const text = t.opt(`${prefix}.${errorCode(msg)}`, errorVars(msg));
    if (text) return text;
  }
  return errorText(e, t);
}
