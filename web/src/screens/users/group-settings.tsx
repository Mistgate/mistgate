import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useState, type CSSProperties, type ReactNode } from "react";
import { Button } from "@/components/ui/button";
import { Notice } from "@/components/ui/notice";
import { Select } from "@/components/ui/select";
import { TextField } from "@/components/ui/text-field";
import { useToast } from "@/components/ui/toast";
import type { Group } from "@/gen/mistgate/admin/v1/group_pb";
import { groups as groupsApi } from "@/lib/api";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { groupTone, groupTones } from "./format";
import { barState, padClass, SaveBar, SvCard, type GroupDemo } from "./group-bits";
import { groupMemberIds, lostProfileIds, moveUsers } from "./group-move";
import { isEveryone } from "./groups";
import { profileListQuery } from "./rpc";
import { useTx } from "./t";
import "./group.css";

// The Settings tab of the group page (PnGroup: sv-, ug-two, up-dz): the name and the colour of the tag, and the danger zone, where the
// people move to another group and the group is deleted. A button of the zone asks twice (it turns red, three seconds to press again).

const every = ["groups", "users", "subs", "profiles", "nodes"];

/** The six colours of a tag: round buttons, the chosen one ringed. */
function Swatches({ value, onChange, disabled, label }: { value: string; onChange: (tone: string) => void; disabled?: boolean; label: string }) {
  const t = useTx();
  return (
    <div role="group" aria-label={label} className="flex h-10 items-center gap-1.5">
      {groupTones.map((tone) => (
        <button
          key={tone}
          type="button"
          aria-pressed={value === tone}
          aria-label={t(`settings.accent.${tone}`)}
          title={t(`settings.accent.${tone}`)}
          disabled={disabled}
          onClick={() => onChange(tone)}
          style={{ "--gc": `var(--accent-${tone})` } as CSSProperties}
          className="group/c grid size-7 place-items-center rounded-full border border-transparent transition-[border-color,transform] duration-150 ease-(--e) aria-pressed:border-(--gc) disabled:opacity-60"
        >
          <i className="size-4 rounded-full bg-(--gc) transition-transform duration-200 ease-spring group-hover/c:scale-[1.15]" />
        </button>
      ))}
    </div>
  );
}

function NameCard({ group, manage, demo }: { group: Group; manage: boolean; demo?: GroupDemo }) {
  const t = useTx();
  const toast = useToast();
  const qc = useQueryClient();
  const [name, setName] = useState(demo?.rename ?? group.name);
  const [color, setColor] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [ok, setOk] = useState(false);
  const shown = color ?? groupTone(group.id, group.color);
  const dirty = name.trim() !== group.name || (color !== null && color !== group.color);
  const save = useMutation({
    mutationFn: () => groupsApi.updateGroup({ groupId: group.id, ...(name.trim() !== group.name && { name: name.trim() }), ...(color !== null && color !== group.color && { color }) }),
    onSuccess: async (r) => {
      await Promise.all(every.map((k) => qc.invalidateQueries({ queryKey: [k] })));
      setColor(null);
      setName(r.group?.name ?? name.trim());
      setOk(true);
      setTimeout(() => setOk(false), 2400);
      toast(t("users.groupSaved", { name: r.group?.name ?? name.trim() }));
    },
    onError: (e) => setError(errorText(e, t)),
  });
  const submit = () => {
    if (!name.trim() || save.isPending || !dirty) return;
    setError(null);
    save.mutate();
  };
  return (
    <SvCard title={t("grp.name.title")} sub={t("grp.name.sub")}>
      <div className={padClass}>
        <div className="grid grid-cols-[minmax(0,1fr)_minmax(0,1.3fr)] items-start gap-4 max-md:grid-cols-1">
          <TextField
            label={t("grp.name.label")}
            value={name}
            maxLength={64}
            autoComplete="off"
            spellCheck={false}
            disabled={!manage}
            onChange={(e) => {
              setName(e.target.value);
              setOk(false);
            }}
            onKeyDown={(e) => {
              if (e.key !== "Enter") return;
              e.preventDefault();
              submit();
            }}
          />
          <div className="flex flex-col gap-1.5">
            <span className="text-[13px] text-sec">{t("grp.color")}</span>
            <Swatches
              label={t("users.groupColor")}
              value={shown}
              disabled={!manage}
              onChange={(c) => {
                setColor(c);
                setOk(false);
              }}
            />
          </div>
        </div>
        {error && <Notice tone="danger">{error}</Notice>}
      </div>
      <SaveBar
        state={manage ? barState(dirty, save.isPending, ok) : "idle"}
        text={save.isPending ? t("grp.bar.saving") : ok ? t("grp.bar.saved") : t("grp.bar.dirty")}
        disabled={!name.trim()}
        onUndo={() => {
          setName(group.name);
          setColor(null);
          setError(null);
        }}
        onSave={submit}
      />
    </SvCard>
  );
}

/** A row of the danger zone: what it does and the means to do it. */
function ZoneRow({ title, sub, children }: { title: string; sub: ReactNode; children: ReactNode }) {
  return (
    <div className="grid grid-cols-[minmax(0,1fr)_auto] items-center gap-4 border-t border-line px-6 py-3.5 max-md:grid-cols-1 max-md:gap-2.5 max-md:px-4">
      <div>
        <b className="block font-medium">{title}</b>
        <span className="mt-0.5 block max-w-[560px] text-[13px] leading-[18px] text-muted">{sub}</span>
      </div>
      {children}
    </div>
  );
}

function DangerZone({ group, groups, onDeleted, demo }: { group: Group; groups: readonly Group[]; onDeleted: () => void; demo?: GroupDemo }) {
  const t = useTx();
  const toast = useToast();
  const qc = useQueryClient();
  const profiles = useQuery(profileListQuery).data ?? [];
  const others = groups.filter((g) => g.id !== group.id);
  const [target, setTarget] = useState("");
  const to = others.find((g) => g.id === target) ?? others.find((g) => isEveryone(g.name)) ?? others[0];
  const [ask, setAsk] = useState<"" | "mv" | "del">(demo?.ask ?? "");
  const [busy, setBusy] = useState("");
  const [error, setError] = useState<string | null>(null);
  // an asking button goes back to itself after three seconds
  useEffect(() => {
    if (!ask || demo?.ask) return;
    const id = setTimeout(() => setAsk(""), 3000);
    return () => clearTimeout(id);
  }, [ask, demo?.ask]);
  const people = group.userCount;
  const lost = to ? lostProfileIds([group], to).map((id) => profiles.find((p) => p.id === id)?.name ?? id) : [];
  const refresh = () => Promise.all(every.map((k) => qc.invalidateQueries({ queryKey: [k] })));

  const moveAll = async () => {
    if (!to || people === 0) return;
    setError(null);
    if (ask !== "mv") return setAsk("mv");
    setAsk("");
    setBusy(t("grp.mv.busy"));
    try {
      const ids = await groupMemberIds(group.id);
      const r = await moveUsers(ids, to.id, (n) => setBusy(`${t("grp.mv.busy")} ${n}/${ids.length}`));
      await refresh();
      if (r.error) setError(t("grp.moveFail", { done: r.done, total: ids.length, error: errorText(r.error, t) }));
      else toast(t.n("grp.moved", r.done, { name: to.name }));
    } catch (e) {
      setError(errorText(e, t));
    } finally {
      setBusy("");
    }
  };
  const remove = async () => {
    if (people > 0) return;
    setError(null);
    if (ask !== "del") return setAsk("del");
    setAsk("");
    setBusy("del");
    try {
      await groupsApi.deleteGroup({ groupId: group.id, moveUsersTo: "" });
      toast(t("users.groupDeleted", { name: group.name }));
      onDeleted();
      await refresh();
    } catch (e) {
      setError(errorText(e, t));
    } finally {
      setBusy("");
    }
  };
  // only the people that have the group as their MAIN group have to be moved first; the extra members just leave it, and this says so
  const extra = group.extraUserCount;
  const deleteSub = (people > 0 ? t.n("grp.del.sub", people) : extra > 0 ? "" : t("grp.del.subEmpty")) + (extra > 0 ? `${people > 0 ? " " : ""}${t.n("grp.del.extraNote", extra)}` : "");
  // the button that asks again turns red; the plain button of its kind has its own colours, so these win with `!`
  const asking = "border-danger! text-danger-text! hover:border-danger! hover:text-danger-text!";
  return (
    <SvCard title={t("grp.dz")}>
      <ZoneRow
        title={t("grp.mv.title")}
        sub={
          people === 0 ? (
            t("grp.mv.none")
          ) : others.length === 0 ? (
            t("grp.mv.noOther")
          ) : (
            <>
              {t.n("grp.mv.sub", people)}
              {lost.length > 0 && (
                <span className="mt-1 block text-warn-text">
                  {t.n("users.impactGroup", people)} {lost.map((n) => t("common.quoted", { name: n })).join(", ")}. {t("grp.pick.keys")}
                </span>
              )}
            </>
          )
        }
      >
        {people > 0 && others.length > 0 && to && (
          <div className="flex flex-wrap items-center gap-2">
            {others.length > 1 && (
              <div className="w-48 max-md:w-full">
                <Select
                  aria-label={t("grp.mv.target")}
                  value={to.id}
                  onValueChange={(v) => {
                    setTarget(v);
                    setAsk("");
                  }}
                  options={others.map((g) => ({ value: g.id, label: g.name }))}
                />
              </div>
            )}
            <Button size="lg" disabled={busy !== ""} className={cx(ask === "mv" && asking)} onClick={() => void moveAll()}>
              {busy && busy !== "del" ? busy : ask === "mv" ? t("grp.mv.sure", { n: people }) : t("grp.mv.do", { name: to.name })}
            </Button>
          </div>
        )}
      </ZoneRow>
      <ZoneRow title={t("grp.del.title")} sub={deleteSub}>
        <Button size="lg" aria-disabled={people > 0 || undefined} disabled={busy !== ""} className={cx("hover:border-danger! hover:text-danger-text!", people > 0 && "opacity-45", ask === "del" && asking)} onClick={() => void remove()}>
          {ask === "del" ? t("grp.del.sure") : t("grp.del.title")}
        </Button>
      </ZoneRow>
      {error && <Notice tone="danger" className="mx-6 mb-4 max-md:mx-4">{error}</Notice>}
    </SvCard>
  );
}

export function SettingsTab({ group, groups, manage, onDeleted, demo }: { group: Group; groups: readonly Group[]; manage: boolean; onDeleted: () => void; demo?: GroupDemo }) {
  return (
    <div className="flex min-w-0 flex-col gap-4">
      <NameCard group={group} manage={manage} demo={demo} />
      {manage && <DangerZone group={group} groups={groups} onDeleted={onDeleted} demo={demo} />}
    </div>
  );
}
