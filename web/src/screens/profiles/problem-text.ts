import type { Tx } from "@/screens/users/t";
import type { Problem } from "./schema-form";

/**
 * The words for a field the server refused, from the most specific text to the most general: the protocol's own for this
 * path and code (`field.hysteria2.tls_mode.unsupported`), the same for a numbered field (`/obfuscation/s3` reads
 * `field.awg.obfuscation.s.<code>` with {n}: 3), the protocol's own for the code alone (`field.awg.not_in_version`), and the
 * general one of path and code (`field.tls_mode.unsupported`). Paths of different protocols are equal, and so are their codes
 * with other meanings, so a protocol's own text must come before the general one. None of them: undefined (the server's phrase).
 */
export function problemText(t: Tx, protocol: string, p: Pick<Problem, "pointer" | "code">): string | undefined {
  if (!p.code) return undefined;
  const path = p.pointer.split("/").filter(Boolean);
  const numbered = /^([a-z])(\d)$/.exec(path.at(-1) ?? "");
  const own = [`field.${protocol}.${path.join(".")}.${p.code}`];
  if (numbered) own.push(`field.${protocol}.${[...path.slice(0, -1), numbered[1]].join(".")}.${p.code}`);
  own.push(`field.${protocol}.${p.code}`);
  for (const key of own) {
    const text = t.opt(key, { n: numbered?.[2] ?? "" });
    if (text !== undefined) return text;
  }
  return t.opt(`field${p.pointer.replaceAll("/", ".")}.${p.code}`);
}
