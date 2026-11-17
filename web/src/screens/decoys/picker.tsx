import { useQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { isDraft, sitesQuery } from "@/lib/decoysites";
import { Pending, QueryError } from "@/components/ui/query-error";
import { usageText } from "./parts";
import { Thumb } from "./thumb";
import "./decoys.css";

/**
 * The choice of one ready site of the library (the board's "Site from the library" on a node's Server tab): the sites as a radio list,
 * each with its miniature, its name and note and where it stands, a link to open it, and a link to all of them. A draft is not
 * offered: it cannot be put on a node. The window of the Nodes page's bulk change and the node's own card use it; `children` go
 * under the list (the node's card puts its drop zone for an archive there).
 */
export function LibraryPicker({ value, onChange, disabled, children }: { value: string; onChange: (siteId: string) => void; disabled?: boolean; children?: ReactNode }) {
  const t = useT();
  const q = useQuery(sitesQuery);
  const sites = (q.data?.sites ?? []).filter((s) => !isDraft(s));
  return (
    <div className="dz-pk">
      <div className="dz-pkh">
        <span>{t("dz.pk.title")}</span>
        <Link to="/decoys" className="dz-lk">
          {t("dz.pk.all")}
        </Link>
      </div>
      {q.isError && !q.data && <QueryError compact error={q.error} onRetry={() => void q.refetch()} />}
      {!q.data && !q.isError && <Pending compact />}
      {q.data && sites.length === 0 && <p className="dz-emp !p-0">{t("dz.pk.none")}</p>}
      {sites.length > 0 && (
        <div className="dz-pkl" role="radiogroup" aria-label={t("dz.pk.title")}>
          {sites.map((s) => (
            <div key={s.id} className={cx("dz-pkr", value === s.id && "on")}>
              <button type="button" className="dz-pkb" role="radio" aria-checked={value === s.id} disabled={disabled} onClick={() => onChange(s.id)}>
                <span className="dz-rd" aria-hidden>
                  <i />
                </span>
                <Thumb site={s} className="dz-pkt" />
                <div>
                  <b dir="auto">{s.name}</b>
                  <span dir="auto">{s.note || t("dz.card.noNote")}</span>
                </div>
                <em>{usageText(t, s.nodeCount, s.onPanel)}</em>
              </button>
              <Link to="/decoys/$id" params={{ id: s.id }} className="inline-flex h-8 items-center rounded-ctl border border-line bg-step px-3 text-[13px] font-medium whitespace-nowrap hover:bg-(--step-hover)">
                {t("dz.pk.open")}
              </Link>
            </div>
          ))}
        </div>
      )}
      {children}
    </div>
  );
}
