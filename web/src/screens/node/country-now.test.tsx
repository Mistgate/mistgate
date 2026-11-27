import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { CheckStatus } from "@/gen/mistgate/admin/v1/health_pb";
import { currentLang, dictionaries, fill, langs, pickForm, setLang, type Lang, type T } from "@/i18n";
import { nowLook, nowToast, type CountryNow } from "@/lib/country-now";
import { setEffectsEnabled } from "@/lib/effects";
import { nowKinds, nowRoundOf, ruChecksOf, type NowKind } from "@/screens/dev-kit-node-ru";
import { CountryNowNote } from "./country-now";
import { NodeChecks } from "./checks";

const getChecks = vi.fn();
const runChecksNow = vi.fn();
vi.mock("@/lib/api", () => ({
  health: { getChecks: (...a: unknown[]) => getChecks(...a), runChecksNow: (...a: unknown[]) => runChecksNow(...a) },
  auth: { me: () => Promise.resolve({ admin: { id: "adm_1", role: 1 } }) },
  isUnauthenticated: () => false,
}));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, className }: { children?: ReactNode; to: string; className?: string }) => (
    <a href={to} className={className}>
      {children}
    </a>
  ),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

const NOW = 1_800_000_000;
let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => {
  vi.spyOn(Date, "now").mockReturnValue(NOW * 1000);
  setLang("en");
  setEffectsEnabled(false);
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  vi.restoreAllMocks();
  getChecks.mockReset();
  runChecksNow.mockReset();
  vi.useRealTimers();
});

/** The translator of a render, outside one. */
const tr: T = Object.assign((key: Parameters<T>[0], vars?: Record<string, string | number>) => fill(dictionaries[currentLangOf()][key], vars), {
  n: (key: Parameters<T>[0], count: number, vars?: Record<string, string | number>) => fill(pickForm(dictionaries[currentLangOf()][key], count, currentLangOf()), { n: count, ...vars }),
});
const currentLangOf = (): Lang => currentLang();
const round = (kind: NowKind) => nowRoundOf(kind, NOW)[0] as unknown as CountryNow;
const mountNote = async (now: CountryNow | undefined) => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(<CountryNowNote now={now} />));
};
const line = (id: string) => host!.querySelector(`[data-line=${id}]`)?.textContent?.replaceAll(" ", " ");
const lines = () => [...host!.querySelectorAll("[data-line]")].map((l) => (l as HTMLElement).dataset.line);

describe("what the country check says of a round asked by 'Check now' (CountryNowNote)", () => {
  it("running: what is rechecked, what is left out and why, what is read again, the Globalping tests; no 'next' while it runs", async () => {
    await mountNote(round("running"));
    expect(host!.querySelector("[data-testid=country-now]")?.getAttribute("data-state")).toBe("running");
    expect(lines()).toEqual(["running", "skipped", "reread", "gp"]);
    expect(line("running")).toBe("Rechecking now: Globalping, TSPU probe, cheburcheck, Own probes");
    expect(line("skipped")).toBe("Not rechecked: cheburcheck, own probe — off, bsbord — on its schedule only");
    expect(line("reread")).toBe("Read again from what is stored: Block lists, kl!ck reports");
    expect(line("gp")).toBe("Globalping: 140 tests left this hour for rechecks by hand after the planned rounds, a round of this node costs 12");
  });

  it("done: what was rechecked and when, the own probes' job that waits for its answer, the next time", async () => {
    await mountNote(round("done"));
    expect(host!.querySelector("[data-testid=country-now]")?.getAttribute("data-state")).toBe("idle");
    expect(line("done")).toBe("Rechecked 3 min ago: Globalping, TSPU probe, cheburcheck");
    expect(line("queued")).toBe("Own probes: asked, the answer comes by itself within 15 minutes");
    expect(line("next")).toBe("Next recheck in 7 min");
  });

  it("too early: every source skipped for it, and when the next recheck may be asked", async () => {
    await mountNote(round("early"));
    expect(lines()).toEqual(["skipped", "gp", "next"]);
    expect(line("skipped")).toContain("Globalping — asked a moment ago");
    expect(line("skipped")).toContain("kl!ck reports — asked a moment ago");
    expect(line("next")).toBe("Next recheck in 7 min");
  });

  it("no tests left: Globalping is left out with the reason, the others went on", async () => {
    await mountNote(round("nobudget"));
    expect(line("skipped")).toContain("Globalping — not enough tests left this hour");
    expect(line("done")).toBe("Rechecked 3 min ago: TSPU probe, cheburcheck");
    expect(line("gp")).toContain("4 tests left");
  });

  it("a source that did not work says why, one on hold and silent probes are left out", async () => {
    await mountNote(round("failed"));
    expect(line("failed")).toBe("Did not work: TSPU probe — the probe's control site did not open, cheburcheck — ran out of time");
    expect(line("skipped")).toContain("cheburcheck, own probe — paused after a refusal");
    expect(line("skipped")).toContain("Own probes — its probes are silent");
  });

  it("nothing could be asked: the reasons, no time of a round, no next time", async () => {
    await mountNote(round("off"));
    expect(lines()).toEqual(["skipped", "reread"]);
    expect(line("skipped")).toContain("TSPU probe — this node is the probe node itself");
    expect(line("skipped")).toContain("Own probes — no probe is online");
    expect(line("reread")).toBe("Read again from what is stored: kl!ck reports");
  });

  it("half way: one source still runs, the others have answered", async () => {
    await mountNote(round("partial"));
    expect(line("running")).toBe("Rechecking now: Globalping, TSPU probe, cheburcheck");
  });

  it("nothing is said while nobody asked, nor about a round of last week", async () => {
    await mountNote(undefined);
    expect(host!.querySelector("[data-testid=country-now]")).toBeNull();
    const old = { ...round("done"), askedUnix: NOW - 7 * 86400, nextUnix: 0 };
    await act(async () => root!.render(<CountryNowNote now={old} />));
    expect(host!.querySelector("[data-testid=country-now]")).toBeNull();
  });

  it("the radar belongs to a round that runs: only 'running' and 'partial' say so", () => {
    expect(nowKinds.filter((k) => nowLook(round(k), NOW)?.running)).toEqual(["running", "partial"]);
  });

  it.each(langs)("%s: every state is worded, with no key and no brace left", async (lang: Lang) => {
    setLang(lang);
    for (const kind of nowKinds) {
      await mountNote(round(kind));
      const said = host!.textContent!;
      expect(said.length, kind).toBeGreaterThan(20);
      expect(said, kind).not.toMatch(/[{}]|\bcn\.|\bcr\.src\./);
      act(() => root?.unmount());
      host?.remove();
      root = null;
    }
    setLang("en");
  });

  it("the words of the toast: when the next recheck may be asked, how many sources are asked, nothing to ask", () => {
    expect(nowToast(tr, round("early"))).toBe("The country check was asked a moment ago: the next recheck in 7 min");
    expect(nowToast(tr, round("running"))).toBe("Country check: 4 sources are being rechecked");
    expect(nowToast(tr, round("off"))).toBe("Nothing to recheck from the country now");
    expect(nowToast(tr, undefined)).toBe("");
    setLang("ru");
    expect(nowToast(tr, round("running"))).toBe("Из страны перепроверяются 4 источника");
    expect(nowToast(tr, round("early"))).toContain("через 7 мин");
  });

  it("every reason the panel can give has its words in every language", () => {
    const reasons = ["off", "too_early", "no_budget", "budget_unknown", "rate_limited", "no_probe", "probe_silent", "probe_node", "busy", "held", "node_offline", "no_address", "no_ports", "schedule_only", "interrupted", "error", "timeout", "control_failed", "no_dpi_hop"];
    for (const lang of langs) for (const r of reasons) expect(dictionaries[lang][`cn.reason.${r}` as never], `${lang} ${r}`).toBeTruthy();
  });
});

// ---------------------------------------------------------------------------------------------------
// The Overview card: the radar and the lines follow the round of the country, not the profile checks

const history = Array.from({ length: 48 }, (_, i) => ({ startUnix: NOW - (48 - i) * 1800, ok: 1, failed: 0, latencyMs: 40 }));
const cell = (at: number) => ({
  deployed: true, inboundId: "inb_1", failStreak: 0, history,
  last: { status: CheckStatus.OK, atUnix: at, latencyMs: 31, exitIp: "", exitCountry: "", errorCode: "", errorDetail: "" },
});
const checksOf = (now: CountryNow[], at = NOW - 120) => ({
  nowUnix: NOW, intervalS: 300,
  columns: [{ profileId: "prf_1", profileName: "hy2", protocol: "hysteria2" }],
  rows: [{ nodeId: "nod_de1", nodeName: "de1", countryCode: "DE", nodeStatus: NodeStatus.ONLINE, cells: [cell(at)] }],
  countryChecks: ruChecksOf("ok", NOW),
  countryNow: now,
});

let qc: QueryClient;
async function mountCard(inbound = { id: "inb_1", port: 443, publicPort: 0, cdn: false, protocol: "hysteria2" }) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <NodeChecks nodeId="nod_de1" inbounds={[inbound]} />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 4; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}
const ru = () => host!.querySelector<HTMLElement>("[data-testid=node-ru]")!;
const press = async () => {
  await act(async () => void [...host!.querySelectorAll("button")].find((b) => b.textContent === "Check now")?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
  for (let i = 0; i < 4; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};

describe("the Overview card of the node: 'Check now'", () => {
  it.each([[false, "tcp/443"], [true, "CDN"]] as const)("labels the tested route (CDN: %s)", async (cdn, label) => {
    getChecks.mockResolvedValue({ ...checksOf([]), columns: [{ profileId: "prf_1", profileName: "Tokyo", protocol: "vless" }] });
    await mountCard({ id: "inb_1", port: 19443, publicPort: cdn ? 0 : 443, cdn, protocol: "vless" });
    const row = host!.querySelector(".nd-pr button");
    expect(row?.textContent).toContain(label);
    expect(row?.textContent).not.toContain("19443");
  });

  it("a press that runs only the profiles does not start the radar: nothing is asked of the country", async () => {
    setEffectsEnabled(true);
    getChecks.mockResolvedValue(checksOf([]));
    runChecksNow.mockResolvedValue({ scheduled: 1, skipped: 0 });
    await mountCard();
    await press();
    expect(runChecksNow).toHaveBeenCalledWith({ nodeId: "nod_de1" });
    expect(ru().classList.contains("now")).toBe(false);
    expect(host!.querySelector(".nd-pr.now")).not.toBeNull(); // the profile rows are being looked at
  });

  it("a press that asks the country starts the radar and says what is rechecked and what is not; the result is picked up without a reload", async () => {
    setEffectsEnabled(true);
    getChecks.mockResolvedValue(checksOf([]));
    runChecksNow.mockImplementation(async () => {
      getChecks.mockResolvedValue(checksOf([round("running")]));
      return { scheduled: 1, skipped: 0, countryNow: round("running") };
    });
    await mountCard();
    expect(host!.querySelector("[data-testid=country-now]")).toBeNull();
    await press();
    expect(ru().classList.contains("now")).toBe(true);
    expect(host!.querySelector("[data-line=running]")?.textContent).toContain("Rechecking now: Globalping");
    expect(host!.querySelector("[data-line=skipped]")?.textContent).toContain("bsbord — on its schedule only");

    // the panel finishes the round: the next read has it done, the radar goes out and the lines say what came
    getChecks.mockResolvedValue(checksOf([round("done")]));
    await act(async () => void (await qc.invalidateQueries({ queryKey: ["health", "checks"] })));
    for (let i = 0; i < 3; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
    expect(ru().classList.contains("now")).toBe(false);
    expect(host!.querySelector("[data-line=done]")?.textContent).toContain("Rechecked 3 min ago");
    expect(host!.querySelector("[data-line=next]")?.textContent).toBe("Next recheck in 7 min");
  });

  it("a press that comes too early says when the next recheck may be asked, with no radar", async () => {
    setEffectsEnabled(true);
    getChecks.mockResolvedValue(checksOf([round("done")]));
    runChecksNow.mockResolvedValue({ scheduled: 0, skipped: 0, countryNow: round("early") });
    await mountCard();
    await press();
    expect(ru().classList.contains("now")).toBe(false);
    expect(document.body.textContent).toContain("The country check was asked a moment ago: the next recheck in 7 min");
  });

  it("shows the lines of a round that ran before the page was opened", async () => {
    getChecks.mockResolvedValue(checksOf([round("nobudget")]));
    await mountCard();
    expect(host!.querySelector("[data-line=skipped]")?.textContent).toContain("Globalping — not enough tests left this hour");
  });
});

describe("the verdict that the panel sent is unchanged by the round", () => {
  it("the country verdict is still the one of the check", async () => {
    getChecks.mockResolvedValue(checksOf([round("running")]));
    await mountCard();
    expect(ru().dataset.kind).toBe("ok");
  });
});
