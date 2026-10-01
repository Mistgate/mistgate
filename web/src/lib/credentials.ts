import { Code, ConnectError } from "@connectrpc/connect";
import { SetupMethod, SignInFailureSchema } from "@/gen/mistgate/admin/v1/auth_pb";
import { auth } from "./api";

// Password + authenticator-code sign-in and first-admin setup: the alternative to a passkey. The server
// does all of it (argon2id hash, TOTP secret, lockout); the browser only carries the words and the code.

export type SignInResult =
  | { kind: "ok" }
  | { kind: "failed"; attemptsLeft: number }
  | { kind: "locked"; until: number }; // epoch ms

export type PasswordLogin = { login: string; password: string; code: string; turnstileToken: string };

/** What a failed PasswordLogin says: attempts left before the lockout, or when the lockout ends. */
export function readSignInFailure(e: unknown): SignInResult | null {
  const err = ConnectError.from(e);
  if (err.code !== Code.Unauthenticated) return null;
  const d = err.findDetails(SignInFailureSchema)[0];
  if (!d) return null;
  if (d.lockedUntilUnix > 0n) return { kind: "locked", until: Number(d.lockedUntilUnix) * 1000 };
  return { kind: "failed", attemptsLeft: d.attemptsLeft };
}

/** Sets the session cookie on success. A wrong login, password or code is a result, not an exception. */
export async function signInWithPassword(input: PasswordLogin): Promise<SignInResult> {
  try {
    await auth.passwordLogin({ login: input.login, password: input.password, totpCode: input.code, turnstileToken: input.turnstileToken });
    return { kind: "ok" };
  } catch (e) {
    const failure = readSignInFailure(e);
    if (failure) return failure;
    throw e;
  }
}

export type PasswordSetup = { setupToken: string; displayName: string; login: string; password: string; turnstileToken: string };
export type TotpEnrolment = { ceremonyId: string; uri: string; secret: string };

/** Step 1 of a password admin: the server hashes the password and issues the TOTP secret to scan. */
export async function beginPasswordSetup(input: PasswordSetup): Promise<TotpEnrolment> {
  const r = await auth.beginSetup({
    setupToken: input.setupToken,
    displayName: input.displayName,
    method: SetupMethod.PASSWORD,
    login: input.login,
    password: input.password,
    turnstileToken: input.turnstileToken,
  });
  return { ceremonyId: r.ceremonyId, uri: r.totpUri, secret: r.totpSecret };
}

/** Step 2: the first code from the authenticator app proves the secret was scanned; creates the admin and signs in. */
export async function finishPasswordSetup(setupToken: string, ceremonyId: string, code: string) {
  await auth.finishSetup({ setupToken, ceremonyId, totpCode: code });
}

// The server's rules, checked here first so the message is ours and in the UI language.
export const loginPattern = /^[a-z0-9._@-]{3,64}$/i;
export const minPasswordLength = 12;
export const maxPasswordBytes = 256;

export const passwordTooShort = (pw: string) => [...pw].length < minPasswordLength;
export const passwordTooLong = (pw: string) => new TextEncoder().encode(pw).length > maxPasswordBytes;
