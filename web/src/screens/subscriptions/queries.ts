import { queryOptions, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { App } from "@/gen/mistgate/admin/v1/common_pb";
import { useT } from "@/i18n";
import { dns, subscriptions, users } from "@/lib/api";
import { plain } from "@/lib/plain";
import type { DnsData, Settings } from "./model";

// Query keys sit under "subs" / "dns". Settings change only when an admin saves, so there is no polling; a save
// invalidates them. The user card reads the presets through the same key, so a change here shows there.
export const settingsQuery = queryOptions({
  queryKey: ["subs", "settings"],
  queryFn: async ({ signal }) => {
    const r = await subscriptions.getSubscriptionSettings({}, { signal });
    // the servers of the busiest group, for the preview of the server names (empty: the preview makes some up)
    const samples = (r.serverSamples ?? []).map((s) => ({ node: s.node, countryCode: s.countryCode, profile: s.profile, loadPercent: s.loadPercent }));
    return { settings: plain(r.settings!), effectiveTitle: r.effectiveTitle, samples, sampleGroup: r.sampleGroup ?? "", namesLanguage: r.namesLanguage ?? "" };
  },
  staleTime: 30_000,
});

/**
 * The apps that take the subscription link (kind HAPP) in this instance's settings, by name without repeats ("Happ,
 * FlClash"), for the admin's labels; the generic words of subs.kind.happ while the settings load or name none.
 */
export function useLinkAppNames(): string {
  const t = useT();
  const { data } = useQuery(settingsQuery);
  const names = [...new Set((data?.settings.apps ?? []).filter((a) => a.kind === App.HAPP).map((a) => a.name).filter(Boolean))];
  return names.length > 0 ? names.join(", ") : t("subs.kind.happ");
}

export const clientsQuery = queryOptions({
  queryKey: ["subs", "clients"],
  queryFn: async ({ signal }) => plain((await subscriptions.listClients({}, { signal })).clients),
  staleTime: Infinity, // clients follow the registered plugins: a panel upgrade
});

export const testAgentQuery = (userAgent: string) =>
  queryOptions({
    queryKey: ["subs", "test-ua", userAgent],
    queryFn: async ({ signal }) => plain(await subscriptions.testUserAgent({ userAgent }, { signal })),
    // the answer depends on the saved rules: the key carries them so a saved change asks again
    staleTime: 0,
  });

/** The user picker of the page preview: the first page of users, enough to see the page as somebody. */
export const pickerUsersQuery = queryOptions({
  queryKey: ["users", "picker"],
  queryFn: async ({ signal }) => plain((await users.listUsers({ pageSize: 50 }, { signal })).users).map((u) => ({ id: u.id, name: u.name })),
  staleTime: 30_000,
});

export const dnsPresetsQuery = queryOptions({
  queryKey: ["dns", "presets"],
  queryFn: async ({ signal }): Promise<DnsData> => {
    const r = plain(await dns.listDnsPresets({}, { signal }));
    return { presets: r.presets, providers: r.providers, clientSupport: r.clientSupport };
  },
  staleTime: 15_000,
});

/**
 * Saves settings: the caller hands a change, it is merged onto the settings as the panel holds them now (the
 * update replaces everything, so a stale copy would undo somebody else's edit), and every view of them refreshes.
 */
export function useSaveSettings() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: async (change: (current: Settings) => Settings) => {
      const fresh = await qc.fetchQuery({ ...settingsQuery, staleTime: 0 });
      const res = await subscriptions.updateSubscriptionSettings({ settings: change(fresh.settings) });
      return plain(res.settings!);
    },
    onSuccess: async () => {
      await Promise.all([qc.invalidateQueries({ queryKey: ["subs"] }), qc.invalidateQueries({ queryKey: ["dns"] }), qc.invalidateQueries({ queryKey: ["users"] })]);
    },
  });
}
