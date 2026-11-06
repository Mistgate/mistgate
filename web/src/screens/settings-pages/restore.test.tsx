import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { ToastProvider } from "@/components/ui/toast";
import { setLang } from "@/i18n";
import { RestoreDialog } from "./restore";

const prepareRestore = vi.fn();
const confirmRestore = vi.fn();
vi.mock("@/lib/api", async (orig) => ({
  ...(await orig<object>()),
  backups: { prepareRestore: (...a: unknown[]) => prepareRestore(...a), confirmRestore: (...a: unknown[]) => confirmRestore(...a) },
}));
const uploadBackup = vi.fn();
const waitForRestart = vi.fn();
vi.mock("@/lib/backup-restore", () => ({
  uploadBackup: (...a: unknown[]) => uploadBackup(...a),
  waitForRestart: (...a: unknown[]) => waitForRestart(...a),
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
afterEach(() => {
  setLang("en");
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  for (const m of [prepareRestore, confirmRestore, uploadBackup, waitForRestart]) m.mockReset();
});

const r2 = [
  { key: "mistgate/2026-11-05T00-00-00Z.tar.gz.age", sizeBytes: 81920n, createdUnix: 1_793_836_800n },
  { key: "mistgate/2026-11-04T00-00-00Z.tar.gz.age", sizeBytes: 80000n, createdUnix: 1_793_750_400n },
] as never;
const summary = { panelName: "Mistgate", madeByVersion: "v0.1.79", createdUnix: 1_793_836_800n, backupNodes: 4n, backupUsers: 120n, backupProfiles: 7n, currentNodes: 5n, currentUsers: 131n, currentProfiles: 7n };

async function mount(initialR2?: string) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  await act(async () =>
    root!.render(
      <QueryClientProvider client={qc}>
        <ToastProvider>
          <RestoreDialog open onOpenChange={() => {}} r2={r2} initialR2={initialR2} />
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
const button = (label: string) => [...document.querySelectorAll("button")].find((b) => b.textContent?.trim() === label) as HTMLButtonElement | undefined;
const click = (b: Element | undefined | null) => act(async () => void b?.dispatchEvent(new MouseEvent("click", { bubbles: true })));
const tab = (label: string) => [...document.querySelectorAll("[role=radio],[role=tab],button")].find((b) => b.textContent?.trim() === label);
function type(input: HTMLInputElement, value: string) {
  return act(async () => {
    const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
    set.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
}
const field = (label: string) => [...document.querySelectorAll("label")].find((l) => l.textContent?.includes(label))?.parentElement?.querySelector("input") as HTMLInputElement;
async function choose(input: HTMLInputElement, file: File) {
  Object.defineProperty(input, "files", { value: [file], configurable: true });
  await act(async () => void input.dispatchEvent(new Event("change", { bubbles: true })));
}

describe("the restore window", () => {
  it("checks an R2 backup with a passphrase, shows what is in it next to now, and restores only after the name is typed", async () => {
    prepareRestore.mockResolvedValue({ restoreId: "rst_1", summary });
    confirmRestore.mockResolvedValue({});
    waitForRestart.mockResolvedValue("back");
    await mount("mistgate/2026-11-04T00-00-00Z.tar.gz.age");
    await click(tab("File passphrase"));
    await type(field("Passphrase of the file"), "correct horse battery");
    await click(button("Check backup"));
    await settle();
    expect(uploadBackup).not.toHaveBeenCalled();
    expect(prepareRestore).toHaveBeenCalledWith(expect.objectContaining({ uploadId: "", r2ObjectKey: "mistgate/2026-11-04T00-00-00Z.tar.gz.age", passphrase: "correct horse battery" }));
    expect((prepareRestore.mock.calls[0]![0] as { identityFile: Uint8Array }).identityFile.length).toBe(0);
    expect(text()).toContain("Panel “Mistgate”, made by v0.1.79");
    expect(text()).toContain("In the backup");
    expect(text()).toContain("131");
    expect(text()).toContain("You sign in with the password and passkeys you had then");
    expect(button("Restore")?.disabled).toBe(true);
    await type(field("to confirm"), "Mistgat");
    expect(button("Restore")?.disabled).toBe(true);
    await type(field("to confirm"), "Mistgate");
    await click(button("Restore"));
    await settle();
    expect(confirmRestore).toHaveBeenCalledWith({ restoreId: "rst_1", typedPanelName: "Mistgate" });
    expect(text()).toContain("The panel is back");
    expect(button("Sign in")).toBeTruthy();
  });

  it("uploads a file from this computer first, then checks it by its upload id", async () => {
    uploadBackup.mockImplementation(async (_f: File, onProgress: (n: number) => void) => {
      onProgress(1);
      return "upl_9";
    });
    prepareRestore.mockResolvedValue({ restoreId: "rst_2", summary });
    await mount();
    const archive = new File(["age-encrypted"], "mistgate-backup-panel-v0.1.79.tar.gz.age");
    await choose(document.querySelector("input[type=file][accept^='.age']") as HTMLInputElement, archive);
    await click(tab("File passphrase"));
    await type(field("Passphrase of the file"), "correct horse battery");
    await click(button("Check backup"));
    await settle();
    expect(uploadBackup.mock.calls[0]![0]).toBe(archive);
    expect(prepareRestore).toHaveBeenCalledWith(expect.objectContaining({ uploadId: "upl_9", r2ObjectKey: "" }));
    expect(text()).toContain("mistgate-backup-panel-v0.1.79.tar.gz.age");
  });

  it("words each refusal of the check by its step", async () => {
    await mount("mistgate/2026-11-05T00-00-00Z.tar.gz.age");
    await click(tab("File passphrase"));
    await type(field("Passphrase of the file"), "wrong passphrase!");
    prepareRestore.mockRejectedValueOnce(new ConnectError("the archive could not be decrypted or did not pass verification", Code.FailedPrecondition));
    await click(button("Check backup"));
    await settle();
    expect(text()).toContain("The backup could not be opened or did not pass the check");
    prepareRestore.mockRejectedValueOnce(new ConnectError("a panel restore is already running", Code.Aborted));
    await click(button("Check backup"));
    await settle();
    expect(text()).toContain("A backup, an update or another restore is running");
    expect(confirmRestore).not.toHaveBeenCalled();
  });

  it("says when the helper cannot restore, and stays on the confirmation", async () => {
    prepareRestore.mockResolvedValue({ restoreId: "rst_3", summary });
    confirmRestore.mockRejectedValue(new ConnectError("panel restore helper is unavailable", Code.FailedPrecondition));
    await mount("mistgate/2026-11-05T00-00-00Z.tar.gz.age");
    await click(tab("File passphrase"));
    await type(field("Passphrase of the file"), "correct horse battery");
    await click(button("Check backup"));
    await settle();
    await type(field("to confirm"), "Mistgate");
    await click(button("Restore"));
    await settle();
    expect(text()).toContain("the panel's update helper is unavailable");
    expect(waitForRestart).not.toHaveBeenCalled();
    expect(button("Restore")).toBeTruthy();
  });
});
