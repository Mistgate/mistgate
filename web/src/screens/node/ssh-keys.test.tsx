import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { en } from "@/i18n/en";
import { keyTypeLabel } from "./ssh-keys";
import { SSHAccessCard } from "./ssh-access";

const listNodeServerAccess = vi.fn();
const listNodeServerKeys = vi.fn();
const addNodeServerKey = vi.fn();
const removeNodeServerKey = vi.fn();
const attachNodeServerAccess = vi.fn();
const getSSHFingerprint = vi.fn();

vi.mock("@/lib/api", () => ({
  provisioning: {
    listNodeServerAccess: (...a: unknown[]) => listNodeServerAccess(...a),
    listNodeServerKeys: (...a: unknown[]) => listNodeServerKeys(...a),
    addNodeServerKey: (...a: unknown[]) => addNodeServerKey(...a),
    removeNodeServerKey: (...a: unknown[]) => removeNodeServerKey(...a),
    attachNodeServerAccess: (...a: unknown[]) => attachNodeServerAccess(...a),
    getSSHFingerprint: (...a: unknown[]) => getSSHFingerprint(...a),
    getPanelSSHKey: async () => ({ publicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGq7 mistgate-panel", fingerprint: panelFp }),
    revealNodeServerPassword: vi.fn(),
    rotateNodeServerPassword: vi.fn(),
    forgetNodeServerAccess: vi.fn(),
  },
}));

vi.mock("@/components/step-up", () => ({
  useStepUp: () => async <T,>(call: () => Promise<T>) => call(),
  isStepUpCancelled: () => false,
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

const panelFp = "SHA256:pAnElKeYfInGeRpRiNt0123456789abcdefghijkl";
const aliceFp = "SHA256:Xy7pL2qR9sT4vW8zA1bC3dE5fG6hJ0kM2nP4rS6tU8w";
const key = (over: object) => ({ type: "ssh-ed25519", bits: 256, fingerprint: aliceFp, comment: "alice@laptop", panelKey: false, restricted: false, lines: 1, ...over });
const panel = key({ fingerprint: panelFp, comment: "mistgate-panel", panelKey: true });
const alice = key({});
const ci = key({ type: "ssh-rsa", bits: 3072, fingerprint: "SHA256:Ab9cD8eF7gH6iJ5kL4mN3oP2qR1sT0uV9wX8yZ7aB6c", comment: "deploy@ci.example.com", restricted: true, lines: 2 });
const access = (over: object = {}) => ({
  nodeId: "nod_1",
  nodeName: "de1",
  host: "203.0.113.7",
  port: 22,
  username: "root",
  fingerprint: "SHA256:server-key",
  passwordSaved: true,
  passwordLogin: true,
  panelKeyFingerprint: panelFp,
  ...over,
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const f of [listNodeServerAccess, listNodeServerKeys, addNodeServerKey, removeNodeServerKey, attachNodeServerAccess, getSSHFingerprint]) f.mockReset();
});

// keys: the file's keys, or the call itself (one that never answers, one that fails first)
async function mount(over: object | null, keys: object[] | (() => Promise<unknown>) = [panel, alice], attach?: { name: string; address: string }) {
  listNodeServerAccess.mockResolvedValue({ access: over ? [access(over)] : [] });
  if (typeof keys === "function") listNodeServerKeys.mockImplementation(keys);
  else listNodeServerKeys.mockResolvedValue({ keys, otherLines: 0 });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={client}>
        <ToastProvider>
          <div role="tabpanel">
            <SSHAccessCard nodeId="nod_1" attach={attach} />
          </div>
        </ToastProvider>
      </QueryClientProvider>,
    ),
  );
  await flush();
}

async function flush() {
  for (let i = 0; i < 4; i++) await act(async () => void (await new Promise((resolve) => setTimeout(resolve, 0))));
}

const text = () => document.body.textContent ?? "";
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label);
const click = async (el: Element | null | undefined) => {
  await act(async () => void el?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
  await flush();
};
// a pill reads glyph + word; the word is what the owner reads
const pills = () => [...document.querySelectorAll(".pill")].map((p) => p.textContent?.replace(/^[–!×~]/, ""));
async function type(el: HTMLInputElement | HTMLTextAreaElement, value: string) {
  await act(async () => {
    const proto = el instanceof HTMLTextAreaElement ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
    Object.getOwnPropertyDescriptor(proto, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
const unbindOf = (comment: string) => [...document.querySelectorAll("li")].find((li) => li.textContent?.includes(comment))!.querySelector<HTMLButtonElement>("[data-unbind]")!;

describe("the keys block", () => {
  it("names key types the way ssh-keygen does", () => {
    expect(keyTypeLabel("ssh-ed25519", 256)).toBe("ED25519");
    expect(keyTypeLabel("ssh-rsa", 3072)).toBe("RSA 3072");
    expect(keyTypeLabel("ecdsa-sha2-nistp256", 256)).toBe("ECDSA 256");
    expect(keyTypeLabel("sk-ssh-ed25519@openssh.com", 256)).toBe("ED25519-SK");
  });

  it("lists authorized_keys of the login: type, comment, fingerprint left to right, its marks and the other lines", async () => {
    listNodeServerKeys.mockResolvedValueOnce({ keys: [panel, ci, key({ type: "ecdsa-sha2-nistp256", fingerprint: "SHA256:e", comment: "" })], otherLines: 3 });
    await mount({}, undefined);
    expect(listNodeServerKeys).toHaveBeenCalledTimes(1);
    expect(listNodeServerKeys.mock.calls[0]![0]).toEqual({ nodeId: "nod_1" });
    expect(text()).toContain("~/.ssh/authorized_keys · root");
    for (const s of ["ED25519", "RSA 3072", "ECDSA 256", "mistgate-panel", "deploy@ci.example.com", en["node.sshKeys.noComment"], "×2 lines", "3 other lines (comments, blank)"]) expect(text()).toContain(s);
    expect(pills()).toEqual(expect.arrayContaining([en["panelKey.title"], en["node.sshKeys.restricted"]]));
    const fp = [...document.querySelectorAll("span[dir=ltr]")].find((s) => s.textContent === panelFp);
    expect(fp).toBeTruthy(); // the <wbr> after "SHA256:" breaks it there without adding a character
  });

  it("shows still rows while the panel logs in to read the keys", async () => {
    await mount({}, () => new Promise(() => {}));
    expect(document.querySelector('[role="status"]')?.textContent).toContain(en["node.sshKeys.loading"]);
  });

  it("a retired node has no keys block and reads nothing over SSH", async () => {
    await mount({ nodeRetired: true });
    expect(text()).not.toContain(en["node.sshKeys.title"]);
    expect(listNodeServerKeys).not.toHaveBeenCalled();
  });

  it("the panel key gone from the file: amber in the header, and Restore puts it back", async () => {
    addNodeServerKey.mockResolvedValue({ key: panel });
    await mount({}, [alice]);
    expect(text()).toContain(en["node.sshKeys.missing"]);
    expect(document.querySelector(".pill.tone-warn")?.textContent).toContain(en["panelKey.title"]);
    await click(button(en["node.sshKeys.restore"]));
    expect(addNodeServerKey).toHaveBeenCalledWith({ nodeId: "nod_1", panelKey: true, confirm: true });
    expect(listNodeServerKeys).toHaveBeenCalledTimes(2); // read again after the change
  });

  it("the panel key in the file but not used: Use takes it into use; the header offers nothing twice", async () => {
    addNodeServerKey.mockResolvedValue({ key: panel });
    await mount({ panelKeyFingerprint: "" }, [panel, alice]);
    expect(pills()).toContain(en["node.sshKeys.unused"]);
    expect(document.querySelector("[data-install-key]")).toBeNull();
    await click(button(en["node.sshKeys.use"]));
    expect(addNodeServerKey).toHaveBeenCalledWith({ nodeId: "nod_1", panelKey: true, confirm: true });
    expect(text()).toContain(en["node.sshKeys.used"]);
  });

  it("a password-only node offers the panel key under the header: the same call", async () => {
    addNodeServerKey.mockResolvedValue({ key: panel });
    await mount({ panelKeyFingerprint: "" }, [alice]);
    expect(text()).toContain(en["node.sshAccess.keyPrompt"]);
    await click(document.querySelector("[data-install-key]"));
    expect(addNodeServerKey).toHaveBeenCalledWith({ nodeId: "nod_1", panelKey: true, confirm: true });
    expect(text()).toContain(en["node.sshAccess.keyInstalled"]);
  });

  describe("binding a key", () => {
    const area = () => document.querySelector<HTMLTextAreaElement>("form[data-bind-form] textarea")!;
    const submit = () => document.querySelector<HTMLButtonElement>("form[data-bind-form] button[type=submit]")!;

    it("refuses a pasted private key in the browser, keeps nothing of it and sends nothing", async () => {
      await mount({});
      await click(document.querySelector("[data-bind]"));
      expect(text()).toContain(en["node.sshKeys.publicKeyHint"]);
      await type(area(), "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjE=\n-----END OPENSSH PRIVATE KEY-----");
      expect(document.querySelector('[role="alert"]')?.textContent).toContain(en["node.sshKeys.privateRefused"]);
      expect(area().value).toBe("");
      expect(submit().disabled || submit().hasAttribute("data-disabled")).toBe(true);
      await act(async () => void area().form!.requestSubmit());
      expect(addNodeServerKey).not.toHaveBeenCalled();
    });

    it("binds a public key, then reads the list again", async () => {
      addNodeServerKey.mockResolvedValue({ key: key({ comment: "bob@desk" }) });
      await mount({});
      await click(document.querySelector("[data-bind]"));
      await type(area(), "  ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIK7f bob@desk\n");
      await click(submit());
      expect(addNodeServerKey).toHaveBeenCalledWith({ nodeId: "nod_1", publicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIK7f bob@desk", confirm: true });
      expect(text()).toContain(en["node.sshKeys.bound"]);
      expect(document.querySelector("form[data-bind-form]")).toBeNull();
      expect(listNodeServerKeys).toHaveBeenCalledTimes(2);
    });

    it("says so when the key was already there with restrictions and left as it is", async () => {
      addNodeServerKey.mockResolvedValue({ key: { ...ci, lines: 1 } });
      await mount({});
      await click(document.querySelector("[data-bind]"));
      await type(area(), "ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQ deploy@ci.example.com");
      await click(submit());
      expect(text()).toContain(en["node.sshKeys.alreadyRestricted"]);
      expect(text()).not.toContain(en["node.sshKeys.bound"]);
    });

    it.each([
      ["ssh_public_key_invalid", "node.sshKeys.error.invalid"],
      ["ssh_public_key_weak", "node.sshKeys.error.weak"],
      ["authorized_keys_changed", "node.sshKeys.error.changed"],
      ["ssh_host_key_changed", "node.ssh.error.hostKey"], // a failed login with the saved access keeps its real cause
    ] as const)("words %s for the owner, inside the form", async (code, text_) => {
      addNodeServerKey.mockRejectedValueOnce(new ConnectError(code, Code.FailedPrecondition));
      await mount({});
      await click(document.querySelector("[data-bind]"));
      await type(area(), "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIK7f bob@desk");
      await click(submit());
      expect(document.querySelector("form[data-bind-form] [role=alert]")?.textContent).toContain(en[text_]);
    });
  });

  describe("unbinding a key", () => {
    const confirm = () => button(en["node.sshKeys.unbindNow"])!;

    it("asks first, naming the key and the lock-out risk, then removes it", async () => {
      removeNodeServerKey.mockResolvedValue({});
      await mount({});
      await click(unbindOf("alice@laptop"));
      expect(removeNodeServerKey).not.toHaveBeenCalled();
      const box = document.querySelector('[role="group"]')!;
      expect(box.textContent).toContain(aliceFp);
      expect(box.textContent).toContain("“alice@laptop”");
      expect(box.textContent).toContain(en["node.sshKeys.unbindWarn"]);
      await click(confirm());
      expect(removeNodeServerKey).toHaveBeenCalledWith({ nodeId: "nod_1", fingerprint: aliceFp, confirm: true, confirmPanelKey: false });
      expect(text()).toContain(en["node.sshKeys.unbound"]);
    });

    it("the panel key: says the password is checked first and confirms it as the panel key", async () => {
      removeNodeServerKey.mockResolvedValue({});
      await mount({});
      await click(unbindOf("mistgate-panel"));
      expect(document.querySelector('[role="group"]')?.textContent).toContain(en["node.sshKeys.unbindPanel"]);
      await click(confirm());
      expect(removeNodeServerKey).toHaveBeenCalledWith({ nodeId: "nod_1", fingerprint: panelFp, confirm: true, confirmPanelKey: true });
      expect(text()).toContain(en["node.sshKeys.panelUnbound"]);
    });

    it("the panel key as the only way in, with no password saved: it cannot be unbound here", async () => {
      await mount({ passwordSaved: false, passwordLogin: false }, [panel]);
      await click(unbindOf("mistgate-panel"));
      expect(document.querySelector('[role="group"]')?.textContent).toContain(en["node.sshKeys.unbindOnly"]);
      expect(confirm().disabled || confirm().hasAttribute("data-disabled")).toBe(true);
      await click(confirm());
      expect(removeNodeServerKey).not.toHaveBeenCalled();
    });

    it("words a refusal in the confirm: the panel key is the last way in", async () => {
      removeNodeServerKey.mockRejectedValueOnce(new ConnectError("ssh_key_last_panel_login", Code.FailedPrecondition));
      await mount({ passwordLogin: false });
      await click(unbindOf("mistgate-panel"));
      await click(confirm());
      expect(document.querySelector('[role="group"] [role="alert"]')?.textContent).toContain(en["node.sshKeys.error.lastLogin"]);
    });
  });

  it("a list the server refused reads as a line with Try again, the cause in the wizard's words", async () => {
    let calls = 0;
    await mount({}, async () => {
      if (calls++ === 0) throw new ConnectError("ssh_connection_timeout", Code.FailedPrecondition);
      return { keys: [alice], otherLines: 0 };
    });
    const alert = document.querySelector('[role="alert"]');
    expect(alert?.textContent).toContain(en["node.sshKeys.loadFailed"]);
    expect(alert?.textContent).toContain("could not reach this SSH address and port in time");
    await click(button(en["common.retry"]));
    expect(listNodeServerKeys).toHaveBeenCalledTimes(2);
    expect(text()).toContain("alice@laptop");
  });
});

describe("connecting SSH access to a node installed by command", () => {
  const dialog = () => document.querySelector('[role="dialog"]')!;

  it("offers Connect on a live node with no access, and connects with the confirmed host key and one login", async () => {
    getSSHFingerprint.mockResolvedValue({ host: "fi1.example.com", port: 22, fingerprint: "SHA256:fi1-host", algorithm: "ssh-ed25519" });
    attachNodeServerAccess.mockImplementation(async () => {
      listNodeServerAccess.mockResolvedValue({ access: [access({ nodeName: "fi1", host: "fi1.example.com", panelKeyFingerprint: "" })] });
      return { access: {}, panelKeyError: "authorized_keys_unsafe" };
    });
    await mount(null, [alice], { name: "fi1", address: "fi1.example.com" });
    expect(text()).toContain(en["node.sshAccess.noAccess"]);
    await click(document.querySelector("[data-attach]"));
    expect(dialog().textContent).toContain("fi1");
    const hostField = dialog().querySelector<HTMLInputElement>('input[placeholder="de1.example.com"]')!;
    expect(hostField.value).toBe("fi1.example.com"); // the node's address, filled in
    await click(button(en["node.ssh.checkServer"]));
    expect(getSSHFingerprint).toHaveBeenCalledWith({ host: "fi1.example.com", port: 22 });
    expect(dialog().textContent).toContain("SHA256:fi1-host");

    const connect = () => button(en["node.sshAccess.connectNow"])!;
    expect(connect().disabled || connect().hasAttribute("data-disabled")).toBe(true);
    await act(async () => dialog().querySelector<HTMLInputElement>('input[type="checkbox"]')!.click());
    await type(dialog().querySelector<HTMLInputElement>('input[type="password"]')!, "server-password");
    expect(dialog().querySelector<HTMLInputElement>("input[data-install-panel-key]")?.checked).toBe(true);
    await click(connect());
    expect(attachNodeServerAccess).toHaveBeenCalledWith({
      nodeId: "nod_1",
      sshHost: "fi1.example.com",
      sshPort: 22,
      fingerprint: "SHA256:fi1-host",
      username: "root",
      password: "server-password",
      usePanelKey: false,
      installPanelKey: true,
      confirm: true,
    });
    await flush();
    // the card appears; the panel key that did not go on is a warning, not a failure
    expect(document.querySelector('input[placeholder="de1.example.com"]')).toBeNull(); // the window closed (the toast is a dialog too)
    expect(text()).toContain(en["node.sshAccess.connected"]);
    expect(text()).toContain(en["node.sshAccess.passwordOnly"]);
    expect(text()).toContain(en["node.sshAccess.keyNotAdded"].replace("{reason}", en["node.sshKeys.error.unsafe"]));
  });

  it("says when the server at that address is another node", async () => {
    getSSHFingerprint.mockResolvedValue({ host: "fi1.example.com", port: 22, fingerprint: "SHA256:fi1-host", algorithm: "ssh-ed25519" });
    attachNodeServerAccess.mockRejectedValueOnce(new ConnectError("node_identity_mismatch", Code.FailedPrecondition));
    await mount(null, [], { name: "fi1", address: "fi1.example.com" });
    await click(document.querySelector("[data-attach]"));
    await click(button(en["node.ssh.checkServer"]));
    await act(async () => dialog().querySelector<HTMLInputElement>('input[type="checkbox"]')!.click());
    await type(dialog().querySelector<HTMLInputElement>('input[type="password"]')!, "server-password");
    await click(button(en["node.sshAccess.connectNow"]));
    expect(dialog().querySelector('[role="alert"]')?.textContent).toContain(en["node.sshAccess.error.identity"]);
    expect(dialog().querySelector<HTMLInputElement>('input[type="password"]')?.value).toBe(""); // typed for one try
  });

  it("offers nothing without a live node to attach to", async () => {
    await mount(null, []);
    expect(document.querySelector("section")).toBeNull();
  });
});
