# Security policy

## Reporting a vulnerability

Please report vulnerabilities privately through GitHub: open the repository's **Security** tab and choose
**Report a vulnerability**. Do not open a public issue or pull request for it.

Include what you can: the affected version or commit, the setup (admin on a prefix, a secret host or its own
listener; node OS), the steps to reproduce and what an attacker gains. You will get an answer in the advisory thread;
a fix and a public advisory follow once it is resolved.

## Supported versions

Mistgate has no stable releases yet. Fixes go to the latest `main` (and to the latest release once there are releases);
older commits are not patched.

## What counts

Anything that breaks the guarantees the panel and the agent make, for example:

- finding or telling apart the panel behind the decoy site: bypassing the secret admin prefix or host, or a response
  (content, headers, timing) that differs between a guessed path or subscription token and a wrong one;
- leaks of subscription links, user page passwords, AmneziaWG or WARP keys, API tokens or session cookies, including
  through the MCP tools, logs, error messages or the audit log;
- getting around the step-up re-authentication, an API token's profile, or the owner's approval of an agent's plan;
- making a node or the panel connect where it should not (SSRF through the node egress, the synthetic checks or
  any URL the panel fetches), or one VPN client reaching another client's traffic on a node;
- forging or replaying node enrollment, agent certificates or signed update bundles.

Out of scope: attacks that need the panel's data directory, root on a node or the owner's release key; denial of
service by sheer volume; missing hardening without a concrete impact.
