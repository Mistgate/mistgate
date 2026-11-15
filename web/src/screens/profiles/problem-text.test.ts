import { describe, expect, it } from "vitest";
import { dictionaries, fill, type Lang } from "@/i18n";
import type { Tx } from "@/screens/users/t";
import { problemText } from "./problem-text";

const txOf = (lang: Lang) => ({ opt: (key: string, vars?: Record<string, string | number>) => (key in dictionaries[lang] ? fill(dictionaries[lang][key as never], vars) : undefined) }) as unknown as Tx;
const en = txOf("en");

describe("the words for a field the server refused", () => {
  it("takes the protocol's own text before the general one: /tls_mode unsupported is a Hysteria2 certificate on an IP, not VLESS's REALITY", () => {
    const p = { pointer: "/tls_mode", code: "unsupported" };
    expect(problemText(en, "hysteria2", p)).toBe("A Let’s Encrypt certificate for an IP address is not available yet: use a domain or self-signed");
    expect(problemText(en, "vless", p)).toBe("The certificate mode belongs to TLS; REALITY has none");
  });

  it("falls back to the general text of the path and code, and to nothing (the server's phrase) when there is none", () => {
    expect(problemText(en, "hysteria2", { pointer: "/port", code: "out_of_range" })).toBe("The port is 1 to 65535");
    expect(problemText(en, "awg", { pointer: "/port", code: "out_of_range" })).toBe("The port is 1 to 65535");
    expect(problemText(en, "mtproto", { pointer: "/fake_tls/domain", code: "invalid" })).toBeUndefined();
    expect(problemText(en, "awg", { pointer: "/port" })).toBeUndefined();
  });

  it("reads a numbered field (S3, H2, I5) as its letter and fills in the number", () => {
    expect(problemText(en, "awg", { pointer: "/obfuscation/s3", code: "duplicate_size" })).toBe("The four packet sizes (148+S1, 92+S2, 64+S3, 32+S4) must all differ: change S3");
    expect(problemText(en, "awg", { pointer: "/obfuscation/h2", code: "overlap" })).toContain("H2 overlaps");
    expect(problemText(en, "awg", { pointer: "/obfuscation/i5", code: "invalid" })).toContain("I5 is written with tags only");
    // subnet4 is a name with a digit, not a numbered field
    expect(problemText(en, "awg", { pointer: "/subnet4", code: "invalid" })).toContain("IPv4");
  });

  it("takes a protocol's text for a code alone", () => {
    expect(problemText(en, "awg", { pointer: "/obfuscation/rekey_timeout", code: "not_in_version" })).toBe("Only in AmneziaWG 3.1: pick 3.1 or clear this setting");
    expect(problemText(en, "awg", { pointer: "/obfuscation/persistent_keepalive", code: "not_in_version" })).toBe("AmneziaWG 2.0 takes one number of seconds, not a range");
  });

  // every (pointer, code) of internal/panel/protocols/awg/validate.go; each has words in all five languages
  const awgCodes: [string, string][] = [
    ["/port", "out_of_range"], ["/version", "invalid_enum"], ["/mtu", "out_of_range"], ["/egress", "invalid_enum"],
    ["/obfuscation/preset", "invalid_enum"], ["/subnet4", "invalid"], ["/subnet6", "invalid"],
    ...["content_padding_addition", "rekey_after_time", "rekey_timeout", "reject_after_time", "keepalive_timeout", "max_handshake_attempts", "random_trailers", "disable_cookies"].map((f): [string, string] => [`/obfuscation/${f}`, "not_in_version"]),
    ["/obfuscation/persistent_keepalive", "not_in_version"], ["/obfuscation/persistent_keepalive", "invalid"],
    ["/obfuscation/jc", "out_of_range"], ["/obfuscation/jc", "handshake_budget"], ["/obfuscation/jmin", "out_of_range"], ["/obfuscation/jmax", "out_of_range"], ["/obfuscation/jmax", "invalid"],
    ...[1, 2, 3, 4].flatMap((n): [string, string][] => [[`/obfuscation/s${n}`, "out_of_range"], [`/obfuscation/h${n}`, "invalid"], ...(n > 1 ? ([[`/obfuscation/s${n}`, "duplicate_size"], [`/obfuscation/h${n}`, "overlap"]] as [string, string][]) : [])]),
    ["/obfuscation/header_protection_key", "rt_range_requires_hpk"], ["/obfuscation/header_protection_key", "invalid"],
    ...[1, 2, 3, 4, 5].map((n): [string, string] => [`/obfuscation/i${n}`, "invalid"]), ["/obfuscation/i1", "too_long"],
    ...["content_padding_addition", "rekey_after_time", "rekey_timeout", "reject_after_time", "keepalive_timeout", "max_handshake_attempts"].map((f): [string, string] => [`/obfuscation/${f}`, "invalid"]),
    ["/obfuscation/domain", "invalid"], ["/obfuscation/signature_seed", "invalid"], ["/obfuscation/signature_seed", "required"],
  ];
  it.each(["en", "ru", "zh", "fa", "tr"] as Lang[])("has a text in %s for every code the AmneziaWG validator sends", (lang) => {
    const t = txOf(lang);
    expect(awgCodes.filter(([pointer, code]) => !problemText(t, "awg", { pointer, code }))).toEqual([]);
  });
});
