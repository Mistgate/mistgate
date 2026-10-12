# 0008. SSH installs on the edge run in a second Worker script, in one Durable Object

Status: accepted, 2026-10-12. Details one bullet of [0004](0004-worker-gaps-move-to-nodes.md).

## Context

ADR 0004 keeps the SSH installation of a node in the panel and gives it an adapter over `cloudflare:sockets connect()`.
`golang.org/x/crypto/ssh` already links into `panel.wasm`; it needs only a `net.Conn` over that socket. The open question
was where the Go code that holds the socket may run.

A socket opened by `connect()` belongs to the I/O context it was created in. A stateless request has a context of its
own; a Durable Object has one for the whole object. Touching a socket from another context throws (workerd,
`io-context.h`). The Go runtime is one per isolate and resumes on any JavaScript event that reaches the isolate, running
every goroutine that is ready, not only the one the event was for. An isolate may host several Durable Objects, and a
stateless request may land in it too. So an SSH goroutine woken by a timer can run inside someone else's event, such as
an agent-link step or a subscription request, and then touch its socket from the wrong context.

Round S1 (a throwaway spike on local workerd 1.20261001.1, with an x/crypto server and with OpenSSH 9.6) measured it:

| Upload of 16 MiB, woken by Go timers between chunks | one script | two scripts |
|---|---|---|
| with 20 parallel stateless requests | 0 of 3 passed: "Cannot perform I/O on behalf of a different request" | 3 of 3 |
| with events of another Durable Object | 0 of 3 passed, hung over 60 s: "different Durable Object" | 3 of 3 |

An upload woken only by the socket's own data passed in both layouts, so the failure shows only when a goroutine wakes by
a Go timer: the cancellation watcher, the wait for the node, any pause, that is any real install. SSH over `connect()`
itself worked, including a 32 MiB upload in about 1.7 s of CPU with the default cipher, well inside the 30 s default.

## Decision

- SSH installs run in a **second Worker script**, `mistgate-edge-provision`: the same `panel.wasm` in its `serve` role and
  one Durable Object, `Provisioner` (a singleton, `idFromName("provisioner")`). The Go instance of that isolate serves
  only this object, so every socket stays in the context it was opened in. The script has no route and no workers.dev
  name, and its own `fetch` answers 404 without starting Go.
- The main Worker keeps the sign-in check and then forwards the `ProvisioningService` calls and the install page to the
  object (a 204 marker `X-Mistgate-Forward`, then `forwardProvision`). The Provisioner checks sign-in and step-up again.
- An install runs inside the object's `alarm()` (15 minutes of wall time; an install takes about 8): `Service.Drain` claims
  one job per pass. The cron of the main Worker wakes the object while a job is queued, running or being cancelled.
- Job state lives in **D1**, in the tables the VPS edition uses, not in the object: cancel, retire and progress reads work
  from either script, a backup or a move between editions carries the jobs, and the object stores only its alarm. An
  evicted or redeployed pass is requeued and runs again; the third interruption fails the job (`job_interrupted`), in both
  editions.
- The agent binary is streamed by the Provisioner from the signed release on GitHub into the SSH upload, checked against
  the release key compiled into the panel and against the manifest's SHA-256 on the way. No R2, and nothing for the node
  to download. The node enrols over the agent link (`--link-url`), as every edge node does.
- Deploy order: the **Provisioner first**, then the main Worker, which binds to it. The marker carries the build; a
  Provisioner of another build answers 503 `panel_updating`, so a half-finished update never runs an old install on a
  new schema. The installer and the self-update roll both scripts.
- `MASTER_KEY` is a secret of **both** scripts, with the same D1 database; rotating it updates both. The SSH password
  travels only in a request body and in memory, and observability is off in both.
- Edge-only settings: the cancellation watcher polls D1 every 2 s (the VPS keeps 200 ms) because D1 allows 1000 queries
  per invocation, and the SSH client prefers `chacha20-poly1305` (2.6 times faster in wasm).

## Consequences

- The edge edition ships two scripts, and a deploy that forgets one fails closed: no binding or an unreachable object
  answers 503 `provisioner_unavailable`.
- The manual install command stays the fallback, and the wizard offers it. It is what a node needs when the panel's
  network cannot reach it over SSH: a Cloudflare address or a hostname behind Cloudflare's proxy, a private or VPN-only
  network, SSH that allows only the owner's own address (the egress prefix of Workers is not published), a server that
  accepts keys only, or three interrupted attempts. A refusal by the platform and a closed port look alike from the
  Worker, so the code `ssh_target_refused` says "refused or unreachable" and not more.
- Not verifiable without a real Cloudflare account, checked in the first real deploy (E8): `connect()` to a bare IP, port 22
  from the production egress, the platform's own refusal text, whether production really shares an isolate between
  Durable Object classes and stateless requests (it does not matter with two scripts, it was decisive with one), the CPU
  and time of an install, and a redeploy in the middle of a job.
- Cloudflare Containers are still not needed (0004). Splitting a rarely used area into its own script, listed in the
  edition plan as a size lever, is now done for this reason and not for size: the wasm is the same.

## Alternatives rejected

- The same script, the object in the shared isolate: broken by S1 as above.
- A second private Go instance inside the object: a second `main` and build, two Go runtimes in 128 MB.
- Step-by-step SSH like the agent link: the transport state of `x/crypto/ssh` (keys, packet numbers, windows) cannot be
  serialised between events.
- Cloudflare Containers with OpenSSH: outside the core (0004), a start of 19-25 s, a separate price.
- The node downloads the agent through a signed route of the panel: a second install path that differs from the VPS one,
  and nodes that reach Cloudflare addresses are shaped.

ADRs 0003 and 0007 are unchanged: the agent link and the session core do not use the second script.
