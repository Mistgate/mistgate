import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Segmented } from "@/components/ui/segmented";
import { useT } from "@/i18n";
import { cx } from "@/lib/cx";
import { builtinPreviewDoc, pageLabel, pagesQuery, previewQuery } from "@/lib/decoysites";
import { errorText } from "@/lib/errors";
import { useIsPhone } from "@/lib/media";
import { DzCard, DzIcon } from "./parts";

export type PreviewDemo = { width?: "ph" | "pc"; page?: number; notFound?: boolean };

/**
 * The site in a frame (the board's .dz-pv): the page the panel assembled into one document (styles and pictures inlined, scripts
 * cut, links made inert), a switcher of its pages above the frame, "Show 404", and the width of a phone or of a computer. The
 * frame is sandboxed with no permissions at all; a phone gets the phone width only. The built-in page is shown from a copy
 * kept here; the panel's directory has no document to show.
 */
export function PreviewCard({ id, sha, kind, bad, demo }: { id: string; sha: string; kind: "site" | "builtin" | "dir"; bad: boolean; demo?: PreviewDemo }) {
  const t = useT();
  const phone = useIsPhone();
  const [width, setWidth] = useState<"ph" | "pc">(demo?.width ?? "pc");
  const [page, setPage] = useState(demo?.page ?? 0);
  const [notFound, setNotFound] = useState(!!demo?.notFound);
  const site = kind === "site" && !bad;
  const pages = useQuery({ ...pagesQuery(id, sha), enabled: site });
  const list = pages.data?.pages ?? [];
  const at = Math.min(page, Math.max(0, list.length - 1));
  const doc = useQuery({ ...previewQuery(id, sha, list[at] === "index.html" ? "" : (list[at] ?? ""), notFound), enabled: site && (!!pages.data || pages.isError) });

  const has404 = pages.data?.has404 ?? false;
  const shown = kind === "builtin" ? builtinPreviewDoc : site ? doc.data : undefined;
  const wd = phone ? "ph" : width;
  const placeholder = kind === "dir" ? "dir" : bad ? "none" : undefined;
  const waiting = site && !doc.data && !doc.isError;
  const note = kind === "builtin" ? t("dz.pv.noteBuiltin") : placeholder ? "" : notFound && site && !has404 ? t("dz.pv.no404") : t("dz.pv.note");

  return (
    <DzCard className={cx("dz-pv", `v${wd}`, placeholder && "nodoc")} data-shot="decoy-preview">
      {(site && list.length > 0) || !phone ? (
        <div className="dz-pvb">
          {site && list.length > 0 && (
            <>
              <Segmented
                aria-label={t("dz.pv.pages")}
                className="max-w-full overflow-x-auto"
                value={String(at)}
                onValueChange={(v) => {
                  setPage(Number(v));
                  setNotFound(false);
                }}
                options={list.map((p, i) => ({ value: String(i), label: <span className="font-mono text-xs" dir="ltr">{pageLabel(p)}</span> }))}
                variant="inset"
              />
              <Button variant="secondary" size="md" aria-pressed={notFound} className={cx(notFound && "dz-on")} onClick={() => setNotFound(!notFound)}>
                {t("dz.pv.show404")}
              </Button>
            </>
          )}
          {!phone && (
            <Segmented
              aria-label={t("dz.pv.width")}
              className="dz-ws"
              value={width}
              onValueChange={setWidth}
              options={[
                { value: "ph" as const, label: <span className="inline-flex items-center gap-1.5"><DzIcon kind="phone" />{t("dz.pv.phone")}</span> },
                { value: "pc" as const, label: <span className="inline-flex items-center gap-1.5"><DzIcon kind="pc" />{t("dz.pv.pc")}</span> },
              ]}
            />
          )}
        </div>
      ) : null}
      <div className="dz-stage">
        <div className="dz-fr">
          {shown !== undefined && (
            // sandbox="": no scripts, forms, popups or same-origin access; the document comes from the panel with its scripts removed
            <iframe sandbox="" tabIndex={-1} srcDoc={shown} title={t("dz.pv.frame")} />
          )}
          {waiting && <div className="dz-nd bg-step!"><span className="dz-spin" aria-hidden /><span role="status">{t("dz.pv.wait")}</span></div>}
          {site && doc.isError && (
            <div className="dz-nd" role="alert">
              <b>{t("dz.pv.failed")}</b>
              <span>{errorText(doc.error, t)}</span>
            </div>
          )}
          {placeholder && (
            <div className="dz-nd">
              {kind === "dir" && <DzIcon kind="folder" size={24} />}
              <b>{kind === "dir" ? t("dz.ph.dir") : t("dz.ph.none")}</b>
              <span>{kind === "dir" ? t("dz.ph.dirText") : t("dz.ph.noneText")}</span>
            </div>
          )}
        </div>
      </div>
      <p className="dz-pvn">{note}</p>
    </DzCard>
  );
}
