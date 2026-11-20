import { QueryClientProvider } from "@tanstack/react-query";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { NodeShareMode, NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { en } from "@/i18n/en";
import { groupRows, seededWindow, windowSeed } from "@/screens/dev-kit-node-share";
import { StepUpProvider } from "./step-up";
import { AddNodeWindow } from "./add-node";

// "Who gets this node" in the New node window: the first node is the panel's own choice, the later ones start at nobody yet (or the
// last choice), and what is chosen goes in the request of either way to install.

const createEnrollment = vi.fn();
const startNodeProvision = vi.fn();
let nodeList: { id: string; name: string; status: NodeStatus }[] = [];
vi.mock("@/lib/api", () => ({
  basepath: "/",
  auth: { finishStepUp: vi.fn(), beginStepUp: vi.fn() },
  webauthnSupported: () => false,
  nodes: { createEnrollment: (...a: unknown[]) => createEnrollment(...a), getNode: async () => ({}), listNodes: async () => ({ nodes: nodeList }) },
  groups: { listGroups: async () => ({ groups: groupRows([]) }) },
  provisioning: {
    startNodeProvision: (...a: unknown[]) => startNodeProvision(...a),
    getNodeProvision: async () => ({ job: undefined }),
    listNodeProvisionEvents: async () => ({ events: [] }),
    listNodeServerAccess: async () => ({ access: [] }),
    getPanelSSHKey: async () => ({ publicKey: "ssh-ed25519 AAAA mistgate-panel", fingerprint: "SHA256:abc" }),
  },
}));
vi.mock("@tanstack/react-router", () => ({
  Link: ({ children }: { children?: ReactNode }) => <a>{children}</a>,
  useNavigate: () => vi.fn(),
  useSearch: () => ({}),
  useParams: () => ({}),
  useLocation: () => ({ pathname: "/", search: "" }),
  useBlocker: () => ({ status: "idle" }),
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  Object.defineProperty(window, "matchMedia", {
    configurable: true,
    value: (query: string) => ({ matches: query.includes("prefers-reduced-motion"), media: query, addEventListener() {}, removeEventListener() {} }),
  });
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  createEnrollment.mockReset();
  startNodeProvision.mockReset();
  nodeList = [];
  localStorage.clear();
});

const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 0))));
const text = () => document.body.textContent ?? "";
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label) as HTMLButtonElement | undefined;
const radio = (label: string) => [...document.querySelectorAll<HTMLElement>("[role=radio]")].find((r) => (r.querySelector("b") ?? r).textContent === label);
const checked = (label: string) => radio(label)?.getAttribute("aria-checked") === "true";
const click = (el: Element | null | undefined) => act(async () => void el?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
// a group is a chip that is pressed or not
const box = (name: string) => [...document.querySelectorAll<HTMLElement>("[role=group] button[aria-pressed]")].find((b) => b.textContent === name);

const second = [{ id: "nod_de1", name: "de1", status: NodeStatus.ONLINE }];

/** The window by a command, on the data of the dev kit's scene (the draft is filled in; `state` picks the way and the choice it starts with). */
async function open(state: string, { nodes = second }: { nodes?: typeof second } = {}) {
  nodeList = nodes;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = seededWindow(state);
  qc.removeQueries({ queryKey: ["nodes"] }); // the window asks the panel (the mock above) as the app does
  qc.removeQueries({ queryKey: ["groups"] });
  const { draft, ssh } = windowSeed(state);
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <StepUpProvider>
          <AddNodeWindow open initialMode={state.startsWith("win-ssh") ? "ssh" : "cmd"} onClose={() => {}} seed={{ draft: { ...draft, share: undefined }, ssh }} />
        </StepUpProvider>
      </QueryClientProvider>,
    ),
  );
  await settle();
  await settle();
}

const issued = { node: { id: "nod_se1", name: "se1", status: NodeStatus.PENDING, online: [], protocols: [] }, installCommand: "x", caFingerprint: "sha256:ab", expiresUnix: 1_790_000_000n, copyCommand: "" };

describe("who gets a new node", () => {
  it("the first node of the panel only says it goes to everyone, and the request leaves it to the panel", async () => {
    createEnrollment.mockResolvedValue(issued);
    await open("win-cmd-none", { nodes: [] });
    expect(text()).toContain(en["ns.pick.first"]);
    expect(radio(en["ns.mode.all"])).toBeUndefined();
    await click(button("Get install command"));
    await settle();
    expect(createEnrollment).toHaveBeenCalledTimes(1);
    expect(createEnrollment.mock.calls[0]![0]).not.toHaveProperty("sharing");
  });

  it("a later node starts at nobody yet, and asks for it", async () => {
    createEnrollment.mockResolvedValue(issued);
    await open("win-cmd-none");
    expect(text()).toContain(en["ns.pick.title"]);
    expect(checked(en["ns.mode.none"])).toBe(true);
    await click(button("Get install command"));
    await settle();
    expect(createEnrollment).toHaveBeenCalledWith({ name: "se1", address: "se1.example.com", countryCode: "SE", sharing: { mode: NodeShareMode.NONE, groupIds: [] } });
    // and remembers it for the next one
    expect(JSON.parse(localStorage.getItem("nodes.share")!)).toEqual({ mode: "none", groupIds: [] });
  });

  it("starts at the last choice", async () => {
    localStorage.setItem("nodes.share", JSON.stringify({ mode: "all", groupIds: [] }));
    await open("win-cmd-none");
    expect(checked(en["ns.mode.all"])).toBe(true);
    expect(text()).toContain(en["ns.opt.all"]);
  });

  it("some groups need a group: the button waits, and the ticked ones go in the request", async () => {
    createEnrollment.mockResolvedValue(issued);
    await open("win-cmd-none");
    await click(radio(en["ns.mode.listed"]));
    await settle();
    expect(checked(en["ns.mode.listed"])).toBe(true);
    expect(text()).toContain(en["ns.pick.pickOne"]);
    expect(button("Get install command")!.disabled).toBe(true);
    await click(box("family"));
    await click(box("team"));
    expect(text()).not.toContain(en["ns.pick.pickOne"]);
    expect(button("Get install command")!.disabled).toBe(false);
    await click(button("Get install command"));
    await settle();
    expect(createEnrollment.mock.calls[0]![0].sharing).toEqual({ mode: NodeShareMode.LISTED, groupIds: ["grp_family", "grp_team"] });
    expect(JSON.parse(localStorage.getItem("nodes.share")!)).toEqual({ mode: "listed", groupIds: ["grp_family", "grp_team"] });
  });

  it("the choice stays when the way to install is switched", async () => {
    await open("win-cmd-none");
    await click(radio(en["ns.mode.all"]));
    await click(radio("Over SSH"));
    await click(radio("By command"));
    expect(checked(en["ns.mode.all"])).toBe(true);
  });

  it("the install over SSH asks for it too", async () => {
    startNodeProvision.mockResolvedValue({ job: { id: "prv_1", state: "running", phase: "connecting" } });
    await open("win-ssh-none");
    expect(checked(en["ns.mode.none"])).toBe(true);
    await click(radio(en["ns.mode.listed"]));
    await settle();
    await click(box("friends"));
    await click(document.querySelector<HTMLElement>("[role=checkbox]:not([aria-label])")); // "Install the Mistgate agent"
    await click(button("Start installation"));
    await settle();
    expect(startNodeProvision).toHaveBeenCalledTimes(1);
    expect(startNodeProvision.mock.calls[0]![0].sharing).toEqual({ mode: NodeShareMode.LISTED, groupIds: ["grp_friends"] });
  });
});
