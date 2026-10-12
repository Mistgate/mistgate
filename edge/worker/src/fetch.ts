import type { Env } from "./env";
import { getPanel } from "./panel";
import { forwardLink, forwardProvision, panelURL, toFetchRequest, toResponse } from "./shell";

export async function fetchPanelRequest(request: Request, env: Env, ctx: ExecutionContext): Promise<Response> {
  let panel;
  try {
    panel = await getPanel(env, new URL(panelURL(request.url)).origin);
  } catch (error) {
    console.error("panel init failed:", error instanceof Error ? error.message : String(error));
    return new Response("Service Unavailable", { status: 503, headers: { "Retry-After": "5" } });
  }
  try {
    const fetchRequest = await toFetchRequest(request);
    const answer = await panel.fetch(fetchRequest);
    ctx.waitUntil(answer.waitUntil);
    // The panel marks what another object serves: an agent link upgrade (the node's NodeLink) and an SSH-install request
    // (the Provisioner of the second script, which gets the body the panel already read).
    return (await forwardLink(env.NODELINK, request, answer)) ?? (await forwardProvision(env.PROVISIONER, fetchRequest, answer)) ?? toResponse(answer);
  } catch (error) {
    console.error("panel request failed:", error instanceof Error ? error.message : String(error));
    return new Response("Internal Server Error", { status: 500 });
  }
}

export async function scheduledPanel(controller: ScheduledController, env: Env, ctx: ExecutionContext): Promise<void> {
  let panel;
  try {
    panel = await getPanel(env, "");
  } catch (error) {
    console.error("panel init failed:", error instanceof Error ? error.message : String(error));
    return;
  }
  try {
    const out = await panel.cron({ at: controller.scheduledTime });
    ctx.waitUntil(out.waitUntil);
  } catch (error) {
    console.error("panel cron failed:", error instanceof Error ? error.message : String(error));
  }
}
