export const nodeDnsPresets = {
  yandex: ["77.88.8.8", "77.88.8.1"],
  cloudflareGoogle: ["1.1.1.1", "8.8.8.8"],
} as const;

/** "system" is an empty list: the node uses the server's own resolver. */
export type NodeDnsMode = "system" | "yandex" | "cloudflareGoogle" | "custom";

export function nodeDnsMode(resolvers: readonly string[]): NodeDnsMode {
  const normalized = resolvers.map((resolver) => resolver.trim()).filter(Boolean);
  if (normalized.length === 0) return "system";
  if (normalized.join(",") === nodeDnsPresets.yandex.join(",")) return "yandex";
  if (normalized.join(",") === nodeDnsPresets.cloudflareGoogle.join(",")) return "cloudflareGoogle";
  return "custom";
}

export function nodeDnsResolvers(mode: Exclude<NodeDnsMode, "custom">): string[] {
  if (mode === "yandex") return [...nodeDnsPresets.yandex];
  if (mode === "cloudflareGoogle") return [...nodeDnsPresets.cloudflareGoogle];
  return [];
}
