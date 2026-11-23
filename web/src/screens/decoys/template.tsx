import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { markFresh } from "@/components/fx/registry";
import { Button } from "@/components/ui/button";
import { Flag } from "@/components/ui/flag";
import { Icon } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { Pending, QueryError } from "@/components/ui/query-error";
import { Select } from "@/components/ui/select";
import { useT } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { decoySites } from "@/lib/api";
import { decoyErrorText, defaultTemplateCountry, sitesQuery, sortTemplates, templateIcon, templateNote, templatesQuery, templateWords } from "@/lib/decoysites";
import { useFmt } from "@/lib/format";
import { plain } from "@/lib/plain";
import { nodesQuery } from "@/lib/queries";
import { dialogWidth } from "./create";
import { Msg } from "./parts";
import { type PhotoDemo, PhotoRow } from "./photos";
import { PreviewCard } from "./site-preview";

// "From a template": the window that makes a site from one of the panel's built-in templates. Pick the country (its language, cities,
// addresses and phone numbers; the kinds the panel has no texts for in that language are not offered) and a kind, see the home page of
// a site the panel built for it, press "Another variant" for a different one, "Create the site" to keep the one in view. A preview saves
// nothing: the panel hands back the seed it built the page from, and creating sends that seed, so what is saved is what was shown.

export type TemplateDemo = {
  /** What the window shows: the kinds, a preview (also while the site is saved or when saving was refused). */
  phase: "pick" | "view" | "run" | "err";
  kind?: string;
  /** The country the site is made for ("de"); the window's own pick when none. */
  country?: string;
  /** The document of the preview and the name the site would get; none yet = the preview is still being made. */
  html?: string;
  name?: string;
  /** The code of the refusal of "err". */
  error?: string;
  /** What the preview says about its photos ("used", "no_key", ...), the source they are about, and how the row of the photo key starts. */
  photos?: string;
  photosSource?: string;
  key?: PhotoDemo;
};

/** What a preview answered: the page, the seed it was made from (opaque), the name the site would get, and why it has photos or not and from which source. */
type Shown = { html: string; seed: string; name: string; photos: string; photosSource: string };

export function TemplateDialog({ onClose, demo }: { onClose: () => void; demo?: TemplateDemo }) {
  const t = useT();
  const qc = useQueryClient();
  const navigate = useNavigate();
  const fmt = useFmt();
  const templates = useQuery(templatesQuery);
  const nodes = useQuery(nodesQuery);
  const [kind, setKind] = useState(demo?.kind ?? "");
  const [picked, setPicked] = useState(demo?.country ?? "");
  const [shown, setShown] = useState<Shown | undefined>(demo?.html ? { html: demo.html, seed: "demo", name: demo.name ?? "", photos: demo.photos ?? "", photosSource: demo.photosSource ?? "" } : undefined);
  const [problem, setProblem] = useState(demo?.error ? t(`err.${demo.error}` as MessageKey) : "");
  const countries = templates.data?.countries ?? [];
  // the country of the window: the one picked, else the one most of the nodes with a shared page stand in, else the first
  const country = countries.includes(picked) ? picked : defaultTemplateCountry(nodes.data?.nodes, countries);
  const kinds = sortTemplates((templates.data?.templates ?? []).filter((k) => k.countries.includes(country)));
  const info = templates.data?.templates.find((x) => x.id === kind);
  const words = templateWords(t, kind, info?.nameEn);

  const preview = useMutation({
    mutationFn: async (v: { kind: string; seed: string }) => plain(await decoySites.previewDecoyTemplate({ ...v, country })),
    onSuccess: setShown,
  });
  const create = useMutation({
    mutationFn: async () => plain(await decoySites.generateDecoySite({ kind, seed: shown?.seed ?? "", note: templateNote(t("dz.tpl.noteLead"), words.name, fmt.country(country)), country })),
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
    preview.mutate({ kind: k, seed: "" });
  };
  const another = () => {
    setShown(undefined);
    setProblem("");
    preview.mutate({ kind, seed: "" });
  };
  // a key was added or removed, or the source changed: the same site again (its seed), now with or without its photos
  const keyChanged = () => {
    if (!view || !shown) return;
    setShown(undefined);
    preview.mutate({ kind, seed: shown.seed });
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
          {templates.data && countries.length > 0 && (
            <div className="dz-cc">
              <div className="tx">
                <b>{t("dz.tpl.country")}</b>
                <span>{t("dz.tpl.countryText")}</span>
              </div>
              <Select
                aria-label={t("dz.tpl.country")}
                value={country}
                onValueChange={setPicked}
                options={countries.map((cc) => ({ value: cc, label: fmt.country(cc.toUpperCase()), icon: <Flag code={cc} size={16} /> }))}
              />
            </div>
          )}
          {templates.data && (
            <div className="dz-ways tpl">
              {kinds.map((k) => {
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
          <PhotoRow demo={demo?.key} />
        </>
      ) : (
        <>
          <div className="dz-uf">
            <span className="g" aria-hidden>
              <Icon name={templateIcon(kind)} size={16} />
            </span>
            <div>
              <b>{words.name}</b>
              <span className="cc">
                <Flag code={country} size={12} /> {fmt.country(country.toUpperCase())}
              </span>
              {words.text && <span>{words.text}</span>}
            </div>
            <Button variant="secondary" size="md" disabled={creating} onClick={back}>
              {t("dz.tpl.back")}
            </Button>
          </div>
          <PhotoRow status={making ? undefined : shown?.photos} statusSource={making ? undefined : shown?.photosSource} onChanged={keyChanged} demo={demo?.key} />
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
