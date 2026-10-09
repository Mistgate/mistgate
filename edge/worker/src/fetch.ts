import type { Env } from "./env";
import { getPanel } from "./panel";
import { forwardLink, panelURL, toFetchRequest, toResponse } from "./shell";

export async function fetchPanelRequest(request: Request, env: Env, ctx: ExecutionContext): Promise<Response> {
  let panel;
  try {
    panel = await getPanel(env, new URL(panelURL(request.url)).origin);
  } catch (error) {
    console.error("panel init failed:", error instanceof Error ? error.message : String(error));
    return new Response("Service Unavailable", { status: 503, headers: { "Retry-After": "5" } });
  }
  try {
    const answer = await panel.fetch(await toFetchRequest(request));
    ctx.waitUntil(answer.waitUntil);
    return (await forwardLink(env.NODELINK, request, answer)) ?? toResponse(answer);
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
