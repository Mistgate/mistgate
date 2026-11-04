import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { en } from "@/i18n/ops";
import { SSHAccessCard } from "./ssh-access";

const listNodeServerAccess = vi.fn();
const revealNodeServerPassword = vi.fn();
const forgetNodeServerAccess = vi.fn();
const rotateNodeServerPassword = vi.fn();

vi.mock("@/lib/api", () => ({
  provisioning: {
    listNodeServerAccess: (...args: unknown[]) => listNodeServerAccess(...args),
    revealNodeServerPassword: (...args: unknown[]) => revealNodeServerPassword(...args),
    forgetNodeServerAccess: (...args: unknown[]) => forgetNodeServerAccess(...args),
    rotateNodeServerPassword: (...args: unknown[]) => rotateNodeServerPassword(...args),
  },
}));

vi.mock("@/components/step-up", () => ({
  useStepUp: () => async <T,>(call: () => Promise<T>) => call(),
  isStepUpCancelled: () => false,
}));

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
let client: QueryClient | null = null;

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  client = null;
  listNodeServerAccess.mockReset();
  revealNodeServerPassword.mockReset();
  forgetNodeServerAccess.mockReset();
  rotateNodeServerPassword.mockReset();
});

function tree(nodeId: string) {
  return (
    <QueryClientProvider client={client!}>
      <ToastProvider>
        <div role="tabpanel">
          <SSHAccessCard nodeId={nodeId} />
        </div>
      </ToastProvider>
    </QueryClientProvider>
  );
}

const saved = (over: object = {}) => ({
  access: ["nod_1", "nod_2"].map((id) => ({
    nodeId: id,
    nodeName: id === "nod_1" ? "de1" : "nl1",
    host: id === "nod_1" ? "203.0.113.7" : "203.0.113.24",
    port: 22,
    username: "root",
    passwordSaved: true,
    passwordLogin: true,
    panelKeyFingerprint: "",
    ...(id === "nod_1" ? over : {}),
  })),
});

async function mount(nodeId = "nod_1", over: object = {}) {
  listNodeServerAccess.mockResolvedValue(saved(over));
  revealNodeServerPassword.mockResolvedValue({ password: "secret-generated-inside-panel" });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () => root!.render(tree(nodeId)));
  await flush();
}

async function renderNode(nodeId: string) {
  await act(async () => root!.render(tree(nodeId)));
  await flush();
}

async function flush() {
  for (let i = 0; i < 4; i++) await act(async () => void (await new Promise((resolve) => setTimeout(resolve, 0))));
}

function deferReveal() {
  let finish!: (response: { password: string }) => void;
  revealNodeServerPassword.mockImplementationOnce(() => new Promise((resolve) => (finish = resolve)));
  return () => finish({ password: "late-secret" });
}

describe("node SSH access", () => {
  it("reveals on demand, then clears the secret when hidden without putting it in the query cache", async () => {
    await mount();
    expect(revealNodeServerPassword).not.toHaveBeenCalled();
    const show = document.querySelector("button");
    expect(show).toBeTruthy();
    await act(async () => show!.click());
    await flush();

    const input = document.querySelector<HTMLInputElement>("#ssh-password-nod_1");
    expect(input?.value).toBe("secret-generated-inside-panel");
    const cached = client?.getQueryData(["node-server-access", "nod_1"]);
    expect(cached).toBeTruthy();
    expect(cached).not.toHaveProperty("password");

    const hide = document.querySelector("button");
    expect(hide).toBeTruthy();
    await act(async () => hide!.click());
    expect(document.querySelector("#ssh-password-nod_1")).toBeNull();
  });

  it("clears a revealed password when its settings tab becomes inactive", async () => {
    await mount();
    await act(async () => document.querySelector("button")!.click());
    await flush();
    expect(document.querySelector<HTMLInputElement>("#ssh-password-nod_1")?.value).toBe("secret-generated-inside-panel");

    await act(async () => {
      document.querySelector('[role="tabpanel"]')!.setAttribute("hidden", "");
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    expect(document.querySelector("#ssh-password-nod_1")).toBeNull();
  });

  it("discards a pending reveal when the browser tab is hidden", async () => {
    await mount();
    const finish = deferReveal();
    await act(async () => document.querySelector("button")!.click());

    const priorVisibility = Object.getOwnPropertyDescriptor(document, "visibilityState");
    try {
      Object.defineProperty(document, "visibilityState", { configurable: true, value: "hidden" });
      await act(async () => document.dispatchEvent(new Event("visibilitychange")));
      Object.defineProperty(document, "visibilityState", { configurable: true, value: "visible" });
      await act(async () => document.dispatchEvent(new Event("visibilitychange")));
      await act(async () => {
        finish();
        await Promise.resolve();
      });
      await flush();
      expect(document.querySelector("#ssh-password-nod_1")).toBeNull();
    } finally {
      if (priorVisibility) Object.defineProperty(document, "visibilityState", priorVisibility);
      else Reflect.deleteProperty(document, "visibilityState");
    }
  });

  it("discards a pending reveal on pagehide", async () => {
    await mount();
    const finish = deferReveal();
    await act(async () => document.querySelector("button")!.click());

    await act(async () => window.dispatchEvent(new Event("pagehide")));
    await act(async () => {
      finish();
      await Promise.resolve();
    });
    await flush();
    expect(document.querySelector("#ssh-password-nod_1")).toBeNull();
  });

  it("discards a pending reveal when its settings tab becomes inactive", async () => {
    await mount();
    const finish = deferReveal();
    await act(async () => document.querySelector("button")!.click());

    await act(async () => {
      document.querySelector('[role="tabpanel"]')!.setAttribute("hidden", "");
      await new Promise((resolve) => setTimeout(resolve, 0));
    });
    await act(async () => {
      finish();
      await Promise.resolve();
    });
    await flush();
    expect(document.querySelector("#ssh-password-nod_1")).toBeNull();
  });

  it("shows both candidates of an interrupted change, labelled unverified, when the server could not be checked", async () => {
    await mount();
    revealNodeServerPassword.mockResolvedValueOnce({ password: "current-one", pendingPassword: "pending-one", unverified: true });
    await act(async () => document.querySelector("button")!.click());
    await flush();
    expect(document.querySelector<HTMLInputElement>("#ssh-password-nod_1")?.value).toBe("current-one");
    expect(document.querySelector<HTMLInputElement>("#ssh-pending-password-nod_1")?.value).toBe("pending-one");
    expect(document.body.textContent).toContain("Current password (not verified)");
    expect(document.body.textContent).toContain("New password from the interrupted change (not verified)");
    await act(async () => document.querySelector("button")!.click()); // hide
    expect(document.querySelector("#ssh-pending-password-nod_1")).toBeNull();
  });

  it("keeps a retired node's access until the owner forgets it, after a second click", async () => {
    await mount();
    expect(document.body.textContent).not.toContain("Forget saved access");
    act(() => root?.unmount());
    host?.remove();

    const retired = { nodeId: "nod_1", nodeName: "de1", host: "203.0.113.7", port: 22, username: "root", nodeRetired: true, passwordGenerated: true, passwordSaved: true, passwordLogin: true, panelKeyFingerprint: "" };
    listNodeServerAccess.mockReset();
    listNodeServerAccess.mockResolvedValueOnce({ access: [retired] }).mockResolvedValue({ access: [] });
    forgetNodeServerAccess.mockResolvedValue({});
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    await act(async () => root!.render(tree("nod_1")));
    await flush();
    expect(document.body.textContent).toContain("only the panel knows it");
    expect(document.body.textContent).toContain("This node is retired");

    const forget = () => [...document.querySelectorAll("button")].find((b) => b.textContent?.startsWith("Forget"))!;
    await act(async () => forget().click());
    expect(forgetNodeServerAccess).not.toHaveBeenCalled();
    expect(document.body.textContent).toContain("deleted for good");
    await act(async () => forget().click());
    await flush();
    expect(forgetNodeServerAccess).toHaveBeenCalledWith({ nodeId: "nod_1" });
    expect(document.querySelector("section")).toBeNull(); // nothing saved any more: the card goes
  });

  it("does not show a previous node's password after nodeId changes during reveal", async () => {
    await mount();
    const finish = deferReveal();
    await act(async () => document.querySelector("button")!.click());

    await renderNode("nod_2");
    await act(async () => {
      finish();
      await Promise.resolve();
    });
    await flush();
    expect(document.querySelector("#ssh-password-nod_1")).toBeNull();
    expect(document.querySelector("#ssh-password-nod_2")).toBeNull();
  });

  describe("password change", () => {
    const toggle = () => document.querySelector<HTMLButtonElement>("button[aria-expanded]");
    const submit = () => document.querySelector<HTMLButtonElement>("form button[type=submit]")!;
    const own = () => document.querySelector<HTMLInputElement>("#ssh-new-password-nod_1");
    const shown = () => document.querySelector<HTMLInputElement>("#ssh-password-nod_1");
    const off = (b: HTMLButtonElement) => b.disabled || b.hasAttribute("data-disabled");
    function typeInto(input: HTMLInputElement, value: string) {
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, value);
      input.dispatchEvent(new Event("input", { bubbles: true }));
    }
    async function pickOwn() {
      await act(async () => toggle()!.click());
      await act(async () => document.querySelectorAll<HTMLElement>("[role=radio]")[1]!.click());
    }

    it("generates by default, shows the progress, then the new password through the reveal until 60 s pass", async () => {
      await mount();
      let finish!: (v: { rotated: boolean }) => void;
      rotateNodeServerPassword.mockImplementationOnce(() => new Promise((resolve) => (finish = resolve)));
      revealNodeServerPassword.mockResolvedValue({ password: "N4QWZ7KXJ2M5RT3VBY6CDF8GHE" });
      vi.useFakeTimers();
      try {
        await act(async () => toggle()!.click());
        expect(document.body.textContent).toContain(en["node.sshAccess.generateHint"]);
        await act(async () => submit().click());
        expect(rotateNodeServerPassword).toHaveBeenCalledWith({ nodeId: "nod_1", newPassword: "", generate: true, confirm: true });
        expect(document.querySelector('[role="status"]')?.textContent).toContain(en["node.sshAccess.changing"]);
        expect(revealNodeServerPassword).not.toHaveBeenCalled();

        await act(async () => {
          finish({ rotated: true });
          await vi.advanceTimersByTimeAsync(10);
        });
        expect(revealNodeServerPassword).toHaveBeenCalledWith({ nodeId: "nod_1" });
        expect(shown()?.value).toBe("N4QWZ7KXJ2M5RT3VBY6CDF8GHE");
        expect(document.body.textContent).toContain(en["node.sshAccess.newPassword"]);
        expect(document.querySelector("form")).toBeNull();

        await act(async () => void (await vi.advanceTimersByTimeAsync(59_000)));
        expect(shown()).not.toBeNull();
        await act(async () => void (await vi.advanceTimersByTimeAsync(1_000)));
        expect(shown()).toBeNull();
      } finally {
        vi.useRealTimers();
      }
    });

    it("hides the new password when its settings tab is left, and does not reveal one finished while away", async () => {
      await mount();
      rotateNodeServerPassword.mockResolvedValueOnce({ rotated: true });
      await act(async () => toggle()!.click());
      await act(async () => submit().click());
      await flush();
      expect(shown()?.value).toBe("secret-generated-inside-panel");
      await act(async () => {
        document.querySelector('[role="tabpanel"]')!.setAttribute("hidden", "");
        await new Promise((resolve) => setTimeout(resolve, 0));
      });
      expect(shown()).toBeNull();

      revealNodeServerPassword.mockClear();
      document.querySelector('[role="tabpanel"]')!.removeAttribute("hidden");
      let finish!: (v: { rotated: boolean }) => void;
      rotateNodeServerPassword.mockImplementationOnce(() => new Promise((resolve) => (finish = resolve)));
      await act(async () => toggle()!.click());
      await act(async () => submit().click());
      document.querySelector('[role="tabpanel"]')!.setAttribute("hidden", "");
      await act(async () => {
        finish({ rotated: true });
        await Promise.resolve();
      });
      await flush();
      expect(revealNodeServerPassword).not.toHaveBeenCalled();
      expect(shown()).toBeNull();
    });

    it("does not reveal for a node the owner switched away from while its change ran", async () => {
      await mount();
      let finish!: (v: { rotated: boolean }) => void;
      rotateNodeServerPassword.mockImplementationOnce(() => new Promise((resolve) => (finish = resolve)));
      await act(async () => toggle()!.click());
      await act(async () => submit().click());
      await renderNode("nod_2");
      await act(async () => {
        finish({ rotated: true });
        await Promise.resolve();
      });
      await flush();
      expect(revealNodeServerPassword).not.toHaveBeenCalled();
    });

    it("keeps an own password under 12 characters from being sent, and empties the field once sent", async () => {
      await mount();
      await pickOwn();
      await act(async () => typeInto(own()!, "elevenchars"));
      expect(off(submit())).toBe(true);
      await act(async () => typeInto(own()!, "twelve-chars"));
      expect(off(submit())).toBe(false);

      rotateNodeServerPassword.mockRejectedValueOnce(new ConnectError("ssh_authentication_failed", Code.FailedPrecondition));
      await act(async () => submit().click());
      await flush();
      expect(rotateNodeServerPassword).toHaveBeenCalledWith({ nodeId: "nod_1", newPassword: "twelve-chars", generate: false, confirm: true });
      expect(own()?.value).toBe("");
      expect(off(submit())).toBe(true);
      expect(document.querySelector('[role="alert"] p')?.textContent).toBe(en["node.sshAccess.error.auth"]);
    });

    it.each([
      ["ssh_rotation_recovery_required", Code.FailedPrecondition, "node.sshAccess.error.recovery"],
      ["ssh_authentication_failed", Code.FailedPrecondition, "node.sshAccess.error.auth"],
      ["node_retired", Code.FailedPrecondition, "node.sshAccess.error.retired"],
      ["new password must differ from the current password", Code.InvalidArgument, "node.sshAccess.error.same"],
    ] as const)("words %s for the owner", async (message, code, key) => {
      await mount();
      rotateNodeServerPassword.mockRejectedValueOnce(new ConnectError(message, code));
      await act(async () => toggle()!.click());
      await act(async () => submit().click());
      await flush();
      expect(document.querySelector('[role="alert"] p')?.textContent).toBe(en[key]);
    });

    it("an unconfirmed change closes the form; the refreshed state says what to do", async () => {
      await mount();
      expect(document.body.textContent).toContain(en["node.sshAccess.verified"]);
      listNodeServerAccess.mockResolvedValue(saved({ rotationPending: true }));
      rotateNodeServerPassword.mockRejectedValueOnce(new ConnectError("ssh_password_change_unverified", Code.Unavailable));
      await act(async () => toggle()!.click());
      await act(async () => submit().click());
      await flush();
      expect(document.querySelector("form")).toBeNull();
      expect(document.body.textContent).toContain(en["node.sshAccess.notConfirmed"]);
      expect(document.body.textContent).toContain(en["node.sshAccess.pendingHint"]);
    });

    it("a reveal refused because neither password logs in says so", async () => {
      await mount("nod_1", { rotationPending: true });
      revealNodeServerPassword.mockRejectedValueOnce(new ConnectError("ssh_rotation_recovery_required", Code.FailedPrecondition));
      await act(async () => document.querySelector("button")!.click());
      await flush();
      expect(document.body.textContent).toContain(en["node.sshAccess.error.recovery"]);
    });

    it("a retired node can still reveal the password but has no Change password", async () => {
      await mount("nod_1", { nodeRetired: true });
      expect(document.body.textContent).toContain(en["node.sshAccess.show"]);
      expect(toggle()).toBeNull();
    });
  });

  describe("the panel key and a node with no password", () => {
    const keyFp = "SHA256:pAnElKeYfInGeRpRiNt0123456789abcdefghijkl";
    // a pill reads glyph + word; the word is what the owner reads
    const pills = () => [...document.querySelectorAll(".pill")].map((p) => p.textContent?.replace(/^[–!×~]/, ""));
    const toggle = () => document.querySelector<HTMLButtonElement>("button[aria-expanded]");
    const buttonTexts = () => [...document.querySelectorAll("button")].map((b) => b.textContent);
    async function remount(over: object) {
      act(() => root?.unmount());
      host?.remove();
      await mount("nod_1", over);
    }

    it("says in the header how the panel gets in", async () => {
      await mount();
      expect(pills()).toContain(en["node.sshAccess.passwordOnly"]);
      expect(pills()).not.toContain(en["panelKey.title"]);
      await remount({ panelKeyFingerprint: keyFp });
      expect(pills()).toContain(en["panelKey.title"]);
      expect(pills()).not.toContain(en["node.sshAccess.passwordOnly"]);
    });

    it("a key install keeps no password: Not saved, nothing to reveal, and Set password through the key", async () => {
      await mount("nod_1", { passwordSaved: false, passwordLogin: false, panelKeyFingerprint: keyFp });
      expect(pills()).toContain(en["node.sshAccess.notSaved"]);
      expect(pills()).not.toContain(en["node.sshAccess.verified"]);
      expect(document.body.textContent).toContain(en["node.sshAccess.notSavedHint"]);
      expect(document.body.textContent).not.toContain(en["node.sshAccess.loginClosed"]); // no password, no fact about it
      expect(buttonTexts()).not.toContain(en["node.sshAccess.show"]);
      expect(toggle()?.textContent).toBe(en["node.sshAccess.setPassword"]);

      rotateNodeServerPassword.mockResolvedValueOnce({ rotated: true });
      revealNodeServerPassword.mockResolvedValue({ password: "N4QWZ7KXJ2M5RT3VBY6CDF8GHE" });
      listNodeServerAccess.mockResolvedValue(saved({ passwordSaved: true, passwordLogin: true, panelKeyFingerprint: keyFp, passwordGenerated: true }));
      await act(async () => toggle()!.click());
      const submit = document.querySelector<HTMLButtonElement>("form button[type=submit]")!;
      expect(submit.textContent).toBe(en["node.sshAccess.setNow"]);
      await act(async () => submit.click());
      await flush();
      expect(rotateNodeServerPassword).toHaveBeenCalledWith({ nodeId: "nod_1", newPassword: "", generate: true, confirm: true });
      expect(document.body.textContent).toContain(en["node.sshAccess.set"]);
      expect(document.querySelector<HTMLInputElement>("#ssh-password-nod_1")?.value).toBe("N4QWZ7KXJ2M5RT3VBY6CDF8GHE");
      expect(pills()).toContain(en["node.sshAccess.verified"]);
    });

    it("notes a saved password that no longer logs in over SSH: it is for the hoster console and sudo", async () => {
      await mount("nod_1", { panelKeyFingerprint: keyFp, passwordLogin: false });
      expect(document.body.textContent).toContain(en["node.sshAccess.loginClosed"]);
      expect(pills()).toContain(en["node.sshAccess.verified"]);
      expect(buttonTexts()).toContain(en["node.sshAccess.show"]);
      await remount({ panelKeyFingerprint: keyFp });
      expect(document.body.textContent).not.toContain(en["node.sshAccess.loginClosed"]);
    });

    it("words a refused login as the panel key when the panel logs in by key", async () => {
      await mount("nod_1", { panelKeyFingerprint: keyFp });
      rotateNodeServerPassword.mockRejectedValueOnce(new ConnectError("ssh_authentication_failed", Code.Unauthenticated));
      await act(async () => toggle()!.click());
      await act(async () => document.querySelector<HTMLButtonElement>("form button[type=submit]")!.click());
      await flush();
      expect(document.querySelector('[role="alert"] p')?.textContent).toBe(en["node.sshAccess.error.authKey"].replace("{file}", "~/.ssh/authorized_keys"));
    });

    it("forgetting a key install's access says the key stays on the server", async () => {
      await mount("nod_1", { nodeRetired: true, passwordSaved: false, passwordLogin: false, panelKeyFingerprint: keyFp });
      const forget = () => [...document.querySelectorAll("button")].find((b) => b.textContent?.startsWith("Forget"))!;
      await act(async () => forget().click());
      expect(document.body.textContent).toContain(en["node.sshAccess.forgetKey"].replace("{file}", "~/.ssh/authorized_keys"));
      expect(document.body.textContent).not.toContain(en["node.sshAccess.forgetConfirm"]);
    });
  });
});
