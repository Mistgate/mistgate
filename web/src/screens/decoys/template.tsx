import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { markFresh } from "@/components/fx/registry";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { Pending, QueryError } from "@/components/ui/query-error";
import { useT } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { decoySites } from "@/lib/api";
import { decoyErrorText, sitesQuery, templateIcon, templatesQuery, templateWords } from "@/lib/decoysites";
import { plain } from "@/lib/plain";
import { dialogWidth } from "./create";
import { Msg } from "./parts";
import { PreviewCard } from "./site-preview";

// "From a template": the window that makes a site from one of the panel's built-in templates. Pick a kind, see the home page of a site
// the panel built for it, press "Another variant" for a different one, "Create the site" to keep the one in view. A preview saves nothing:
// the panel hands back the seed it built the page from, and creating sends that seed, so what is saved is what was shown.

export type TemplateDemo = {
  /** What the window shows: the kinds, a preview (also while the site is saved or when saving was refused). */
  phase: "pick" | "view" | "run" | "err";
  kind?: string;
  /** The document of the preview and the name the site would get; none yet = the preview is still being made. */
  html?: string;
  name?: string;
  /** The code of the refusal of "err". */
  error?: string;
};

/** What a preview answered: the page, the seed it was made from (opaque) and the name the site would get. */
type Shown = { html: string; seed: string; name: string };

export function TemplateDialog({ onClose, demo }: { onClose: () => void; demo?: TemplateDemo }) {
  const t = useT();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const templates = useQuery(templatesQuery);
  const [kind, setKind] = useState(demo?.kind ?? "");
  const [shown, setShown] = useState<Shown | undefined>(demo?.html ? { html: demo.html, seed: "demo", name: demo.name ?? "" } : undefined);
  const [problem, setProblem] = useState(demo?.error ? t(`err.${demo.error}` as MessageKey) : "");
  const info = templates.data?.find((x) => x.id === kind);
  const words = templateWords(t, kind, info?.nameEn);

  const preview = useMutation({
    mutationFn: async (k: string) => plain(await decoySites.previewDecoyTemplate({ kind: k, seed: "" })),
    onSuccess: setShown,
  });
  const create = useMutation({
    mutationFn: async () => plain(await decoySites.generateDecoySite({ kind, seed: shown?.seed ?? "", note: words.name })),
    onSuccess: (r) => {
      if (!r.site) return;
      // its card in the list is washed once the window is gone
      markFresh("site", r.site.id);
      void qc.invalidateQueries({ queryKey: sitesQuery.queryKey });
      void navigate({ to: "/decoys/$id", params: { id: r.site.id } });
    },
    onError: (e) => setProblem(decoyErrorText(e, t)),
  });

  const view = kind !== "";
  const making = preview.isPending || (demo !== undefined && !shown);
  const creating = create.isPending || demo?.phase === "run";
  const ready = !!shown && !making && !creating;

  const pick = (k: string) => {
    setKind(k);
    setShown(undefined);
    setProblem("");
    preview.mutate(k);
  };
  const another = () => {
    setShown(undefined);
    setProblem("");
    preview.mutate(kind);
  };
  const back = () => {
    setKind("");
    setShown(undefined);
    setProblem("");
    preview.reset();
  };
  const make = () => {
    setProblem("");
    create.mutate();
  };

  const foot = !view ? t("dz.tpl.footPick") : problem ? t("dz.tpl.footErr") : creating ? t("dz.tpl.footRun") : shown?.name ? t("dz.tpl.footView", { name: shown.name }) : t("dz.tpl.loading");

  return (
    <Modal
      open
      onOpenChange={(o) => !o && onClose()}
      title={t("dz.d.tpl")}
      closeLabel={t("common.close")}
      wide
      className={dialogWidth}
      shot="decoy-dialog-template"
      footer={
        <>
          <span className="dz-foot tpl">{foot}</span>
          {view && (
            <>
              <Button variant="secondary" size="lg" aria-disabled={making || creating || undefined} className="dz-off" onClick={() => !(making || creating) && another()}>
                {t("dz.tpl.another")}
              </Button>
              <Button variant="primary" size="lg" aria-disabled={!ready || undefined} className="dz-off" onClick={() => ready && make()}>
                {creating && <span aria-hidden className="dz-spin" />}
                {t("dz.tpl.create")}
              </Button>
            </>
          )}
        </>
      }
    >
      {!view ? (
        <>
          <p className="dz-p">{t("dz.tpl.lead")}</p>
          {templates.isError && !templates.data && <QueryError compact error={templates.error} onRetry={() => void templates.refetch()} />}
          {!templates.data && !templates.isError && <Pending compact />}
          {templates.data && (
            <div className="dz-ways tpl">
              {templates.data.map((k) => {
                const w = templateWords(t, k.id, k.nameEn);
                return (
                  <button key={k.id} type="button" className="dz-way" onClick={() => pick(k.id)}>
                    <span className="g" aria-hidden>
                      <Icon name={templateIcon(k.id)} size={16} />
                    </span>
                    <b>{w.name}</b>
                    {w.text && <span>{w.text}</span>}
                  </button>
                );
              })}
            </div>
          )}
        </>
      ) : (
        <>
          <div className="dz-uf">
            <span className="g" aria-hidden>
              <Icon name={templateIcon(kind)} size={16} />
            </span>
            <div>
              <b>{words.name}</b>
              {words.text && <span>{words.text}</span>}
            </div>
            <Button variant="secondary" size="md" disabled={creating} onClick={back}>
              {t("dz.tpl.back")}
            </Button>
          </div>
          {problem && (
            <Msg kind="bad" role="alert" title={t("dz.tpl.errTitle")}>
              <span>{problem}</span>
            </Msg>
          )}
          <PreviewCard id="" sha="" kind="site" bad={false} shown={{ html: shown?.html, error: preview.error ?? undefined, note: t("dz.tpl.note") }} />
        </>
      )}
      <span className="sr-only" aria-live="polite">
        {creating ? t("dz.tpl.footRun") : ""}
      </span>
    </Modal>
  );
}
