import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { AddNodeProvider, useAddNode } from "./add-node";

const createEnrollment = vi.fn();
const getNode = vi.fn();
vi.mock("@/lib/api", () => ({
  nodes: { createEnrollment: (...a: unknown[]) => createEnrollment(...a), getNode: (...a: unknown[]) => getNode(...a) },
}));
const navigate = vi.fn();
vi.mock("@tanstack/react-router", () => ({ useNavigate: () => navigate }));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  createEnrollment.mockReset();
  getNode.mockReset();
  navigate.mockReset();
});

function Opener() {
  const addNode = useAddNode();
  return (
    <button type="button" onClick={() => addNode()}>
      open
    </button>
  );
}

const settle = () => act(async () => void (await new Promise((r) => setTimeout(r, 0))));
const text = () => document.body.textContent ?? "";
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const click = (el: Element | null | undefined) => act(async () => void el?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const input = (placeholder: string) => document.querySelector<HTMLInputElement>(`input[placeholder="${placeholder}"]`)!;
async function type(el: HTMLInputElement, value: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

async function open() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <AddNodeProvider>
          <Opener />
        </AddNodeProvider>
      </QueryClientProvider>,
    ),
  );
  await click(button("open"));
  await settle();
}

const issued = (over: Record<string, unknown> = {}) => ({
  node: { id: "nod_1", name: "de1", status: NodeStatus.PENDING, online: [], protocols: [] },
  installCommand: "chmod +x /root/mistgate-node && /root/mistgate-node enroll --panel p:443 --sni s --ca-sha256 ab --token tok && /root/mistgate-node install",
  caFingerprint: "sha256:ab",
  expiresUnix: 1_790_000_000n,
  copyCommand: "scp /var/lib/mistgate/dist/mistgate-node-linux-amd64 root@de1.example.com:/root/mistgate-node",
  ...over,
});

async function issue(over: Record<string, unknown> = {}) {
  createEnrollment.mockResolvedValue(issued(over));
  await open();
  await type(input("de1"), "de1");
  await type(input("de1.example.com"), "de1.example.com");
  await click(button("Create install command"));
  await settle();
  await settle();
}

describe("the add-node window", () => {
  it("says before the click that an IP gets no Let's Encrypt certificate, and what to do", async () => {
    await open();
    expect(text()).toContain("A Let’s Encrypt certificate needs a domain with an A record");
    expect(text()).toContain("Needed for DNS: on nodes in Russia the panel checks Yandex DNS and Gosuslugi.");
    await type(input("de1.example.com"), "203.0.113.10");
    expect(text()).toContain("This is an IP, not a domain.");
    expect(text()).toContain("Happ is not verified yet");
    await type(input("de1.example.com"), "de1.example.com");
    expect(text()).not.toContain("This is an IP, not a domain.");
  });

  it("keeps a refusal in the window, above its buttons", async () => {
    createEnrollment.mockRejectedValue(new ConnectError("name_taken", Code.AlreadyExists));
    await open();
    await type(input("de1"), "de1");
    await type(input("de1.example.com"), "de1.example.com");
    await click(button("Create install command"));
    await settle();
    await settle();
    expect(document.querySelector("[role=alert]")?.textContent).toContain("This name is taken");
    expect(button("Create install command")).toBeDefined(); // the form is still there
  });

  it("gives three steps: the ready scp from the panel, the one-line command as the main button, the live wait", async () => {
    getNode.mockReturnValue(new Promise(() => {})); // the agent has not come yet
    await issue();
    const steps = [...document.querySelectorAll("ol > li")].map((li) => li.textContent ?? "");
    expect(steps).toHaveLength(3);
    expect(steps[0]).toContain("Put mistgate-node on the server");
    expect(steps[0]).toContain("scp /var/lib/mistgate/dist/mistgate-node-linux-amd64 root@de1.example.com:/root/mistgate-node");
    expect(steps[0]).toContain("Run it on the panel’s server");
    expect(steps[1]).toContain("chmod +x /root/mistgate-node");
    expect(steps[1]).toContain("The command works once, until");
    expect(button("Copy the command")?.className).toContain("bg-accent");
    expect(steps[2]).toContain("Waiting for the node to connect");
    expect(text()).not.toContain("Shown only once"); // the old banner is gone; the fingerprint is under Details
    expect(document.querySelector("details")?.textContent).toContain("sha256:ab");
  });

  it("without a trusted bundle says where the binary comes from", async () => {
    getNode.mockReturnValue(new Promise(() => {}));
    await issue({ copyCommand: "" });
    expect(document.querySelector("ol > li")?.textContent).toContain("Take mistgate-node from the same release as the panel and put it at /root/mistgate-node.");
  });

  it("once the node is connected, the next step is a profile on it", async () => {
    getNode.mockResolvedValue({ node: { id: "nod_1", name: "de1", status: NodeStatus.ONLINE }, inbounds: [] });
    await issue();
    await settle();
    expect(text()).toContain("Connected");
    await click(button("Add a profile to de1"));
    expect(navigate).toHaveBeenCalledWith({ to: "/nodes/$id", params: { id: "nod_1" }, search: { tab: "profiles" } });
  });
});
