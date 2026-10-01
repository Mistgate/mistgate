import type { T } from "@/i18n";
import { en, type MessageKey } from "@/i18n/en";
import type { DoctorItem } from "@/lib/health";

/**
 * The sentence for why the automatic build of the module did not happen or did not work: a code of the node's vocabulary
 * (agent.proto "AWG AND WARP"). A code this build does not know reads as the short English fact the node sent.
 */
export function prepReasonText(t: T, code: string, reason: string): string {
  const key = `awg.prep.reason.${code}`;
  return Object.hasOwn(en, key) ? t(key as MessageKey) : reason || code;
}

/** Codes whose English detail adds something the sentence does not say (which step, what the module answered). */
export const prepDetailCodes: ReadonlySet<string> = new Set(["step_failed", "module_unusable", "launch_failed"]);

/** Whole minutes a build has been running, "<1" in the first one. */
export function prepMinutes(sinceUnix: number, nowMs: number): string {
  const m = Math.floor((nowMs / 1000 - sinceUnix) / 60);
  return m < 1 ? "<1" : String(m);
}

/** Hosts where a kernel module cannot be loaded: the same list the node's doctor uses (doctor/env.go, container()). */
const containerVirts = new Set(["openvz", "lxc", "lxc-libvirt", "docker", "podman", "systemd-nspawn", "wsl", "container-other"]);

export type KernelReadiness = {
  /** ready: headers and build tools are there; missing: something is not; container: it cannot work here; unchecked: nobody looked yet. */
  state: "ready" | "missing" | "container" | "unchecked";
  /** What is missing ("headers, dkms"), for state "missing". */
  missing: string;
  virt: string;
};

/**
 * Can the AmneziaWG kernel module be built on this node? The node's doctor answers in the kernel_headers check, but only
 * once the node serves AmneziaWG (before that the check is skipped as "no_awg"), so a node that is about to get its first
 * AmneziaWG profile is "unchecked" unless its facts already say it is a container.
 */
export function kernelReadiness(item: Pick<DoctorItem, "detailCode" | "params"> | undefined, virt: string): KernelReadiness {
  const v = virt.toLowerCase();
  if (containerVirts.has(v)) return { state: "container", missing: "", virt };
  switch (item?.detailCode) {
    case "kernel_headers.ready":
      return { state: "ready", missing: "", virt };
    case "kernel_headers.missing":
    case "kernel_headers.optional":
    case "kernel_headers.userspace":
      return { state: "missing", missing: (item.params.missing ?? "").split(",").filter(Boolean).join(", "), virt };
    case "kernel_headers.container":
      return { state: "container", missing: "", virt: item.params.virt ?? virt };
  }
  return { state: "unchecked", missing: "", virt };
}
