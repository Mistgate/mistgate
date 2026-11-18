import { useQuery } from "@tanstack/react-query";
import { useId, useState, type FormEvent } from "react";
import { Notice } from "@/components/ui/notice";
import { Pending, QueryError } from "@/components/ui/query-error";
import { Button } from "@/components/ui/button";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { useEdition } from "@/lib/edition";
import { useIsOwner } from "@/lib/updates";
import { cleanReachDomain, isReachDomain, isReachUnavailable, listProblem, reachMax, reachProblemOf, reachStateOf, useWarpReachActions, warpReachQuery, type ReachProblem, type ReachState } from "@/lib/warpreach";
import { G } from "@/screens/users/user-bits";
import { BusyButton, Foot, ICONS, Otg, Sec } from "./sx-bits";
import "./settings-sys.css";

// Settings -> System: the sites every node's WARP is checked against (WarpReachService, owner only). The names are chips in a list with
// a field to add more; what the browser checks is a hint (the panel judges), and a refusal of the panel shows by its code. An empty list
// switches the check off. The Cloudflare edition runs no such check: the card says so, as the other settings of the edition do.

/** What a scene opens the card with: a list typed but not saved yet, a name typed, or a refusal of the panel. */
export type ReachDemo = { draft?: string[]; typed?: string; refused?: ReachProblem; busy?: "save" | "reset" };

const problemKey = {
  reach_domains_too_many: "err.reach_domains_too_many",
  reach_domain_invalid: "err.reach_domain_invalid",
  reach_domain_duplicate: "err.reach_domain_duplicate",
} as const;

/** The card, or nothing for anyone but the owner (the panel refuses both calls to the others). */
export function WarpReachCard({ demo }: { demo?: ReachDemo }) {
  const t = useT();
  const owner = useIsOwner();
  const edition = useEdition().off("health_checks");
  const q = useQuery({ ...warpReachQuery, enabled: owner && !edition });
  if (!owner) return null;
  const head = { tone: "sky" as const, d: ICONS.globe, title: t("sx.warp.title") };
  if (edition || (q.isError && isReachUnavailable(q.error))) {
    return (
      <Sec {...head} data-card="warp-reach">
        <div className="sx-pad">
          <Notice tone="quiet" title={t("err.unavailable_in_edition", { feature: t("edition.feature.health_checks") })} />
        </div>
      </Sec>
    );
  }
  if (q.isError) {
    return (
      <Sec {...head} data-card="warp-reach">
        <div className="sx-pad">
          <QueryError error={q.error} onRetry={() => void q.refetch()} compact />
        </div>
      </Sec>
    );
  }
  if (!q.data) {
    return (
      <Sec {...head} data-card="warp-reach">
        <div className="sx-pad">
          <Pending compact />
        </div>
      </Sec>
    );
  }
  const state = reachStateOf(q.data);
  // a list that came from the panel is a new draft: the editor starts over with it
  return <ReachEditor key={`${state.customised}:${state.domains.join(",")}`} state={state} demo={demo} />;
}

function ReachEditor({ state, demo }: { state: ReachState; demo?: ReachDemo }) {
  const t = useT();
  const actions = useWarpReachActions();
  const id = useId();
  const [draft, setDraft] = useState<string[]>(demo?.draft ?? state.domains);
  const [typed, setTyped] = useState(demo?.typed ?? "");
  const [refused, setRefused] = useState<ReachProblem | undefined>(demo?.refused);
  const [again, setAgain] = useState(false);
  const saving = actions.save.isPending || demo?.busy === "save";
  const resetting = actions.reset.isPending || demo?.busy === "reset";
  const busy = saving || resetting;

  const hint = listProblem(draft);
  const problem = refused ?? (again ? "reach_domain_duplicate" : undefined) ?? hint;
  const dirty = draft.join(",") !== state.domains.join(",");
  const tag = !state.domains.length ? "off" : state.customised ? "custom" : "default";
  // the built-in list is already what is in force: nothing to go back to
  const builtIn = !state.customised;

  const edit = (next: string[]) => {
    setDraft(next);
    setRefused(undefined);
    setAgain(false);
  };
  const add = (e: FormEvent) => {
    e.preventDefault();
    const names = typed.split(/[\s,;]+/).map(cleanReachDomain).filter(Boolean);
    if (!names.length) return;
    const fresh = names.filter((n, i) => !draft.includes(n) && names.indexOf(n) === i);
    edit([...draft, ...fresh]);
    setAgain(fresh.length < names.length);
    setTyped("");
  };
  const fail = (e: unknown) => setRefused(reachProblemOf(e));
  const save = () => actions.save.mutate(draft, { onError: fail });
  const reset = () =>
    actions.reset.mutate(undefined, {
      onError: fail,
      onSuccess: () => setRefused(undefined),
    });

  return (
    <Sec
      tone="sky"
      d={ICONS.globe}
      title={t("sx.warp.title")}
      lead={t("sx.warp.lead")}
      aside={<Otg className={cx("sx-wtag", tag)}>{t(`sx.warp.tag.${tag}`)}</Otg>}
      data-card="warp-reach"
    >
      <div className="sx-wr">
        <div className="sx-wh">
          <span className="rw-sl">{t("sx.warp.label")}</span>
          <em className={cx("mono", draft.length > reachMax && "bad")}>{t("sx.warp.count", { n: draft.length, max: reachMax })}</em>
        </div>
        {draft.length ? (
          <ul className="sx-wl" aria-label={t("sx.warp.label")}>
            {draft.map((d, i) => (
              <li key={`${d}:${i}`} className={cx(!isReachDomain(d) && "bad", draft.indexOf(d) !== i && "bad")}>
                <b className="mono">{d}</b>
                <button type="button" className="sx-wx" disabled={busy} aria-label={t("sx.warp.remove", { name: d })} onClick={() => edit(draft.filter((_, j) => j !== i))}>
                  <G d="M6 6l12 12M18 6L6 18" size={14} stroke={1.6} />
                </button>
              </li>
            ))}
          </ul>
        ) : (
          <p className="sx-wo">{t("sx.warp.empty")}</p>
        )}
        <form className="sx-wa" onSubmit={add}>
          <label className={cx("an-f", problem && "err")}>
            <span>{t("sx.warp.add.label")}</span>
            <span className="sx-wi">
              <input
                className="an-i mono"
                type="text"
                dir="ltr"
                autoComplete="off"
                autoCapitalize="none"
                spellCheck={false}
                placeholder={t("sx.warp.add.ph")}
                value={typed}
                disabled={busy}
                aria-invalid={!!problem}
                aria-describedby={`${id}-h`}
                onChange={(e) => setTyped(e.target.value)}
              />
              <Button type="submit" size="md" disabled={busy || !typed.trim()}>
                {t("sx.warp.add")}
              </Button>
            </span>
            <small id={`${id}-h`} role={refused ? "alert" : undefined}>
              {problem ? t(problemKey[problem]) : t("sx.warp.hint", { max: reachMax })}
            </small>
          </label>
        </form>
      </div>
      <Foot>
        <BusyButton variant="primary" size="md" busy={saving} disabled={!dirty || resetting} onClick={save}>
          {t("sx.warp.save")}
        </BusyButton>
        <BusyButton size="md" busy={resetting} disabled={builtIn || saving} onClick={reset}>
          {t("sx.warp.reset")}
        </BusyButton>
      </Foot>
    </Sec>
  );
}
