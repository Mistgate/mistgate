import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState, type ReactNode } from "react";
import { CopyButton } from "@/components/copy-button";
import { Flourish } from "@/components/fx/flourish";
import { markFresh } from "@/components/fx/registry";
import { Avatar } from "@/components/ui/bits";
import { Button, buttonClass } from "@/components/ui/button";
import { Flag } from "@/components/ui/flag";
import { Icon } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { Notice } from "@/components/ui/notice";
import { Segmented } from "@/components/ui/segmented";
import { Select } from "@/components/ui/select";
import { Switch } from "@/components/ui/switch";
import { QuotaReset } from "@/gen/mistgate/admin/v1/user_pb";
import type { Group } from "@/gen/mistgate/admin/v1/group_pb";
import { users } from "@/lib/api";
import { cx } from "@/lib/cx";
import { useEdition } from "@/lib/edition";
import { errorText } from "@/lib/errors";
import { nodesQuery } from "@/lib/queries";
import { useLinkAppNames } from "@/screens/subscriptions/queries";
import { avatarIndex, GB, groupTone } from "./format";
import { big, userN, type UserN as User } from "./model";
import { DnsSelect, useInheritedDns } from "./dns-select";
import { defaultGroup, GroupEditModal, NewGroupModal } from "./groups";
import { groupsQuery, profileListQuery } from "./rpc";
import { useTx, type Tx } from "./t";
import { InfoTip, ResetSelect, resetCode } from "./ui";
import { MistgateAppRow } from "./mistgate-app";
import { LinkModal, linkTargetOf } from "./link-modal";
import { AppLink } from "./nav";

export type Created = { user: User; url: string; password: string };

/**
 * The window of a new person, as the redesign board draws it (us-dlg): two columns on a wide window, the form on the left and
 * "What you get" on the right, a bottom sheet on the phone. After "Create" the same window turns into the done screen with the
 * link (and the page password, when the instance asks for one). The content is mounted only while the window is open, so the
 * form starts clean every time.
 */
export function CreateUserModal({ open, onOpenChange, onCreated, startName, startDone, startGroup }: { open: boolean; onOpenChange: (o: boolean) => void; onCreated: (user: User, url: string, password: string) => void; startName?: string; startDone?: Created; startGroup?: string }) {
  const t = useTx();
  // `startName` and `startDone` are the dev kit's: a name already typed, or the done screen at once
  const [done, setDone] = useState<Created | null>(startDone ?? null);
  // "Add another": the form is mounted anew (a new round), the done screen goes
  const [round, setRound] = useState(0);
  const [qr, setQr] = useState(false);
  return (
    <Modal
      open={open}
      onOpenChange={(o) => {
        if (!o) {
          setDone(null);
          setQr(false);
        }
        onOpenChange(o);
      }}
      title={done ? t("ul.new.done") : t("users.new")}
      closeLabel={t("common.close")}
      // the window is 880 wide, and 520 once the person is made (a window under 720 takes the narrow room on its own); the width moves
      className={cx("transition-[translate,scale,opacity,max-width]", done ? "md:max-w-[520px]! [&>div:last-child]:justify-start!" : "md:max-w-[880px]!")}
      shot="create-user"
      footer={
        done ? (
          <>
            {/* the board's foot opens with an empty line (footer>span takes the whole row on a narrow window), which is what its 8 more are */}
            <span aria-hidden className="basis-full" />
            <Button variant="secondary" size="lg" onClick={() => setQr(true)}>
              {t("ul.act.link")}
            </Button>
            <Button
              variant="secondary"
              size="lg"
              onClick={() => {
                setDone(null);
                setRound((r) => r + 1);
              }}
            >
              {t("ul.new.again")}
            </Button>
            <Button variant="primary" size="lg" onClick={() => onOpenChange(false)}>
              {t("ul.done")}
            </Button>
          </>
        ) : undefined
      }
    >
      {done ? (
        <Done created={done} qr={qr} onQr={setQr} />
      ) : (
        <CreateForm
          key={round}
          startName={round === 0 ? startName : undefined}
          startGroup={startGroup}
          onClose={() => onOpenChange(false)}
          onCreated={(c) => {
            setDone(c);
            onCreated(c.user, c.url, c.password);
          }}
        />
      )}
    </Modal>
  );
}

const box = "rounded-xl border border-line";
// the items of a Segmented are span[role=radio], not buttons
const seg = "w-auto max-w-full flex-wrap [&_[role=radio]]:px-2.5 [&_[role=radio]]:text-xs [&_[role=radio]]:font-mono";

/** A limit as the board draws it: its name and a line of where the value comes from, presets in a segment, and a stepper for a value of its own. */
function LimitRow<V extends number>({
  title,
  hint,
  own,
  presets,
  value,
  onChange,
  step,
  min,
  max,
  unit,
  noneLabel,
}: {
  title: string;
  hint: string;
  own?: boolean;
  /** The values on offer, each with its words. */
  presets: readonly { v: V; label: string }[];
  value: V;
  onChange: (v: V) => void;
  step: number;
  min: number;
  max: number;
  unit: string;
  noneLabel?: string;
}) {
  const t = useTx();
  const preset = presets.some((p) => p.v === value);
  const [custom, setCustom] = useState(!preset);
  const showStepper = custom || !preset;
  return (
    <div className="flex min-h-14 flex-wrap items-center justify-between gap-x-4 gap-y-2 py-2 ps-3.5 pe-2">
      <div className="flex min-w-0 flex-col">
        <b className="text-sm font-medium">{title}</b>
        <span className={cx("text-xs leading-4", own ? "text-fg" : "text-muted")}>{hint}</span>
      </div>
      <div className="flex flex-wrap items-center justify-end gap-2 @max-[672px]/usd:w-full @max-[672px]/usd:justify-between">
        <Segmented
          variant="flat"
          aria-label={title}
          className={seg}
          value={showStepper ? "custom" : String(value)}
          onValueChange={(v) => {
            if (v === "custom") return setCustom(true);
            setCustom(false);
            onChange(Number(v) as V);
          }}
          options={[...presets.map((p) => ({ value: String(p.v), label: p.label })), { value: "custom", label: t("ul.new.other") }]}
        />
        {showStepper && (
          <div className="flex h-9 items-center gap-0.5 rounded-field border border-line bg-canvas p-0.5">
            <StepBtn label={t("users.less", { what: title })} disabled={value <= min} onClick={() => onChange(Math.max(min, value - step) as V)}>
              <path d="M6 12h12" />
            </StepBtn>
            <b className="min-w-16 text-center font-mono text-sm font-medium">{value === 0 && noneLabel ? noneLabel : `${value} ${unit}`}</b>
            <StepBtn label={t("users.more", { what: title })} disabled={value >= max} onClick={() => onChange(Math.min(max, value + step) as V)}>
              <path d="M6 12h12M12 6v12" />
            </StepBtn>
          </div>
        )}
      </div>
    </div>
  );
}

function StepBtn({ label, disabled, onClick, children }: { label: string; disabled?: boolean; onClick: () => void; children: ReactNode }) {
  return (
    <button type="button" aria-label={label} disabled={disabled} onClick={onClick} className="grid size-8 place-items-center rounded-ctl text-sec transition-colors duration-150 hover:bg-step hover:text-fg disabled:pointer-events-none disabled:opacity-35">
      <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
        {children}
      </svg>
    </button>
  );
}

/** A line of a box with a switch: a name, what it does, the switch; the whole line is the hit area. */
function SwitchLine({ title, hint, checked, onChange, tag }: { title: string; hint: string; checked: boolean; onChange: (on: boolean) => void; tag?: string }) {
  return (
    <label className="grid cursor-pointer grid-cols-[minmax(0,1fr)_auto] items-center gap-3 p-3.5">
      <span className="flex min-w-0 flex-col">
        <b className="text-sm font-medium">
          {title}
          {tag && <em className="ms-2 text-xs font-normal text-muted not-italic">{tag}</em>}
        </b>
        <span className="text-[13px] text-muted">{hint}</span>
      </span>
      <Switch aria-label={title} checked={checked} onCheckedChange={onChange} />
    </label>
  );
}

const quotas = [50, 100, 300, 0] as const;
const terms = [30, 90, 365, 0] as const;
/** What a new person starts with: the numbers the form opened with (the preview marks what the admin moved). */
const start = { quota: 100, term: 30, devices: 5 };

function CreateForm({ onCreated, onClose, startName, startGroup }: { onCreated: (c: Created) => void; onClose: () => void; startName?: string; startGroup?: string }) {
  const t = useTx();
  const qc = useQueryClient();
  const groupList = useQuery(groupsQuery);
  const nodeList = useQuery(nodesQuery);
  const profileList = useQuery(profileListQuery);
  const linkApps = useLinkAppNames();

  const [name, setName] = useState(startName ?? "");
  const [pickedGroup, setGroupId] = useState(startGroup ?? "");
  const [quotaGb, setQuotaGb] = useState(start.quota);
  const [reset, setReset] = useState(QuotaReset.MONTH);
  const [termDays, setTermDays] = useState(start.term);
  const [devices, setDevices] = useState(start.devices);
  const [apps, setApps] = useState<{ happ: boolean; amnezia: boolean; telegram: boolean; mistgate: boolean; trustTunnel: boolean } | null>(null); // null = what the group gives
  const [allNodes, setAllNodes] = useState(true);
  const [picked, setPicked] = useState<string[]>([]);
  const [editingGroup, setEditingGroup] = useState(false);
  const [makingGroup, setMakingGroup] = useState(false);
  const [dnsPreset, setDnsPreset] = useState(""); // "" = inherit
  const [speed, setSpeed] = useState(0); // bit/s, 0 = no limit
  const [devSpeed, setDevSpeed] = useState(0);
  const [more, setMore] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const groups = groupList.data ?? [];
  const nodeItems = nodeList.data?.nodes ?? [];
  const allIds = nodeItems.map((n) => n.id);
  const groupId = pickedGroup || defaultGroup(groups)?.id || "";
  const group = groups.find((g) => g.id === groupId);
  const noProfiles = profileList.data?.length === 0;
  const inheritedDns = useInheritedDns(groupId);
  // The switches follow the group until one is flipped: a way the group gives nothing for starts off (with the reason
  // under it); a group that gives nothing at all leaves Happ on, and the red line says why it is empty.
  const auto = { happ: !group || group.happNodes > 0 || group.amneziaNodes === 0, amnezia: !!group && group.amneziaNodes > 0, telegram: false, mistgate: true, trustTunnel: false };
  const { happ, amnezia, telegram, mistgate, trustTunnel } = apps ?? auto;
  // not in the Cloudflare edition: no switch, so a new person is never refused for it there
  const edition = useEdition();
  const ttOff = edition.edge || edition.off("trusttunnel");

  const create = useMutation({
    mutationFn: () =>
      users.createUser({
        name: name.trim(),
        groupId,
        quotaBytes: big(quotaGb * GB),
        quotaReset: reset,
        termDays,
        deviceLimit: devices,
        apps: { happ, amnezia, telegram, mistgate, ...(trustTunnel && { trustTunnel }) },
        nodes: { all: allNodes, nodeIds: allNodes ? [] : picked.filter((id) => allIds.includes(id)) },
        dnsPresetId: dnsPreset,
        speedLimitBps: big(speed),
        deviceSpeedLimitBps: big(devSpeed),
      }),
    onSuccess: (res) => {
      void qc.invalidateQueries({ queryKey: ["users"] });
      void qc.invalidateQueries({ queryKey: ["groups"] });
      if (!res.user) return;
      markFresh("user", res.user.id); // the row is washed once, when it shows up in the list
      onCreated({ user: userN(res.user), url: res.subscriptionUrl, password: res.pagePassword });
    },
    onError: (e) => setError(errorText(e, t)),
  });

  const toggleNode = (id: string) => {
    const cur = allNodes ? allIds : picked;
    setAllNodes(false);
    setPicked(cur.includes(id) ? cur.filter((x) => x !== id) : [...cur, id]);
  };
  const flipAll = () => {
    if (allNodes) {
      setAllNodes(false);
      setPicked(allIds);
    } else setAllNodes(true);
  };
  // at least one app stays on: the switch that would turn the last one off does nothing
  const flipApp = (which: "happ" | "amnezia" | "telegram" | "mistgate" | "trustTunnel", on: boolean) => {
    const next = { happ, amnezia, telegram, mistgate, trustTunnel, [which]: on };
    if (!next.happ && !next.amnezia) return;
    setApps(next);
  };
  const sel = allNodes ? allIds.length : picked.filter((id) => allIds.includes(id)).length;
  const ready = name.trim() !== "" && groupId !== "" && !create.isPending;
  const offHint = (which: "happ" | "amnezia") => {
    if (apps || !group) return undefined;
    if (which === "happ" && !happ) return t("users.happOff");
    if (which === "amnezia" && !amnezia) return t("users.awgOff");
    return undefined;
  };
  const mbit = (bps: number) => Math.round(bps / 1e6);
  const extras = [speed, devSpeed, dnsPreset ? 1 : 0, reset !== QuotaReset.MONTH ? 1 : 0].filter((v) => v > 0).length;
  const submit = () => {
    if (!ready) return;
    setError(null);
    create.mutate();
  };

  const gives = givesOf({ group, happ, amnezia, telegram, mistgate, trustTunnel, devices, linkApps, t });

  return (
    <form
      className="@container/usd flex min-h-0 flex-col gap-5"
      onSubmit={(e) => {
        e.preventDefault();
        submit();
      }}
    >
      <div className="grid grid-cols-[minmax(0,1fr)_272px] items-start gap-6 @max-[672px]/usd:grid-cols-1">
        <div className="flex min-w-0 flex-col gap-5">
          <label className="flex flex-col gap-1.5 text-[13px] text-sec">
            {t("users.name")}
            <span className="relative block">
              <span className="pointer-events-none absolute start-2 top-1/2 -mt-3.5">
                <Avatar name={name.trim() || "?"} index={avatarIndex(name.trim())} size={28} fontSize={12} />
              </span>
              <input
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder={t("ul.new.namePh")}
                autoFocus
                autoComplete="off"
                spellCheck={false}
                maxLength={64}
                className="h-11 w-full rounded-field border border-line bg-canvas ps-[46px] pe-3 font-mono text-sm text-fg outline-none transition-[border-color,box-shadow] duration-150 placeholder:text-muted focus:border-accent-line focus:shadow-[0_0_0_3px_var(--focus-ring)]"
              />
            </span>
            <small className="text-xs text-muted">{t("ul.new.nameHint")}</small>
          </label>

          <div className="flex flex-col gap-1.5 text-[13px] text-sec">
            <span id="ul-group">{t("users.group")}</span>
            {groupList.data && groups.length === 0 ? (
              // nobody can be added before there is a group: the new-group window opens from here and the person lands in it
              <div className={cx(box, "flex flex-col items-start gap-2.5 p-3.5")}>
                <p className="text-xs leading-normal text-muted">{t("users.groupFirst")}</p>
                <Button variant="secondary" size="lg" onClick={() => setMakingGroup(true)}>
                  {t("users.newGroup")}
                </Button>
              </div>
            ) : groups.length <= 6 ? (
              <div role="group" aria-labelledby="ul-group" className="grid grid-cols-2 gap-2 @max-[420px]/usd:grid-cols-1">
                {groups.map((g) => (
                  <GroupButton
                    key={g.id}
                    g={g}
                    on={g.id === groupId}
                    t={t}
                    onClick={() => {
                      setGroupId(g.id);
                      setApps(null); // a new group: the switches follow what it gives again
                    }}
                  />
                ))}
              </div>
            ) : (
              <Select
                aria-label={t("users.group")}
                value={groupId}
                onValueChange={(v) => {
                  setGroupId(v);
                  setApps(null);
                }}
                options={groups.map((g) => ({ value: g.id, label: g.name }))}
              />
            )}
            {group && (
              <button type="button" onClick={() => setEditingGroup(true)} className="self-start text-xs text-sec underline decoration-line underline-offset-4 hover:text-fg">
                {t("users.groupEdit")}
              </button>
            )}
            {noProfiles && (
              <p className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs leading-snug text-warn-text">
                {t("users.reachNoProfiles")}
                <Link to="/profiles" className="inline-flex items-center gap-1 font-medium text-accent-text hover:underline">
                  {t("nav.profiles")}
                  <Icon name="chevronRight" size={12} />
                </Link>
              </p>
            )}
          </div>

          <div className="flex flex-col gap-1.5 text-[13px] text-sec">
            {t("ul.new.limits")}
            <div className={cx(box, "flex flex-col divide-y divide-line")}>
              <LimitRow
                title={t("ul.new.traffic")}
                hint={t(`users.quotaPer.${resetCode(reset)}`)}
                own={quotaGb !== start.quota}
                presets={quotas.map((v) => ({ v, label: v === 0 ? t("ul.noLimit") : `${v} ${t("users.unitGb")}` }))}
                value={quotaGb}
                onChange={setQuotaGb}
                step={10}
                min={0}
                max={10000}
                unit={t("users.unitGb")}
                noneLabel="∞"
              />
              <LimitRow
                title={t("users.term")}
                hint={t("users.termHint")}
                own={termDays !== start.term}
                presets={terms.map((v) => ({ v, label: v === 0 ? t("ul.term.never") : v === 365 ? t("ul.new.year") : `${v} ${t("users.unitD")}` }))}
                value={termDays}
                onChange={setTermDays}
                step={5}
                min={0}
                max={3650}
                unit={t("users.unitD")}
                noneLabel="∞"
              />
              <div className="flex min-h-14 flex-wrap items-center justify-between gap-x-4 gap-y-2 py-2 ps-3.5 pe-2">
                <div className="flex min-w-0 flex-col">
                  <b className="text-sm font-medium">{t("users.devices")}</b>
                  <span className={cx("text-xs leading-4", devices !== start.devices ? "text-fg" : "text-muted")}>{t("users.devicesHint")}</span>
                </div>
                <div className="flex items-center gap-3">
                  {devices <= 10 && (
                    <span aria-hidden className="flex gap-[3px]">
                      {Array.from({ length: devices }, (_, i) => (
                        <i key={i} className="size-1.5 animate-[fz-pop_0.25s_var(--ease-spring)_both] rounded-full bg-accent-mark" />
                      ))}
                    </span>
                  )}
                  <div className="flex h-9 items-center gap-0.5 rounded-field border border-line bg-canvas p-0.5">
                    <StepBtn label={t("users.less", { what: t("users.devices") })} disabled={devices <= 1} onClick={() => setDevices(Math.max(1, devices - 1))}>
                      <path d="M6 12h12" />
                    </StepBtn>
                    <b className="min-w-7 text-center font-mono text-sm font-medium">{devices}</b>
                    <StepBtn label={t("users.more", { what: t("users.devices") })} disabled={devices >= 100} onClick={() => setDevices(Math.min(100, devices + 1))}>
                      <path d="M6 12h12M12 6v12" />
                    </StepBtn>
                  </div>
                </div>
              </div>
            </div>
          </div>

          <div className={cx(box, "grid grid-cols-[minmax(0,1fr)_auto] items-center gap-3 p-3.5")}>
            <label className="flex min-w-0 cursor-pointer flex-col">
              <b className="text-sm font-medium">{t("users.nodesAll")}</b>
              <span className="text-[13px] text-muted">{nodeItems.length === 0 ? t("users.nodesNone") : allNodes ? t("users.nodesAllHint") : t("users.nodesSelHint", { a: sel, b: allIds.length })}</span>
            </label>
            <Switch aria-label={t("users.nodesAll")} checked={allNodes} onCheckedChange={flipAll} />
            {nodeItems.length > 0 && (
              <div className="col-span-full flex flex-wrap gap-1.5">
                {nodeItems.map((n) => {
                  const on = allNodes || picked.includes(n.id);
                  return (
                    <button
                      key={n.id}
                      type="button"
                      aria-pressed={on}
                      onClick={() => !allNodes && toggleNode(n.id)}
                      className={cx(
                        "inline-flex h-8 items-center gap-2 rounded-2xl border ps-2 pe-3 text-xs transition-[border-color,color,opacity] duration-150",
                        on ? "border-accent-line text-fg" : "border-line text-muted",
                        allNodes && "cursor-default border-line",
                      )}
                    >
                      <Flag code={n.countryCode} size={14} className={cx("transition-opacity", !on && "opacity-40")} />
                      <span className="font-mono">{n.name}</span>
                    </button>
                  );
                })}
              </div>
            )}
          </div>

          <div className={cx(box, "flex flex-col divide-y divide-line")}>
            <SwitchLine title={linkApps} hint={offHint("happ") ?? t("users.happHint")} checked={happ} onChange={(on) => flipApp("happ", on)} />
            <SwitchLine title="AmneziaVPN" hint={offHint("amnezia") ?? t("users.awgHint")} checked={amnezia} onChange={(on) => flipApp("amnezia", on)} />
            <SwitchLine title={t("tgproxy.toggle")} hint={t("users.telegramHint")} checked={telegram} onChange={(on) => flipApp("telegram", on)} />
            {!ttOff && <SwitchLine title={t("tt.toggle")} tag={t("ul.new.experimental")} hint={trustTunnel ? t("ul.new.ttOn") : t("ul.new.ttOff")} checked={trustTunnel} onChange={(on) => flipApp("trustTunnel", on)} />}
            <MistgateAppRow checked={mistgate} linkOn={happ} onCheckedChange={(on) => flipApp("mistgate", on)} className="border-t-0 px-3.5" />
          </div>

          <button type="button" aria-expanded={more} onClick={() => setMore(!more)} className="inline-flex items-center gap-1 self-start text-[13px] text-sec transition-colors hover:text-fg">
            {t("users.advanced")}
            {extras > 0 && <em className="grid h-[18px] min-w-[18px] place-items-center rounded-[9px] bg-tone-mint-bg px-[5px] font-mono text-[11px] text-accent-text not-italic">{extras}</em>}
            <Icon name="chevronDown" size={16} className={cx("transition-transform duration-250", more && "rotate-180")} />
          </button>
          {more && (
            <div className={cx(box, "-mt-2 flex animate-[mg-up_0.25s_var(--e)_both] flex-col divide-y divide-line")}>
              <div className="flex flex-wrap items-center gap-x-2 gap-y-1 px-3.5 py-2.5">
                <span className="text-xs text-muted">{t("users.speedT")}</span>
                <span className="rounded-md border border-line px-1.5 py-px text-[10px] font-medium text-muted">{t("users.experimental")}</span>
              </div>
              <LimitRow
                title={t("users.speedUser")}
                hint={speed ? t("ul.new.own") : t("users.speedUserHint")}
                own={speed > 0}
                presets={[0, 20, 50, 100].map((v) => ({ v, label: v === 0 ? t("ul.noLimit") : String(v) }))}
                value={mbit(speed)}
                onChange={(v) => setSpeed(v * 1e6)}
                step={5}
                min={0}
                max={10000}
                unit={t("users.unitMbit")}
                noneLabel="∞"
              />
              <LimitRow
                title={t("users.speedDevice")}
                hint={devSpeed ? t("ul.new.own") : t("users.speedDeviceHint")}
                own={devSpeed > 0}
                presets={[0, 10, 20, 50].map((v) => ({ v, label: v === 0 ? t("ul.noLimit") : String(v) }))}
                value={mbit(devSpeed)}
                onChange={(v) => setDevSpeed(v * 1e6)}
                step={5}
                min={0}
                max={10000}
                unit={t("users.unitMbit")}
                noneLabel="∞"
              />
              <div className="flex flex-col gap-2 p-3.5">
                <b className="text-sm font-medium">{t("subs.dns.field")}</b>
                <DnsSelect value={dnsPreset} inherited={inheritedDns} onChange={setDnsPreset} note={false} />
                {/* one line; when it reaches the apps and what "empty" means wait behind the (i) */}
                <span className="flex items-start gap-1.5 text-xs leading-snug text-muted">
                  <span className="min-w-0 flex-1">{t("subs.dns.scopeUser")}</span>
                  <InfoTip lines={[t("subs.dns.delivery", { apps: linkApps }), t("subs.dns.inheritHint")]} />
                </span>
              </div>
              <div className="flex flex-wrap items-center justify-between gap-2 py-2 ps-3.5 pe-2">
                <b className="text-sm font-medium">{t("users.resets")}</b>
                <div className="w-[190px] max-w-full">
                  <ResetSelect value={reset} onChange={setReset} />
                </div>
              </div>
            </div>
          )}
        </div>

        <aside aria-label={t("ul.new.preview")} className="sticky top-0 flex flex-col gap-4 rounded-card bg-[color-mix(in_srgb,var(--text)_3%,transparent)] p-4 @max-[672px]/usd:static">
          <span className="text-xs text-muted">{t("ul.new.preview")}</span>
          <div className="flex min-w-0 items-center gap-3 @max-[672px]/usd:hidden">
            <Avatar name={name.trim() || "?"} index={avatarIndex(name.trim())} size={40} fontSize={15} />
            <div className="flex min-w-0 flex-col">
              <b className={cx("truncate text-[15px] font-medium", name.trim() ? "font-mono" : "font-mono text-muted")}>{name.trim() || t("ul.new.nameLow")}</b>
              {group && (
                <span data-tone={groupTone(group.id, group.color)} className="tone-chip mt-0.5 inline-flex h-5 w-fit items-center px-1.5 text-[11px]">
                  {group.name}
                </span>
              )}
            </div>
          </div>
          <div className="flex flex-col gap-2.5 border-t border-line pt-4">
            {gives.length === 0 ? (
              <p className="text-xs leading-snug text-danger-text">{t("users.reachNothing", { group: group?.name ?? "" })}</p>
            ) : (
              gives.map((x) => (
                <div key={x.t} className="grid animate-[mg-up_0.25s_var(--e)_both] grid-cols-[16px_minmax(0,1fr)] gap-2">
                  <Icon name="check" size={14} strokeWidth={2.5} className="mt-[3px] text-accent-mark" />
                  <div className="min-w-0">
                    <b className="block text-[13px] font-medium">{x.t}</b>
                    <span className="text-xs leading-4 text-muted">{x.s}</span>
                  </div>
                </div>
              ))
            )}
          </div>
          <dl className="m-0 flex flex-col gap-2 border-t border-line pt-4">
            {facts({ quotaGb, termDays, devices, sel, total: allIds.length, allNodes, t }).map((f) => (
              <div key={f.l} className="flex justify-between gap-3 text-[13px]">
                <dt className="text-muted">{f.l}</dt>
                <dd className={cx("m-0 text-end", f.own ? "text-fg" : "text-sec")}>{f.v}</dd>
              </div>
            ))}
          </dl>
          <p className="text-xs leading-4 text-muted @max-[672px]/usd:hidden">{t("ul.new.linkNote")}</p>
        </aside>
      </div>
      {error && <Notice tone="danger">{error}</Notice>}
      <div className="sticky -bottom-6 -mx-6 -mb-6 flex flex-wrap items-center gap-2 border-t border-line @max-[720px]/win:-bottom-5 @max-[720px]/win:-mx-4 @max-[720px]/win:-mb-5 bg-[color-mix(in_srgb,var(--text)_2%,var(--surface))] px-6 py-3.5 @max-[720px]/win:px-4 @max-[720px]/win:py-3">
        <span className="me-auto min-w-0 text-xs text-muted @max-[672px]/usd:basis-full">{name.trim() ? t("ul.new.enter") : t("ul.new.footEmpty")}</span>
        <Button variant="secondary" size="lg" onClick={onClose}>
          {t("users.cancel")}
        </Button>
        <Button type="submit" variant="primary" size="lg" aria-disabled={!ready} className={cx(!ready && "opacity-45")} disabled={create.isPending}>
          {create.isPending && <span aria-hidden className="size-3.5 animate-[mg-ui-spin_0.7s_linear_infinite] rounded-full border-[1.5px] border-current border-t-transparent" />}
          {create.isPending ? t("ul.new.creating") : t("users.createBtn")}
        </Button>
      </div>
      <GroupEditModal group={group} open={editingGroup} onOpenChange={setEditingGroup} />
      <NewGroupModal open={makingGroup} onOpenChange={setMakingGroup} onCreated={(g) => setGroupId(g.id)} />
    </form>
  );
}

function GroupButton({ g, on, t, onClick }: { g: Group; on: boolean; t: Tx; onClick: () => void }) {
  return (
    <button type="button" aria-pressed={on} onClick={onClick} className={cx("relative grid min-w-0 gap-0.5 rounded-xl border px-3.5 py-3 text-start transition-[border-color,background-color] duration-150", on ? "border-accent-line bg-[color-mix(in_srgb,var(--accent-mark)_5%,transparent)]" : "border-line hover:border-[color-mix(in_srgb,var(--text)_22%,transparent)]")}>
      <b className="flex min-w-0 items-center gap-2 pe-6 font-medium">
        <i aria-hidden data-tone={groupTone(g.id, g.color)} className="size-2 flex-none rounded-full bg-(--t)" />
        <span className="truncate">{g.name}</span>
      </b>
      <span className="text-xs text-muted">{t.n("users.groupPeople", g.userCount)}</span>
      <em className="mt-1.5 truncate font-mono text-xs text-sec not-italic">{t("ul.new.reach", { a: g.happNodes, b: g.amneziaNodes })}</em>
      <i aria-hidden className={cx("absolute end-3 top-3 grid size-5 place-items-center rounded-full border text-transparent transition-[background-color,border-color] duration-150", on ? "border-accent-mark bg-accent-mark text-on-accent" : "border-[color-mix(in_srgb,var(--text)_22%,transparent)]")}>
        <Icon name="check" size={12} strokeWidth={3} />
      </i>
    </button>
  );
}

/** What the person gets, by way: only the ways switched on, each with where it works (the group's nodes) in a line. */
function givesOf({ group, happ, amnezia, telegram, mistgate, trustTunnel, devices, linkApps, t }: { group: Group | undefined; happ: boolean; amnezia: boolean; telegram: boolean; mistgate: boolean; trustTunnel: boolean; devices: number; linkApps: string; t: Tx }) {
  if (!group) return [];
  const out: { t: string; s: string }[] = [];
  if (happ && group.happNodes > 0) out.push({ t: t("ul.get.sub"), s: `${linkApps} · ${t.n("users.reachNodes", group.happNodes)}` });
  if (amnezia && group.amneziaNodes > 0) out.push({ t: t("ul.get.awg"), s: `${t("ul.get.awgHint")} · ${t.n("users.reachNodes", group.amneziaNodes)}` });
  if (telegram && group.telegramNodes > 0) out.push({ t: t("tgproxy.toggle"), s: t("ul.get.tg") });
  if (trustTunnel) out.push({ t: t("tt.toggle"), s: t("ul.get.tt", { n: devices }) });
  if (mistgate && happ && group.happNodes > 0) out.push({ t: t("users.mistgate"), s: t("ul.get.app") });
  return out;
}

function facts({ quotaGb, termDays, devices, sel, total, allNodes, t }: { quotaGb: number; termDays: number; devices: number; sel: number; total: number; allNodes: boolean; t: Tx }) {
  return [
    { l: t("ul.new.traffic"), v: quotaGb === 0 ? t("ul.noLimit") : t("ul.new.perMonth", { n: quotaGb }), own: quotaGb !== start.quota },
    { l: t("users.term"), v: termDays === 0 ? t("ul.term.never") : termDays === 365 ? t("ul.new.year") : `${termDays} ${t("users.unitD")}`, own: termDays !== start.term },
    { l: t("users.devices"), v: String(devices), own: devices !== start.devices },
    { l: t("ul.new.nodes"), v: allNodes ? t("ul.new.nodesAll", { n: total }) : t("ul.new.nodesOf", { a: sel, b: total }), own: !allNodes },
  ];
}

/** The done screen: a tick, the link with its copy button (and the page password), the facts, "Add another" and "Done". */
function Done({ created, qr, onQr }: { created: Created; qr: boolean; onQr: (open: boolean) => void }) {
  const t = useTx();
  const { user, url, password } = created;
  const groupList = useQuery(groupsQuery);
  const target = linkTargetOf(user, groupList.data?.find((g) => g.id === user.groupId));
  const nothing = !target.access.happ && !target.access.amnezia;
  return (
    <div className="relative flex flex-col items-center gap-2 text-center">
      <Flourish x="50%" y={68} />
      <span aria-hidden className="grid size-14 animate-[fz-pop_0.45s_var(--ease-spring)_both] place-items-center rounded-full bg-accent text-on-accent">
        <Icon name="check" size={28} strokeWidth={2.5} />
      </span>
      <h3 className="mt-2 text-xl leading-5 font-medium">{t("ul.new.added", { name: user.name })}</h3>
      <p className="mb-2 max-w-[380px] text-sm leading-5 text-sec">{t("ul.new.sendLink")}</p>
      <div className="flex w-full min-w-0 items-center gap-2 rounded-field border border-line bg-canvas py-1 ps-3 pe-1">
        <code className="min-w-0 flex-1 truncate text-start font-mono text-[13px] text-sec select-all" title={url}>
          {url}
        </code>
        <CopyButton value={url} size="md" variant="primary" label={t("common.copy")} />
      </div>
      {password && (
        <div className="flex w-full min-w-0 items-center gap-2 rounded-field border border-line bg-canvas py-1 ps-3 pe-1">
          <span className="flex-none text-xs text-muted">{t("ul.new.password")}</span>
          <code className="min-w-0 flex-1 truncate text-start font-mono text-[13px] text-sec select-all">{password}</code>
          <CopyButton value={password} size="md" label={t("users.copyPassword")} />
        </div>
      )}
      {nothing && (
        <Notice tone="danger" className="w-full items-start text-start">
          <span className="flex flex-col items-start gap-2">
            <span>{t(target.group?.givesNothing ? "users.linkNothing" : "users.linkNothingApps", { name: user.name, group: user.groupName })}</span>
            <AppLink to="/users/groups/$id" params={{ id: user.groupId }} className={buttonClass("secondary", "sm")}>
              {t("users.groupOpen", { name: user.groupName })}
            </AppLink>
          </span>
        </Notice>
      )}
      <dl className="m-0 grid w-full grid-cols-2 gap-3 px-1 pt-3 text-start">
        {[
          [t("users.group"), user.groupName],
          [t("ul.new.traffic"), user.quotaBytes === 0 ? t("ul.noLimit") : t("ul.new.perMonth", { n: Math.round(user.quotaBytes / GB) })],
          [t("users.term"), user.expiresUnix === 0 ? t("ul.term.never") : `${termDaysOf(user)} ${t("users.unitD")}`],
          [t("users.devices"), String(user.deviceLimit)],
        ].map(([l, v]) => (
          <div key={l}>
            <dt className="text-xs leading-5 text-muted">{l}</dt>
            <dd className="m-0 mt-0.5 text-sm">{v}</dd>
          </div>
        ))}
      </dl>
      <LinkModal target={qr ? target : null} initialUrl={url} initialPassword={password} onClose={() => onQr(false)} />
    </div>
  );
}

/** Whole days from now to the end of the term (the person is new: it is the term they were given). */
const termDaysOf = (u: User) => Math.max(0, Math.round((u.expiresUnix - Math.floor(Date.now() / 1000)) / 86400));
