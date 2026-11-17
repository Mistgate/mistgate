import { QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { setLang } from "@/i18n";
import { nodePageData } from "./dev-kit-node-page";
import { seedWays, waysFrames, waysSceneOf } from "./dev-kit-node-ways";
import { useWays, WaysTab } from "./node/ways";

// The scenes of the Ways in tab (/dev-kit?only=nodepage&tab=ways) draw the real tab on made-up data: every scene must draw, in every
// language, and say what it is about. The calls a click would make never answer (the cache is the scene).

const search: { current: Record<string, string> } = { current: {} };
vi.mock("@/lib/api", () => {
  const never = () => new Proxy({}, { get: () => () => new Promise(() => {}) });
  return new Proxy({ basepath: "/" } as Record<string, unknown>, { get: (t, k) => (k in t ? t[k as string] : never()) });
});
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, className }: { children?: ReactNode; className?: string }) => <a className={className}>{children}</a>,
  useBlocker: () => ({ status: "idle" }),
  useSearch: () => search.current,
  useNavigate: () => vi.fn(),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  Element.prototype.scrollIntoView ??= () => {};
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  localStorage.clear();
  setLang("en");
});

function Tab({ data }: { data: never }) {
  const w = useWays(data);
  return <WaysTab data={data} ways={w} />;
}

async function draw(q: string, state: string) {
  const p = new URLSearchParams(q);
  search.current = { tab: "ways", ...Object.fromEntries(p) };
  const { qc, data } = nodePageData(state as never);
  const d = seedWays(qc, data as never, waysSceneOf(p), data.node.status);
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <Tab data={d as never} />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 6; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
  return host.textContent ?? "";
}

describe("the scenes of the Ways in tab", () => {
  it.each(waysFrames.map((f) => [f.id, f] as const))("%s draws", async (_id, f) => {
    const text = await draw(f.q, f.state ?? "calm");
    expect(text).not.toContain("undefined");
    expect(text).not.toMatch(/\bwy\.[a-z]/);
    expect(text).toContain(f.q.includes("role=entry") ? "Whom it carries" : "How people get here");
  });

  it("says what each state of the cascade is", async () => {
    const say = async (q: string) => draw(`w=1&way=via:nod_msk1&${q}`, "calm");
    expect(await say("")).toContain("Forwarding");
    expect(await say("")).toContain("msk1 connects to de1 itself");
    act(() => root?.unmount());
    host?.remove();
    expect(await say("cas=br")).toContain("de1 connects to msk1 itself and keeps the line open");
    act(() => root?.unmount());
    host?.remove();
    expect(await say("cas=full")).toContain("New people are refused over the bridge");
    act(() => root?.unmount());
    host?.remove();
    expect(await say("cas=nobr")).toContain("update the agent of de1");
    act(() => root?.unmount());
    host?.remove();
    expect(await say("cause=fw")).toContain("Allow forwarding");
  });

  it("draws the direct way's ports as one table with the switches of the subscriptions", async () => {
    const text = await draw("w=1", "calm");
    expect(text).toContain("Servers and ports");
    expect(text).toContain("udp/443");
    expect(text).toContain("tcp/80");
    expect(host!.querySelectorAll("[data-testid=port-row] [role=switch]").length).toBeGreaterThan(0);
  });

  it.each(["ru", "zh", "fa", "tr"] as const)("draws an exit and an entry in %s with every word translated", async (lang) => {
    setLang(lang);
    for (const q of ["w=1&way=via:nod_msk1&cas=br", "w=1&role=entry&cas=full&way=out:nod_nl1"]) {
      const text = await draw(q, "calm");
      expect(text).not.toMatch(/\b(wy|nw|ways|relay)\.[a-z]/);
      act(() => root?.unmount());
      host?.remove();
    }
  });
});
