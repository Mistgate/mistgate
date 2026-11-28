import { queryOptions, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { App } from "@/gen/mistgate/admin/v1/common_pb";
import { useT } from "@/i18n";
import { dns, subscriptions, users } from "@/lib/api";
import { plain } from "@/lib/plain";
import { textKey, type DnsData, type ResponseHeader, type Settings } from "./model";

// Query keys sit under "subs" / "dns". Settings change only when an admin saves, so there is no polling; a save
// invalidates them. The user card reads the presets through the same key, so a change here shows there.
export const settingsQuery = queryOptions({
  queryKey: ["subs", "settings"],
  queryFn: async ({ signal }) => {
    const r = await subscriptions.getSubscriptionSettings({}, { signal });
    // the servers of the busiest group, for the preview of the server names (empty: the preview makes some up)
    const samples = (r.serverSamples ?? []).map((s) => ({ node: s.node, countryCode: s.countryCode, profile: s.profile, loadPercent: s.loadPercent }));
    // sharedLinkUsers: people on the place the apps without a device ID share, for the "strict" confirmation; linkAwgKeys: the
    // AmneziaWG keys the link apps hold, for the confirmation of taking AmneziaWG out of them
    return { settings: plain(r.settings!), reservedHeaderNames: r.reservedHeaderNames ?? [], effectiveTitle: r.effectiveTitle, samples, sampleGroup: r.sampleGroup ?? "", namesLanguage: r.namesLanguage ?? "", sharedLinkUsers: r.sharedLinkUsers ?? 0, linkAwgKeys: r.linkAwgKeys ?? 0 };
  },
  staleTime: 30_000,
});

/**
 * The apps that take the subscription link (kind HAPP) in this instance's settings, by name without repeats ("Happ,
 * FlClash"), for the admin's labels; the generic words of subs.kind.happ while the settings load or name none.
 */
export function useLinkAppNames(): string {
  const t = useT();
  const names = useLinkAppList();
  return names.length > 0 ? names.join(", ") : t("subs.kind.happ");
}

/** The names of the apps on the link, each once; empty when the page lists none (a sentence then words it without names). */
export function useLinkAppList(): string[] {
  const { data } = useQuery(settingsQuery);
  return [...new Set((data?.settings.apps ?? []).filter((a) => a.kind === App.HAPP).map((a) => a.name).filter(Boolean))];
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
  queryFn: async ({ signal }) => plain((await users.listUsers({ pageSize: 50 }, { signal })).users).map((u) => ({ id: u.id, name: u.name, groupId: u.groupId })),
  staleTime: 30_000,
});

/** What the answer of one person to one User-Agent carries of the headers in `headers` (the editor's draft), asked of the panel. */
export const headerPreviewQuery = (userId: string, userAgent: string, headers: ResponseHeader[]) =>
  queryOptions({
    queryKey: ["subs", "hdr-preview", userId, userAgent, headers],
    queryFn: async ({ signal }) => plain(await subscriptions.previewResponseHeaders({ userId, userAgent, headers }, { signal })),
    staleTime: 0,
  });

/**
 * What the panel makes of a template text for one person (the profile and what to look at), asked of the text as the form holds it.
 * A text the panel would refuse comes back as an error (InvalidArgument) that names the line. The key includes the person's
 * current primary group and a short hash of the source; a save refreshes every one of these.
 */
export const mihomoPreviewQuery = (userId: string, primaryGroupId: string, yaml: string, groupIds: string[]) =>
  queryOptions({
    queryKey: ["subs", "mt-preview", userId, primaryGroupId, textKey(yaml), groupIds],
    queryFn: async ({ signal }) => plain(await subscriptions.previewMihomoTemplate({ userId, yaml, groupIds }, { signal })),
    staleTime: 60_000,
    retry: false,
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

/**
 * What the panel makes of a pasted profile (a link, base64 or JSON): the entry it would store, with what it holds, or its refusal as an
 * error (InvalidArgument) that says what is wrong. The key carries a short hash of the text; nothing is written.
 */
export const routingPreviewQuery = (input: string) =>
  queryOptions({
    queryKey: ["subs", "rt-preview", textKey(input)],
    queryFn: async ({ signal }) => plain((await subscriptions.previewRoutingProfile({ input }, { signal })).profile!),
    staleTime: 60_000,
    retry: false,
  });
