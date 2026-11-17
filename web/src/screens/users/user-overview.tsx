import { useMutation, useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { Scramble } from "@/components/fx/scramble-react";
import { Button } from "@/components/ui/button";
import { Flag } from "@/components/ui/flag";
import { ProtocolTag } from "@/components/ui/protocol-tag";
import { useToast } from "@/components/ui/toast";
import { users } from "@/lib/api";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { useFmt, type Fmt } from "@/lib/format";
import { UserStatus } from "@/gen/mistgate/admin/v1/user_pb";
import { useLinkAppNames } from "@/screens/subscriptions/queries";
import { useDnsLabel } from "./dns-select";
import { DAY, daysLeft, nowSec } from "./format";
import { ConnectionAlerts } from "./user-connection";
import { CopyBtn, CardHead, G, GroupTag, P } from "./user-bits";
import type { DetailN, UserN as User } from "./model";
import { protocolsQuery } from "./rpc";
import { useTx, type Tx } from "./t";
import { ConfirmModal } from "./ui";
import { useReadonly } from "./user-actions";

const dayFmt = (t: Tx, unix: number, o: Intl.DateTimeFormatOptions) => new Intl.DateTimeFormat(t.locale, { timeZone: "UTC", ...o }).format(new Date(unix * 1000));
const trafficRows = 5; // the biggest node × protocol rows; the rest behind "Show all"

/**
 * The traffic card of the Overview (the board's .up-tr): the figure and the quota, the bar, the chart by day (the days the panel keeps for
 * the page: the last 14, today the last), a line of the value that follows the pointer, and the biggest node × profile rows under it.
 * Entrance motion (the bars rising, the rows) is the effects' own; the figure shuffles into place under them as well.
 */
export function TrafficPanel({ data, fmt }: { data: DetailN; fmt: Fmt }) {
  const t = useTx();
  const user = data.user!;
  const protocols = useQuery(protocolsQuery);
  const protoName = (id: string) => protocols.data?.find((p) => p.id === id)?.displayName ?? id;
  const [hi, setHi] = useState(-1);
  const [allNodes, setAllNodes] = useState(false);
  const days = data.dailyTraffic;
  const used = user.usedBytes;
  const over = user.status === UserStatus.LIMITED || (user.quotaBytes > 0 && used > user.quotaBytes);
  const pct = user.quotaBytes > 0 ? Math.min(100, (used / user.quotaBytes) * 100) : 0;
  const max = Math.max(0, ...days.map((x) => x.bytes));
  const avg = days.length > 0 ? days.reduce((a, x) => a + x.bytes, 0) / days.length : 0;
  const hov = hi >= 0 && hi < days.length;
  const line = max > 0 ? (hov ? days[hi]!.bytes : avg) / max : 0;
  const folds = data.nodeTraffic.length > trafficRows + 1; // one more row is not worth a button
  const rows = folds && !allNodes ? data.nodeTraffic.slice(0, trafficRows) : data.nodeTraffic;
  const top = Math.max(1, ...data.nodeTraffic.map((r) => r.bytes));
  const countryOf = (id: string) => data.nodeAccess.find((n) => n.nodeId === id)?.countryCode ?? "";
  const now = nowSec();
  const lbl = (d: { dayUnix: number }, i: number) => {
    const wd = new Date(d.dayUnix * 1000).getUTCDay();
    return { we: wd === 0 || wd === 6, mon: wd === 1 && i > 0, lb: i === 0 || i === days.length - 1 || wd === 1, dn: new Date(d.dayUnix * 1000).getUTCDate() };
  };
  const reset = user.nextResetUnix > 0 ? daysLeft(user.nextResetUnix, now) : null;

  return (
    <section className={cx("up-card up-tr", over && "over")}>
      <div className="up-trh">
        <div>
          <span>{t("up.trafficPeriod")}</span>
          <div className="up-big">
            <b>
              <Scramble value={fmt.bytes(used)} />
            </b>
            <em>{user.quotaBytes > 0 ? t("users.ofQuota", { q: fmt.bytes(user.quotaBytes) }) : t("users.unlimited")}</em>
          </div>
        </div>
        <div className="up-trr">
          <b>{user.nextResetUnix > 0 ? t("up.resetOn", { date: dayFmt(t, user.nextResetUnix, { day: "numeric", month: "long" }) }) : t("users.reset.none")}</b>
          {reset !== null && <span>{t("up.resetIn", { n: Math.max(0, Math.ceil((user.nextResetUnix - now) / DAY)) })}</span>}
        </div>
      </div>
      <i className="up-bar" style={{ "--w": `${pct.toFixed(1)}%` } as React.CSSProperties} aria-hidden />
      <div className="up-chw" role="img" aria-label={t("users.chartLabel")}>
        <div className="up-gl" aria-hidden>
          <span>{max > 0 ? fmt.bytes(max) : ""}</span>
          <span>{max > 0 ? fmt.bytes(max / 2) : ""}</span>
          <span>0</span>
        </div>
        <div className={cx("up-ch", hov && "hov", max === 0 && "nil")} style={{ "--ln": line.toFixed(3) } as React.CSSProperties} onMouseLeave={() => setHi(-1)}>
          {days.map((d, i) => {
            const l = lbl(d, i);
            const h = max > 0 ? Math.max(3, (d.bytes / max) * 100) : 0;
            return (
              <span key={d.dayUnix} className={cx(hi === i && "hi", i === days.length - 1 && "now", l.we && "we", l.mon && "mon", l.lb && "lb")} style={{ "--h": `${h.toFixed(0)}%`, "--i": i } as React.CSSProperties} onMouseEnter={() => setHi(i)} title={t("common.labeled", { label: fmt.date(d.dayUnix), value: fmt.bytes(d.bytes) })}>
                <i />
                <em>{l.dn}</em>
              </span>
            );
          })}
          <div className="up-hl" aria-hidden>
            <b>
              <span>{hov ? dayFmt(t, days[hi]!.dayUnix, { weekday: "short", day: "numeric", month: "long" }) : t("up.avgDay")}</span>
              <span>{fmt.bytes(hov ? days[hi]!.bytes : avg)}</span>
            </b>
          </div>
        </div>
      </div>
      <div className="up-by">
        <span className="up-l">{t("up.byNode")}</span>
        {rows.map((r) => (
          <div key={r.nodeId + r.protocol} data-testid="traffic-row">
            <span aria-hidden className="up-fl">
              <Flag code={countryOf(r.nodeId)} size={14} />
            </span>
            <Link to="/nodes/$id" params={{ id: r.nodeId }} className="font-mono font-medium hover:text-accent-text hover:underline">
              {r.nodeName}
            </Link>
            <span>
              <ProtocolTag protocol={r.protocol} label={protoName(r.protocol)} />
            </span>
            <i className="b" style={{ "--w": `${((r.bytes / top) * 100).toFixed(0)}%` } as React.CSSProperties} aria-hidden />
            <em>{fmt.bytes(r.bytes)}</em>
          </div>
        ))}
        {data.nodeTraffic.length === 0 && <p>{t("up.noTraffic")}</p>}
        {folds && (
          <Button variant="ghost" size="md" aria-expanded={allNodes} className="up-by-more" onClick={() => setAllNodes(!allNodes)}>
            {allNodes ? t("users.trafficLess") : t("users.trafficAll", { n: data.nodeTraffic.length })}
          </Button>
        )}
      </div>
    </section>
  );
}

/** "Online now" (the board's .up-now): the devices that hold a session, with their node; or why nobody does. */
function NowCard({ data, nodeName, onDevices, blocked }: { data: DetailN; nodeName: (id: string) => string; onDevices: () => void; blocked: boolean }) {
  const t = useTx();
  const fmt = useFmt();
  const now = data.devices.filter((d) => d.online);
  const last = [...data.devices].filter((d) => d.lastSeenUnix > 0).sort((a, b) => b.lastSeenUnix - a.lastSeenUnix)[0];
  const nameOf = (d: { model: string; platform: string }) => d.model || d.platform || "—";
  return (
    <section className="up-card">
      <CardHead title={t("up.nowTitle")}>
        <a
          className="up-lk up-r"
          href="#devices"
          onClick={(e) => {
            e.preventDefault();
            onDevices();
          }}
        >
          {t("up.allDevices")}
        </a>
      </CardHead>
      <div className="up-now">
        {now.map((d) => (
          <div key={d.id}>
            <i className="up-dot" aria-hidden />
            <b>{nameOf(d)}</b>
            <span>{[d.client && d.client.split("/").slice(0, 2).join(" "), d.protocols?.[0]].filter(Boolean).join(" · ")}</span>
            <span className="up-tag font-mono">{d.onlineNodeIds.map(nodeName).join(", ")}</span>
            <em>{t("users.devKeyOnline")}</em>
          </div>
        ))}
        {now.length === 0 && <p>{data.devices.length === 0 ? t("up.nowNever") : blocked ? t("up.nowBlocked") : last ? t("up.nowLast", { name: nameOf(last), ago: fmt.ago(last.lastSeenUnix) }) : t("up.nowNobody")}</p>}
      </div>
    </section>
  );
}

/**
 * The subscription link of a person and its page password (derived from the link: a new link is a new password). Read when the page opens
 * (owner and helpers; an observer is not let), never cached; "rotate" makes a new link, which stops the old one.
 */
export function useSubLink(user: User) {
  const t = useTx();
  const toast = useToast();
  const readonly = useReadonly();
  const [rotated, setRotated] = useState<{ url: string; password: string } | null>(null);
  const q = useQuery({
    queryKey: ["users", "link", user.id],
    queryFn: async ({ signal }) => {
      const r = await users.getSubscriptionLink({ userId: user.id }, { signal });
      return { url: r.url, password: r.pagePassword };
    },
    enabled: !readonly,
    gcTime: 0,
    staleTime: 0,
  });
  const rotate = useMutation({
    mutationFn: async () => {
      const r = await users.getSubscriptionLink({ userId: user.id, rotate: true });
      return { url: r.url, password: r.pagePassword };
    },
    onSuccess: (r) => {
      setRotated(r);
      toast(t("users.rotated"));
    },
    onError: (e) => toast.error(errorText(e, t)),
  });
  return { readonly, url: rotated?.url ?? q.data?.url ?? "", password: rotated ? rotated.password : (q.data?.password ?? ""), error: q.isError && !rotated ? errorText(q.error, t) : "", rotate };
}

/** The page password (the board's .us-pw): hidden dots, "Show", "Change" (a new link and password: asks first). Not set: the page opens for anyone with the link. */
export function PagePassword({ link }: { link: ReturnType<typeof useSubLink> }) {
  const t = useTx();
  const [shown, setShown] = useState(false);
  const [ask, setAsk] = useState(false);
  const has = !!link.password;
  return (
    <div className={cx("up-pw", has && "set")}>
      <span className="g" aria-hidden>
        <G d={P.lock} size={16} />
      </span>
      <div className="t">
        <b>{t("users.pagePassword")}</b>
        <span className={cx(has && "mono")}>{has ? (shown ? link.password : "••••••••••••") : t("up.pwNone")}</span>
      </div>
      {has && (
        <div className="a">
          <Button size="md" aria-pressed={shown} onClick={() => setShown(!shown)}>
            {shown ? t("up.pwHide") : t("up.pwShow")}
          </Button>
          <Button size="md" disabled={!link.url || link.rotate.isPending || link.readonly} onClick={() => setAsk(true)}>
            {t("up.pwChange")}
          </Button>
        </div>
      )}
      <ConfirmModal
        open={ask}
        onOpenChange={setAsk}
        title={t("users.rotateT")}
        description={has ? t("users.rotateBodyPw") : t("users.rotateBody")}
        confirmLabel={t("users.rotateDo")}
        danger
        onConfirm={() => link.rotate.mutateAsync().then(() => setShown(false))}
      />
    </div>
  );
}

/** The link card (the board's .up-lkc): the subscription link to copy, a way to the QR window, the page's password. */
export function LinkCard({ user, onQr }: { user: User; onQr: () => void }) {
  const t = useTx();
  const link = useSubLink(user);
  return (
    <section className="up-card up-lkc">
      <CardHead title={t("up.linkTitle")}>
        {link.url && (
          <a href={link.url} target="_blank" rel="noopener noreferrer" className="up-lk up-r">
            {t("users.openPage")}
          </a>
        )}
      </CardHead>
      <div className="up-pad">
        {link.readonly ? (
          <small>{t("up.linkHidden")}</small>
        ) : (
          <>
            <div className="up-code">
              <code>{link.url || link.error || "…"}</code>
              <CopyBtn value={link.url} label={t("common.copy")} variant="primary" ariaLabel={t("users.copyUrl")} />
              <Button size="md" aria-label={t("users.linkQr")} disabled={!link.url} onClick={onQr}>
                <G d={P.qr} size={16} />
              </Button>
            </div>
            <small>{t("up.linkHint")}</small>
            <PagePassword link={link} />
          </>
        )}
      </div>
    </section>
  );
}

/** "In short" (the board's .up-facts): the group, what the person gets, the term, the places, the nodes, the DNS and since when they exist. */
function FactsCard({ data, onEditNodes }: { data: DetailN; onEditNodes: () => void }) {
  const t = useTx();
  const linkApps = useLinkAppNames();
  const dnsLabel = useDnsLabel();
  const user = data.user!;
  const now = nowSec();
  const left = daysLeft(user.expiresUnix, now);
  const expired = user.expiresUnix > 0 && user.expiresUnix <= now;
  const apps = [user.apps?.happ && linkApps, user.apps?.amnezia && "AmneziaVPN", user.apps?.telegram && "Telegram", user.apps?.trustTunnel && "TrustTunnel", user.apps?.mistgate !== false && user.apps?.happ && t("users.mistgate")].filter(Boolean).join(", ");
  const all = data.nodeAccess.length;
  const picked = user.nodes?.all ? all : data.nodeAccess.filter((n) => user.nodes?.nodeIds.includes(n.nodeId)).length;
  const facts: { l: string; v: React.ReactNode; c?: string }[] = [
    { l: t("users.group"), v: <GroupTag id={user.groupId} name={user.groupName} color={user.groupColor} /> },
    { l: t("up.facts.gets"), v: apps || "—" },
    { l: t("users.term"), v: left === null ? t("users.never") : expired ? t("users.status.expired") : `${t("users.until")} ${dayFmt(t, user.expiresUnix, { day: "numeric", month: "long" })} · ${t("users.days", { n: left })}`, c: expired ? "up-crit" : left !== null && left <= 7 ? "up-warn" : "" },
    { l: t("users.devices"), v: user.deviceLimit > 0 ? t("users.devCount", { n: user.devicesUsed, limit: user.deviceLimit }) : String(user.devicesUsed), c: user.deviceLimit > 0 && user.devicesUsed >= user.deviceLimit ? "up-warn" : "" },
    { l: t("up.facts.nodes"), v: user.nodes?.all ? t("up.facts.nodesAll", { n: all }) : t("up.facts.nodesSome", { a: picked, b: all }) },
    { l: t("subs.dns.field"), v: user.effectiveDnsPresetName ? dnsLabel(user.effectiveDnsPresetId, user.effectiveDnsPresetName) : "—" },
    { l: t("up.facts.created"), v: user.createdUnix > 0 ? dayFmt(t, user.createdUnix, { day: "numeric", month: "long" }) : "—" },
  ];
  void onEditNodes;
  return (
    <section className="up-card">
      <CardHead title={t("up.facts.title")} />
      <dl className="up-facts">
        {facts.map((x) => (
          <div key={x.l}>
            <dt>{x.l}</dt>
            <dd className={x.c}>{x.v}</dd>
          </div>
        ))}
      </dl>
    </section>
  );
}

/** The Overview tab: the traffic and who is online on the left; the link, its password and the facts on the right (a column of 400 px; one column under 1040). */
export function OverviewTab({ data, onTab, onQr }: { data: DetailN; onTab: (tab: "devices" | "access") => void; onQr: () => void }) {
  const fmt = useFmt();
  const user = data.user!;
  const nodeName = (id: string) => data.nodeAccess.find((n) => n.nodeId === id)?.nodeName ?? id;
  const blocked = user.status === UserStatus.DISABLED || user.status === UserStatus.EXPIRED || user.status === UserStatus.LIMITED;
  return (
    <div className="up-g">
      <div className="col">
        <ConnectionAlerts userId={user.id} />
        <TrafficPanel data={data} fmt={fmt} />
        <NowCard data={data} nodeName={nodeName} onDevices={() => onTab("devices")} blocked={blocked} />
      </div>
      <div className="col">
        <LinkCard user={user} onQr={onQr} />
        <FactsCard data={data} onEditNodes={() => onTab("access")} />
      </div>
    </div>
  );
}

