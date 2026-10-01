import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { beforeAll, describe, expect, it, vi } from "vitest";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { AlertSeverity } from "@/gen/mistgate/admin/v1/health_pb";
import { fill, pickForm, setLang } from "@/i18n";
import { ru } from "@/i18n/ru";
import type { Alert } from "@/lib/health";
import { HealthStrip, stripView } from "./strip";

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, className }: { children: React.ReactNode; className?: string }) => <a className={className}>{children}</a>,
  useNavigate: () => () => {},
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  setLang("ru");
});

// Russian on purpose: these are the sentences the owner reads.
const t = Object.assign((key: keyof typeof ru, vars?: Record<string, string | number>) => fill(ru[key], vars), {
  n: (key: keyof typeof ru, count: number, vars?: Record<string, string | number>) => fill(pickForm(ru[key], count, "ru"), { n: count, ...vars }),
});
const clock = (unix: number) => `@${unix}`;
const NOW = 1_790_000_000;

let seq = 0;
const node = (name: string, status: NodeStatus, code?: string, params: Record<string, string> = {}) =>
  ({ id: `nod_${++seq}`, name, countryCode: "", location: "", provider: "", status, reason: code ? { code, params } : undefined, online: [], downBps: 0, upBps: 0, hasMetrics: false, cpuPct: 0, sparkBytes: [] }) as never;
const view = (cards: unknown[], over: Partial<Parameters<typeof stripView>[2]> = {}) =>
  stripView(t as never, clock, { cards: cards as never, usersOnline: 0, alertCount: 0, criticalCount: 0, alerts: [], now: NOW * 1000, ...over });

describe("the Overview strip", () => {
  it("is grey while the only node waits for its install, says until when the command works and offers a new one", () => {
    const v = view([node("de1", NodeStatus.PENDING, "enrollment_pending", { expires_in_minutes: "40", expires_unix: String(NOW + 2400) })]);
    expect(v.kind).toBe("off");
    expect(v.headline).toBe(`de1 ждёт установки · команда действует до @${NOW + 2400}`);
    expect(v.target).toMatchObject({ name: "de1", action: "newCommand" });
    const expired = view([node("de1", NodeStatus.PENDING, "enrollment_expired")]);
    expect(expired.headline).toBe("de1 ждёт установки · команда истекла");
    expect(expired.detail).toBe("Сделай новую команду установки и вставь её на сервере от root.");
  });
  it("counts only the nodes that really serve in 'up' and puts the rest in a tail", () => {
    expect(view([node("de1", NodeStatus.ONLINE), node("fi1", NodeStatus.ONLINE)]).headline).toBe("Все 2 ноды работают");
    expect(view([node("de1", NodeStatus.ONLINE)]).headline).toBe("Нода de1 работает");
    const mixed = view([node("de1", NodeStatus.ONLINE), node("fi1", NodeStatus.ONLINE), node("nl1", NodeStatus.PENDING, "enrollment_pending"), node("de2", NodeStatus.BLIP, "host_blip")]);
    expect(mixed.kind).toBe("ok");
    expect(mixed.headline).toBe("2 ноды работают · nl1 ждёт установки · de2: хостер моргнул");
  });
  it("names the problems with one word and the number of the header: the alerts or the problem nodes, whichever is more", () => {
    const v = view([node("de1", NodeStatus.ONLINE, "no_profiles"), node("de2", NodeStatus.ONLINE)]);
    expect(v.kind).toBe("warn");
    expect(v.headline).toBe("1 проблема: de1");
    expect(v.detail).toBe("de1: нет профилей — пользователи её не получат");
    expect(v.target).toMatchObject({ name: "de1", action: "profiles" }); // the next step is a profile, not the node page

    const alert = { nodeId: "nod_x", nodeName: "nl1", kind: 0, severity: AlertSeverity.CRITICAL, params: {}, titleKey: "", whyKey: "" } as unknown as Alert;
    const down = node("nl1", NodeStatus.DOWN, "agent_silent", { minutes: "3065" });
    const both = view([down, node("de1", NodeStatus.ONLINE, "no_profiles")], { alertCount: 3, criticalCount: 1, alerts: [{ ...alert, nodeId: (down as { id: string }).id }] });
    expect(both.kind).toBe("bad");
    expect(both.headline).toBe("3 проблемы: nl1, de1");
    expect(both.detail).toContain("de1: нет профилей");
    expect(both.problems).toBe(3);
  });
  it("leaves the onboarding steps to the first-run list above it, and keeps the rest", async () => {
    const strip = async (cards: unknown[], onboarding: boolean) => {
      const host = document.createElement("div");
      document.body.append(host);
      const root: Root = createRoot(host);
      await act(async () =>
        root.render(
          <QueryClientProvider client={new QueryClient()}>
            <HealthStrip cards={cards as never} usersOnline={0} alertCount={0} criticalCount={0} onboarding={onboarding} />
          </QueryClientProvider>,
        ),
      );
      const out = { text: host.textContent ?? "", buttons: [...host.querySelectorAll("a, button")].map((b) => b.textContent) };
      act(() => root.unmount());
      host.remove();
      return out;
    };
    const waiting = [node("de1", NodeStatus.PENDING, "enrollment_pending", { expires_unix: String(Math.floor(Date.now() / 1000) + 600) })];
    expect((await strip(waiting, false)).buttons).toEqual(["Новая команда установки"]);
    expect((await strip(waiting, true)).text).toBe(""); // the list says the same, with the same button
    const empty = [node("de1", NodeStatus.ONLINE, "no_profiles")];
    expect((await strip(empty, false)).buttons).toEqual(["Добавить профиль на de1"]);
    const quiet = await strip(empty, true);
    expect(quiet.text).toContain("1 проблема: de1"); // the problem is still said
    expect(quiet.buttons).toEqual([]);
    const down = [node("nl1", NodeStatus.DOWN, "agent_silent", { minutes: "30" })];
    expect((await strip(down, true)).buttons).toEqual(["Открыть nl1"]); // a fault is not onboarding
  });

  it("tells a partly working node from a broken one in the detail", () => {
    const v = view([node("de2", NodeStatus.ONLINE, "inbound_failed", { profile: "hy2 · WARP · 8443", error: "bind: address already in use", failed: "1", total: "3" })]);
    expect(v.kind).toBe("bad");
    expect(v.detail).toBe("de2: не запустился «hy2 · WARP · 8443»: bind: address already in use");
    expect(v.target).toMatchObject({ action: "open" });
  });
});
