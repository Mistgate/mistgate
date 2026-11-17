import { QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { setLang } from "@/i18n";
import { seeded, type State } from "./dev-kit-overview";
import { OverviewFailure, OverviewLoading, OverviewScreen } from "./overview";

// The Overview on the data of the dev-kit scenes (the board's demo data): what each state shows, in Russian, as the owner reads it.

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, params, className, title }: { children?: ReactNode; to: string; params?: Record<string, string>; className?: string; title?: string }) => (
    <a href={to.replace(/\$(\w+)/g, (_, k: string) => params?.[k] ?? "")} className={className} title={title}>
      {children}
    </a>
  ),
  useNavigate: () => () => {},
  useSearch: () => ({}),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  setLang("ru");
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
});

async function mount(state: State) {
  act(() => root?.unmount());
  host?.remove();
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () =>
    root!.render(
      <QueryClientProvider client={seeded(state)}>
        <ToastProvider>
          <OverviewScreen />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
}
const text = () => host!.textContent ?? "";
const buttons = () => [...host!.querySelectorAll("a, button")].map((b) => b.textContent ?? "");

describe("the Overview, a calm fleet", () => {
  it("says how many nodes are on the line, the three numbers, and that all is calm", async () => {
    await mount("calm");
    expect(host!.querySelector("h2")!.textContent).toBe("4 ноды на связи, всё спокойно");
    expect(text()).toContain("Флот работает");
    expect(text()).toContain("Сейчас онлайн");
    expect(text()).toContain("из 8");
    expect(text()).toContain("Скорость сейчас");
    expect(text()).toContain("Трафик за 24 часа");
    expect(text()).toContain("Панель проверяет ноды каждые 5 мин");
    expect(text()).not.toContain("Ждёт тебя 1");
  });

  it("lists the nodes with their checks, people and speed, and the people who download", async () => {
    await mount("calm");
    const rows = [...host!.querySelectorAll("a.ov-nr")];
    expect(rows.map((r) => r.querySelector("b")?.textContent)).toEqual(["de1", "fi1", "nl1", "msk1"]);
    expect(rows[0]!.querySelectorAll(".ov-tk > i")).toHaveLength(24);
    expect(rows[0]!.textContent).toContain("3 онлайн");
    expect(rows[0]!.textContent).toContain("↓ 64 Мбит/с");
    expect(text()).toContain("Сейчас качают 6");
    expect(text()).toContain("alice");
  });

  it("draws the chart with a tab for traffic and one for people, and a switch of the period", async () => {
    await mount("calm");
    const tabs = [...host!.querySelectorAll("[role=radio]")].map((r) => r.textContent);
    expect(tabs).toEqual(["Трафик", "Онлайн", "24 часа", "7 дней"]);
    expect(host!.querySelector(".ov-plot")!.getAttribute("aria-label")).toContain("Трафик");
    await act(async () => void (host!.querySelectorAll("[role=radio]")[1] as HTMLElement).click());
    expect(host!.querySelector(".ov-plot")!.getAttribute("aria-label")).toContain("онлайн сейчас");
  });
});

describe("the Overview with things that need the owner", () => {
  it("lists them: the fault first, the warnings, what the agent asks; a fix for a role that may", async () => {
    await mount("issues");
    expect(text()).toContain("Ждёт тебя 4");
    expect(host!.querySelector("h2")!.textContent).toContain("3 проблемы");
    const rows = [...host!.querySelectorAll(".ov-td")];
    expect(rows).toHaveLength(4);
    expect(rows[0]!.textContent).toContain("de1");
    expect(rows[0]!.textContent).toContain("Размер журнала systemd");
    expect(buttons()).toContain("Сжать журнал");
    expect(rows.at(-1)!.textContent).toContain("Агент ждёт твоего одобрения");
    expect(rows.at(-1)!.querySelector("a")!.getAttribute("href")).toBe("/integrations");
  });

  it("makes a fault red and counts the node out of those on the line", async () => {
    await mount("down");
    const hero = host!.querySelectorAll("h2")[0]!;
    expect(hero.textContent).toBe("3 ноды на связи, 4 проблемы");
    expect(hero.querySelector("span")!.className).toContain("text-danger");
    expect(text()).toContain("Нода недоступна");
    expect(text()).toContain("Что-то не работает");
  });

  it("folds a long list into its first four and 'N more in Health', and a long fleet into eight rows", async () => {
    await mount("many");
    expect(host!.querySelectorAll(".ov-td")).toHaveLength(4);
    expect(text()).toContain("Ещё 5 в «Здоровье»");
    expect(host!.querySelectorAll("a.ov-nr")).toHaveLength(8);
    expect(text()).toContain("Ещё 4 ноды · без проблем");
  });

  it("gives a read-only role no way to add anything or to fix", async () => {
    await mount("viewer");
    expect(buttons()).not.toContain("Нода");
    expect(buttons()).not.toContain("Человек");
    expect(buttons()).not.toContain("Сжать журнал");
    expect(text()).not.toContain("Агент ждёт твоего одобрения");
    await mount("issues");
    expect(buttons()).toContain("Нода");
  });
});

describe("the Overview with no node, a waiting node, the first run and the edition without checks", () => {
  it("shows the first-run card and the three ghost previews, no fleet", async () => {
    await mount("empty");
    expect(host!.querySelector("h2")!.textContent).toBe("Добавь первый сервер, дальше панель сама");
    expect(text()).toContain("Панель готова");
    expect(text()).toContain("нод пока нет");
    expect(text()).toContain("Трафик и онлайн");
    expect(host!.querySelectorAll("ol li")).toHaveLength(4);
    expect(host!.querySelector(".ov-td")).toBeNull();
    expect(buttons()).toContain("Добавить ноду");
    expect(buttons()).toContain("Нода");
  });

  it("says that the only node waits for its install, above an empty fleet", async () => {
    await mount("pending");
    expect(text()).toContain("de1 ждёт установки · команда действует до");
    expect(host!.querySelectorAll("h2")[1]!.textContent).toBe("Ни одной ноды на связи, 1 нода ждёт установки");
    expect(text()).toContain("Новая команда установки");
  });

  it("keeps the first-run card above the fleet while a step is left, and folds it to one line when all are done", async () => {
    await mount("first");
    expect(text()).toContain("Первая настройка · 2 из 4");
    expect(host!.querySelectorAll("h2")[0]!.textContent).toBe("Дай ноде профиль, и она начнёт принимать людей");
    expect(host!.querySelector(".ov-td")).not.toBeNull(); // the node without a profile is a row of "Needs you"
    await mount("done");
    expect(text()).toContain("Всё готово");
    expect(text()).toContain("Скрыть");
  });

  it("leaves the Checks column and its note out where the edition has no tunnel checks", async () => {
    await mount("edge");
    expect(text()).not.toContain("Проверки");
    expect(host!.querySelector(".ov-tk")).toBeNull();
    await mount("calm");
    expect(text()).toContain("проверки глазами клиента, последние 24 часа");
  });
});

describe("the Overview while it loads and when the load fails", () => {
  it("draws a skeleton of its own shape, and says why with a way to try again", async () => {
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    const retry = vi.fn();
    await act(async () =>
      root!.render(
        <QueryClientProvider client={seeded("loading")}>
          <OverviewLoading />
        </QueryClientProvider>,
      ),
    );
    expect(host.querySelector("[aria-busy=true]")).not.toBeNull();
    await act(async () =>
      root!.render(
        <QueryClientProvider client={seeded("error")}>
          <OverviewFailure error={new Error("boom")} onRetry={retry} />
        </QueryClientProvider>,
      ),
    );
    expect(host.textContent).toContain("Обзор не загрузился");
    await act(async () => void [...host!.querySelectorAll("button")].find((b) => b.textContent === "Повторить")?.click());
    expect(retry).toHaveBeenCalledOnce();
  });
});
