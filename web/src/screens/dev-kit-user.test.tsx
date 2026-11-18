import { QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { setLang } from "@/i18n";
import { cacheOf, detailOf, frames } from "./dev-kit-user";
import { GoneCard } from "./users/user-detail";
import { userTabs, UserDetail, type UserTab } from "./users/user-detail";

// The scenes of /dev-kit?only=pn-user draw the real page on made-up data: every state a frame names must draw, in every language and
// for every role, and each tab shows its own cards.

vi.mock("@/lib/api", () => ({ basepath: "/", auth: { me: () => Promise.resolve({}) } }));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, className }: { children?: ReactNode; className?: string }) => <a className={className}>{children}</a>,
  useBlocker: () => ({ status: "idle" }),
  useSearch: () => ({}),
  useParams: () => ({}),
  useNavigate: () => vi.fn(),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
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

const sceneOf = (f: (typeof frames)[number]) => {
  const q = new URLSearchParams(f.params ?? "");
  const tab = (userTabs.includes(q.get("tab") as UserTab) ? q.get("tab") : "overview") as UserTab;
  const role = q.get("role") === "helper" ? Role.HELPER : q.get("role") === "readonly" ? Role.READONLY : Role.OWNER;
  return { state: f.state, tab, role, ui: "" };
};
const marker: Record<UserTab, string> = { overview: ".up-tr", devices: ".up-sum", access: ".up-lim", logs: "#client-logs", settings: ".up-dz" };

async function draw(scene: ReturnType<typeof sceneOf>) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = cacheOf(scene);
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <UserDetail data={detailOf(scene.state)} scene={{ tab: scene.tab }} />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 4; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}

describe("the scenes of the person's page", () => {
  it.each(frames.filter((f) => f.state !== "gone").map((f) => [f.id, f] as const))("%s draws", async (_, f) => {
    const scene = sceneOf(f);
    await draw(scene);
    expect(host!.querySelector("h1")?.textContent).toBe("alice");
    expect(host!.textContent).not.toContain("undefined");
    expect(host!.querySelector(marker[scene.tab])).not.toBeNull();
    expect(host!.querySelectorAll("[role=tab]")).toHaveLength(5);
  });

  it.each(["ru", "zh", "fa", "tr"] as const)("every tab draws in %s", async (lang) => {
    for (const tab of userTabs) {
      setLang(lang);
      await draw({ state: "logs", tab, role: Role.OWNER, ui: "" });
      expect(host!.querySelector(marker[tab]), `${lang} ${tab}`).not.toBeNull();
      expect(host!.textContent).not.toContain("up.");
      act(() => root?.unmount());
      host?.remove();
    }
  });

  it("says a deleted person is gone and leads back to the list", async () => {
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    await act(async () => root!.render(<GoneCard name="alice" />));
    expect(host.textContent).toContain("alice is deleted");
    // the back link says "Users" (the board), the card's button "To users"
    expect(host.querySelector(".up-back")?.textContent).toBe("Users");
    expect(host.textContent).toContain("To users");
  });

  it("keeps the write buttons from an observer", async () => {
    await draw({ state: "ok", tab: "settings", role: Role.READONLY, ui: "" });
    expect([...host!.querySelectorAll(".up-dz button")].every((b) => (b as HTMLButtonElement).disabled || b.hasAttribute("data-disabled"))).toBe(true);
    expect(host!.querySelector(".up-ha")).toBeNull();
  });
});
