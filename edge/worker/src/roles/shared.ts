import type { AdminEnv, JobsEnv, LinkEnv, ProvisionEnv, PublicEnv } from "../env";
import { fetchWith, scheduledWith } from "../handler";
import { panelLinkOf } from "../panellink";
import { provisionEntry, wireProvisioner } from "../provisioner";
import type { Runtime } from "../runtime";
import { type Connect, openSocket } from "../shell";

// What each role script does around the Go panel of its own wasm. The role files (public.ts, link.ts ...) import the wasm and
// glue, make the Runtime and call one of these, so that everything here runs under a fake Runtime in the tests.

/** `<name>-public`: every request to the panel, which serves the subscription surface and the decoy. */
export function publicRole(runtime: Runtime) {
  return { fetch: fetchWith(runtime.getPanel) } satisfies ExportedHandler<PublicEnv>;
}

/** `<name>-admin`: every request to the panel. The admin surface, the sign-in and the first-run setup are in its wasm. */
export function adminRole(runtime: Runtime) {
  return { fetch: fetchWith(runtime.getPanel) } satisfies ExportedHandler<AdminEnv>;
}

/**
 * `<name>-link`: requests to the panel (link-only enrolment, and the upgrade the panel marks, which goes to the NodeLink object
 * of its script), plus the PanelLink entrypoint the NodeLink objects call. The script must export `PanelLink` (linkEntrypoint).
 */
export function linkRole(runtime: Runtime) {
  return { fetch: fetchWith(runtime.getPanel) } satisfies ExportedHandler<LinkEnv>;
}

/** The class a link script exports as `PanelLink`: `export class PanelLink extends linkEntrypoint(runtime) {}`. */
export function linkEntrypoint(runtime: Runtime) {
  return panelLinkOf(runtime.getPanel);
}

/** `<name>-jobs`: the cron tick. It has no route, so its own fetch answers 404 without starting Go. */
export function jobsRole(runtime: Runtime) {
  return {
    fetch: () => new Response("Not Found", { status: 404 }),
    scheduled: scheduledWith(runtime.getPanel),
  } satisfies ExportedHandler<JobsEnv>;
}

/**
 * `<name>-provision`: the Provisioner object and nothing else, as src/provision-index.ts has it, over this script's wasm. The
 * script exports the Provisioner class itself and this as its default. `connect` is `connect` of cloudflare:sockets (injected
 * so that this file loads under plain Node too).
 */
export function provisionRole(runtime: Runtime, connect: Connect) {
  wireProvisioner((env: ProvisionEnv, kick) => runtime.getServePanel(env, { kick, sshConnect: (host, port, timeoutMs) => openSocket(connect, host, port, timeoutMs) }));
  return provisionEntry;
}
