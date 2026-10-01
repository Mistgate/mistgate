import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { describe, expect, it } from "vitest";
import { SignInFailureSchema } from "@/gen/mistgate/admin/v1/auth_pb";
import { loginPattern, passwordTooLong, passwordTooShort, readSignInFailure } from "./credentials";

const failure = (attemptsLeft: number, lockedUntilUnix = 0n, code = Code.Unauthenticated) =>
  new ConnectError("sign-in failed", code, undefined, [
    { desc: SignInFailureSchema, value: create(SignInFailureSchema, { attemptsLeft, lockedUntilUnix }) },
  ]);

describe("readSignInFailure", () => {
  it("reads the attempts left", () => {
    expect(readSignInFailure(failure(3))).toEqual({ kind: "failed", attemptsLeft: 3 });
  });
  it("reads a lockout and turns its end into epoch milliseconds", () => {
    expect(readSignInFailure(failure(0, 1_800_000_000n))).toEqual({ kind: "locked", until: 1_800_000_000_000 });
  });
  it("ignores other failures", () => {
    expect(readSignInFailure(failure(3, 0n, Code.Unavailable))).toBeNull();
    expect(readSignInFailure(new ConnectError("no detail", Code.Unauthenticated))).toBeNull();
    expect(readSignInFailure(new Error("boom"))).toBeNull();
  });
});

describe("the server's credential rules", () => {
  it("accepts logins of 3-64 characters from a-z 0-9 . _ @ -", () => {
    for (const ok of ["adm", "Admin", "a.b_c@d-e", "x".repeat(64)]) expect(loginPattern.test(ok), ok).toBe(true);
    for (const bad of ["ab", "a b", "админ", "x".repeat(65), "a/b"]) expect(loginPattern.test(bad), bad).toBe(false);
  });
  it("counts password characters and bytes", () => {
    expect(passwordTooShort("12345678901")).toBe(true);
    expect(passwordTooShort("123456789012")).toBe(false);
    expect(passwordTooShort("пароль-пароль")).toBe(false); // 13 characters, more bytes
    expect(passwordTooLong("я".repeat(129))).toBe(true); // 258 bytes
    expect(passwordTooLong("я".repeat(128))).toBe(false);
  });
});
