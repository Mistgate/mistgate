import type { StatusKind } from "@/components/ui/status";
import type { MessageKey } from "@/i18n/en";
import type { Os } from "./os-updates";

// The kernel table of a node's OS status (OsStatus.kernels, rebootBlockers): one row per installed kernel with five checks, and the
// verdict "can the next reboot go through". The agent's reader fills them from files it can read; the job decides at the reboot.

export type KernelRow = Os["kernels"][number];

/** The state of one check: the word under it ("up.os.kern.state.<state>") and the colour of its mark. */
export type KernelMark = { id: "boot" | "grub" | "headers" | "module" | "packages"; state: string; kind: StatusKind };

export const kernelMarkIds = ["boot", "grub", "headers", "module", "packages"] as const;

/** Red is for what a reboot would be refused for (the next boot and the kernels GRUB falls back to); the rest is amber or grey. */
export function kernelMarks(k: KernelRow): KernelMark[] {
  const bootKind: StatusKind = k.candidate ? "bad" : "warn";
  const boot: KernelMark = !k.image ? { id: "boot", state: "no_image", kind: bootKind } : !k.initrd ? { id: "boot", state: "no_initrd", kind: bootKind } : { id: "boot", state: "ok", kind: "ok" };
  const grub: KernelMark = k.grubEntry ? { id: "grub", state: "ok", kind: "ok" } : { id: "grub", state: "missing", kind: k.nextBoot ? "bad" : "warn" };
  const headers: KernelMark = k.headers === "present" ? { id: "headers", state: "present", kind: "ok" } : { id: "headers", state: "missing", kind: "warn" };
  const module: KernelMark =
    k.module === "built" ? { id: "module", state: "built", kind: "ok" }
    : k.module === "missing" ? { id: "module", state: "missing", kind: k.candidate ? "bad" : "warn" }
    : k.module === "unknown" ? { id: "module", state: "unknown", kind: "off" }
    : { id: "module", state: "none", kind: "off" };
  const packages: KernelMark = k.packages === "half_configured" ? { id: "packages", state: "half", kind: "bad" } : { id: "packages", state: "ok", kind: "ok" };
  return [boot, grub, headers, module, packages];
}

export type KernelVerdict =
  /** Nothing to say: a container, no status yet, a failed read. */
  | { kind: "none" }
  /** The agent predates the table. */
  | { kind: "old" }
  | { kind: "ok" }
  /** The first blocker, the kernel it is about ("" when none), and how many more there are. */
  | { kind: "blocked"; code: string; kernel: string; why: MessageKey; more: number };

/** The kernel a blocker is about: the first row (newest first) the rule looks at. */
function blockerKernel(s: Os, code: string): KernelRow | undefined {
  const rows = s.kernels ?? [];
  switch (code) {
    case "initramfs_missing":
      return rows.find((k) => k.candidate && (!k.image || !k.initrd));
    case "boot_entry_missing":
      return rows.find((k) => k.nextBoot);
    case "awg_module_missing":
      return rows.find((k) => k.candidate && k.module === "missing");
    default:
      return undefined;
  }
}

/** The words of a blocker ("up.os.kern.why.<code>", with {kernel} where the rule is about one kernel). */
const whyKeys: Record<string, MessageKey> = {
  initramfs_missing: "up.os.kern.why.initramfs_missing",
  boot_entry_missing: "up.os.kern.why.boot_entry_missing",
  awg_module_missing: "up.os.kern.why.awg_module_missing",
  dpkg_interrupted: "up.os.kern.why.dpkg_interrupted",
  kernel_older: "up.os.kern.why.kernel_older",
  boot_default_custom: "up.os.kern.why.boot_default_custom",
};

export function kernelVerdict(s: Os | undefined): KernelVerdict {
  if (!s || s.error || s.container) return { kind: "none" };
  if (!s.kernels?.length) return { kind: "old" };
  // a code a newer agent sends and this panel does not know is not shown as a reason
  const codes = (s.rebootBlockers ?? []).filter((c) => c in whyKeys);
  if (codes.length === 0) return { kind: "ok" };
  const code = codes[0]!;
  const k = blockerKernel(s, code);
  return { kind: "blocked", code, kernel: k?.version ?? "", why: whyKeys[code]!, more: codes.length - 1 };
}

/** The reason a reboot is off because of the table, as a block key for blockOf ("kernel_<code>:<kernel>"); "" when nothing stands in the way. */
export function kernelBlock(s: Os | undefined): string {
  const v = kernelVerdict(s);
  return v.kind === "blocked" ? `kernel_${v.code}:${v.kernel}` : "";
}

/** The parts of a "kernel_<code>:<kernel>" block key. */
export function parseKernelBlock(key: string): { code: string; kernel: string } | null {
  if (!key.startsWith("kernel_")) return null;
  const rest = key.slice("kernel_".length);
  const at = rest.indexOf(":");
  const code = at < 0 ? rest : rest.slice(0, at);
  return code in whyKeys ? { code, kernel: at < 0 ? "" : rest.slice(at + 1) } : null;
}

export const kernelWhyKey = (code: string): MessageKey => whyKeys[code]!;
