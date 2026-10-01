import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { UserStatus } from "@/gen/mistgate/admin/v1/user_pb";
import { UsersScreen } from "./list";

const listUsers = vi.fn();
const createUser = vi.fn();
const navigate = vi.fn();
let search: Record<string, unknown> = {};
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  users: { listUsers: (...a: unknown[]) => listUsers(...a), createUser: (...a: unknown[]) => createUser(...a) },
  groups: {
    listGroups: () =>
      Promise.resolve({
        groups: [
          { id: "grp_empty", name: "empty", profileIds: [], userCount: 2, dnsPresetId: "", happNodes: 0, amneziaNodes: 0 },
          { id: "grp_ok", name: "ok", profileIds: ["p_hy"], userCount: 1, dnsPresetId: "", happNodes: 1, amneziaNodes: 0 },
        ],
      }),
  },
  profiles: { listProfiles: () => Promise.resolve({ profiles: [{ id: "p_hy", name: "hy2", protocol: "hysteria2" }] }), listProtocols: () => Promise.resolve({ protocols: [] }) },
  nodes: { listNodes: () => Promise.resolve({ nodes: [] }) },
  dns: { listDnsPresets: () => Promise.resolve({ presets: [], providers: [], clientSupport: [] }) },
  auth: { me: () => Promise.resolve({ admin: { id: "adm_1", role: Role.OWNER } }) },
}));
vi.mock("@tanstack/react-router", () => ({
  useSearch: () => search,
  useNavigate: () => navigate,
  Link: ({ children, to, params, search: s, className, onClick, ...rest }: { children?: ReactNode; to: string; params?: object; search?: object; className?: string; onClick?: () => void }) => (
    <a href={to} data-params={JSON.stringify(params ?? {})} data-search={JSON.stringify(s ?? {})} className={className} onClick={onClick} aria-label={(rest as { "aria-label"?: string })["aria-label"]}>
      {children}
    </a>
  ),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

const person = (over: Record<string, unknown>) => ({
  id: "usr_x",
  name: "x",
  groupId: "grp_empty",
  groupName: "empty",
  status: UserStatus.ACTIVE,
  apps: { happ: true, amnezia: false },
  via: [],
  devicesUsed: 0,
  deviceLimit: 5,
  usedBytes: 0n,
  quotaBytes: 0n,
  expiresUnix: 0n,
  lastSeenUnix: 0n,
  nextResetUnix: 0n,
  speedLimitBps: 0n,
  createdUnix: 0n,
  accessHapp: false,
  accessAmnezia: false,
  ...over,
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
const written: string[] = [];
beforeEach(() => {
  search = {};
  listUsers.mockResolvedValue({
    users: [
      person({ id: "usr_a", name: "Anna" }),
      person({ id: "usr_b", name: "Boris", groupId: "grp_ok", groupName: "ok", accessHapp: true }),
      person({ id: "usr_c", name: "Clara", status: UserStatus.DISABLED }),
    ],
    nextPageToken: "",
    counts: { all: 3, online: 0, expiring: 0, overQuota: 0 },
  });
  Object.assign(navigator, { clipboard: { writeText: (s: string) => (written.push(s), Promise.resolve()) } });
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const m of [listUsers, createUser, navigate]) m.mockReset();
  written.length = 0;
});

async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <UsersScreen />
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  await settle();
}
const settle = async () => {
  for (let i = 0; i < 6; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
};
const text = () => document.body.textContent ?? "";
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const click = (b: Element | undefined | null) => act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const type = (el: HTMLInputElement, value: string) =>
  act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });

describe("the people list", () => {
  it("marks an active person who would get nothing, with the reason on hover", async () => {
    await mount();
    expect(text()).toContain("App"); // the column
    const marks = [...document.querySelectorAll<HTMLElement>("[title]")].filter((e) => e.textContent?.includes("No access"));
    // Anna (twice: the table and the phone card); Boris gets something, Clara is disabled
    expect(marks.length).toBe(2);
    expect(marks[0]!.title).toBe("Gets nothing yet: no profile of the group runs on a node");
  });

  it("names the group as a link to it", async () => {
    await mount();
    const g = document.querySelector<HTMLAnchorElement>('a[aria-label="Open group “ok”"]')!;
    expect(g.dataset.search).toBe(JSON.stringify({ tab: "groups", group: "grp_ok" }));
  });

  it("opens the Groups tab by its address", async () => {
    await mount();
    await click([...document.querySelectorAll("[role=tab]")].find((x) => x.textContent === "Groups"));
    expect(navigate).toHaveBeenCalledWith(expect.objectContaining({ to: "/users", search: { tab: "groups" } }));
    act(() => root?.unmount());
    search = { tab: "groups" };
    await mount();
    expect(button("New group")).toBeDefined();
    expect(text()).toContain("2 people");
  });

  it("does not copy the link of a new person who gets nothing yet: the window says why first", async () => {
    createUser.mockResolvedValue({ user: person({ id: "usr_n", name: "Nina" }), subscriptionUrl: "https://sub.example.com/p/T", pagePassword: "" });
    await mount();
    await click([...document.querySelectorAll("button")].find((b) => b.textContent?.includes("New user")));
    await settle();
    await type(document.querySelector<HTMLInputElement>("input[placeholder='E.g. Marina']")!, "Nina");
    await click(button("Create"));
    await settle();
    expect(createUser).toHaveBeenCalled();
    expect(written).toEqual([]);
    expect(text()).toContain("Nina gets nothing yet: no profile of the group “empty” runs on a node.");
  });
});
