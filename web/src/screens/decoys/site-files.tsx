import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { useToast } from "@/components/ui/toast";
import { useT } from "@/i18n";
import { decoySites } from "@/lib/api";
import { cx } from "@/lib/cx";
import { maxBundleBytes } from "@/lib/decoy";
import { decoyErrorText, fileQuery, isHtmlPath, isImagePath, isTextPath, treeRows, type Site, type SiteCheck, type SiteFile } from "@/lib/decoysites";
import { errorText } from "@/lib/errors";
import { useFmt } from "@/lib/format";
import { CardHead, DzCard } from "./parts";

const icons = { dir: "M3.5 7a2 2 0 0 1 2-2h4l2 2.5h7a2 2 0 0 1 2 2V17a2 2 0 0 1-2 2h-13a2 2 0 0 1-2-2z", file: "M6 3.5h8l4 4V20.5H6zM14 3.5v4h4", img: "M4.5 5.5h15v13h-15zM4.5 15l4.5-4.5 4 4 2.5-2.5 4 4M15 9.5h.01", more: "M6 12h.01M12 12h.01M18 12h.01" } as const;
const Glyph = ({ d }: { d: string }) => (
  <svg aria-hidden width={16} height={16} viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={1.5} strokeLinecap="round" strokeLinejoin="round" className="block flex-none">
    <path d={d} />
  </svg>
);

export type FilesDemo = { file?: string; open?: readonly string[] };

/**
 * The files of the current version (the board's .dz-fs): the tree with sizes on the left, the text of the chosen file on the
 * right. A draft lets one file be replaced or deleted (a new version of the draft); a ready site is changed by an archive.
 * A directory with many files folds the rest into "N more files", which opens on a click.
 */
export function FilesCard({ site, files, demo }: { site: Site; files: readonly SiteFile[]; demo?: FilesDemo }) {
  const t = useT();
  const fmt = useFmt();
  const toast = useToast();
  const qc = useQueryClient();
  const draft = site.state === "draft";
  const [open, setOpen] = useState<ReadonlySet<string>>(new Set(demo?.open));
  const rows = treeRows(files, open);
  const pick = useRef<HTMLInputElement>(null);
  const firstFile = files.find((f) => f.path === "index.html") ?? files[0];
  const [chosen, setChosen] = useState(demo?.file ?? "");
  const sel = files.find((f) => f.path === chosen) ?? firstFile;
  const text = !!sel && isTextPath(sel.path);
  const body = useQuery({ ...fileQuery(site.id, site.bundleSha256, sel?.path ?? ""), enabled: text });
  const [problem, setProblem] = useState("");

  const changed = () => {
    void qc.invalidateQueries({ queryKey: ["decoy-site", site.id] });
    void qc.invalidateQueries({ queryKey: ["decoy-sites"] });
  };
  const edit = useMutation({
    mutationFn: (v: { path: string; content?: Uint8Array; remove?: boolean }) => decoySites.editDecoySiteFile({ siteId: site.id, path: v.path, content: v.content ?? new Uint8Array(), delete: !!v.remove }),
    onSuccess: (_r, v) => {
      setProblem("");
      toast(t(v.remove ? "dz.t.fileGone" : "dz.t.file"));
      if (v.remove) setChosen("");
      changed();
    },
    onError: (e) => setProblem(decoyErrorText(e, t)),
  });
  async function replaceWith(f: File | undefined) {
    if (!f || !sel) return;
    if (f.size > maxBundleBytes) return setProblem(t("dz.v.big", { name: f.name, size: fmt.bytes(f.size, 1024) }));
    edit.mutate({ path: sel.path, content: new Uint8Array(await f.arrayBuffer()) });
  }

  const warnPaths = (code: string) => site.checks.find((c: SiteCheck) => c.level === "warning" && c.code === code)?.paths ?? [];
  const note = !sel
    ? ""
    : body.data?.truncated
      ? t("dz.v.cut")
      : isHtmlPath(sel.path) || /\.css$/i.test(sel.path)
        ? warnPaths("external_urls").includes(sel.path)
          ? t("dz.v.ext")
          : ""
        : /\.m?js$/i.test(sel.path)
          ? t("dz.v.js")
          : "";

  return (
    <DzCard className="dz-fs" data-shot="decoy-files">
      <CardHead title={t("dz.files")}>
        <span className="dz-lc dz-m">{t.n("dz.files.sum", site.fileCount, { size: fmt.bytes(site.sizeBytes, 1024) })}</span>
        <span className="dz-lc dz-m dz-sha" title={t("dz.shaTitle")} dir="ltr">
          {t("dz.sha", { sha: site.bundleSha256.slice(0, 8) })}
        </span>
      </CardHead>
      <div className="dz-fsb">
        <ul className="dz-tree" role="listbox" aria-label={t("dz.tree")}>
          {rows.map((r) => {
            const depth = Math.min(r.depth, 2);
            const label = r.kind === "more" ? t.n("dz.tree.more", r.hidden) : r.name;
            return (
              <li key={r.key} className={cx(`d${depth}`, r.kind === "dir" && "dir", r.kind === "more" && "more")}>
                <button
                  type="button"
                  role="option"
                  aria-selected={r.kind === "file" && sel?.path === r.path}
                  aria-disabled={r.kind === "dir" || undefined}
                  onClick={() => {
                    if (r.kind === "file") setChosen(r.path);
                    else if (r.kind === "more") setOpen(new Set([...open, r.path]));
                  }}
                >
                  <Glyph d={r.kind === "more" ? icons.more : r.kind === "dir" ? icons.dir : isImagePath(r.path) ? icons.img : icons.file} />
                  <span className="dz-m" dir="ltr">
                    {label}
                  </span>
                  {r.kind === "file" && <em className="dz-m">{fmt.bytes(r.size, 1024)}</em>}
                </button>
              </li>
            );
          })}
        </ul>
        <div className="dz-view">
          {sel && (
            <div className="dz-vh">
              <b className="dz-m" dir="ltr">
                {sel.path}
              </b>
              <span className="dz-m">{fmt.bytes(sel.size, 1024)}</span>
              {draft && (
                <>
                  <input
                    ref={pick}
                    type="file"
                    hidden
                    aria-label={t("dz.v.pick")}
                    onChange={(e) => {
                      void replaceWith(e.target.files?.[0]);
                      e.target.value = "";
                    }}
                  />
                  <Button variant="secondary" size="md" disabled={edit.isPending} onClick={() => pick.current?.click()}>
                    {t("dz.v.replace")}
                  </Button>
                  <Button variant="secondary" size="md" disabled={edit.isPending || files.length <= 1} onClick={() => edit.mutate({ path: sel.path, remove: true })}>
                    {t("dz.v.delete")}
                  </Button>
                </>
              )}
            </div>
          )}
          {sel && text && body.data && <pre className="dz-code" dir="ltr">{body.data.text}</pre>}
          {sel && text && !body.data && !body.isError && (
            <p className="dz-bin" role="status">
              <span className="dz-spin" aria-hidden />
              {t("dz.v.wait")}
            </p>
          )}
          {sel && ((text && body.isError) || !text) && (
            <div className="dz-bin" role={text ? "alert" : undefined}>
              <Glyph d={icons.img} />
              <span>{text && body.error ? errorText(body.error, t) : t("dz.v.bin")}</span>
            </div>
          )}
          {(note || problem) && <p className={cx("dz-vn", problem && "text-danger-text")} role={problem ? "alert" : undefined}>{problem || note}</p>}
        </div>
      </div>
    </DzCard>
  );
}
