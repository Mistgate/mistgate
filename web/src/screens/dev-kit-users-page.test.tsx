import { QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { AddNodeProvider } from "@/components/add-node";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { setLang } from "@/i18n";
import { demoOf, editOf, frames, SceneEdit, seeded } from "./dev-kit-users-page";
import { UsersScreen } from "./users/list";

// The scenes of /dev-kit?only=pn-users draw the real screen on made-up data: every state a scene names must draw, in every language.

// the scene's own calls are put on these objects by `seeded`
vi.mock("@/lib/api", () => ({ basepath: "/", auth: { me: () => Promise.resolve({}) }, users: {}, groups: {} }));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, className }: { children?: ReactNode; className?: string }) => <a className={className}>{children}</a>,
  useBlocker: () => ({ status: "idle" }),
  useSearch: () => ({}),
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
  document.body.innerHTML = "";
});

const states = [...new Set(frames.map((f) => f.state))].filter((s) => !s.startsWith("job-"));
const groupsTab = (s: string) => s.startsWith("groups") || s === "group-new" || s in editOf;

async function draw(state: string, role = Role.OWNER) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = seeded(state, role);
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <AddNodeProvider>
            <UsersScreen demo={{ ...demoOf[state], ...(groupsTab(state) && { tab: "groups" as const }) }} />
            <SceneEdit state={state} />
          </AddNodeProvider>
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  for (let i = 0; i < 8; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}

const rows = () => document.querySelectorAll("section .us-e");

describe("the scenes of the People screen", () => {
  it.each(states)("%s draws", async (state) => {
    await draw(state);
    expect(document.querySelector("h1")?.textContent).toBeTruthy();
    expect(document.body.textContent).not.toContain("undefined");
    if (state === "list") expect(rows()).toHaveLength(3);
    if (state === "many" || state === "open-erin") expect(rows()).toHaveLength(10);
    if (state === "none") expect(document.querySelectorAll("ol li")).toHaveLength(3);
    if (state === "search") expect(rows()).toHaveLength(1);
    if (state === "nomatch") expect(document.body.textContent).toContain("Nobody found");
    if (state === "open") expect(document.querySelector("button[aria-label='alice'][aria-expanded=true]")).not.toBeNull();
    if (state === "select") expect(document.querySelector("[role=toolbar]")?.textContent).toContain("2 people");
    if (state === "select-ask") expect(document.querySelector("[role=toolbar]")?.textContent).toContain("Links stop working. Sure?");
    if (state === "groups") expect(document.querySelectorAll("h3")).toHaveLength(2);
    if (state === "groups-many") expect(document.querySelectorAll("h3")).toHaveLength(10);
    if (state === "groups-none") expect(document.body.textContent).toContain("No such group");
    if (state === "group-new") expect(document.querySelector("[data-shot=new-group]")).not.toBeNull();
    if (state === "group-edit") expect(document.querySelector<HTMLInputElement>("[data-shot=edit-group] input")?.value).toBe("friends");
    if (state === "group-edit-lose") expect(document.querySelector("[data-shot=edit-group]")?.textContent).toContain("6 people lose: “AmneziaWG 3.1” (3 AmneziaVPN keys stop working)");
    if (state === "group-edit-held") expect(document.querySelector("[data-shot=edit-group]")?.textContent).toContain("“msk1” is not handed out to this group");
    if (state === "person-new") expect(document.querySelector("[data-shot=create-user]")?.textContent).toContain("Result");
    if (state === "person-name") expect(document.querySelector<HTMLInputElement>("[data-shot=create-user] input")?.value).toBe("carol");
    if (state.startsWith("person-done")) expect(document.querySelector("[data-shot=create-user]")?.textContent).toContain("carol added");
  });

  it("opens alice's row on the devices and the link of the made-up panel", async () => {
    await draw("open");
    expect(rows()[0]!.textContent).toContain("https://sub.example.com/s/alice-7f3k9q");
    expect(rows()[0]!.textContent).toContain("Desktop-Alice");
  });

  it.each(["ru", "zh", "fa", "tr"] as const)("the list and its open row draw in %s", async (lang) => {
    setLang(lang);
    await draw("open");
    expect(rows()).toHaveLength(3);
    expect(document.body.textContent).not.toContain("undefined");
  });

  it("gives a helper the list and an observer a list with nothing to change", async () => {
    await draw("list", Role.HELPER);
    expect([...document.querySelectorAll("button")].some((b) => b.textContent === "Select")).toBe(true);
    act(() => root?.unmount());
    host?.remove();
    await draw("list", Role.READONLY);
    expect([...document.querySelectorAll("button")].some((b) => b.textContent === "Select")).toBe(false);
    expect(rows()).toHaveLength(3);
  });
});
