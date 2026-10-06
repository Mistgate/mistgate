# 0005. One password hashing profile for both editions

Status: accepted, 2026-10-06.

## Context

Admin passwords are hashed with argon2id at m = 64 MiB, t = 3, p = 2. A Worker isolate has 128 MB including WASM memory;
the spike measured 470–750 ms CPU for that profile and Go memory up to 194 MiB, beyond the limit (the isolate was
recreated, which is not something to rely on). The OWASP minimum m = 19 MiB, t = 2, p = 1 took ~92 ms. WebCrypto has no
argon2 and caps PBKDF2 at 100 000 iterations. A password is never the only factor here: it always comes with TOTP, and
passkeys are the primary sign-in.

## Decision

- Both editions hash with argon2id m = 19 MiB, t = 2, p = 1, and mix in a pepper derived from the master key with
  `vault.Derive` (a stolen database alone is not enough to test guesses offline).
- The parameters stay inside each PHC hash string, so any hash verifies in either edition; a hash made with older
  parameters (or without the pepper) is re-hashed with the current profile at the next successful sign-in.
- `verifyPassword` keeps refusing parameters above a sane ceiling, as today.

## Consequences

- The VPS edition gets lighter sign-ins and the same hashes as the edge, which keeps backups portable.
- A database moved without its master key cannot verify peppered passwords; the master key already travels with every
  backup and is required anyway.
