import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import { halo } from "@/components/fx/halo";
import { markFresh } from "@/components/fx/registry";
import { Button } from "@/components/ui/button";
import { Flag } from "@/components/ui/flag";
import { Icon, IconChip, type IconName } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { ProtocolTag } from "@/components/ui/protocol-tag";
import { Segmented } from "@/components/ui/segmented";
import { Switch } from "@/components/ui/switch";
import { useToast } from "@/components/ui/toast";
import { App, NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import type { Group } from "@/gen/mistgate/admin/v1/group_pb";
import { groups as groupsApi } from "@/lib/api";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { nodesQuery } from "@/lib/queries";
import { telegramOnly, trustTunnelOnly } from "@/screens/profiles/protocol-meta";
import { useEdition } from "@/lib/edition";
import { useLinkAppNames } from "@/screens/subscriptions/queries";
import { DnsSelect, useInheritedDns } from "./dns-select";
import { groupTones, leastUsedTone } from "./format";
import { groupsQuery, profileListQuery, protocolsQuery } from "./rpc";
import { useTx } from "./t";

// "New group" as the redesign board draws it (ug-dlg): the name with its colour, what to take the settings from, and what the
// people of the group get, as four switches (the subscription, AmneziaVPN keys, the Telegram proxy, TrustTunnel), then the nodes.
// A way is on while the group holds a profile of it; the switch puts all the profiles of the way in or out, and "Profiles one by one"
// picks them singly. The board's "Limits for each" has no backend (a group has no limits yet) and is not here.

type Profile = { id: string; name: string; protocol: string };
type WayId = "sub" | "awg" | "tg" | "tt";
const ways: { id: WayId; icon: IconName; title: "ul.get.subTitle" | "ul.get.awgTitle" | "tgproxy.toggle" | "tt.toggle"; hint: "ul.get.subHint" | "ul.get.awgHint2" | "ul.get.tg" | "ul.get.ttHint" }[] = [
  { id: "sub", icon: "link", title: "ul.get.subTitle", hint: "ul.get.subHint" },
  { id: "awg", icon: "key", title: "ul.get.awgTitle", hint: "ul.get.awgHint2" },
  { id: "tg", icon: "plane", title: "tgproxy.toggle", hint: "ul.get.tg" },
  { id: "tt", icon: "tunnel", title: "tt.toggle", hint: "ul.get.ttHint" },
];

/** The way a profile reaches people by: its own links (Telegram, TrustTunnel), a key (AmneziaVPN only) or the subscription. */
export function useWayOf() {
  const protocols = useQuery(protocolsQuery).data;
  return (p: { protocol: string }): WayId => {
    const apps = protocols?.find((x) => x.id === p.protocol)?.apps;
    if (apps ? telegramOnly(apps) : p.protocol === "mtproto") return "tg";
    if (apps ? trustTunnelOnly(apps) : p.protocol === "trusttunnel") return "tt";
    if (apps) return apps.includes(App.AMNEZIA) && !apps.includes(App.HAPP) ? "awg" : "sub";
    return p.protocol === "awg" ? "awg" : "sub";
  };
}

/** "New group": the board's window; `initialProfiles` (the profile page) starts with those profiles in. */
export function NewGroupModal({ open, onOpenChange, initialProfiles, initialName, onCreated }: { open: boolean; onOpenChange: (o: boolean) => void; initialProfiles?: string[]; initialName?: string; onCreated?: (g: Group) => void }) {
  const t = useTx();
  return (
    <Modal open={open} onOpenChange={onOpenChange} title={t("users.newGroup")} closeLabel={t("common.close")} className="md:max-w-[920px]!" shot="new-group">
      <GroupWindow initialProfiles={initialProfiles} initialName={initialName} onClose={() => onOpenChange(false)} onCreated={onCreated} />
    </Modal>
  );
}

const field = "flex flex-col gap-1.5 text-[13px] text-sec";

function GroupWindow({ initialProfiles, initialName, onClose, onCreated }: { initialProfiles?: string[]; initialName?: string; onClose: () => void; onCreated?: (g: Group) => void }) {
  const t = useTx();
  const toast = useToast();
  const qc = useQueryClient();
  const all = useQuery(groupsQuery).data ?? [];
  const profiles: Profile[] = useQuery(profileListQuery).data ?? [];
  const nodeList = useQuery(nodesQuery);
  const wayOf = useWayOf();
  const edition = useEdition();
  const apps = useLinkAppNames();
  const ttOff = edition.edge || edition.off("trusttunnel");
  const nodes = (nodeList.data?.nodes ?? []).filter((n) => n.status !== NodeStatus.RETIRED).sort((a, b) => a.name.localeCompare(b.name));

  const [name, setName] = useState(initialName ?? "");
  const [color, setColor] = useState<string | null>(null);
  const [base, setBase] = useState("");
  // null = every profile (a new group starts with all of them in, as the old form did)
  const [chosen, setChosen] = useState<string[] | null>(initialProfiles ?? null);
  const [allNodes, setAllNodes] = useState(true);
  const [off, setOff] = useState<string[]>([]);
  const [dnsPreset, setDnsPreset] = useState("");
  const [one, setOne] = useState(initialProfiles !== undefined);
  const [more, setMore] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const inheritedDns = useInheritedDns();

  const ids = chosen ?? profiles.map((p) => p.id);
  const shownColor = color ?? leastUsedTone(all);
  const taken = name.trim() !== "" && all.some((g) => g.name.trim().toLowerCase() === name.trim().toLowerCase());
  const ok = name.trim() !== "" && !taken;
  const byWay = Object.fromEntries(ways.map((w) => [w.id, profiles.filter((p) => wayOf(p) === w.id)])) as Record<WayId, Profile[]>;
  const onWay = (w: WayId) => byWay[w].some((p) => ids.includes(p.id));
  const needWay = profiles.length > 0;
  const hasWay = ways.some((w) => onWay(w.id));
  const flip = (w: WayId, on: boolean) => {
    const rest = ids.filter((id) => !byWay[w].some((p) => p.id === id));
    setChosen(on ? [...rest, ...byWay[w].map((p) => p.id)] : rest);
    setBase("");
  };
  const fromGroup = (g: Group | undefined) => {
    setBase(g?.id ?? "");
    if (!g) return;
    setChosen([...g.profileIds]);
    setOff([...g.disabledNodeIds]);
    setAllNodes(g.disabledNodeIds.length === 0);
    setDnsPreset(g.dnsPresetId);
  };
  const disabled = allNodes ? [] : off;

  const create = useMutation({
    mutationFn: async () => (await groupsApi.createGroup({ name: name.trim(), profileIds: ids, ...(disabled.length > 0 && { disabledNodeIds: disabled }), dnsPresetId: dnsPreset, ...(color !== null && { color }) })).group,
    onSuccess: async (g) => {
      if (!g) return;
      markFresh("group", g.id); // the card sweeps in once
      await Promise.all(["groups", "users", "subs", "profiles", "nodes"].map((k) => qc.invalidateQueries({ queryKey: [k] })));
      toast(t("users.groupCreated", { name: g.name }));
      halo("create");
      onCreated?.(g);
      onClose();
    },
    onError: (e) => setError(errorText(e, t)),
  });
  const ready = ok && (!needWay || hasWay) && !create.isPending;
  const submit = () => {
    if (!ready) return;
    setError(null);
    create.mutate();
  };
  const summary = !name.trim() ? t("ul.g.enterName") : taken ? t("ul.g.fixName") : needWay && !hasWay ? t("ul.g.needWay") : t("ul.g.sum", { n: ids.length });
  const hint = !name.trim() ? t("ul.g.nameHint") : taken ? t("ul.g.dup") : t("ul.g.free");

  return (
    <form
      className="@container/usd flex min-h-0 flex-col gap-5"
      onSubmit={(e) => {
        e.preventDefault();
        submit();
      }}
    >
      <div className="grid grid-cols-2 items-start gap-6 @max-[672px]/usd:grid-cols-1">
        <div className="flex min-w-0 flex-col gap-5">
          <label className={field}>
            {t("users.groupName")}
            <span className="relative block">
              <span
                aria-hidden
                data-tone={shownColor}
                className="pointer-events-none absolute start-2 top-1/2 -mt-3.5 grid size-7 place-items-center rounded-full bg-(--t) text-xs font-medium text-on-accent transition-colors duration-200"
              >
                {name.trim() ? [...name.trim()][0]!.toUpperCase() : "?"}
              </span>
              <input
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder={t("users.groupNamePh")}
                autoFocus
                autoComplete="off"
                spellCheck={false}
                maxLength={64}
                aria-invalid={taken || undefined}
                className={cx(
                  "h-11 w-full rounded-field border bg-canvas ps-[46px] pe-3 text-sm text-fg outline-none transition-[border-color,box-shadow] duration-150 placeholder:text-muted focus:shadow-[0_0_0_3px_var(--focus-ring)]",
                  taken ? "border-danger" : ok ? "border-accent-line" : "border-line focus:border-accent-line",
                )}
              />
            </span>
            <small className={cx("text-xs", taken ? "text-danger-text" : ok ? "text-accent-text" : "text-muted")}>{hint}</small>
          </label>

          <div className="grid grid-cols-[auto_minmax(0,1fr)] items-start gap-4 @max-[672px]/usd:grid-cols-1">
            <div className={field}>
              <span id="ug-color">{t("users.groupColor")}</span>
              <div role="group" aria-labelledby="ug-color" className="flex h-10 items-center gap-1.5">
                {groupTones.map((tone) => (
                  <button
                    key={tone}
                    type="button"
                    aria-pressed={shownColor === tone}
                    aria-label={t(`settings.accent.${tone}`)}
                    title={t(`settings.accent.${tone}`)}
                    data-tone={tone}
                    onClick={() => setColor(tone)}
                    className="group/sw grid size-7 place-items-center rounded-full border border-transparent transition-[border-color,transform] duration-150 aria-pressed:border-(--t)"
                  >
                    <i className="size-4 rounded-full bg-(--t) transition-transform duration-200 ease-spring group-hover/sw:scale-[1.15]" />
                  </button>
                ))}
              </div>
            </div>
            <div className={field}>
              <span>{t("ul.g.base")}</span>
              <Segmented
                variant="flat"
                aria-label={t("ul.g.baseLabel")}
                className="w-full [&_button]:flex-1 [&_button]:truncate"
                value={base}
                onValueChange={(v) => fromGroup(all.find((g) => g.id === v))}
                options={[{ value: "", label: t("ul.g.scratch") }, ...all.slice(0, 2).map((g) => ({ value: g.id, label: g.name }))]}
              />
            </div>
          </div>
        </div>

        <div className="flex min-w-0 flex-col gap-5">
          <div className={field}>
            {t("ul.g.gets")}
            <div className="overflow-hidden rounded-xl border border-line">
              {ways
                .filter((w) => w.id !== "tt" || !ttOff)
                .map((w) => {
                  const list = byWay[w.id];
                  const protocols = [...new Set(list.map((p) => p.protocol))];
                  const on = onWay(w.id);
                  return (
                    <label key={w.id} className={cx("grid cursor-pointer grid-cols-[32px_minmax(0,1fr)_auto] items-center gap-3 border-t border-line px-3 py-2 first:border-t-0", !on && "[&>span:nth-child(2)]:opacity-60", list.length === 0 && "cursor-default")}>
                      <IconChip icon={w.icon} tone={on ? "mint" : "sage"} size={32} />
                      <span className="flex min-w-0 flex-col transition-opacity">
                        <b className="truncate text-sm font-medium text-fg">{w.id === "sub" ? apps : t(w.title)}</b>
                        <span className="truncate text-xs text-muted">{list.length === 0 ? t("ul.g.noProfiles") : t(w.hint)}</span>
                        {protocols.length > 0 && (
                          <span className="mt-1 flex flex-wrap gap-1">
                            {protocols.map((p) => (
                              <ProtocolTag key={p} protocol={p} className="h-5 text-[11px]" />
                            ))}
                          </span>
                        )}
                      </span>
                      <Switch aria-label={w.id === "sub" ? apps : t(w.title)} checked={on} disabled={list.length === 0} onCheckedChange={(v) => flip(w.id, v)} />
                    </label>
                  );
                })}
            </div>
            {profiles.length > 0 && (
              <>
                <button type="button" aria-expanded={one} onClick={() => setOne(!one)} className="inline-flex items-center gap-1 self-start text-xs text-sec transition-colors hover:text-fg">
                  {t("ul.g.one")}
                  <Icon name="chevronDown" size={14} className={cx("transition-transform duration-250", one && "rotate-180")} />
                </button>
                {one && (
                  <div className="flex flex-wrap gap-1.5">
                    {profiles.map((p) => (
                      <Pill
                        key={p.id}
                        on={ids.includes(p.id)}
                        onClick={() => {
                          setBase("");
                          setChosen(ids.includes(p.id) ? ids.filter((x) => x !== p.id) : [...ids, p.id]);
                        }}
                      >
                        {p.name}
                      </Pill>
                    ))}
                  </div>
                )}
              </>
            )}
          </div>

          <div className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-3 rounded-xl border border-line p-3.5">
            <label className="flex min-w-0 cursor-pointer flex-col">
              <b className="text-sm font-medium text-fg">{t("users.nodesAll")}</b>
              <span className="text-[13px] text-muted">{allNodes ? t("users.nodesAllHint") : t("ul.g.nodesSome")}</span>
            </label>
            <Switch aria-label={t("users.nodesAll")} checked={allNodes} onCheckedChange={setAllNodes} />
            {nodes.length > 0 && (
              <div className="col-span-full flex flex-wrap gap-1.5">
                {nodes.map((n) => {
                  const on = allNodes || !off.includes(n.id);
                  return (
                    <button
                      key={n.id}
                      type="button"
                      aria-pressed={on}
                      onClick={() => !allNodes && setOff(off.includes(n.id) ? off.filter((x) => x !== n.id) : [...off, n.id])}
                      className={cx("inline-flex h-8 items-center gap-2 rounded-2xl border ps-2 pe-3 text-xs transition-[border-color,color] duration-150", on ? "border-accent-line text-fg" : "border-line text-muted", allNodes && "cursor-default border-line")}
                    >
                      <Flag code={n.countryCode} size={14} />
                      <span className="font-mono">{n.name}</span>
                    </button>
                  );
                })}
              </div>
            )}
          </div>

          <div className="flex flex-col gap-2">
            <button type="button" aria-expanded={more} onClick={() => setMore(!more)} className="inline-flex items-center gap-1 self-start text-[13px] text-sec transition-colors hover:text-fg">
              {t("users.groupMore")}
              <Icon name="chevronDown" size={16} className={cx("transition-transform duration-250", more && "rotate-180")} />
            </button>
            {more && (
              <div className="flex animate-[mg-up_0.25s_var(--e)_both] flex-col gap-1.5 rounded-xl border border-line p-3.5">
                <b className="text-sm font-medium">{t("subs.dns.field")}</b>
                <DnsSelect value={dnsPreset} inherited={inheritedDns} onChange={setDnsPreset} scope="group" />
                <span className="text-xs leading-snug text-muted">{t("subs.dns.inheritGroupHint")}</span>
              </div>
            )}
          </div>
        </div>
      </div>
      {error && <Notice tone="danger">{error}</Notice>}
      <div className="sticky -bottom-px -mx-4 -mb-5 flex flex-wrap items-center gap-2 border-t border-line bg-surface px-4 py-3 md:-mx-6 md:-mb-6 md:px-6 md:py-3.5">
        <span className="me-auto min-w-0 text-xs text-muted @max-[672px]/usd:basis-full">{summary}</span>
        <Button variant="secondary" size="lg" onClick={onClose}>
          {t("users.cancel")}
        </Button>
        <Button type="submit" variant="primary" size="lg" aria-disabled={!ready} className={cx(!ready && "opacity-45")} disabled={create.isPending}>
          {create.isPending && <span aria-hidden className="size-3.5 animate-[mg-ui-spin_0.7s_linear_infinite] rounded-full border-[1.5px] border-current border-t-transparent" />}
          {t("users.groupCreate")}
        </Button>
      </div>
    </form>
  );
}

function Pill({ on, onClick, children }: { on: boolean; onClick: () => void; children: ReactNode }) {
  return (
    <button type="button" aria-pressed={on} onClick={onClick} className={cx("inline-flex h-8 items-center rounded-2xl border px-3 text-xs transition-colors duration-150", on ? "border-accent-line bg-accent-soft text-fg" : "border-line text-muted")}>
      {children}
    </button>
  );
}

