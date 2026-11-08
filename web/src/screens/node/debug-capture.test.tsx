import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { dictionaries, setLang } from "@/i18n";
import { en } from "@/i18n/en";
import { clockLeft, secondsLeft } from "@/lib/debug-capture";
import { meQuery } from "@/lib/session";
import { DebugCaptureCard } from "./debug-capture";

// Node -> Server -> "Debug capture": the button and its choices, the countdown while the node captures, and the viewer of the
// records it sent (levels, source, search, the cursor). Only the owner starts, stops or reads one.

const ops = vi.hoisted(() => ({ listDebugCaptures: vi.fn(), startDebugCapture: vi.fn(), stopDebugCapture: vi.fn(), getDebugCapture: vi.fn() }));
vi.mock("@/lib/api", () => ({ ops, auth: { me: () => Promise.resolve({}) } }));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => {
  setLang("en");
  for (const f of Object.values(ops)) f.mockReset();
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  vi.useRealTimers();
});

const nowS = () => Math.floor(Date.now() / 1000);
const capture = (over: Record<string, unknown> = {}) => ({
  captureId: "req_1",
  nodeId: "nod_1",
  nodeName: "de1",
  state: "done",
  level: "debug",
  sources: ["xray"],
  durationS: 300,
  startedUnix: BigInt(nowS() - 600),
  endsUnix: BigInt(nowS() - 300),
  actor: "adm_1",
  records: 3,
  dropped: 0,
  endReason: "expired",
  ...over,
});
const rec = (seq: number, level: string, source: string, message: string, attrs: Record<string, string> = {}) => ({
  seq: BigInt(seq),
  timeUnixMs: BigInt(1_700_000_000_000 + seq),
  level,
  source,
  message,
  attrs,
});
const records = (over: Record<string, unknown> = {}) => ({
  capture: capture(),
  records: [rec(1, "debug", "xray", "tls handshake with <ip>:port", { code: "tls" }), rec(2, "info", "agent", "second line"), rec(3, "error", "xray", "boom")],
  nextCursor: 0n,
  sources: ["agent", "xray"],
  ...over,
});
const node = (over: Record<string, unknown> = {}) => ({ id: "nod_1", name: "de1", status: NodeStatus.ONLINE, ...over });

async function mount(n = node(), me: Record<string, unknown> = { admin: { id: "adm_1", role: Role.OWNER } }) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  qc.setQueryData(meQuery.queryKey, me as never);
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <DebugCaptureCard node={n as never} />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  await settle();
}
const settle = async () => {
  for (let i = 0; i < 3; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};
const text = () => document.body.textContent ?? "";
const button = (label: string) => [...document.querySelectorAll<HTMLElement>("button")].find((b) => b.textContent?.trim() === label);
const radio = (label: string) => [...document.querySelectorAll<HTMLElement>("[role=radio]")].find((b) => b.textContent?.trim() === label);
const click = async (el: Element | null | undefined) => {
  expect(el).toBeTruthy();
  await act(async () => void el?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
  await settle();
};
const label = (key: keyof typeof en, vars: Record<string, string | number> = {}) =>
  Object.entries(vars).reduce((s, [k, v]) => s.replace(`{${k}}`, String(v)), en[key]);

describe("the button", () => {
  it("starts a capture of the chosen sources, level and length", async () => {
    ops.listDebugCaptures.mockResolvedValue({ captures: [] });
    ops.startDebugCapture.mockResolvedValue({ capture: capture({ state: "running" }) });
    await mount();
    expect(text()).toContain(en["debug.none"]);
    expect(button(label("debug.button", { n: 5 }))).toBeTruthy(); // 5 minutes unless chosen otherwise
    await click(radio(label("debug.minutes", { n: 10 })));
    await click(radio(en["debug.level.info"]));
    await click(button("xray"));
    await click(button("hysteria2"));
    await click(button("hysteria2")); // picked and unpicked again
    await click(button(label("debug.button", { n: 10 })));
    expect(ops.startDebugCapture).toHaveBeenCalledWith({ nodeId: "nod_1", level: "info", sources: ["xray"], durationS: 600 });
  });

  it("sends every source (none picked) as an empty list", async () => {
    ops.listDebugCaptures.mockResolvedValue({ captures: [] });
    ops.startDebugCapture.mockResolvedValue({ capture: capture({ state: "running" }) });
    await mount();
    expect(text()).toContain(en["debug.sourcesAll"]);
    await click(button(label("debug.button", { n: 5 })));
    expect(ops.startDebugCapture).toHaveBeenCalledWith({ nodeId: "nod_1", level: "debug", sources: [], durationS: 300 });
  });

  it("says why a start was refused", async () => {
    ops.listDebugCaptures.mockResolvedValue({ captures: [] });
    ops.startDebugCapture.mockRejectedValue(new ConnectError("agent too old", Code.FailedPrecondition));
    await mount();
    await click(button(label("debug.button", { n: 5 })));
    expect(text()).toContain(en["err.agent_too_old"]);
  });

  it("does not fetch or show captures to a helper", async () => {
    ops.listDebugCaptures.mockResolvedValue({ captures: [capture()] });
    ops.getDebugCapture.mockResolvedValue(records());
    await mount(node(), { admin: { id: "adm_2", role: Role.HELPER } });
    expect(button(label("debug.button", { n: 5 }))).toBeUndefined();
    expect(text()).toContain(en["debug.ownerOnly"]);
    expect(text()).not.toContain("second line");
    expect(ops.listDebugCaptures).not.toHaveBeenCalled();
    expect(ops.getDebugCapture).not.toHaveBeenCalled();
  });

  it("is off while the node is not connected", async () => {
    ops.listDebugCaptures.mockResolvedValue({ captures: [] });
    await mount(node({ status: NodeStatus.DOWN }));
    expect(button(label("debug.button", { n: 5 }))!.hasAttribute("data-disabled")).toBe(true);
    expect(text()).toContain(en["debug.offline"]);
  });

  it("says so on the Cloudflare edition", async () => {
    ops.listDebugCaptures.mockRejectedValue(new ConnectError("not available on this edition", Code.Unimplemented));
    await mount();
    expect(text()).toContain(en["debug.unavailable"]);
  });
});

describe("while the node captures", () => {
  it("shows the countdown and stops on request", async () => {
    ops.listDebugCaptures.mockResolvedValue({ captures: [capture({ state: "running", endsUnix: BigInt(nowS() + 247), records: 0 })] });
    ops.stopDebugCapture.mockResolvedValue({ capture: capture() });
    await mount();
    const shown = /Capturing, (\d+:\d\d) left/.exec(text());
    expect(shown).toBeTruthy();
    const [m, s] = shown![1]!.split(":").map(Number);
    expect(m! * 60 + s!).toBeGreaterThan(240);
    expect(button(label("debug.button", { n: 5 }))!.hasAttribute("data-disabled")).toBe(true); // one at a time
    await click(button(en["debug.stop"]));
    expect(ops.stopDebugCapture).toHaveBeenCalledWith({ captureId: "req_1" });
  });

  it("says the records are on their way once the time is up", async () => {
    ops.listDebugCaptures.mockResolvedValue({ captures: [capture({ state: "running", endsUnix: BigInt(nowS() - 5), records: 0 })] });
    await mount();
    expect(text()).toContain(en["debug.shipping"]);
  });

  it("counts down in whole seconds", () => {
    expect(secondsLeft(1000, 999_000)).toBe(1);
    expect(secondsLeft(1000, 1_000_500)).toBe(0);
    expect(clockLeft(247)).toBe("4:07");
    expect(clockLeft(0)).toBe("0:00");
  });
});

describe("the records", () => {
  it("lists what the node sent, with the level, source and attributes", async () => {
    ops.listDebugCaptures.mockResolvedValue({ captures: [capture()] });
    ops.getDebugCapture.mockResolvedValue(records());
    await mount();
    expect(ops.getDebugCapture).toHaveBeenCalledWith(expect.objectContaining({ captureId: "req_1", minLevel: "", source: "", query: "", afterSeq: 0n }));
    expect(text()).toContain("tls handshake with <ip>:port");
    expect(text()).toContain("code=tls");
    expect(text()).toContain("boom");
    expect(text()).toContain(label("debug.by", { actor: "adm_1", sources: "xray" }));
  });

  it("asks the panel for the filter chosen", async () => {
    ops.listDebugCaptures.mockResolvedValue({ captures: [capture()] });
    ops.getDebugCapture.mockResolvedValue(records());
    await mount();
    await click(radio(en["debug.filter.warn"]));
    expect(ops.getDebugCapture).toHaveBeenLastCalledWith(expect.objectContaining({ minLevel: "warn" }));
    await click(radio(en["debug.filter.all"]));
    expect(ops.getDebugCapture).toHaveBeenLastCalledWith(expect.objectContaining({ minLevel: "" }));
  });

  it("searches the text after a short pause", async () => {
    ops.listDebugCaptures.mockResolvedValue({ captures: [capture()] });
    ops.getDebugCapture.mockResolvedValue(records());
    await mount();
    const input = document.querySelector<HTMLInputElement>(`input[aria-label="${en["debug.filter.search"]}"]`)!;
    await act(async () => {
      const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
      set.call(input, " handshake ");
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => void (await new Promise((r) => setTimeout(r, 400))));
    await settle();
    expect(ops.getDebugCapture).toHaveBeenLastCalledWith(expect.objectContaining({ query: "handshake" }));
  });

  it("walks the cursor with 'More records'", async () => {
    ops.listDebugCaptures.mockResolvedValue({ captures: [capture({ records: 4 })] });
    ops.getDebugCapture
      .mockResolvedValueOnce(records({ nextCursor: 3n }))
      .mockResolvedValueOnce(records({ records: [rec(4, "info", "agent", "fourth line")], nextCursor: 0n }));
    await mount();
    expect(text()).not.toContain("fourth line");
    await click(button(en["debug.more"]));
    expect(ops.getDebugCapture).toHaveBeenLastCalledWith(expect.objectContaining({ afterSeq: 3n }));
    expect(text()).toContain("fourth line");
    expect(text()).toContain("tls handshake"); // the first page stays
    expect(button(en["debug.more"])).toBeUndefined(); // no cursor left
  });

  it("explains a capture whose records never came, and what did not fit", async () => {
    ops.listDebugCaptures.mockResolvedValue({ captures: [capture({ state: "expired", records: 0, dropped: 12 })] });
    await mount();
    expect(text()).toContain(en["debug.expired"]);
    expect(text()).toContain(label("debug.dropped", { n: 12 }));
    expect(ops.getDebugCapture).not.toHaveBeenCalled();
  });

  it("says when nothing matches", async () => {
    ops.listDebugCaptures.mockResolvedValue({ captures: [capture()] });
    ops.getDebugCapture.mockResolvedValue(records({ records: [] }));
    await mount();
    expect(text()).toContain(en["debug.empty"]);
  });
});

describe("the words", () => {
  it("has every debug sentence in all five languages with the same placeholders", () => {
    const keys = Object.keys(en).filter((k) => k.startsWith("debug.") || k === "err.debug_busy" || k.startsWith("audit.node.debug_"));
    expect(keys.length).toBeGreaterThan(40);
    for (const lang of ["ru", "zh", "fa", "tr"] as const) {
      for (const k of keys) {
        const other = (dictionaries[lang] as Record<string, string>)[k];
        expect(other, `${lang} ${k}`).toBeTruthy();
        const holes = (s: string) => (s.match(/\{\w+\}/g) ?? []).sort().join();
        expect(holes(other!), `${lang} ${k}`).toBe(holes((en as Record<string, string>)[k]!));
      }
    }
  });
});
