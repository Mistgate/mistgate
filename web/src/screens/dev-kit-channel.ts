import { probe } from "@/lib/api";
import { channelDemo } from "./node/channel-measure";

// Development only. The states of "Measure the channel" (screens/node/channel-measure.tsx) on the Server tab, section Channel:
// /dev-kit?only=nodepage-frame&tab=server&sv=ch&cm=<state>. Each opens the window (or not) in the state a click would bring it to; the
// panel calls are stubbed with the scene's answers, so nothing leaves the page. Made-up nodes de1 (the one measured), nl1, se1.
//   confirm | none | old | offline | soon | busy | running | done | same | partial | capped | failed | canceled | last
export const channelStates = ["confirm", "none", "old", "offline", "soon", "busy", "running", "done", "same", "partial", "capped", "failed", "canceled", "last"] as const;
export type ChannelState = (typeof channelStates)[number];

const NOW = () => Math.floor(Date.now() / 1000);
const dir = (mbitS: number, partner: string, capped = false) => ({ mbitS, partner, streams: mbitS > 0 ? 8 : 0, capped });
const step = (partner: string, direction: string, state: string, mbitS = 0, error = "", capped = false) => ({ partner, direction, state, mbitS, error, streams: state === "done" ? 8 : 0, capped });
const partners = [
  { name: "nl1", knownMbps: 2000 },
  { name: "se1", knownMbps: 1000 },
  { name: "fi1", knownMbps: 0 },
];
const plan = (over: Record<string, unknown> = {}) => ({ nodeName: "de1", error: "", partners, configuredMbps: 250, maxMb: 2048, maxGb: 12, estSeconds: 72, retryAfterS: 0, last: undefined, ...over });
const last = (down: number, up: number, cap = false) => ({ atUnix: BigInt(NOW() - 3 * 3600), down: dir(down, "nl1"), up: dir(up, "se1", cap), partners: 3 });

export function channelStateOf(v: string | null): ChannelState | undefined {
  return (channelStates as readonly string[]).includes(v ?? "") ? (v as ChannelState) : undefined;
}

/** Installs the scene: the stubs of the four calls and what the window starts as. Returns the node's own channel number the scene wants. */
export function channelScene(state: ChannelState): { configured?: number } {
  const t0 = NOW();
  const idle = { nodeName: "de1", state: "idle", error: "", retryAfterS: 0, startedUnix: 0n, steps: [] as unknown[], result: undefined as unknown, last: undefined as unknown, configuredMbps: 250 };
  const result = (down: number, up: number, cap = false) => ({ atUnix: BigInt(t0), down: dir(down, "nl1"), up: dir(up, "se1", cap), partners: 3 });
  const finished = (down: number, up: number, cap = false) => ({
    ...idle,
    state: "done",
    startedUnix: BigInt(t0 - 74),
    steps: [step("nl1", "down", "done", down), step("nl1", "up", "done", up * 0.9), step("se1", "down", "done", down * 0.8), step("se1", "up", "done", up, "", cap), step("fi1", "down", "done", down * 0.5), step("fi1", "up", "done", up * 0.4)],
    result: result(down, up, cap),
    last: result(down, up, cap),
  });
  let p: Record<string, unknown> = plan();
  let status: Record<string, unknown> = { ...idle };
  let demo: { open?: boolean; asked?: boolean } = {};
  let configured: number | undefined;
  switch (state) {
    case "confirm":
      demo = { open: true };
      break;
    case "none":
      p = plan({ error: "no_partner", partners: [] });
      demo = { open: true };
      break;
    case "old":
      p = plan({ error: "agent_too_old", partners: [] });
      demo = { open: true };
      break;
    case "offline":
      p = plan({ error: "node_offline", partners: [] });
      demo = { open: true };
      break;
    case "soon":
      p = plan({ error: "too_soon", retryAfterS: 421, partners: partners.slice(0, 2) });
      status = { ...idle, last: last(812.4, 455.1) };
      demo = { open: true };
      break;
    case "busy":
      p = plan({ error: "busy", partners: [] });
      demo = { open: true };
      break;
    case "running":
      status = {
        ...idle,
        state: "running",
        startedUnix: BigInt(t0 - 31),
        steps: [step("nl1", "down", "done", 812.4), step("nl1", "up", "done", 455.1), step("se1", "down", "done", 612.9), step("se1", "up", "running"), step("fi1", "down", "waiting"), step("fi1", "up", "waiting")],
        last: last(700, 400),
      };
      demo = { open: true, asked: true };
      break;
    case "done":
      status = finished(812.4, 455.1);
      demo = { open: true, asked: true };
      break;
    case "same":
      status = finished(812.4, 455.1);
      demo = { open: true, asked: true };
      configured = 450;
      break;
    case "partial":
      status = { ...finished(812.4, 0), result: result(812.4, 0), last: result(812.4, 0) };
      status.steps = [step("nl1", "down", "done", 812.4), step("nl1", "up", "failed", 0, "timeout"), step("se1", "down", "done", 600), step("se1", "up", "failed", 0, "timeout")];
      demo = { open: true, asked: true };
      break;
    case "capped":
      status = finished(8589.9, 4200, true);
      demo = { open: true, asked: true };
      configured = 2500;
      break;
    case "failed":
      status = { ...idle, state: "failed", error: "unreachable", startedUnix: BigInt(t0 - 9), steps: [step("nl1", "down", "failed", 0, "unreachable"), step("nl1", "up", "failed", 0, "unreachable"), step("se1", "down", "failed", 0, "unreachable"), step("se1", "up", "failed", 0, "unreachable")] };
      demo = { open: true, asked: true };
      break;
    case "canceled":
      status = { ...idle, state: "canceled", startedUnix: BigInt(t0 - 20), steps: [step("nl1", "down", "done", 812.4), step("nl1", "up", "failed", 0, "canceled"), step("se1", "down", "failed", 0, "canceled")] };
      demo = { open: true, asked: true };
      break;
    case "last":
      status = { ...idle, last: last(812.4, 455.1) };
      break;
  }
  const stub = probe as unknown as Record<string, unknown>;
  stub.channelMeasurePlan = async () => p;
  stub.channelMeasureStatus = async () => status;
  stub.channelMeasureStart = async () => ({ status });
  stub.channelMeasureCancel = async () => ({ status: { ...idle, state: "canceled" } });
  channelDemo.current = demo;
  return { configured };
}
