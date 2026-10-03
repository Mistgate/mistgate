import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { AddNodeProvider, useAddNode } from "./add-node";

const createEnrollment = vi.fn();
const getNode = vi.fn();
const getSSHFingerprint = vi.fn();
const checkSSH = vi.fn();
const startNodeProvision = vi.fn();
const getNodeProvision = vi.fn();
const retryNodeProvision = vi.fn();
vi.mock("@/lib/api", () => ({
  basepath: "/secret-panel/",
  nodes: { createEnrollment: (...a: unknown[]) => createEnrollment(...a), getNode: (...a: unknown[]) => getNode(...a) },
  provisioning: {
    getSSHFingerprint: (...a: unknown[]) => getSSHFingerprint(...a),
    checkSSH: (...a: unknown[]) => checkSSH(...a),
    startNodeProvision: (...a: unknown[]) => startNodeProvision(...a),
    getNodeProvision: (...a: unknown[]) => getNodeProvision(...a),
    retryNodeProvision: (...a: unknown[]) => retryNodeProvision(...a),
  },
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
  getSSHFingerprint.mockReset();
  checkSSH.mockReset();
  startNodeProvision.mockReset();
  getNodeProvision.mockReset();
  retryNodeProvision.mockReset();
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
const toggle = (el: HTMLInputElement | null | undefined) => act(async () => void el?.click());
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

async function manual() {
  await click(button("Get a manual install command"));
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
  await manual();
  await type(input("de1"), "de1");
  await type(input("de1.example.com"), "de1.example.com");
  await click(button("Create install command"));
  await settle();
  await settle();
}

describe("the add-node window", () => {
  it("says before the click that an IP gets no Let's Encrypt certificate, and what to do", async () => {
    await open();
    expect(document.querySelector<HTMLAnchorElement>('a[href="/secret-panel/nodes/install"]')).toBeNull();
    expect(button("Install automatically over SSH")).toBeDefined();
    expect(text()).toContain("Automatic installation over SSH");
    expect(input("de1")).toBeNull();
    await manual();
    expect(input("de1")).not.toBeNull();
    expect(text()).toContain("run the one-time install command on the server yourself");
    expect(text()).toContain("A Let’s Encrypt certificate needs a domain with an A record");
    expect(text()).toContain("Needed for DNS: on nodes in Russia the panel checks Yandex DNS and Gosuslugi.");
    await type(input("de1.example.com"), "203.0.113.10");
    expect(text()).toContain("This is an IP, not a domain.");
    expect(text()).toContain("Happ is not verified yet");
    await type(input("de1.example.com"), "de1.example.com");
    expect(text()).not.toContain("This is an IP, not a domain.");
  });

  it("opens the SSH setup as a panel modal and confirms the server fingerprint before credentials", async () => {
    getSSHFingerprint.mockResolvedValue({ host: "de1.example.com", port: 22, fingerprint: "SHA256:server-key" });
    await open();
    await click(button("Install automatically over SSH"));
    expect(document.querySelector('[role="dialog"]')).not.toBeNull();
    expect(text()).toContain("Install a node over SSH");
    expect(input("de1.example.com")).not.toBeNull();
    expect(document.querySelector<HTMLAnchorElement>('a[href="/secret-panel/nodes/install"]')).toBeNull();

    await type(input("de1.example.com"), "de1.example.com");
    await click(button("Check server"));
    await settle();
    expect(getSSHFingerprint).toHaveBeenCalledWith({ host: "de1.example.com", port: 22 });
    expect(text()).toContain("Confirm the SSH host key");
    expect(text()).toContain("SHA256:server-key");
    expect(button("Continue")?.disabled).toBe(true);
  });

  it("explains that SSH fingerprint timeouts are measured from the panel server", async () => {
    getSSHFingerprint.mockRejectedValue(new ConnectError("ssh_fingerprint_timeout", Code.DeadlineExceeded));
    await open();
    await click(button("Install automatically over SSH"));
    await type(input("de1.example.com"), "de1.example.com");
    await click(button("Check server"));
    await settle();
    expect(text()).toContain("from the panel server");
    expect(text()).toContain("allow TCP access from the panel server");
    expect(document.querySelector('input[type="password"]')).toBeNull();
  });

  it("preflights before installation, asks for the password again, and starts only after explicit confirmation", async () => {
    getSSHFingerprint.mockResolvedValue({ host: "de1.example.com", port: 22, fingerprint: "SHA256:server-key" });
    checkSSH.mockResolvedValue({
      preflight: {
        distribution: "Ubuntu",
        version: "22.04",
        kernel: "6.8.0",
        architecture: "amd64",
        cpuCount: 2,
        memoryBytes: 2_147_483_648n,
        diskAvailableBytes: 10_737_418_240n,
        systemd: true,
        alreadyEnrolled: false,
        panelReachable: true,
      },
    });
    startNodeProvision.mockResolvedValue({
      job: { id: "prv_1", nodeId: "nod_1", name: "de1", state: "queued", phase: "queued", errorCode: "" },
    });
    getNodeProvision.mockReturnValue(new Promise(() => {}));

    await open();
    await click(button("Install automatically over SSH"));
    await type(input("de1.example.com"), "de1.example.com");
    await click(button("Check server"));
    await settle();
    expect(text()).toContain("Confirm the SSH host key");
    expect(checkSSH).not.toHaveBeenCalled();

    const passwords = () => document.querySelector<HTMLInputElement>('input[type="password"]')!;
    await type(passwords(), "check-only-secret");
    await toggle(document.querySelector<HTMLInputElement>('input[type="checkbox"]'));
    await click(button("Continue"));
    await settle();
    expect(checkSSH).toHaveBeenCalledWith({
      host: "de1.example.com",
      port: 22,
      fingerprint: "SHA256:server-key",
      password: "check-only-secret",
      username: "root",
    });
    expect(text()).toContain("Server checks passed");
    expect(passwords().value).toBe("");

    await type(passwords(), "install-secret");
    await toggle(document.querySelector<HTMLInputElement>('input[type="checkbox"]'));
    await click(button("Start installation"));
    await settle();
    expect(startNodeProvision).toHaveBeenCalledWith({
      confirmInstall: true,
      name: "de1",
      address: "de1.example.com",
      countryCode: "",
      location: "",
      provider: "",
      sshHost: "de1.example.com",
      sshPort: 22,
      fingerprint: "SHA256:server-key",
      password: "install-secret",
      sshUsername: "root",
    });
    expect(text()).toContain("Waiting to start");
  });

  it("keeps a refusal in the window, above its buttons", async () => {
    createEnrollment.mockRejectedValue(new ConnectError("name_taken", Code.AlreadyExists));
    await open();
    await manual();
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
