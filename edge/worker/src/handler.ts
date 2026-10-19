import type { PanelEnv } from "./env";
import type { PanelApi } from "./runtime";
import { forwardLink, forwardProvision, keepAlive, panelURL, toFetchRequest, toResponse } from "./shell";

// The Worker's two entry points over a panel: an HTTP request and a cron tick. They do not know which wasm the panel is, so
// the single script (src/fetch.ts) and every role script of the split edition (src/roles) share them.

export type GetPanel = (env: PanelEnv, origin: string) => Promise<PanelApi>;

export function fetchWith(getPanel: GetPanel) {
  return async (request: Request, env: PanelEnv, ctx: ExecutionContext): Promise<Response> => {
    let panel;
    try {
      panel = await keepAlive(ctx, getPanel(env, new URL(panelURL(request.url)).origin));
    } catch (error) {
      console.error("panel init failed:", error instanceof Error ? error.message : String(error));
      return new Response("Service Unavailable", { status: 503, headers: { "Retry-After": "5" } });
    }
    try {
      const fetchRequest = await toFetchRequest(request);
      const answer = await keepAlive(ctx, panel.fetch(fetchRequest));
      ctx.waitUntil(answer.waitUntil);
      // The panel marks what another object serves: an agent link upgrade (the node's NodeLink) and an SSH-install request
      // (the Provisioner of the second script, which gets the body the panel already read).
      return (
        (env.NODELINK ? await forwardLink(env.NODELINK, request, answer) : undefined) ??
        (await forwardProvision(env.PROVISIONER, fetchRequest, answer)) ??
        toResponse(answer)
      );
    } catch (error) {
      console.error("panel request failed:", error instanceof Error ? error.message : String(error));
      return new Response("Internal Server Error", { status: 500 });
    }
  };
}

export function scheduledWith(getPanel: GetPanel) {
  return async (controller: ScheduledController, env: PanelEnv, ctx: ExecutionContext): Promise<void> => {
    let panel;
    try {
      panel = await keepAlive(ctx, getPanel(env, ""));
    } catch (error) {
      console.error("panel init failed:", error instanceof Error ? error.message : String(error));
      return;
    }
    try {
      const out = await keepAlive(ctx, panel.cron({ at: controller.scheduledTime }));
      ctx.waitUntil(out.waitUntil);
    } catch (error) {
      console.error("panel cron failed:", error instanceof Error ? error.message : String(error));
    }
  };
}
