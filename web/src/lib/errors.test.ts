import { existsSync, readFileSync } from "node:fs";
import { Code, ConnectError } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";
import { fill, type Lang, type T } from "@/i18n";
import { en } from "@/i18n/en";
import { ru } from "@/i18n/ru";
import { errorCodes, errorText, errorVars } from "./errors";

const dict = { en, ru };
const tOf = (lang: Lang) => Object.assign((key: keyof typeof en, vars?: Record<string, string | number>) => fill(dict[lang][key], vars), { n: () => "" }) as unknown as T;
const say = (msg: string, code: Code, lang: Lang = "en") => errorText(new ConnectError(msg, code), tOf(lang));

describe("the panel's refusal codes", () => {
  it("have a sentence in both languages", () => {
    for (const c of errorCodes) {
      expect(en[`err.${c}`], `en ${c}`).toBeTruthy();
      expect(ru[`err.${c}`], `ru ${c}`).toBeTruthy();
      expect(ru[`err.${c}`], `ru ${c}`).not.toBe(en[`err.${c}`]);
    }
  });

  it("read as our sentence, with the values the server put after the code", () => {
    expect(say("name_taken", Code.AlreadyExists, "ru")).toBe("Это имя уже занято — выбери другое.");
    expect(say("group_not_empty: users=3", Code.FailedPrecondition, "ru")).toBe("В группе ещё 3 чел. — перенеси их в другую группу, потом удаляй.");
    expect(say("profile_deployed: nodes=de1%2C+nl1", Code.FailedPrecondition, "ru")).toBe("Профиль стоит на нодах de1, nl1 — сначала убери его с них.");
    expect(say("stale_version", Code.Aborted)).toBe(en["err.stale_version"]);
    expect(say("agent too old", Code.FailedPrecondition)).toBe(en["err.agent_too_old"]); // the older spelling of the code
    expect(errorVars("device_limit: 5/5").detail).toBe("5/5"); // the older "code: detail" keeps its detail
  });

  it("localizes known API-token denial reasons and keeps role denial separate", () => {
    const cases = [
      ["this call is not available to API tokens", "err.apiTokenCallUnavailable"],
      ["this token's profile cannot do this", "err.apiTokenProfileDenied"],
      ["this needs the owner's approval; it is not available over the API", "err.apiOwnerApprovalRequired"],
    ] as const;

    for (const [message, key] of cases) {
      expect(say(message, Code.PermissionDenied)).toBe(en[key]);
      expect(say(message, Code.PermissionDenied, "ru")).toBe(ru[key]);
      expect(ru[key]).not.toBe(en[key]);
    }
    expect(say("your role cannot do this", Code.PermissionDenied)).toBe(en["err.denied"]);
    expect(say("your role cannot do this", Code.PermissionDenied, "ru")).toBe(ru["err.denied"]);
    expect(say("unknown denial", Code.PermissionDenied)).toBe(en["err.permissionDenied"]);
    expect(say("unknown denial", Code.PermissionDenied, "ru")).toBe(ru["err.permissionDenied"]);
  });

  it("leave a code they do not know as the server wrote it, as before", () => {
    expect(say("brand_new_code: x=1", Code.FailedPrecondition)).toBe("brand_new_code: x=1");
    expect(say("a rollout is already active", Code.FailedPrecondition)).toBe("a rollout is already active");
    expect(say("slow down", Code.ResourceExhausted)).toBe(en["err.rateLimited"]);
    expect(say("the node link dropped", Code.Unavailable)).toBe(en["err.network"]);
    expect(say("node_offline", Code.Internal)).toBe(en["err.generic"]); // only a refusal is worded
  });

  it("cover every code the server writes (read from the Go source, so a new one cannot ship as a bare code)", () => {
    const files = ["access/user_rpc.go", "access/group_rpc.go", "access/profile_rpc.go", "fleet/admin_node.go", "dns/dns.go"].map((f) => `${process.cwd()}/../internal/panel/${f}`); // vitest runs in web/
    if (!files.every(existsSync)) return; // the web tree built on its own: nothing to compare with
    const go = files.map((f) => readFileSync(f, "utf8")).join("\n");
    const codes = new Set([...go.matchAll(/coded\(connect\.Code\w+, "([a-z_]+)"|errors\.New\("([a-z]+(?:_[a-z]+)+)"\)/g)].map((m) => m[1] ?? m[2]!));
    expect(codes.size).toBeGreaterThan(5);
    for (const c of codes) expect(errorCodes, `no sentence for the server's code ${c}`).toContain(c);
  });
});
