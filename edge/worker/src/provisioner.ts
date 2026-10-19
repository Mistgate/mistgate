import { DurableObject } from "cloudflare:workers";
import type { ProvisionEnv } from "./env";
import type { PanelApi } from "./panel";
import { keepAlive, toFetchRequest, toResponse } from "./shell";

// The Provisioner: the one Durable Object of the second script (mistgate-edge-provision). It holds what a Worker needs to
// install a node over SSH. A socket belongs to the I/O context it was opened in, and Go resumes on whichever JavaScript event
// arrives, so Go must serve exactly one object in its isolate: that is why this is a script of its own (design §2.3, §4.2).
//
// - fetch(): the main Worker's forwarded request (the ProvisioningService, the install page). The same panel handler as in
//   the main Worker, in the serve role, answers it: sign-in and step-up are checked again, the fingerprint and login checks
//   dial from here, the job is stored in D1.
// - kick(): wakes the object, i.e. sets the alarm to now unless one is set earlier. The service calls it when it queues an
//   install and the main Worker's cron calls it while one is pending.
// - alarm(): one pass of mgPanel.provision(), which claims one queued install and runs it to its end inside this event
//   (an alarm may run 15 minutes). The state of the install is in D1, not here: the object stores nothing but its alarm.
//   After a pass the alarm is set again: at once when another install waits, in 30 s when a pass was already running in
//   this isolate (a zombie of a reset object), in 60 s after an error. An earlier alarm is never moved later.

/** Another install is waiting: the next pass starts after this, not in a tight loop should the Go side keep saying "more". */
const MORE_GAP_MS = 1_000;
const BUSY_RETRY_MS = 30_000;
const ERROR_RETRY_MS = 60_000;

/** How the Provisioner gets the initialised panel; provision-index.ts wires the wasm in, the tests a fake. */
export type PanelLoader = (env: ProvisionEnv, kick: () => Promise<void>) => Promise<PanelApi>;

let load: PanelLoader | undefined;
let current: Provisioner | undefined;

export function wireProvisioner(loader: PanelLoader): void {
  load = loader;
}

/** The kick the Go side holds: it must reach the live object even after the object was recreated in this isolate. */
async function kickCurrent(): Promise<void> {
  await current?.kick();
}

const message = (error: unknown) => (error instanceof Error ? error.message : String(error));

export class Provisioner extends DurableObject<ProvisionEnv> {
  constructor(ctx: DurableObjectState, env: ProvisionEnv) {
    super(ctx, env);
    current = this;
  }

  private panel(): Promise<PanelApi> {
    if (!load) return Promise.reject(new Error("the Provisioner has no panel wired in"));
    return keepAlive(this.ctx, load(this.env, kickCurrent));
  }

  async fetch(request: Request): Promise<Response> {
    let panel: PanelApi;
    try {
      panel = await this.panel();
    } catch (error) {
      console.error("provisioner init failed:", message(error));
      return new Response("Service Unavailable", { status: 503, headers: { "Retry-After": "5" } });
    }
    try {
      const answer = await panel.fetch(await toFetchRequest(request));
      this.ctx.waitUntil(answer.waitUntil);
      return toResponse(answer);
    } catch (error) {
      console.error("provisioner request failed:", message(error));
      return new Response("Internal Server Error", { status: 500 });
    }
  }

  /** Runs the next pass as soon as possible, unless a pass is already due earlier. */
  async kick(): Promise<void> {
    await this.arm(Date.now());
  }

  async alarm(): Promise<void> {
    let pass: Awaited<ReturnType<PanelApi["provision"]>>;
    try {
      pass = await (await this.panel()).provision();
    } catch (error) {
      // Handled here, not thrown: the platform would retry the alarm from the start and the install has its own attempt counter.
      console.error("provision pass failed:", message(error));
      await this.arm(Date.now() + ERROR_RETRY_MS);
      return;
    }
    try {
      await pass.waitUntil; // the pass's after-response work (node pokes) belongs to this event
    } catch (error) {
      console.error("provision after-work failed:", message(error));
    }
    if (pass.busy) await this.arm(Date.now() + BUSY_RETRY_MS);
    else if (pass.more) await this.arm(Date.now() + MORE_GAP_MS);
  }

  /** Sets the alarm to `at` unless one is set at or before it. */
  private async arm(at: number): Promise<void> {
    const set = await this.ctx.storage.getAlarm();
    if (set === null || set > at) await this.ctx.storage.setAlarm(at);
  }
}

/** The default handler of the second script: it serves nobody, so there is no Go in it. Requests reach the Provisioner by binding only. */
export const provisionEntry = {
  fetch: () => new Response("Not Found", { status: 404 }),
} satisfies ExportedHandler<ProvisionEnv>;
