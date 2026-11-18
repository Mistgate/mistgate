import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState, type ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { useToast } from "@/components/ui/toast";
import type { Group } from "@/gen/mistgate/admin/v1/group_pb";
import { groups as groupsApi } from "@/lib/api";
import { errorText } from "@/lib/errors";
import { useT } from "@/i18n";
import { groupTone } from "@/screens/users/format";
import { useTx } from "@/screens/users/t";
import { G } from "@/screens/users/user-bits";
import { PfMenu, PfMenuItem } from "./pf-frame";
import type { TabCtx } from "./page-types";
import "./profiles.css";
import "./profile-page.css";
import "./profile-tabs.css";

const Lnk = Link as unknown as (props: Record<string, unknown>) => ReactNode;
const PEOPLE = "M9 11a3.5 3.5 0 1 0 0-7 3.5 3.5 0 0 0 0 7zM3 20c0-3.3 2.7-5.5 6-5.5s6 2.2 6 5.5M16 4.5a3.5 3.5 0 0 1 0 6.5M17.5 14.8c2 .7 3.5 2.5 3.5 5.2";
const SEND = "M20 4L3 11l6 2.5L11 20l3-4.5 4 3z";

/** What a group's colour says on this page: its dot and the line of its share. */
const colorOf = (g: Group) => `var(--tone-${groupTone(g.id, g.color)})`;

/**
 * The People tab (the board's PnProfile, "People"): who gets the profile, group by group. A group row says how many people it brings, links
 * to the group's page and can be taken out; "Add to a group…" puts the profile into another one (UpdateGroup, the same call the group's
 * own page makes), or makes the first group when the panel has none. A helper changes groups; an observer reads.
 */
export function PeopleTab(ctx: TabCtx) {
  const { data, canGroups, groups, inGroups, variant } = ctx;
  const t = useTx();
  const tt = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const profile = data.profile;
  const [menuOpen] = useState(variant.split(".").includes("menu"));
  const free = groups.filter((g) => !g.profileIds.includes(profile.id));
  const sum = inGroups.reduce((n, g) => n + g.userCount + g.extraUserCount, 0);
  const refresh = () => Promise.all(["groups", "users", "subs", "profiles"].map((k) => qc.invalidateQueries({ queryKey: [k] })));
  // The set is read from the cached list, so a concurrent edit of the same group in another tab is overwritten (as the group's own page does)
  const change = useMutation({
    mutationFn: (p: { g: Group; add: boolean }) => groupsApi.updateGroup({ groupId: p.g.id, profileIds: { values: p.add ? [...p.g.profileIds, profile.id] : p.g.profileIds.filter((x) => x !== profile.id) } }),
    onSuccess: async (_, p) => {
      await refresh();
      toast(t(p.add ? "where.addedToGroup" : "ps.pp.removed", { group: p.g.name }));
    },
    onError: (e) => toast.error(errorText(e, tt)),
  });
  const everyone = useMutation({
    mutationFn: () => groupsApi.createGroup({ name: t("users.everyone"), profileIds: [profile.id] }),
    onSuccess: async () => {
      await refresh();
      toast(t("where.addedToGroup", { group: t("users.everyone") }));
    },
    onError: (e) => toast.error(errorText(e, tt)),
  });
  const proto = profile.protocol;
  const deliv = proto === "trusttunnel" ? t("ps.pp.delivTt") : proto === "awg" ? t("ps.pp.delivAwg") : proto === "mtproto" ? t("ps.pp.delivTg") : t("ps.pp.delivSub");
  const one = inGroups.length === 1 ? inGroups[0]!.id : "";
  const open = (
    <Lnk className="nd-lk" to="/users" search={one ? { group: one } : {}}>
      {t("ps.pp.open")}
    </Lnk>
  );
  return (
    <div className="col pp-col">
      <section className="card pp-sec">
        <div className="pp-sh">
          <span className="pp-ic mint" aria-hidden>
            <G d={PEOPLE} size={20} stroke={1.5} />
          </span>
          <h3>{t("ps.pp.title")}</h3>
        </div>
        <p className="pp-quiet pp-b">{profile.userCount > 0 ? t.n("ps.pp.sum", profile.userCount, { g: inGroups.length }) : t("ps.pp.sumNone")}</p>
        {inGroups.map((g) => (
          <div key={g.id} className="pp-gr">
            <i className="ug-d" style={{ background: colorOf(g) }} aria-hidden />
            <b>{g.name}</b>
            <span className="mono">{g.userCount + g.extraUserCount}</span>
            <i className="pp-gb" style={{ ["--w" as string]: `${g.userCount + g.extraUserCount > 0 ? Math.max(4, Math.min(100, Math.round(((g.userCount + g.extraUserCount) / Math.max(1, sum)) * 100))) : 0}%`, ["--gc" as string]: colorOf(g) }} aria-hidden />
            <Lnk className="an-lk" to="/users/groups/$id" params={{ id: g.id }} aria-label={`${t("common.edit")}: ${g.name}`}>
              {t("common.edit")}
            </Lnk>
            {canGroups ? (
              <button type="button" className="an-lk pp-grx" disabled={change.isPending} aria-label={`${t("ps.pp.remove")}: ${g.name}`} onClick={() => change.mutate({ g, add: false })}>
                {t("ps.pp.remove")}
              </button>
            ) : (
              <span />
            )}
          </div>
        ))}
        {inGroups.length === 0 && (
          <p className="pp-rmk part">
            <i className="rw-gl part" aria-hidden />
            <span>{t("ps.pp.noGroup")}</span>
          </p>
        )}
        <div className="pp-secf">
          {canGroups && free.length > 0 && (
            <PfMenu
              narrow
              label={t("ps.pp.add")}
              defaultOpen={menuOpen}
              trigger={
                <button type="button" className="btn sm">
                  <G d="M12 5v14M5 12h14" size={16} stroke={1.5} />
                  {t("ps.pp.add")}
                </button>
              }
            >
              {free.map((g) => (
                <PfMenuItem key={g.id} onClick={() => change.mutate({ g, add: true })}>
                  <i className="ug-d" style={{ background: colorOf(g) }} aria-hidden />
                  {g.name}
                  <em className="mono">{g.userCount + g.extraUserCount}</em>
                </PfMenuItem>
              ))}
            </PfMenu>
          )}
          {canGroups && groups.length === 0 && (
            <Button size="md" disabled={everyone.isPending} onClick={() => everyone.mutate()}>
              <G d="M12 5v14M5 12h14" size={16} stroke={1.5} />
              {t("ps.pp.everyone", { name: t("users.everyone") })}
            </Button>
          )}
          {open}
        </div>
        <p className="pp-q1 pp-pad">{deliv}</p>
      </section>
      {proto === "mtproto" && (
        <section className="card pp-sec">
          <div className="pp-sh">
            <span className="pp-ic rose" aria-hidden>
              <G d={SEND} size={20} stroke={1.5} />
            </span>
            <h3>{t("ps.pp.tgTitle")}</h3>
          </div>
          <p className="pp-quiet">{t("ps.pp.tgBody")}</p>
          <div className="pp-secf">{open}</div>
        </section>
      )}
    </div>
  );
}
