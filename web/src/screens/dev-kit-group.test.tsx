import { QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { setLang } from "@/i18n";
import { demoOf, frames, seeded } from "./dev-kit-group";
import { GroupScreen } from "./users/group-page";

// The scenes of /dev-kit?only=pn-group draw the real page on made-up data: every state a scene names must draw, in every language.

vi.mock("@/lib/api", () => ({ basepath: "/", auth: { me: () => Promise.resolve({}) } }));
let search: Record<string, unknown> = {};
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, className }: { children?: ReactNode; className?: string }) => <a className={className}>{children}</a>,
  useSearch: () => search,
  useParams: () => ({}),
  useLocation: () => ({ pathname: "/dev-kit", search: "" }),
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

const tabOf = (params = "") => new URLSearchParams(params).get("tab") ?? undefined;

async function draw(state: string, params = "", role = Role.OWNER) {
  search = { tab: tabOf(params) };
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = seeded(state, role);
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <GroupScreen id={state === "missing" ? "grp_gone" : "grp_friends"} demo={demoOf[state]} />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 4; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}

const scenes = frames.filter((f, i) => frames.findIndex((g) => g.state === f.state && (g.params ?? "").replace(/&(theme|lang)=\w+/g, "") === (f.params ?? "").replace(/&(theme|lang)=\w+/g, "")) === i);

describe("the scenes of the group page", () => {
  it.each(scenes.map((f) => [f.id, f] as const))("%s draws", async (_id, f) => {
    await draw(f.state, f.params, f.params?.includes("readonly") ? Role.READONLY : Role.OWNER);
    expect(host!.textContent).not.toContain("undefined");
    if (f.state === "missing") return expect(host!.textContent).toContain("No such group");
    if (f.state === "deleted") return expect(host!.textContent).toContain("Group deleted");
    expect(host!.querySelector("h1")?.textContent).toBe("friends");
    if (f.state === "people") expect(host!.textContent).toContain("carol");
    if (f.state === "pick") expect(host!.textContent).toContain("Move 1");
    if (f.state === "find") expect(host!.textContent).toContain("Nobody found");
    if (f.state === "empty") expect(host!.textContent).toContain("Nobody in the group yet");
    if (f.state === "access-held") {
      const row = [...host!.querySelectorAll("[role=checkbox]")].find((e) => e.textContent?.includes("msk1"));
      expect(row?.getAttribute("aria-disabled")).toBe("true");
      expect(row?.textContent).toContain("not handed out to this group");
    }
    if (f.state === "access-dirty") expect(host!.textContent).toContain("Changes not saved");
    if (f.state === "settings-ask-mv") expect(host!.textContent).toContain("Really move 3?");
    if (f.state === "settings-ask-del") expect(host!.textContent).toContain("Really delete?");
  });

  it.each(["ru", "zh", "fa", "tr"] as const)("the held-back node says why in %s", async (lang) => {
    setLang(lang);
    await draw("access-held", "&tab=access");
    const row = [...host!.querySelectorAll("[role=checkbox]")].find((e) => e.textContent?.includes("msk1"));
    expect(row?.getAttribute("aria-disabled")).toBe("true");
    expect(row?.getAttribute("title")).toBeTruthy();
    expect(host!.textContent).not.toContain("grp.");
  });

  it.each(["ru", "zh", "fa", "tr"] as const)("the three tabs draw in %s", async (lang) => {
    for (const tab of [undefined, "access", "settings"]) {
      setLang(lang);
      await draw("people", tab ? `&tab=${tab}` : "");
      expect(host!.querySelector("h1")?.textContent).toBe("friends");
      expect(host!.textContent).not.toContain("grp.");
      act(() => root?.unmount());
      host?.remove();
    }
  });
});
