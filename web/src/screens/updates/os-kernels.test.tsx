import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { OsUpdateState } from "@/gen/mistgate/admin/v1/update_pb";
import { dictionaries, fill, setLang, type T } from "@/i18n";
import { en } from "@/i18n/en";
import { blockOf, blockText } from "@/lib/os-run";
import { kernelMarks, kernelVerdict } from "@/lib/os-kernels";
import type { Os, OsRow } from "@/lib/os-updates";
import { OsKernels } from "./os-kernels";

const t = ((key: keyof typeof en, vars?: Record<string, string | number>) => fill(dictionaries.en[key], vars)) as unknown as T;

// The kernels block of a node: an agent without the table gets one quiet line, a clean node reads "Next boot: ready" with its
// rows folded, a node whose reboot would be refused shows why, open, and its Reboot is off with the same reason.

beforeAll(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;
beforeEach(() => setLang("en"));
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
  setLang("en");
});

const now = Math.floor(Date.now() / 1000);
const status = (over: Partial<Os> = {}): Os =>
  ({
    measuredUnix: now - 60, distroId: "ubuntu", distroVersion: "24.04", pending: 0, security: 0, notable: [], kernelRunning: "7.0.0-37-generic", kernelNewest: "7.0.0-38-generic",
    rebootRequired: true, rebootReasons: ["kernel"], held: [], apt: "ok", halfConfigured: [], listsUnix: now - 3600, dpkgUnix: now - 7200, autoUpdates: "on", autoReboot: false,
    awgModuleNext: "", container: "", bootIdHash: "", error: "", kernels: [], awgSource: "", rebootBlockers: [], ...over,
  }) as Os;
const kernel = (version: string, over: Record<string, unknown> = {}) => ({
  version, running: false, nextBoot: false, candidate: true, image: true, initrd: true, grubEntry: true, headers: "present", module: "built", packages: "ok", ...over,
});
const clean = [kernel("7.0.0-38-generic", { nextBoot: true }), kernel("7.0.0-37-generic", { running: true }), kernel("7.0.0-36-generic", { candidate: false })];
// the incident: the newest kernel has no initrd, no GRUB entry, no module build, and its headers are half configured
const broken = [
  kernel("7.0.0-38-generic", { nextBoot: true, initrd: false, grubEntry: false, headers: "missing", module: "missing", packages: "half_configured" }),
  kernel("7.0.0-37-generic", { running: true }),
];
const row = (s: Os | undefined, over: Partial<OsRow> = {}): OsRow => ({
  id: "nod_1", name: "de1", nodeId: "nod_1", online: 2, onPanelServer: false, state: OsUpdateState.REBOOT_REQUIRED, status: s, updateCapable: true, updateBlock: "", updateBlockAll: "", rebootBlock: "", ...over,
});

function mount(r: OsRow, onlyBlocked = false) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root!.render(<OsKernels row={r} onlyBlocked={onlyBlocked} />));
  return host;
}

describe("the kernels block", () => {
  it("an agent without the table gets one quiet line", () => {
    const el = mount(row(status()));
    expect(el.textContent).toBe("Update the agent to see the kernel table");
    expect(el.querySelector("button")).toBeNull();
  });

  it("is not shown for a container, an unread node, or the panel's own server row", () => {
    expect(mount(row(status({ container: "lxc" }))).textContent).toBe("");
    act(() => root!.unmount());
    host!.remove();
    expect(mount(row(undefined)).textContent).toBe("");
    act(() => root!.unmount());
    host!.remove();
    expect(mount(row(status({ kernels: clean }), { nodeId: undefined })).textContent).toBe("");
  });

  it("a clean node: the verdict, rows folded away until opened, no marks in red", () => {
    const el = mount(row(status({ kernels: clean })));
    expect(el.textContent).toContain("Kernels");
    expect(el.textContent).toContain("Next boot: ready");
    expect(el.querySelectorAll("li")).toHaveLength(0);
    act(() => el.querySelector("button")!.click());
    expect(el.querySelectorAll("li")).toHaveLength(3);
    expect(el.textContent).toContain("7.0.0-38-generic");
    expect(el.textContent).toContain("next boot");
    expect(el.textContent).toContain("running");
    // five marks per kernel, each named
    const marks = el.querySelectorAll("li [role=img]");
    expect(marks).toHaveLength(15);
    expect(marks[0]!.getAttribute("aria-label")).toBe("Boot: OK");
    expect(el.querySelector(".tone-bad")).toBeNull();
    // nothing to show in the Updates table while all is well
    act(() => root!.unmount());
    host!.remove();
    expect(mount(row(status({ kernels: clean })), true).textContent).toBe("");
  });

  it("a node whose reboot would be refused: the reason in words, the rows open, the failing marks red", () => {
    const s = status({ kernels: broken, rebootBlockers: ["initramfs_missing", "boot_entry_missing", "dpkg_interrupted"] });
    const el = mount(row(s), true);
    expect(el.textContent).toContain("A reboot would be refused: no initrd for 7.0.0-38-generic");
    expect(el.textContent).toContain("+2 more");
    expect(el.querySelectorAll("li")).toHaveLength(2);
    const labels = [...el.querySelectorAll("li:first-child [role=img]")].map((m) => m.getAttribute("aria-label"));
    expect(labels).toEqual(["Boot: no initrd", "GRUB: missing", "Headers: missing", "Module: missing", "Packages: half-configured"]);
    expect(el.querySelectorAll("li:first-child .tone-bad").length).toBeGreaterThanOrEqual(4);
    expect(el.querySelectorAll("li:nth-child(2) .tone-bad")).toHaveLength(0);
  });
});

describe("the verdict and the reboot button", () => {
  it("names the kernel each blocker is about", () => {
    const v = (rebootBlockers: string[], kernels = broken) => kernelVerdict(status({ kernels: kernels as never, rebootBlockers }));
    expect(v(["boot_entry_missing"])).toMatchObject({ kind: "blocked", code: "boot_entry_missing", kernel: "7.0.0-38-generic", more: 0 });
    expect(v(["awg_module_missing"])).toMatchObject({ kernel: "7.0.0-38-generic" });
    expect(v(["dpkg_interrupted"])).toMatchObject({ kernel: "" });
    // an unknown code from a newer agent is not shown as a reason
    expect(v(["something_new"])).toEqual({ kind: "ok" });
    expect(kernelVerdict(status({ container: "lxc", kernels: broken as never }))).toEqual({ kind: "none" });
  });

  it("red marks only for what a reboot is refused for", () => {
    const old = kernelMarks(kernel("7.0.0-36-generic", { candidate: false, initrd: false, module: "missing", headers: "missing" }) as never);
    expect(old.map((m) => m.kind)).toEqual(["warn", "ok", "warn", "warn", "ok"]);
    expect(kernelMarks(kernel("7.0.0-38-generic", { module: "unknown" }) as never)[3]).toMatchObject({ state: "unknown", kind: "off" });
    expect(kernelMarks(kernel("7.0.0-38-generic", { module: "" }) as never)[3]).toMatchObject({ state: "none", kind: "off" });
  });

  it("Reboot is off with the reason when the table has blockers, and the panel's own reason comes first", () => {
    const blocked = row(status({ kernels: broken, rebootBlockers: ["initramfs_missing"] }));
    const key = blockOf(blocked, "reboot");
    expect(key).toBe("kernel_initramfs_missing:7.0.0-38-generic");
    expect(blockText(t, key)).toBe("A reboot would be refused: no initrd for 7.0.0-38-generic");
    // installing is not held by the table: the job installs and holds the reboot itself
    expect(blockOf(blocked, "install")).toBe("");
    expect(blockOf({ ...blocked, rebootBlock: "offline" }, "reboot")).toBe("offline");
    expect(blockOf(row(status({ kernels: clean })), "reboot")).toBe("");
    expect(blockOf(row(status()), "reboot")).toBe("");
  });
});
