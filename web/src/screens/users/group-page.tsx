import { useQuery } from "@tanstack/react-query";
import { Link, useParams, useSearch } from "@tanstack/react-router";
import { useState } from "react";
import { PageTitle } from "@/components/ui/bits";
import { Button, buttonClass } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { Notice } from "@/components/ui/notice";
import { Pending, QueryError } from "@/components/ui/query-error";
import { Tabs } from "@/components/ui/tabs";
import type { Group } from "@/gen/mistgate/admin/v1/group_pb";
import { cx } from "@/lib/cx";
import { initialOf } from "@/lib/initial";
import { useMove } from "@/lib/paging";
import { meQuery } from "@/lib/session";
import { AccessTab } from "./group-access";
import type { GroupDemo } from "./group-bits";
import { groupCountsQuery, PeopleTab } from "./group-people";
import { SettingsTab } from "./group-settings";
import { canManage } from "./groups";
import { groupTone } from "./format";
import { groupsQuery } from "./rpc";
import { useTx } from "./t";
import "./group.css";

// The page of a group (/users/groups/<id>), the board PnGroup: a head with the group's mark, its count, who is online and what it
// gives; three tabs, People (the group in figures, who is in it, who to move in), Access (profiles, nodes, DNS) and Settings (name,
// colour, the danger zone). It replaces the window that edited a group.

type TabId = "people" | "access" | "settings";
const tabIds: readonly TabId[] = ["people", "access", "settings"];

function Back() {
  const t = useTx();
  return (
    <Link to="/users" className="group/b -ms-2 inline-flex h-8 items-center gap-1.5 self-start rounded-field ps-2 pe-3 text-sm text-sec transition-[background-color,color] duration-150 ease-(--e) hover:bg-step hover:text-fg">
      <Icon name="back" size={16} className="block flex-none transition-transform duration-200 ease-(--e) group-hover/b:-translate-x-0.5 rtl:group-hover/b:translate-x-0.5" />
      {t("grp.back")}
    </Link>
  );
}

/** A card in the middle of the page: the group is gone (deleted a moment ago) or was never there. */
function Gone({ done, title, body }: { done?: boolean; title: string; body: string }) {
  const t = useTx();
  return (
    <section className="mt-4 flex animate-[mg-up_0.3s_var(--e)_both] flex-col items-center gap-1.5 rounded-card border border-line bg-surface px-6 py-14 text-center">
      <span aria-hidden className="mb-2 grid size-12 place-items-center rounded-full bg-step text-sec">
        <Icon name={done ? "check" : "info"} size={22} />
      </span>
      <b className="text-lg font-medium">{title}</b>
      <span className="text-sm text-sec">{body}</span>
      <Link to="/users" className={cx(buttonClass("secondary", "lg"), "mt-3")}>
        {t("grp.toUsers")}
      </Link>
    </section>
  );
}

export function GroupScreen({ id: idProp, demo }: { id?: string; demo?: GroupDemo }) {
  const t = useTx();
  const params = useParams({ strict: false }) as { id?: string };
  const id = idProp ?? params.id ?? "";
  const groups = useQuery(groupsQuery);
  const manage = canManage(useQuery(meQuery).data?.admin?.role);
  const [deleted, setDeleted] = useState(demo?.deleted ?? false);
  const group = groups.data?.find((g) => g.id === id);
  let body;
  if (deleted) body = <Gone done title={t("grp.deleted.title")} body={t("grp.deleted.body")} />;
  else if (groups.isPending) body = <Pending />;
  else if (groups.isError && !groups.data) body = <QueryError error={groups.error} onRetry={() => void groups.refetch()} />;
  else if (!group) body = <Gone title={t("grp.missing.title")} body={t("grp.missing.body")} />;
  else body = <GroupPage key={group.id} group={group} groups={groups.data ?? []} manage={manage} onDeleted={() => setDeleted(true)} demo={demo} />;
  return (
    <div className="flex flex-col gap-4">
      <Back />
      {body}
    </div>
  );
}

function GroupPage({ group, groups, manage, onDeleted, demo }: { group: Group; groups: readonly Group[]; manage: boolean; onDeleted: () => void; demo?: GroupDemo }) {
  const t = useTx();
  const move = useMove();
  const search = useSearch({ strict: false }) as { tab?: string };
  const tab: TabId = tabIds.find((x) => x === search.tab) ?? "people";
  const [pick, setPick] = useState(demo?.pick ?? false);
  const counts = useQuery(groupCountsQuery(group.id)).data;
  const all = counts?.all ?? group.userCount;
  const online = counts?.online ?? 0;
  const setTab = (next: string) => move({ tab: next === "people" ? undefined : next, page: undefined }, { replace: true });
  const openPick = () => {
    setTab("people");
    setPick(true);
  };
  const profiles = group.profileIds.length;
  return (
    <>
      <header className="mt-1 flex items-center gap-4 max-[1200px]:flex-wrap">
        <span
          aria-hidden
          data-tone={groupTone(group.id, group.color)}
          className="grid size-12 flex-none place-items-center rounded-[14px] bg-(--t) text-lg font-extrabold text-[#0c0c0e] select-none"
        >
          {initialOf(group.name, t.lang)}
        </span>
        <div className="flex min-w-0 flex-1 flex-col gap-1.5">
          <PageTitle className="break-words">{group.name}</PageTitle>
          <div className="flex flex-wrap items-center gap-x-3.5 gap-y-1.5 text-[13px] text-sec">
            <span>{t.n("grp.count", all)}</span>
            <span className={cx("inline-flex items-center gap-1.5", online > 0 && "text-fg")}>
              <i aria-hidden className={cx("size-2 flex-none rounded-full", online > 0 ? "bg-accent-mark" : "bg-[color-mix(in_srgb,var(--text)_22%,transparent)]")} />
              {online > 0 ? t("grp.onlineN", { n: online }) : t("grp.noOnline")}
            </span>
            <span className={cx(profiles === 0 && "text-warn-text")}>{profiles > 0 ? t.n("grp.profilesN", profiles) : t("grp.noProfilesMeta")}</span>
          </div>
        </div>
        {manage && (
          <div className="flex flex-none gap-2 max-[1200px]:w-full max-md:flex-wrap max-md:[&>*]:flex-1">
            <Button size="lg" onClick={openPick}>
              {t("grp.pickOpen")}
            </Button>
            <Link to="/users" search={{ create: true, group: group.id }} className={cx(buttonClass("primary", "lg"), "no-underline")}>
              <Icon name="plus" size={16} />
              {t("grp.newPerson")}
            </Link>
          </div>
        )}
      </header>
      {!manage && <Notice tone="quiet">{t("grp.readOnly")}</Notice>}
      <Tabs
        className="grp-tabs"
        aria-label={t("grp.tabs")}
        value={tab}
        onValueChange={setTab}
        items={[
          {
            value: "people",
            label: (
              <>
                {t("grp.tab.people")}
                {all > 0 && <span className="grid h-5 min-w-5 place-items-center rounded-[10px] bg-step px-1.5 font-mono text-[11px] font-normal text-sec">{all}</span>}
              </>
            ),
            content: <PeopleTab group={group} groups={groups} manage={manage} pick={pick} setPick={setPick} demo={demo} />,
          },
          { value: "access", label: t("grp.tab.access"), content: <AccessTab group={group} manage={manage} demo={demo} /> },
          { value: "settings", label: t("grp.tab.settings"), content: <SettingsTab group={group} groups={groups} manage={manage} onDeleted={onDeleted} demo={demo} /> },
        ]}
      />
    </>
  );
}
