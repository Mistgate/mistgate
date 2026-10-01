import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { fill, pickForm, setLang } from "@/i18n";
import { ru } from "@/i18n/ru";
import { firstRunSteps, FirstRunView, type Step } from "./first-run";

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, className }: { children: React.ReactNode; className?: string }) => <a className={className}>{children}</a>,
  useNavigate: () => () => {},
  useSearch: () => ({}),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  setLang("ru"); // the owner's language: the sentences below are his
});

const t = Object.assign((key: keyof typeof ru, vars?: Record<string, string | number>) => fill(ru[key], vars), {
  n: (key: keyof typeof ru, count: number, vars?: Record<string, string | number>) => fill(pickForm(ru[key], count, "ru"), { n: count, ...vars }),
});
const clock = (unix: number) => `@${unix}`;
const node = (name: string, status: NodeStatus, address = `${name}.example.com`, code?: string, params: Record<string, string> = {}) => ({
  id: `nod_${name}`,
  name,
  address,
  status,
  reason: code ? { code, params } : undefined,
});
const steps = (over: Partial<Parameters<typeof firstRunSteps>[2]> = {}) =>
  firstRunSteps(t as never, clock, { nodes: [], profiles: [], groups: [], users: 0, ...over });

describe("the first-run checklist", () => {
  it("on an empty panel asks for a node first and says what the server needs; later steps wait", () => {
    const s = steps();
    expect(s.map((x) => x.done)).toEqual([false, false, false, false]);
    expect(s[0]).toMatchObject({ title: "Нода подключена", text: "Сервер с Ubuntu или Debian и доступом root по SSH. Панель даст команду установки.", action: { run: { kind: "addNode" } } });
    expect(s[1]!.action).toMatchObject({ disabled: "Сначала подключи ноду" });
    expect(s[2]!.text).toContain("группу «Все»");
    expect(s[3]!.action).toMatchObject({ label: "Создать пользователя", run: { kind: "createUser" } });
  });

  it("says how long a pending node's command works, or that it expired, and offers a new one", () => {
    const pending = steps({ nodes: [node("de1", NodeStatus.PENDING, "de1.example.com", "enrollment_pending", { expires_unix: "1790000000" })] });
    expect(pending[0]).toMatchObject({ done: false, text: "de1 ждёт установки · команда действует до @1790000000", action: { run: { kind: "newCommand", id: "nod_de1" } } });
    const expired = steps({ nodes: [node("de1", NodeStatus.PENDING, "de1.example.com", "enrollment_expired")] });
    expect(expired[0]!.text).toBe("de1 ждёт установки · команда истекла");
  });

  it("points the profile at the node that has none and tells an IP node about the certificate", () => {
    const ip = steps({ nodes: [node("nl1", NodeStatus.ONLINE), node("de1", NodeStatus.ONLINE, "203.0.113.10", "no_profiles")] });
    expect(ip[0]).toMatchObject({ done: true, text: "2 ноды на связи" });
    expect(ip[1]!.action).toMatchObject({ label: "Добавить профиль на de1", run: { kind: "profiles", id: "nod_de1" } });
    expect(ip[1]!.text).toContain("У de1 адрес — IP: для Hysteria2 выбери самоподписанный сертификат");
    const domain = steps({ nodes: [node("de1", NodeStatus.ONLINE, "de1.example.com", "no_profiles")] });
    expect(domain[1]!.text).toContain("Let’s Encrypt выдаёт Hysteria2 сертификат только на домен");
  });

  it("a node that went down after it connected keeps its step done: the list does not jump back", () => {
    expect(steps({ nodes: [node("de1", NodeStatus.DOWN, "de1.example.com", "agent_silent")] })[0]!.done).toBe(true);
  });

  it("is done step by step from what the panel has: a deployed profile, a group with it, a user", () => {
    const s = steps({
      nodes: [node("de1", NodeStatus.ONLINE)],
      profiles: [{ id: "prf_1", name: "Hysteria2 · 443", nodeCount: 1 }, { id: "prf_2", name: "AWG 3.1", nodeCount: 0 }],
      groups: [{ name: "Все", profileIds: ["prf_1", "prf_2"] }],
      users: 2,
    });
    expect(s.map((x) => x.done)).toEqual([true, true, true, true]);
    expect(s.map((x) => x.text)).toEqual(["de1 на связи", "«Hysteria2 · 443» на 1 ноде", "«Все»: 1 профиль", "2 пользователя"]);
    // a group that only holds profiles on no node gives nobody anything: not done
    expect(steps({ profiles: [{ id: "prf_2", name: "AWG 3.1", nodeCount: 0 }], groups: [{ name: "Все", profileIds: ["prf_2"] }] })[2]!.done).toBe(false);
  });
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
});

async function mount(s: Step[], onHide = () => {}) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root!.render(<FirstRunView steps={s} onHide={onHide} />));
}

describe("the checklist on screen", () => {
  it("shows the progress, and the main button only on the first open step", async () => {
    await mount(steps({ nodes: [node("de1", NodeStatus.ONLINE, "de1.example.com", "no_profiles")] }));
    const text = document.body.textContent ?? "";
    expect(text).toContain("1 из 4");
    const add = [...document.querySelectorAll("a")].find((a) => a.textContent === "Добавить профиль на de1")!;
    const groups = [...document.querySelectorAll("a")].find((a) => a.textContent === "Открыть группы")!;
    expect(add.className).toContain("bg-accent");
    expect(groups.className).not.toContain("bg-accent");
  });

  it("folds into one line once everything is done, and hides on request", async () => {
    const onHide = vi.fn();
    const done = steps({
      nodes: [node("de1", NodeStatus.ONLINE)],
      profiles: [{ id: "prf_1", name: "p", nodeCount: 1 }],
      groups: [{ name: "Все", profileIds: ["prf_1"] }],
      users: 1,
    });
    await mount(done, onHide);
    expect(document.querySelectorAll("li")).toHaveLength(0);
    expect(document.body.textContent).toContain("Всё готово");
    await act(async () => void [...document.querySelectorAll("button")].find((b) => b.textContent === "Скрыть")!.click());
    expect(onHide).toHaveBeenCalledOnce();
  });
});
