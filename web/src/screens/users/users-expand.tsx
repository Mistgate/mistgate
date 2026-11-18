import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { CopyButton } from "@/components/copy-button";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { useToast } from "@/components/ui/toast";
import { UserStatus } from "@/gen/mistgate/admin/v1/user_pb";
import type { Group } from "@/gen/mistgate/admin/v1/group_pb";
import { users } from "@/lib/api";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { useFmt } from "@/lib/format";
import { agoText, devicesView, lastOnlineOf, nowSec, warnOf } from "./format";
import { detailN, type DeviceN, type UserN as User } from "./model";
import { AppLink } from "./nav";
import { platformLabel } from "./awg-devices";
import { useTx, type Tx } from "./t";
import { G, P } from "./user-bits";

// What a person's row opens into (the board's us-x): a line about what is wrong or what to do, the subscription link and the
// page password (to the owner and the helper), the devices, and the quick actions. The devices come from the person's card
// (GetUser, the same cache the card uses), the link from GetSubscriptionLink (never cached: it is a secret).

const warnTone = {
  crit: "bg-danger-soft [--c:var(--danger)]",
  warn: "bg-warn-soft [--c:var(--warning)]",
  info: "bg-step [--c:var(--muted)]",
} as const;

export function UserExpand({ u, group, manage, onLink }: { u: User; group: Group | undefined; manage: boolean; onLink: () => void }) {
  const t = useTx();
  const fmt = useFmt();
  const detail = useQuery({ queryKey: ["users", "detail", u.id], queryFn: ({ signal }) => users.getUser({ userId: u.id }, { signal }).then(detailN), staleTime: 10_000 });
  const [all, setAll] = useState(false);
  const warn = warnOf(u, group, t, fmt);
  const devs = detail.data?.devices ?? [];
  const shown = all ? devs : devs.slice(0, 4);
  const dev = devicesView(u);
  const left = dev.full ? t("ul.dev.full") : u.deviceLimit > 0 ? t("ul.dev.free", { n: u.deviceLimit - u.devicesUsed }) : "";
  return (
    <div className="flex animate-[mg-up_0.25s_var(--e)_both] flex-col gap-4 pt-1 pe-6 pb-5 ps-[68px] @max-[760px]/ust:ps-6 @max-[520px]/ust:px-4 @max-[520px]/ust:pb-4">
      {warn && (
        <div className={cx("flex items-center gap-2.5 rounded-field px-3 py-2.5 text-[13px]", warnTone[warn.tone])}>
          <i aria-hidden className="size-2 flex-none rounded-full bg-(--c)" />
          <b className="font-medium">{warn.text}</b>
        </div>
      )}
      <div className={cx("grid gap-x-10 gap-y-4 @max-[760px]/ust:grid-cols-1", manage && !all ? "grid-cols-[minmax(0,1.2fr)_minmax(0,1fr)]" : "grid-cols-1")}>
        {manage && <LinkBlock u={u} onLink={onLink} />}
        <div className="flex min-w-0 flex-col gap-0.5">
          <span className="flex items-baseline gap-1.5 text-xs leading-5 text-muted">
            {t("ul.col.devices")}
            <b className="font-mono font-medium text-sec">{dev.text}</b>
            {left && <em className={cx("text-xs leading-5 not-italic", dev.full ? "text-warn-text" : "text-muted")}>{left}</em>}
          </span>
          <ul className={cx("flex flex-col gap-0.5", all && "grid animate-[mg-up_0.25s_var(--e)_both] grid-cols-2 gap-x-10 @max-[520px]/ust:grid-cols-1")}>
            {shown.map((d) => (
              <DeviceLine key={d.id} d={d} t={t} />
            ))}
          </ul>
          {devs.length > 4 && (
            <button type="button" onClick={() => setAll(!all)} className="mt-1 self-start text-[13px] text-fg underline decoration-line underline-offset-4 hover:decoration-current">
              {all ? t("ul.dev.fold") : t("ul.dev.showAll", { n: devs.length })}
            </button>
          )}
          {devs.length === 0 && !detail.isPending && <p className="mt-1 text-[13px] text-muted">{t("ul.dev.none")}</p>}
        </div>
      </div>
      <Actions u={u} manage={manage} />
    </div>
  );
}

function DeviceLine({ d, t }: { d: DeviceN; t: Tx }) {
  const name = d.model || platformLabel(d.platform || "other", t);
  const app = d.client ? d.client.split("/")[0] : "";
  return (
    <li className="grid h-7 grid-cols-[8px_minmax(0,1fr)_auto_88px] items-center gap-2.5 text-[13px] @max-[520px]/ust:grid-cols-[8px_minmax(0,1fr)_auto]">
      <i aria-hidden className={cx("size-1.5 rounded-full", d.online ? "bg-accent-mark" : "bg-[color-mix(in_srgb,var(--text)_20%,transparent)]")} />
      <b className="truncate font-medium">{name}</b>
      <span className="whitespace-nowrap text-sec @max-[520px]/ust:hidden">{app}</span>
      <em className="text-end text-xs leading-5 whitespace-nowrap text-muted not-italic">{d.online ? t("users.now") : agoText(lastOnlineOf(d), t, nowSec())}</em>
    </li>
  );
}

/**
 * The subscription link (one for all devices) with its copy button and "QR and send" (the send window), and the page password under
 * it. The password is the link's own (derived from its token): the server can neither set one nor remove it, so the board's
 * "Change" and "Remove" are not drawn; "new link" in the send window changes both. An instance that asks for none has no password:
 * the line says that anyone with the link opens the page.
 */
function LinkBlock({ u, onLink }: { u: User; onLink: () => void }) {
  const t = useTx();
  const q = useQuery({
    queryKey: ["users", "link", u.id],
    queryFn: async ({ signal }) => {
      const r = await users.getSubscriptionLink({ userId: u.id }, { signal });
      return { url: r.url, password: r.pagePassword };
    },
    gcTime: 0,
    staleTime: 0,
    retry: false,
  });
  const [shown, setShown] = useState(false);
  const url = q.data?.url ?? "";
  const pw = q.data?.password ?? "";
  return (
    <div className="flex min-w-0 flex-col gap-1.5">
      <span className="text-xs leading-5 text-muted">{t("ul.link")}</span>
      <div className="flex min-w-0 items-center gap-2 rounded-field border border-line bg-canvas py-1 ps-3 pe-1">
        <code className="min-w-0 flex-1 truncate font-mono text-xs leading-5 text-sec select-all" title={url}>
          {q.isError ? "—" : url || "…"}
        </code>
        <CopyButton value={url} size="md" label={t("common.copy")} />
        <Button size="md" className="gap-1.5!" onClick={onLink}>
          <G d={P.qr} size={16} stroke={1.5} />
          {t("ul.qrSend")}
        </Button>
      </div>
      <small className="text-xs leading-5 text-muted">{t("ul.link.hint")}</small>
      {!q.isPending && (
        <div className="mt-2.5 grid grid-cols-[28px_minmax(0,1fr)_auto] items-center gap-x-3 gap-y-2 rounded-field border border-line py-2 ps-2.5 pe-2 @max-[520px]/ust:grid-cols-[28px_minmax(0,1fr)]">
          <span aria-hidden className={cx("grid size-7 place-items-center rounded-ctl", pw ? "bg-tone-mint-bg text-accent-text" : "bg-step text-sec")}>
            <Icon name="lock" size={16} />
          </span>
          <div className="flex min-w-0 flex-col">
            <b className="text-[13px] font-medium">{t("users.pagePassword")}</b>
            <span className={cx("truncate text-xs leading-4", pw ? "font-mono font-medium text-sec" : "text-muted")}>{pw ? (shown ? pw : "••••••••••••") : t("ul.pw.none")}</span>
          </div>
          {pw && (
            <div className="flex items-center gap-1.5 @max-[520px]/ust:col-span-full">
              <Button size="md" onClick={() => setShown(!shown)}>
                {shown ? t("ul.pw.hide") : t("ul.pw.show")}
              </Button>
              <CopyButton iconOnly value={pw} size="md" label={t("users.copyPassword")} />
            </div>
          )}
        </div>
      )}
    </div>
  );
}

/** Extend by 30 days, reset the traffic, switch off (with a way back) or on; the card's page. */
function Actions({ u, manage }: { u: User; manage: boolean }) {
  const t = useTx();
  const toast = useToast();
  const qc = useQueryClient();
  const off = u.status === UserStatus.DISABLED;
  const refresh = () => qc.invalidateQueries({ queryKey: ["users"] });
  const run = useMutation({
    mutationFn: async (what: "extend" | "reset" | "off" | "on") => {
      if (what === "extend") await users.extendUsers({ userIds: [u.id], days: 30 });
      else if (what === "reset") await users.resetUserTraffic({ userIds: [u.id] });
      else await users.setUsersEnabled({ userIds: [u.id], enabled: what === "on" });
      await refresh();
      return what;
    },
    onSuccess: (what) => {
      if (what === "extend") toast(t("users.extended", { names: u.name }));
      else if (what === "reset") toast(t("users.resetTrafficDone", { name: u.name }));
      else if (what === "on") toast(t("users.enabledToast", { names: u.name }));
      else toast(t("users.disabledOne", { name: u.name }), { undo: () => void users.setUsersEnabled({ userIds: [u.id], enabled: true }).then(refresh) });
    },
    onError: (e) => toast.error(errorText(e, t)),
  });
  return (
    // the board's .btn: 36 high, 10 round, 14px, 16 of padding (the base sizes are 32 and 40)
    <div className="flex flex-wrap items-center gap-2 [&_button]:h-9 [&_button]:rounded-[10px] [&_button]:px-4 [&_button]:text-sm">
      {manage && (
        <>
          <Button disabled={run.isPending || u.expiresUnix === 0} title={u.expiresUnix === 0 ? t("bulk.people.skip.noTerm") : undefined} onClick={() => run.mutate("extend")}>
            {t("ul.act.extend")}
          </Button>
          <Button disabled={run.isPending || u.usedBytes === 0} onClick={() => run.mutate("reset")}>
            {t("ul.act.reset")}
          </Button>
          <Button variant={off ? "primary" : "secondary"} disabled={run.isPending} className={cx(!off && "hover:border-danger hover:text-danger-text")} onClick={() => run.mutate(off ? "on" : "off")}>
            {off ? t("ul.act.on") : t("ul.act.off")}
          </Button>
        </>
      )}
      <AppLink to="/users/$id" params={{ id: u.id }} className="ms-auto @max-[520px]/ust:ms-0 text-[13px] text-fg underline decoration-line underline-offset-4 hover:decoration-current">
        {t("ul.act.page")}
      </AppLink>
    </div>
  );
}
