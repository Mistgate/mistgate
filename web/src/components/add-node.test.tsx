import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { StepUpRequiredSchema } from "@/gen/mistgate/admin/v1/auth_pb";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { fill, type T } from "@/i18n";
import { en } from "@/i18n/en";
import { ru } from "@/i18n/ru";
import { StepUpProvider } from "./step-up";
import { AddNodeProvider, useAddNode } from "./add-node";
import { jobError, sshError } from "./ssh-node-install";

const createEnrollment = vi.fn();
const getNode = vi.fn();
const getSSHFingerprint = vi.fn();
const checkSSH = vi.fn();
const startNodeProvision = vi.fn();
const getNodeProvision = vi.fn();
const listNodeProvisionEvents = vi.fn();
const retryNodeProvision = vi.fn();
const finishStepUp = vi.fn();
// the saved access of other nodes: one with panelKeyFingerprint makes the panel key the default way in
let accessList: { nodeId: string; panelKeyFingerprint: string }[] = [];
const panelKey = { publicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGq7cYk4m1x mistgate-panel", fingerprint: "SHA256:pAnElKeYfInGeRpRiNt0123456789abcdefghijkl" };
vi.mock("@/lib/api", () => ({
  basepath: "/secret-panel/",
  auth: { finishStepUp: (...a: unknown[]) => finishStepUp(...a), beginStepUp: vi.fn() },
  webauthnSupported: () => false,
  nodes: { createEnrollment: (...a: unknown[]) => createEnrollment(...a), getNode: (...a: unknown[]) => getNode(...a) },
  provisioning: {
    getSSHFingerprint: (...a: unknown[]) => getSSHFingerprint(...a),
    checkSSH: (...a: unknown[]) => checkSSH(...a),
    startNodeProvision: (...a: unknown[]) => startNodeProvision(...a),
    getNodeProvision: (...a: unknown[]) => getNodeProvision(...a),
    listNodeProvisionEvents: (...a: unknown[]) => listNodeProvisionEvents(...a),
    retryNodeProvision: (...a: unknown[]) => retryNodeProvision(...a),
    listNodeServerAccess: async () => ({ access: accessList }),
    getPanelSSHKey: async () => panelKey,
  },
}));
const navigate = vi.fn();
vi.mock("@tanstack/react-router", () => ({ useNavigate: () => navigate }));

// the rows of the check answer one by one on the board; here (and for anyone who asks for calm) all at once
let calm = true;
beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  Object.defineProperty(window, "matchMedia", {
    configurable: true,
    value: (query: string) => ({ matches: calm && query.includes("prefers-reduced-motion"), media: query, addEventListener() {}, removeEventListener() {} }),
  });
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const m of [createEnrollment, getNode, getSSHFingerprint, checkSSH, startNodeProvision, getNodeProvision, listNodeProvisionEvents, retryNodeProvision, finishStepUp, navigate]) m.mockReset();
  accessList = [];
  calm = true;
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
const wait = (ms: number) => act(async () => void (await new Promise((r) => setTimeout(r, ms))));
const text = () => document.body.textContent ?? "";
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const click = (el: Element | null | undefined) => act(async () => void el?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const input = (placeholder: string) => document.querySelector<HTMLInputElement>(`input[placeholder="${placeholder}"]`)!;
const radio = (label: string) => [...document.querySelectorAll<HTMLElement>("[role=radio]")].find((r) => (r.querySelector("b") ?? r).textContent === label);
const checked = (label: string) => radio(label)?.getAttribute("aria-checked") === "true";
// the boxes of the window: the first is the one the step is about (the server key, the install)
const box = (i = 0) => document.querySelectorAll<HTMLElement>("[role=checkbox]")[i];
async function type(el: HTMLInputElement | HTMLTextAreaElement, value: string) {
  await act(async () => {
    const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
    Object.getOwnPropertyDescriptor(proto, "value")!.set!.call(el, value);
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
        <StepUpProvider>
          <AddNodeProvider>
            <Opener />
          </AddNodeProvider>
        </StepUpProvider>
      </QueryClientProvider>,
    ),
  );
  await click(button("open"));
  await settle();
}

/** The first page: the server, the name, the address for clients. */
async function fillServer(address = "de1.example.com", name = "de1") {
  await type(input("IP or domain"), address);
  await type(input("de1"), name);
  await type(input("de1.example.com"), address);
}
const probe = async () => {
  await click(button("Check server"));
  await settle();
};
async function byCommand() {
  await click(radio("By command"));
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
  await byCommand();
  await type(input("de1"), "de1");
  await type(input("de1.example.com"), "de1.example.com");
  await click(button("Get install command"));
  await settle();
  await settle();
}

const preflight = { distribution: "Ubuntu", version: "24.04", kernel: "6.8.0", architecture: "amd64", cpuCount: 2, memoryBytes: 2_147_483_648n, diskAvailableBytes: 10_737_418_240n, systemd: true, alreadyEnrolled: false, panelReachable: true };
const rail = () => [...document.querySelectorAll(".an-st li")].map((li) => li.textContent ?? "");

describe("the add-node window", () => {
  it("opens on the server step: the way to install, four steps, nothing sent yet", async () => {
    await open();
    expect(document.querySelector('[role="dialog"]')).not.toBeNull();
    expect(text()).toContain(en["win.node.title"]);
    expect(checked("Over SSH")).toBe(true);
    expect(rail()).toHaveLength(4);
    expect(text()).toContain(en["win.node.srvTitle"]);
    expect(text()).toContain(en["win.node.noteStart"]);
    // the board's first page asks for the server, the name and the address for clients; the button waits for all three
    expect(button("Check server")?.disabled).toBe(true);
    expect(text()).toContain(en["win.node.srvFill"]);
    await type(input("de1"), "DE 1");
    expect(input("de1").value).toBe("de 1");
    expect(text()).toContain(en["node.add.nameError"]);
    expect(getSSHFingerprint).not.toHaveBeenCalled();
  });

  it("fills the name and the address for clients from the host when the host field is left", async () => {
    await open();
    await type(input("IP or domain"), "nl1.example.com");
    await act(async () => void input("IP or domain").dispatchEvent(new FocusEvent("focusout", { bubbles: true })));
    expect(input("de1").value).toBe("nl1");
    expect(input("de1.example.com").value).toBe("nl1.example.com");
    expect(button("Check server")?.disabled).toBe(false);
  });

  it("confirms the server fingerprint before credentials", async () => {
    getSSHFingerprint.mockResolvedValue({ host: "de1.example.com", port: 22, fingerprint: "SHA256:server-key", algorithm: "ssh-ed25519" });
    await open();
    await fillServer();
    await probe();
    expect(getSSHFingerprint).toHaveBeenCalledWith({ host: "de1.example.com", port: 22 });
    expect(text()).toContain(en["win.node.authTitle"]);
    expect(text()).toContain("SHA256:server-key");
    expect(text()).toContain("ssh-ed25519"); // a provider lists one fingerprint per key type
    expect(button("Continue")?.disabled).toBe(true);
    expect(text()).toContain(en["win.node.authHint"]);
    expect(rail()[0]).toContain("de1.example.com:22"); // what was entered stays in the rail
  });

  it("shows the fingerprint in groups of four and the command that prints it on the server", async () => {
    getSSHFingerprint.mockResolvedValue({ host: "de1.example.com", port: 22, fingerprint: "SHA256:Vq3n0cX9Lr2bT8kM1pZs6YwE4hJ7uGdA5fNiQoRlKe0", algorithm: "ssh-rsa" });
    await open();
    await fillServer();
    await probe();
    const groups = [...document.querySelectorAll("[data-testid=fingerprint] [aria-hidden]")].map((s) => s.textContent);
    expect(groups.join("")).toBe("Vq3n0cX9Lr2bT8kM1pZs6YwE4hJ7uGdA5fNiQoRlKe0");
    expect(groups[0]).toBe("Vq3n");
    expect(text()).not.toContain("ssh_host_rsa_key.pub");
    await click(button(en["win.node.keyWhereLink"]));
    expect(text()).toContain("ssh-keygen -lf /etc/ssh/ssh_host_rsa_key.pub");
    await click(button(en["win.node.hide"]));
    expect(text()).not.toContain("ssh-keygen");
  });

  it("explains that SSH fingerprint timeouts are measured from the panel server", async () => {
    getSSHFingerprint.mockRejectedValue(new ConnectError("ssh_fingerprint_timeout", Code.DeadlineExceeded));
    await open();
    await fillServer();
    await probe();
    expect(text()).toContain("from the panel server");
    expect(text()).toContain("allow inbound TCP from the panel server's public egress IP");
    expect(text()).toContain("If the host firewall blocks SSH, use provider console or recovery access to open it first");
    expect(text()).toContain("provider rules must be changed in provider controls");
    expect(document.querySelector('input[type="password"]')).toBeNull();
  });

  it("keeps what the first page asked for when the way to install changes, in both directions", async () => {
    await open();
    await fillServer("nl1.example.com", "nl1");
    await byCommand();
    expect(checked("By command")).toBe(true);
    expect(input("de1").value).toBe("nl1");
    expect(input("de1.example.com").value).toBe("nl1.example.com");
    await click(radio("Over SSH"));
    await settle();
    expect(input("de1").value).toBe("nl1");
    expect(input("IP or domain")).not.toBeNull();
  });

  it("preflights before installation, asks for the password again, and starts only after explicit confirmation", async () => {
    getSSHFingerprint.mockResolvedValue({ host: "de1.example.com", port: 22, fingerprint: "SHA256:server-key" });
    checkSSH.mockResolvedValue({ preflight: { ...preflight, nftables: true } });
    startNodeProvision.mockResolvedValue({
      job: { id: "prv_1", nodeId: "nod_1", name: "de1", state: "queued", phase: "queued", errorCode: "" },
    });
    getNodeProvision.mockReturnValue(new Promise(() => {}));
    listNodeProvisionEvents.mockResolvedValue({ events: [] });

    await open();
    await fillServer();
    await probe();
    expect(text()).toContain(en["win.node.authTitle"]);
    expect(checkSSH).not.toHaveBeenCalled();

    const passwords = () => document.querySelector<HTMLInputElement>('input[type="password"]')!;
    await type(passwords(), "check-only-secret");
    await click(box(0));
    await click(button("Continue"));
    await settle();
    expect(checkSSH).toHaveBeenCalledWith({
      host: "de1.example.com",
      port: 22,
      fingerprint: "SHA256:server-key",
      password: "check-only-secret",
      username: "root",
      usePanelKey: false,
    });
    expect(text()).toContain(en["win.node.chkOk"]);
    expect(text()).toContain(en["win.node.planTitle"]);
    expect(text()).not.toContain("An inactive firewall stays inactive"); // behind the link
    await click(button(en["win.node.fwMore"]));
    expect(text()).toContain("An inactive firewall stays inactive");
    expect(passwords().value).toBe("");
    expect(button("Start installation")?.disabled).toBe(true); // nothing starts before the box is ticked

    await type(passwords(), "install-secret");
    await click(box(0));
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
      usePanelKey: false,
      installPanelKey: true, // a password install adds the panel key unless the owner unticks it
    });
    expect(document.querySelector(".an-sg li.now")?.textContent).toContain("Connecting over SSH");
  });

  it("shows the rows of the check one by one when calm is not asked for", async () => {
    calm = false;
    getSSHFingerprint.mockResolvedValue({ host: "de1.example.com", port: 22, fingerprint: "SHA256:server-key" });
    checkSSH.mockResolvedValue({ preflight: { ...preflight, nftables: true } });
    await open();
    await fillServer();
    await probe();
    await type(document.querySelector<HTMLInputElement>('input[type="password"]')!, "x");
    await click(box(0));
    await click(button("Continue"));
    await settle();
    const rows = () => [...document.querySelectorAll(".an-fr")].map((r) => r.className.replace("an-fr", "").trim());
    expect(text()).toContain(en["win.node.chkBusy"]);
    expect(rows()[0]).toBe("now");
    expect(rows()[1]).toBe("wait");
    expect(document.querySelector(".an-fx")?.getAttribute("aria-busy")).toBe("true");
    expect(text()).not.toContain(en["win.node.planTitle"]);
    // 450 ms, then 190 per row, then a moment: in steps, so that every answer is drawn before the next
    for (let i = 0; i < 14; i++) await wait(200);
    // all well: the rows fold behind one line, and open again with it
    expect(rows()).toEqual([]);
    expect(document.querySelector(".an-sum")?.textContent).toContain("9 checks passed");
    expect(text()).toContain(en["win.node.planTitle"]);
    await click(document.querySelector(".an-sum"));
    expect(rows()).toEqual(Array(9).fill("done"));
    expect(document.querySelector(".an-fx")?.getAttribute("aria-busy")).toBe("false");
  }, 8_000);

  describe("how the panel logs in", () => {
    const pre = { distribution: "Ubuntu", version: "24.04", architecture: "amd64", cpuCount: 2, memoryBytes: 1n, diskAvailableBytes: 1n, systemd: true, panelReachable: true };
    const how = () => !!button(en["win.node.hideHow"]);
    async function toLogin() {
      getSSHFingerprint.mockResolvedValue({ host: "nl1.example.com", port: 22, fingerprint: "SHA256:server-key", algorithm: "ssh-ed25519" });
      await open();
      await fillServer("nl1.example.com", "nl1");
      await probe();
    }

    async function toChecks(nftables: boolean) {
      checkSSH.mockResolvedValue({ preflight: { ...pre, nftables } });
      await toLogin();
      await click(box(0));
      await type(document.querySelector<HTMLInputElement>('input[type="password"]')!, "check-only-secret");
      await click(button("Continue"));
      await settle();
      expect(text()).toContain(en["win.node.chkOk"]);
    }

    it("says the install adds the nftables package when the server has no nft", async () => {
      await toChecks(false);
      // a remark: the rows stay open, its row first, with what it means under it
      expect(text()).toContain(en["win.node.chkWarnTitle"]);
      expect(document.querySelector(".an-sum")?.textContent).toContain("8 checks passed, 1 remark");
      expect(document.querySelector(".an-fr")?.className).toContain("warn");
      expect(document.querySelector(".an-fr")?.textContent).toContain("nftables");
      expect(text()).toContain("No, the install adds it");
      expect(text()).toContain(en["win.node.chkNftNote"]);
      expect(text()).toContain(en["win.node.planNftables"]);
      expect(text()).toContain(en["win.node.planNftablesBody"]);
    });

    it("shows nft as there, and no note, when the server has it", async () => {
      await toChecks(true);
      expect(text()).not.toContain("nftables"); // folded behind "9 checks passed"
      await click(document.querySelector(".an-sum"));
      expect(text()).toContain("nftables");
      expect(text()).not.toContain("No, the install adds it");
      expect(text()).not.toContain(en["win.node.chkNftNote"]);
      expect(text()).not.toContain(en["win.node.planNftables"]);
    });

    it("starts on the password while no node uses the panel key, and installs by key with no password anywhere", async () => {
      checkSSH.mockResolvedValue({ preflight: pre });
      startNodeProvision.mockResolvedValue({ job: { id: "prv_2", nodeId: "nod_2", name: "nl1", state: "queued", phase: "queued", errorCode: "" } });
      getNodeProvision.mockReturnValue(new Promise(() => {}));
      const writeText = vi.fn().mockResolvedValue(undefined);
      Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
      await toLogin();
      expect(text()).toContain(en["win.node.authTitle"]);
      expect(checked("Password")).toBe(true);
      expect(document.querySelector('input[type="password"]')).not.toBeNull();

      await click(radio("Panel key"));
      await settle();
      expect(document.querySelector('input[type="password"]')).toBeNull();
      expect(document.querySelector("[data-testid=panel-key]")?.textContent).toBe(panelKey.publicKey);
      expect(document.querySelector("[data-testid=panel-key]")?.getAttribute("dir")).toBe("ltr");
      expect(text()).toContain(panelKey.fingerprint);
      // a key nobody uses yet comes with the how-to open: the hoster's SSH keys, then the one-line command
      expect(how()).toBe(true);
      expect(text()).toContain("Name it mistgate");
      expect(document.querySelector(".an-cw code")?.textContent).toBe(`mkdir -p ~/.ssh && chmod 700 ~/.ssh && echo '${panelKey.publicKey}' >> ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys`);
      await click(button("Copy key"));
      expect(writeText).toHaveBeenCalledWith(panelKey.publicKey);
      expect(checkSSH).not.toHaveBeenCalled(); // copying inside the form does not submit it

      await click(box(0));
      expect(button("Continue")?.disabled).toBe(false);
      await click(button("Continue"));
      await settle();
      expect(checkSSH).toHaveBeenCalledWith({ host: "nl1.example.com", port: 22, fingerprint: "SHA256:server-key", password: "", username: "root", usePanelKey: true });
      expect(text()).toContain(en["win.node.chkOk"]);
      expect(document.querySelector('input[type="password"]')).toBeNull();

      await click(box(0));
      await click(button("Start installation"));
      await settle();
      expect(startNodeProvision).toHaveBeenCalledWith(expect.objectContaining({ password: "", usePanelKey: true, sshUsername: "root" }));
    });

    it("picks the panel key once it logs in to some node, its how-to folded", async () => {
      accessList = [{ nodeId: "nod_9", panelKeyFingerprint: panelKey.fingerprint }];
      await toLogin();
      expect(checked("Panel key")).toBe(true);
      expect(document.querySelector('input[type="password"]')).toBeNull();
      expect(how()).toBe(false);
      await click(button("How to add it at your hoster"));
      expect(how()).toBe(true);
    });

    it("says where a refused panel key must be, and opens the how-to", async () => {
      accessList = [{ nodeId: "nod_9", panelKeyFingerprint: panelKey.fingerprint }];
      checkSSH.mockRejectedValue(new ConnectError("ssh_panel_key_not_accepted", Code.Unauthenticated));
      await toLogin();
      await type(input("root"), "deploy");
      await click(box(0));
      await click(button("Continue"));
      await settle();
      // the check page opened for the call and went back to the login with the refusal
      expect(text()).toContain(en["win.node.authTitle"]);
      expect(document.querySelector("[role=alert]")?.textContent).toBe("The server did not accept the panel key. Check that it is in ~/.ssh/authorized_keys of deploy.");
      expect(how()).toBe(true);
    });

    it("retries a key install by key, and words a refused login as the key", async () => {
      accessList = [{ nodeId: "nod_9", panelKeyFingerprint: panelKey.fingerprint }];
      checkSSH.mockResolvedValue({ preflight: pre });
      startNodeProvision.mockResolvedValue({ job: { id: "prv_2", nodeId: "nod_2", name: "nl1", state: "queued", phase: "queued", errorCode: "" } });
      getNodeProvision.mockResolvedValue({ job: { id: "prv_2", nodeId: "nod_2", name: "nl1", state: "failed", phase: "failed", errorCode: "ssh_authentication_failed" } });
      listNodeProvisionEvents.mockResolvedValue({ events: [{ id: 1, phase: "queued", code: "queued" }, { id: 2, phase: "connecting", code: "connecting" }] });
      retryNodeProvision.mockResolvedValue({});
      await toLogin();
      await click(box(0));
      await click(button("Continue"));
      await settle();
      await click(box(0));
      await click(button("Start installation"));
      await settle();
      await settle();
      expect(text()).toContain("The server did not accept the panel key.");
      expect(document.querySelector('input[type="password"]')).toBeNull();
      // the stage the worker stopped at says why, in two words
      expect(document.querySelector(".an-sg li.err")?.textContent).toContain("Connecting over SSH");
      expect(document.querySelector(".an-sg li.err")?.textContent).toContain(en["win.node.loginRefused"]);
      expect(text()).toContain(en["win.node.serverUntouched"]);
      await click(button("Retry installation"));
      await settle();
      expect(retryNodeProvision).toHaveBeenCalledWith({ jobId: "prv_2", confirmInstall: true, password: "", sshUsername: "root", usePanelKey: true });
    });

    const ownKey = "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAACmFlczI1Ni1jdHI=\n-----END OPENSSH PRIVATE KEY-----\n";
    const keyField = () => document.querySelector<HTMLTextAreaElement>("textarea");
    const queued = { job: { id: "prv_4", nodeId: "nod_4", name: "nl1", state: "queued", phase: "queued", errorCode: "" } };

    it("adds the panel key on a password install by default, and not when the owner unticks it", async () => {
      checkSSH.mockResolvedValue({ preflight: pre });
      startNodeProvision.mockResolvedValue(queued);
      getNodeProvision.mockReturnValue(new Promise(() => {}));
      await toLogin();
      // the box of the panel key is the one after the server key's
      const panelBox = () => box(1);
      expect(panelBox()?.getAttribute("aria-checked")).toBe("true");
      expect(text()).toContain(en["node.ssh.installPanelKeyHint"]);
      await click(panelBox());
      expect(panelBox()?.getAttribute("aria-checked")).toBe("false");

      await type(document.querySelector<HTMLInputElement>('input[type="password"]')!, "check-secret");
      await click(box(0));
      await click(button("Continue"));
      await settle();
      expect(checkSSH.mock.calls[0]![0]).not.toHaveProperty("installPanelKey"); // the check installs nothing
      await type(document.querySelector<HTMLInputElement>('input[type="password"]')!, "install-secret");
      await click(box(0));
      await click(button("Start installation"));
      await settle();
      expect(startNodeProvision).toHaveBeenCalledWith(expect.objectContaining({ password: "install-secret", usePanelKey: false, installPanelKey: false }));
      expect(startNodeProvision.mock.calls[0]![0]).not.toHaveProperty("privateKey");
      // the stages of the install follow the choice: no step for a panel key that was not asked for
      expect(document.querySelector(".an-sg")?.textContent).not.toContain("Adding the panel key to the server");
    });

    it("logs in with an own key: last and never the default, never shown back, no password sent, the passphrase only once asked", async () => {
      accessList = [{ nodeId: "nod_9", panelKeyFingerprint: panelKey.fingerprint }];
      checkSSH.mockRejectedValueOnce(new ConnectError("ssh_key_passphrase_required", Code.FailedPrecondition)).mockResolvedValue({ preflight: pre });
      startNodeProvision.mockResolvedValue(queued);
      getNodeProvision.mockReturnValue(new Promise(() => {}));
      await toLogin();
      expect([...document.querySelectorAll(".an-wc [role=radio] b")].map((r) => r.textContent)).toEqual(["Panel key", "Password", "Own key"]);
      expect(checked("Panel key")).toBe(true);

      await click(radio("Own key"));
      await settle();
      expect(text()).toContain(en["node.ssh.ownWarn"]);
      expect(document.querySelector('input[type="password"]')).toBeNull(); // no password, no passphrase yet
      expect(button("Continue")?.disabled).toBe(true);

      // a .pub line is refused in the browser and not kept
      await type(keyField()!, "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGq7cYk4 alice@laptop");
      expect(text()).toContain(en["node.ssh.ownKeyPublic"]);
      expect(text()).not.toContain(en["node.ssh.ownKeyLoaded"]);

      await type(keyField()!, ownKey);
      expect(text()).toContain(en["node.ssh.ownKeyLoaded"]);
      expect(keyField()).toBeNull(); // the field goes once the key is in: nothing on screen holds it
      expect(document.body.innerHTML).not.toContain("b3BlbnNzaC1rZXktdjE");

      await click(box(0));
      await click(button("Continue"));
      await settle();
      expect(checkSSH).toHaveBeenLastCalledWith({ host: "nl1.example.com", port: 22, fingerprint: "SHA256:server-key", password: "", usePanelKey: false, privateKey: ownKey, username: "root" });
      // the server asked for the passphrase: its field, no error, and the window is back on the login
      expect(document.querySelector("[role=alert]")).toBeNull();
      const phrase = document.querySelector<HTMLInputElement>('input[type="password"]');
      expect(phrase).not.toBeNull();
      expect(button("Continue")?.disabled).toBe(true);
      await type(phrase!, "correct horse");
      await click(button("Continue"));
      await settle();
      expect(checkSSH).toHaveBeenLastCalledWith(expect.objectContaining({ password: "", usePanelKey: false, privateKey: ownKey, privateKeyPassphrase: "correct horse" }));
      expect(text()).toContain(en["win.node.chkOk"]);

      await click(box(0));
      await click(button("Start installation"));
      await settle();
      expect(startNodeProvision).toHaveBeenCalledWith(
        expect.objectContaining({ password: "", usePanelKey: false, privateKey: ownKey, privateKeyPassphrase: "correct horse", installPanelKey: true }),
      );
    });

    it("drops an own key when the method changes and when the window closes", async () => {
      await toLogin();
      await click(radio("Own key"));
      await type(keyField()!, ownKey);
      expect(text()).toContain(en["node.ssh.ownKeyLoaded"]);
      await click(radio("Password"));
      await click(radio("Own key"));
      expect(text()).not.toContain(en["node.ssh.ownKeyLoaded"]);
      expect(keyField()).not.toBeNull();

      await type(keyField()!, ownKey);
      await click(document.querySelector('[aria-label="Close"]'));
      await settle();
      await click(button("open"));
      await settle();
      await fillServer("nl1.example.com", "nl1");
      await probe();
      expect(checked("Password")).toBe(true);
      await click(radio("Own key"));
      expect(text()).not.toContain(en["node.ssh.ownKeyLoaded"]);
      expect(keyField()).not.toBeNull();
    });

    it("retries an own-key install only with the key given again, and words the server refusing it", async () => {
      checkSSH.mockResolvedValue({ preflight: pre });
      startNodeProvision.mockResolvedValue(queued);
      getNodeProvision.mockResolvedValue({ job: { ...queued.job, state: "failed", phase: "failed", errorCode: "ssh_authentication_failed" } });
      retryNodeProvision.mockResolvedValue({});
      await toLogin();
      await click(radio("Own key"));
      await type(keyField()!, ownKey);
      await click(box(0));
      await click(button("Continue"));
      await settle();
      await click(box(0));
      await click(button("Start installation"));
      await settle();
      await settle();
      expect(text()).toContain("The server did not accept this key for root.");
      expect(text()).toContain(en["node.ssh.retryOwnKey"]);
      expect(button("Retry installation")?.disabled).toBe(true); // the key went with the start
      await type(keyField()!, ownKey);
      await click(button("Retry installation"));
      await settle();
      expect(retryNodeProvision).toHaveBeenCalledWith({ jobId: "prv_4", confirmInstall: true, password: "", usePanelKey: false, privateKey: ownKey, sshUsername: "root", installPanelKey: true });
    });
  });

  const job = { id: "prv_1", nodeId: "nod_1", name: "de1", errorCode: "" };
  async function startInstall(over: Partial<typeof preflight & { nftables: boolean }> = {}) {
    getSSHFingerprint.mockResolvedValue({ host: "de1.example.com", port: 22, fingerprint: "SHA256:server-key" });
    checkSSH.mockResolvedValue({ preflight: { distribution: "Ubuntu", version: "22.04", architecture: "amd64", cpuCount: 2, memoryBytes: 1n, diskAvailableBytes: 1n, systemd: true, panelReachable: true, nftables: true, ...over } });
    startNodeProvision.mockResolvedValue({ job: { ...job, state: "queued", phase: "queued" } });
    await open();
    await fillServer();
    await probe();
    const password = () => document.querySelector<HTMLInputElement>('input[type="password"]')!;
    await type(password(), "check-only-secret");
    await click(box(0));
    await click(button("Continue"));
    await settle();
    await type(password(), "install-secret");
    await click(box(0));
    await click(button("Start installation"));
    await settle();
    await settle();
  }

  it("offers a retry for a cancelled install", async () => {
    getNodeProvision.mockResolvedValue({ job: { ...job, state: "cancelled", phase: "cancelled", errorCode: "remote_outcome_unknown" } });
    await startInstall();
    expect(text()).toContain("inspect the server before retrying");
    expect(button("Retry installation")).toBeDefined();
    expect(text()).not.toContain(en["win.node.serverUntouched"]); // the server may have been changed
  });

  it("words the worker's phases, follows them down the list and shows a completed install with a way to the node", async () => {
    getNodeProvision.mockResolvedValueOnce({ job: { ...job, state: "running", phase: "transfer" } });
    listNodeProvisionEvents.mockResolvedValue({ events: [{ id: 1, phase: "connecting" }, { id: 2, phase: "preflight" }, { id: 3, phase: "firewall" }, { id: 4, phase: "transfer" }] });
    await startInstall();
    // the stage that runs, the ones behind it done, the ones ahead waiting
    expect(document.querySelector(".an-sg li.now")?.textContent).toContain("Sending the agent");
    expect([...document.querySelectorAll(".an-sg li.done")].map((l) => l.querySelector("b")?.textContent)).toEqual(["Connecting over SSH", "Checking the server", "Adding the panel key to the server", "Preparing the host firewall"]);
    expect(rail()[3]).toContain("stage 5 of 8"); // a password install adds the panel key, nftables was there, so no step for it
    expect(text()).not.toContain("transfer");

    // the backend's terminal state is "completed" (store.CompleteNodeProvisionJob)
    getNodeProvision.mockResolvedValue({ job: { ...job, state: "completed", phase: "completed" } });
    await wait(2_600);
    await settle();
    expect(text()).toContain("Node installed and connected");
    expect(rail()[3]).toContain(en["win.node.connected"]);
    const calls = getNodeProvision.mock.calls.length;
    await wait(2_600);
    expect(getNodeProvision.mock.calls.length).toBe(calls); // no more polling once it is done
    expect(text()).toContain(en["win.node.noProfiles"]); // the next thing is a profile
    await click(button("Open node"));
    expect(navigate).toHaveBeenCalledWith({ to: "/nodes/$id", params: { id: "nod_1" } });
  }, 10_000);

  it("has a stage of its own for the nftables package when the server has none, and where it stopped", async () => {
    getNodeProvision.mockResolvedValue({ job: { ...job, state: "failed", phase: "failed", errorCode: "nftables_install_failed" } });
    listNodeProvisionEvents.mockResolvedValue({ events: [{ id: 1, phase: "connecting" }, { id: 2, phase: "preflight" }, { id: 3, phase: "nftables" }] });
    await startInstall({ nftables: false });
    const stages = [...document.querySelectorAll(".an-sg li")].map((l) => l.querySelector("b")?.textContent);
    expect(stages).toContain("Installing nftables");
    expect(stages.indexOf("Installing nftables")).toBeLessThan(stages.indexOf("Preparing the host firewall")); // before the firewall step, as the worker does
    expect(document.querySelector(".an-sg li.err")?.textContent).toContain("Installing nftables");
    expect(text()).toContain("The server has no nft command and the panel could not install the nftables package");
  });

  it("opens step-up for the SSH window and retries the credential check after confirmation", async () => {
    getSSHFingerprint.mockResolvedValue({ host: "de1.example.com", port: 22, fingerprint: "SHA256:server-key" });
    checkSSH
      .mockRejectedValueOnce(
        new ConnectError("confirm it is you first (step-up required)", Code.PermissionDenied, undefined, [
          { desc: StepUpRequiredSchema, value: { passkey: false, totp: true } },
        ]),
      )
      .mockResolvedValue({ preflight: { ...preflight, nftables: true } });
    finishStepUp.mockResolvedValue({ stepUpUntilUnix: 1n });

    await open();
    await fillServer();
    await probe();
    await type(document.querySelector<HTMLInputElement>('input[type="password"]')!, "check-only-secret");
    await click(box(0));
    await click(button("Continue"));
    await settle();

    expect(text()).toContain("Confirm it is you");
    expect(text()).toContain("Check the server de1.example.com over SSH"); // what is being allowed
    const code = document.querySelector<HTMLInputElement>('input[autocomplete="one-time-code"]');
    expect(code).not.toBeNull();
    await type(code!, "123456");
    await click(button("Confirm"));
    await settle();
    await wait(400); // the window says "Confirmed" for a moment, then the call goes on

    expect(finishStepUp).toHaveBeenCalledWith({ totpCode: "123456" });
    expect(checkSSH).toHaveBeenCalledTimes(2);
    expect(text()).toContain(en["win.node.chkOk"]);
  });

  describe("by a command", () => {
    it("says before the click that an IP gets no Let's Encrypt certificate, and what to do", async () => {
      await open();
      await byCommand();
      expect(text()).toContain(en["win.node.cmdTitle"]);
      expect(text()).toContain("you run it on the server as root");
      expect(text()).toContain("A Let’s Encrypt certificate needs a domain with an A record");
      expect(text()).toContain("Needed for DNS: on nodes in Russia the panel checks Yandex DNS and Gosuslugi, in China AliDNS, DNSPod and Baidu.");
      await type(input("de1.example.com"), "203.0.113.10");
      expect(text()).toContain("This is an IP, not a domain.");
      expect(text()).toContain("Happ is not verified yet");
      await type(input("de1.example.com"), "de1.example.com");
      expect(text()).not.toContain("This is an IP, not a domain.");
      expect(rail().map((s) => s.replace(/\d/, ""))).toEqual([en["win.node.cmdStep1"], en["win.node.cmdStep2"], en["win.node.cmdStep3"]]);
    });

    it("keeps a refusal in the window, above its buttons", async () => {
      createEnrollment.mockRejectedValue(new ConnectError("name_taken", Code.AlreadyExists));
      await open();
      await byCommand();
      await type(input("de1"), "de1");
      await type(input("de1.example.com"), "de1.example.com");
      await click(button("Get install command"));
      await settle();
      await settle();
      expect(document.querySelector("[role=alert]")?.textContent).toContain("This name is taken");
      expect(button("Get install command")).toBeDefined(); // the form is still there
    });

    it("sends the country picked from the list with the name and the address", async () => {
      createEnrollment.mockResolvedValue(issued());
      getNode.mockReturnValue(new Promise(() => {}));
      await open();
      await byCommand();
      await type(input("de1"), "de1");
      await type(input("de1.example.com"), "de1.example.com");
      await click(document.querySelector(".an-sel"));
      await settle();
      await type(document.querySelector<HTMLInputElement>(`input[placeholder="${en["win.node.findCountry"]}"]`)!, "germ");
      await click([...document.querySelectorAll("[role=option]")].find((o) => o.textContent?.includes("Germany")));
      await settle();
      await click(button("Get install command"));
      await settle();
      expect(createEnrollment).toHaveBeenCalledWith({ name: "de1", address: "de1.example.com", countryCode: "DE" });
    });

    it("gives three steps: the ready scp from the panel, the one-line command, the live wait", async () => {
      getNode.mockReturnValue(new Promise(() => {})); // the agent has not come yet
      await issue();
      const steps = [...document.querySelectorAll("ol.an-ms > li")].map((li) => li.textContent ?? "");
      expect(steps).toHaveLength(3);
      expect(steps[0]).toContain("Put mistgate-node on the server");
      expect(steps[0]).toContain("scp /var/lib/mistgate/dist/mistgate-node-linux-amd64 root@de1.example.com:/root/mistgate-node");
      expect(steps[0]).toContain("Run it on the panel’s server");
      expect(steps[1]).toContain("chmod +x /root/mistgate-node");
      expect(steps[1]).toContain("The command works once, until");
      expect(button("Copy the command")).toBeDefined();
      expect(steps[2]).toContain("Waiting for the node to connect");
      expect(text()).not.toContain("Shown only once"); // the old banner is gone; the fingerprint is behind "Details"
      expect(text()).not.toContain("sha256:ab");
      await click(button("Details"));
      expect(text()).toContain("sha256:ab");
      expect(rail()[1]).toContain(en["win.node.expires"].replace("{time}", "")); // the rail keeps how long the command lives
      // both ways to install are locked once a node is waiting: a second one would be a second node
      expect(radio("Over SSH")?.getAttribute("aria-disabled")).toBe("true");
    });

    it("issues a new command for the same node, not for a new one", async () => {
      getNode.mockReturnValue(new Promise(() => {}));
      await issue();
      createEnrollment.mockResolvedValue(issued({ installCommand: "second --token tok2" }));
      await click(button("New install command"));
      await settle();
      expect(createEnrollment).toHaveBeenLastCalledWith({ nodeId: "nod_1" });
      expect(text()).toContain("second --token tok2");
    });

    it("without a trusted bundle says where the binary comes from", async () => {
      getNode.mockReturnValue(new Promise(() => {}));
      await issue({ copyCommand: "" });
      expect(document.querySelector("ol.an-ms > li")?.textContent).toContain("Take mistgate-node from the same release as the panel and put it at /root/mistgate-node.");
    });

    it("once the node is connected, the next step is a profile on it", async () => {
      getNode.mockResolvedValue({ node: { id: "nod_1", name: "de1", status: NodeStatus.ONLINE }, inbounds: [] });
      await issue();
      await settle();
      expect(text()).toContain(en["node.ssh.done"]);
      await click(button(en["win.node.addProfile"]));
      expect(navigate).toHaveBeenCalledWith({ to: "/nodes/$id", params: { id: "nod_1" }, search: { tab: "profiles" } });
    });
  });
});

describe("why an SSH install failed", () => {
  const tOf = (dict: Record<string, string>) => Object.assign((key: string, vars?: Record<string, string | number>) => fill(dict[key]!, vars), { n: () => "" }) as unknown as T;
  const codes = ["panel_unreachable", "agent_bundle_unavailable", "insufficient_resources", "insufficient_disk_space", "node_identity_mismatch", "node_identity_unreadable", "systemd_required", "nftables_install_failed"];
  it("names each preflight and job code in its own words, never as the generic error or the panel-address one", () => {
    for (const dict of [en, ru] as Record<string, string>[]) {
      const t = tOf(dict);
      const generic = dict["node.ssh.error.install"];
      const seen = new Set<string>();
      for (const code of codes) {
        const job = jobError(code, t);
        expect(job, code).not.toBe(generic);
        expect(job, code).not.toBe(dict["node.ssh.error.panelAddress"]);
        expect(sshError(new ConnectError(code, Code.FailedPrecondition), t), code).toBe(job); // a code reads the same from CheckSSH and from a job
        seen.add(job);
      }
      expect(seen.size).toBe(codes.length - 1); // the two identity codes share a message
      expect(jobError("a_code_from_tomorrow", t)).toBe(generic);
      expect(sshError(new ConnectError("panel_address_not_configured", Code.FailedPrecondition), t)).toBe(dict["node.ssh.error.panelAddress"]);
    }
  });

  it("says a refused or unreachable target in the same words from the wizard and from a job, and points to the manual command", () => {
    for (const dict of [en, ru] as Record<string, string>[]) {
      const t = tOf(dict);
      const refused = jobError("ssh_target_refused", t);
      expect(refused).toBe(dict["node.ssh.error.refused"]);
      expect(refused).not.toBe(dict["node.ssh.error.install"]);
      expect(sshError(new ConnectError("ssh_target_refused", Code.FailedPrecondition), t)).toBe(refused);
      expect(refused).toMatch(/Cloudflare/);
    }
  });

  it("explains a timeout by the Cloudflare network on the edge only", () => {
    for (const dict of [en, ru] as Record<string, string>[]) {
      const t = tOf(dict);
      for (const code of ["ssh_fingerprint_timeout", "ssh_connection_timeout", "ssh_preflight_timeout"]) {
        expect(jobError(code, t), code).toBe(dict["node.ssh.error.timeout"]);
        expect(jobError(code, t, true), code).toBe(dict["node.ssh.error.timeoutEdge"]);
        expect(sshError(new ConnectError(code, Code.DeadlineExceeded), t, true), code).toBe(dict["node.ssh.error.timeoutEdge"]);
      }
      expect(dict["node.ssh.error.timeoutEdge"]).toMatch(/Cloudflare/);
      expect(jobError("ssh_authentication_failed", t, true)).toBe(dict["node.ssh.error.authentication"]); // only the timeouts change
    }
  });

  it("offers the manual command after a refusal of the target", async () => {
    getSSHFingerprint.mockRejectedValue(new ConnectError("ssh_target_refused", Code.FailedPrecondition));
    await open();
    await fillServer();
    await probe();
    expect(text()).toContain(en["node.ssh.error.refused"]);
    await click(button("Use the manual command"));
    await settle();
    expect(checked("By command")).toBe(true);
    expect(text()).toContain(en["win.node.cmdTitle"]); // the form by command
  });
});
