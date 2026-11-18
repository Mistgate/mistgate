import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useMemo, useState, type ReactNode } from "react";
import { Flag } from "@/components/ui/flag";
import { Icon, type IconName } from "@/components/ui/icons";
import { Notice } from "@/components/ui/notice";
import { ProtocolTag } from "@/components/ui/protocol-tag";
import { Pending, QueryError } from "@/components/ui/query-error";
import { Switch } from "@/components/ui/switch";
import { useToast } from "@/components/ui/toast";
import { NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import type { Group } from "@/gen/mistgate/admin/v1/group_pb";
import type { AccessImpact } from "@/gen/mistgate/admin/v1/user_pb";
import { groups as groupsApi } from "@/lib/api";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { nodesQuery } from "@/lib/queries";
import { telegramOnly, trustTunnelOnly } from "@/screens/profiles/protocol-meta";
import { catalogOf, presetName, presetSubtitle, transportKey } from "@/screens/subscriptions/model";
import { dnsPresetsQuery, useLinkAppList } from "@/screens/subscriptions/queries";
import { useInheritedDns } from "./dns-select";
import { barState, GlyphTile, heldBack, heldNodeOf, HeldNote, Note, padClass, SaveBar, SvCard, Tick, type GroupDemo } from "./group-bits";
import { ImpactText, wayOf } from "./groups";
import { profileListQuery, protocolsQuery } from "./rpc";
import { useTx } from "./t";
import "./group.css";

// The Access tab of the group page (PnGroup: up-): what the group's people get (the profiles, one switch each), on which nodes, and
// the DNS preset they start with. Each card keeps its own draft and has its own save bar; every save is one UpdateGroup that names
// only what changed. (The board's "Limits for everyone" card is not here: the panel has no limits of a group.)

const same = (a: readonly string[], b: readonly string[]) => a.length === b.length && a.every((x) => b.includes(x));
const every = ["groups", "users", "subs", "profiles", "nodes"];

/** The row of a switch with a glyph tile in front (up-gt). */
function GiveRow({ icon, title, sub, tags, on, disabled, onChange }: { icon: ReactNode; title: string; sub: string; tags?: ReactNode; on: boolean; disabled?: boolean; onChange: (on: boolean) => void }) {
  return (
    <div className="grid grid-cols-[36px_minmax(0,1fr)_auto] items-center gap-3 border-t border-line px-6 py-3 transition-opacity duration-200 ease-(--e) max-md:px-4">
      <span className={cx("transition-opacity duration-200 ease-(--e)", !on && "opacity-45")}>
        <GlyphTile>{icon}</GlyphTile>
      </span>
      <div className={cx("min-w-0 transition-opacity duration-200 ease-(--e)", !on && "opacity-45")}>
        <b className="block font-medium">{title}</b>
        <span className="block text-xs leading-4 text-muted">{sub}</span>
        {tags && <span className={cx("mt-2 flex flex-wrap gap-1 [&>span]:h-[22px]", !on && "grayscale")}>{tags}</span>}
      </div>
      <Switch checked={on} disabled={disabled} aria-label={title} onCheckedChange={onChange} />
    </div>
  );
}

type WayId = "sub" | "awg" | "tg" | "tt";
const wayIcon: Record<WayId, IconName> = { sub: "link", awg: "key", tg: "plane", tt: "tunnel" };

// ---- what the group gives: its profiles ----

function GivesCard({ group, manage, demo }: { group: Group; manage: boolean; demo?: GroupDemo }) {
  const t = useTx();
  const toast = useToast();
  const qc = useQueryClient();
  const profiles = useQuery(profileListQuery);
  const protocols = useQuery(protocolsQuery);
  const apps = useLinkAppList();
  const base = group.profileIds;
  const [draft, setDraft] = useState<string[] | null>(demo?.dirty && base[0] ? base.slice(1) : null);
  const chosen = draft ?? base;
  const dirty = !same(chosen, base);
  const [impact, setImpact] = useState<AccessImpact | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [ok, setOk] = useState(false);
  const save = useMutation({
    mutationFn: async (confirmed: boolean) => {
      const req = { groupId: group.id, profileIds: { values: chosen } };
      if (!confirmed && group.userCount + group.extraUserCount > 0 && base.some((id) => !chosen.includes(id))) {
        const dry = await groupsApi.updateGroup({ ...req, dryRun: true });
        if (dry.impact && dry.impact.lost.length > 0) {
          setImpact(dry.impact);
          return false;
        }
      }
      await groupsApi.updateGroup(req);
      return true;
    },
    onSuccess: async (done) => {
      if (!done) return;
      await Promise.all(every.map((k) => qc.invalidateQueries({ queryKey: [k] })));
      setDraft(null);
      setOk(true);
      setTimeout(() => setOk(false), 2400);
      toast(t("users.groupSaved", { name: group.name }));
    },
    onError: (e) => setError(errorText(e, t)),
  });
  const submit = (confirmed = false) => {
    setError(null);
    save.mutate(confirmed);
  };
  const wayOfProfile = (p: { protocol: string }): WayId => {
    const appsOf = protocols.data?.find((x) => x.id === p.protocol)?.apps;
    if (appsOf ? telegramOnly(appsOf) : p.protocol === "mtproto") return "tg";
    if (appsOf ? trustTunnelOnly(appsOf) : p.protocol === "trusttunnel") return "tt";
    return wayOf(p.protocol, protocols.data);
  };
  const order: WayId[] = ["sub", "awg", "tg", "tt"];
  const list = (profiles.data ?? []).map((p) => ({ p, way: wayOfProfile(p) })).sort((a, b) => order.indexOf(a.way) - order.indexOf(b.way));
  const subLabel = apps.length > 0 ? t("users.reachSub", { apps: apps.join(", ") }) : t("users.reachSubPlain");
  const reach = [
    { id: "sub", label: subLabel, n: group.happNodes, no: t("users.reachNoSub") },
    { id: "awg", label: t("users.reachAwg"), n: group.amneziaNodes, no: t("users.reachNoAwg") },
    ...(group.telegramNodes > 0 ? [{ id: "tg", label: t("tgproxy.toggle"), n: group.telegramNodes, no: "" }] : []),
  ];
  const nothing = group.happNodes === 0 && group.amneziaNodes === 0;

  return (
    <SvCard title={t("grp.gives")} sub={group.userCount + group.extraUserCount > 0 ? t.n("grp.gives.people", group.userCount + group.extraUserCount) : t("grp.gives.empty")}>
      {profiles.isPending ? (
        <Pending compact className="border-t border-line px-6 py-4" />
      ) : profiles.isError ? (
        <QueryError error={profiles.error} compact onRetry={() => void profiles.refetch()} />
      ) : list.length === 0 ? (
        <p className="m-0 border-t border-line px-6 py-4 text-[13px] leading-[18px] text-muted max-md:px-4">
          {t("users.groupNoProfiles")} <Link to="/profiles" className="text-accent-text hover:underline">{t("nav.profiles")}</Link>
        </p>
      ) : (
        list.map(({ p, way }) => {
          const on = chosen.includes(p.id);
          return (
            <GiveRow
              key={p.id}
              icon={<Icon name={wayIcon[way]} size={18} />}
              title={p.name}
              sub={t(`grp.way.${way}`)}
              tags={<ProtocolTag protocol={p.protocol} />}
              on={on}
              disabled={!manage}
              onChange={(next) => {
                setImpact(null);
                setOk(false);
                setDraft(next ? [...chosen, p.id] : chosen.filter((x) => x !== p.id));
              }}
            />
          );
        })
      )}
      <div className="flex flex-col gap-2 border-t border-line px-6 py-3 max-md:px-4">
        <span className="text-[13px] text-sec">{t("grp.reach")}</span>
        <ul className="m-0 flex list-none flex-col gap-1.5 p-0">
          {reach.map((r) => (
            <li key={r.id} className="flex items-start gap-2 text-xs leading-snug">
              <span aria-hidden className={cx("mt-px grid size-4 flex-none place-items-center rounded-full text-[10px] font-extrabold", r.n > 0 ? "bg-accent-soft text-accent-text" : "bg-step text-muted")}>
                {r.n > 0 ? "✓" : "–"}
              </span>
              <span className={r.n > 0 ? "text-fg" : "text-muted"}>
                <b className="font-medium">{r.label}</b> · {r.n > 0 ? t.n("users.reachNodes", r.n) : r.no}
              </span>
            </li>
          ))}
        </ul>
        {nothing && list.length > 0 && <Notice tone="danger">{t("users.reachNothing", { group: group.name })}</Notice>}
      </div>
      <Note icon="info" className="border-t border-line px-6 pt-3 pb-4 max-md:px-4">
        {t("grp.gives.hint")}
      </Note>
      {error && <Notice tone="danger" className="mx-6 mb-4 max-md:mx-4">{error}</Notice>}
      <SaveBar
        state={manage ? barState(dirty, save.isPending || !!demo?.saving, ok || !!demo?.ok) : "idle"}
        text={impact ? <ImpactText impact={impact} /> : demo?.ok ? t("grp.bar.saved") : save.isPending || demo?.saving ? t("grp.bar.saving") : ok ? t("grp.bar.saved") : t("grp.bar.dirty")}
        danger={!!impact}
        saveLabel={impact ? t.n("users.groupLoseDo", impact.users) : undefined}
        onUndo={() => {
          setDraft(null);
          setImpact(null);
          setError(null);
        }}
        onSave={() => submit(!!impact)}
      />
    </SvCard>
  );
}

// ---- the nodes ----

function NodesCard({ group, manage }: { group: Group; manage: boolean }) {
  const t = useTx();
  const toast = useToast();
  const qc = useQueryClient();
  const nodeList = useQuery(nodesQuery);
  const profiles = useQuery(profileListQuery);
  const base = group.disabledNodeIds;
  const [off, setOff] = useState<string[] | null>(null);
  const [unlocked, setUnlocked] = useState(base.length > 0);
  const disabled = off ?? base;
  const dirty = !same(disabled, base);
  const [error, setError] = useState<string | null>(null);
  // the panel refused to switch on a node the owner holds back (node_held): the node's id, for the way to its page
  const [held, setHeld] = useState<string | null>(null);
  const [ok, setOk] = useState(false);
  const items = (nodeList.data?.nodes ?? []).filter((n) => n.status !== NodeStatus.RETIRED).slice().sort((a, b) => a.name.localeCompare(b.name));
  const protocolsOfGroup = new Set((profiles.data ?? []).filter((p) => group.profileIds.includes(p.id)).map((p) => p.protocol));
  const save = useMutation({
    mutationFn: () => groupsApi.updateGroup({ groupId: group.id, disabledNodeIds: { values: disabled } }),
    onSuccess: async () => {
      await Promise.all(every.map((k) => qc.invalidateQueries({ queryKey: [k] })));
      setOff(null);
      setOk(true);
      setTimeout(() => setOk(false), 2400);
      toast(t("users.groupSaved", { name: group.name }));
    },
    onError: (e) => {
      const node = heldNodeOf(e);
      if (node !== undefined) setHeld(node);
      else setError(errorText(e, t));
    },
  });
  const all = !unlocked && disabled.length === 0;
  // held-back nodes the group was not given cannot be switched on here: said in the row, not after a click
  const heldN = items.filter((n) => heldBack(n, base)).length;
  const set = (next: string[]) => {
    setOk(false);
    setOff(next);
  };
  return (
    <SvCard title={t("grp.nodes")} sub={all ? t("grp.nodes.sumAll", { n: items.length }) : t("grp.nodes.sumSome", { off: disabled.filter((id) => items.some((n) => n.id === id)).length, total: items.length })}>
      <div className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-3 border-t border-line px-6 py-3 max-md:px-4">
        <div>
          <b className="block font-medium">{t("grp.nodes.all")}</b>
          <span className="block text-xs leading-4 text-muted">{heldN > 0 ? t("grp.nodes.heldSome", { n: heldN }) : t("grp.nodes.allHint")}</span>
        </div>
        <Switch
          checked={all}
          disabled={!manage || (heldN > 0 && !all)}
          aria-label={t("grp.nodes.all")}
          onCheckedChange={(on) => {
            setUnlocked(!on);
            if (on) set([]);
          }}
        />
      </div>
      {nodeList.isPending ? (
        <Pending compact className="border-t border-line px-6 py-3" />
      ) : nodeList.isError ? (
        <QueryError error={nodeList.error} compact onRetry={() => void nodeList.refetch()} />
      ) : items.length === 0 ? (
        <p className="m-0 border-t border-line px-6 py-3 text-[13px] text-muted max-md:px-4">{t("users.groupNoNodes")}</p>
      ) : (
        <div role="group" aria-label={t("grp.nodes")}>
          {items.map((n) => {
            const fit = n.protocols.filter((p) => protocolsOfGroup.has(p));
            const none = fit.length === 0;
            const on = !disabled.includes(n.id);
            const back = heldBack(n, base);
            const locked = !manage || all || none || back;
            return (
              <button
                key={n.id}
                type="button"
                role="checkbox"
                aria-checked={on}
                aria-disabled={locked || undefined}
                title={back ? t("grp.nodes.heldHint") : undefined}
                onClick={() => {
                  if (locked) return;
                  set(on ? [...disabled, n.id] : disabled.filter((id) => id !== n.id));
                }}
                className={cx(
                  "grid min-h-12 w-full grid-cols-[20px_20px_44px_minmax(0,1fr)_auto] items-center gap-2.5 border-t border-line px-6 py-1.5 text-start text-[13px] transition-colors duration-150 ease-(--e) max-md:grid-cols-[20px_20px_44px_minmax(0,1fr)] max-md:gap-y-1 max-md:px-4",
                  locked ? "cursor-default" : "hover:bg-[color-mix(in_srgb,var(--text)_3%,transparent)]",
                )}
              >
                <Tick on={on && !none} className={cx(all && !none && "opacity-50", (none || back) && "opacity-35")} />
                <Flag code={n.countryCode} size={14} className={cx((none || back) && "opacity-35")} />
                <b className={cx("font-mono font-medium", (none || back) && "opacity-35")}>{n.name}</b>
                <span className="truncate text-muted">{[n.location, n.provider].filter(Boolean).join(" · ")}</span>
                <span className="flex flex-wrap justify-end gap-1 max-md:col-[2/-1] max-md:justify-start">
                  {none ? (
                    <em className="text-xs text-muted not-italic">{t("grp.nodes.nofit")}</em>
                  ) : back ? (
                    <em className="text-xs text-muted not-italic">{t("grp.nodes.held")}</em>
                  ) : (
                    fit.map((p) => (
                      <span key={p} className={cx("[&>span]:h-[22px]", !on && "opacity-60 grayscale")}>
                        <ProtocolTag protocol={p} />
                      </span>
                    ))
                  )}
                </span>
              </button>
            );
          })}
        </div>
      )}
      {held !== null && <HeldNote nodeId={held} className="mx-6 mb-4 max-md:mx-4" />}
      {error && <Notice tone="danger" className="mx-6 mb-4 max-md:mx-4">{error}</Notice>}
      <SaveBar
        state={manage ? barState(dirty, save.isPending, ok) : "idle"}
        text={save.isPending ? t("grp.bar.saving") : ok ? t("grp.bar.saved") : t("grp.bar.dirty")}
        onUndo={() => {
          setOff(null);
          setUnlocked(base.length > 0);
          setError(null);
          setHeld(null);
        }}
        onSave={() => {
          setError(null);
          setHeld(null);
          save.mutate();
        }}
      />
    </SvCard>
  );
}

// ---- DNS ----

function DnsCard({ group, manage }: { group: Group; manage: boolean }) {
  const t = useTx();
  const toast = useToast();
  const qc = useQueryClient();
  const presets = useQuery(dnsPresetsQuery);
  const inherited = useInheritedDns();
  const apps = useLinkAppList();
  const [open, setOpen] = useState(false);
  const [sel, setSel] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const list = presets.data?.presets ?? [];
  const catalog = useMemo(() => catalogOf(presets.data?.providers ?? []), [presets.data?.providers]);
  const save = useMutation({
    mutationFn: (id: string) => groupsApi.updateGroup({ groupId: group.id, dnsPresetId: id }),
    onSuccess: async () => {
      await Promise.all(every.map((k) => qc.invalidateQueries({ queryKey: [k] })));
      toast(t("users.groupSaved", { name: group.name }));
    },
    onError: (e) => setError(errorText(e, t)),
    onSettled: () => setSel(null),
  });
  const hint = (p: (typeof list)[number]) => presetSubtitle(p, (s) => t("subs.dns.sub.direct", { s }), catalog, (tr) => t(transportKey[tr]));
  const inh = list.find((p) => p.id === inherited.id);
  const options = [
    { id: "", title: t("subs.dns.inherit", { name: inh ? presetName(inh, t.lang) : "…" }), sub: inh ? hint(inh) : "" },
    ...list.map((p) => ({ id: p.id, title: presetName(p, t.lang), sub: hint(p) })),
  ];
  const value = sel ?? group.dnsPresetId;
  const current = options.find((o) => o.id === value) ?? options[0]!;
  return (
    <SvCard title={t("grp.dns")} sub={t("grp.dns.sub")}>
      <div className={padClass}>
        {!presets.data ? (
          <div className="h-[52px] rounded-xl bg-step" />
        ) : (
          <>
            <button
              type="button"
              aria-expanded={open}
              disabled={!manage}
              onClick={() => setOpen(!open)}
              className="grid min-h-[52px] w-full grid-cols-[minmax(0,1fr)_20px] items-center gap-3 rounded-xl border border-line bg-canvas px-3.5 py-2 text-start transition-[border-color] duration-150 ease-(--e) hover:border-(--border-hover) disabled:opacity-60 aria-expanded:border-(--border-hover)"
            >
              <div className="min-w-0">
                <b className="block truncate font-medium">{current.title}</b>
                <span className="block truncate text-xs leading-4 text-muted">{current.sub}</span>
              </div>
              <Icon name="chevronDown" size={18} className={cx("block flex-none text-sec transition-transform duration-[250ms] ease-(--e)", open && "rotate-180")} />
            </button>
            {open && (
              <div role="radiogroup" aria-label={t("grp.dns")} className="flex animate-[mg-up_0.2s_var(--e)_both] flex-col rounded-xl border border-line p-1">
                {options.map((o) => {
                  const on = o.id === value;
                  return (
                    <button
                      key={o.id || "inherit"}
                      type="button"
                      role="radio"
                      aria-checked={on}
                      onClick={() => {
                        setOpen(false);
                        setError(null);
                        if (o.id !== group.dnsPresetId) {
                          setSel(o.id);
                          save.mutate(o.id);
                        }
                      }}
                      className="grid min-h-11 grid-cols-[20px_minmax(0,1fr)] items-center gap-3 rounded-ctl px-2.5 py-1.5 text-start transition-colors duration-150 ease-(--e) hover:bg-step"
                    >
                      <span aria-hidden className={cx("grid size-5 place-items-center rounded-full border-[1.5px] transition-[border-color] duration-150 ease-(--e)", on ? "border-accent-mark" : "border-[color-mix(in_srgb,var(--text)_30%,transparent)]")}>
                        <i className={cx("size-2.5 rounded-full bg-accent-mark transition-transform duration-[250ms] ease-spring", on ? "scale-100" : "scale-0")} />
                      </span>
                      <div className="min-w-0">
                        <b className="block text-sm font-medium">{o.title}</b>
                        <span className="block text-xs leading-4 text-muted">{o.sub}</span>
                      </div>
                    </button>
                  );
                })}
              </div>
            )}
            <Note icon="clock">{apps.length > 0 ? t("subs.dns.delivery", { apps: apps.join(", ") }) : t("subs.dns.deliveryPlain")}</Note>
            {error && <Notice tone="danger">{error}</Notice>}
          </>
        )}
      </div>
    </SvCard>
  );
}

export function AccessTab({ group, manage, demo }: { group: Group; manage: boolean; demo?: GroupDemo }) {
  return (
    <div className="grid grid-cols-[minmax(0,1.2fr)_minmax(0,1fr)] items-start gap-4 max-[1040px]:grid-cols-1">
      <div className="flex min-w-0 flex-col gap-4">
        <GivesCard group={group} manage={manage} demo={demo} />
      </div>
      <div className="flex min-w-0 flex-col gap-4">
        <NodesCard group={group} manage={manage} />
        <DnsCard group={group} manage={manage} />
      </div>
    </div>
  );
}
