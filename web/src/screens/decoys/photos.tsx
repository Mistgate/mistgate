import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { Segmented } from "@/components/ui/segmented";
import { StatusDot } from "@/components/ui/status";
import { TextField } from "@/components/ui/text-field";
import { useT } from "@/i18n";
import { decoySites } from "@/lib/api";
import { decoyErrorText, isPhotoSource, type PhotoSource, photoSourceOrder, photoSources, photoStateKey, photosQuery } from "@/lib/decoysites";
import { plain } from "@/lib/plain";

// The photos of generated sites come from Pexels or Pixabay with the owner's own free key of that source. This row is where the owner
// picks the source, sees whether it has a key, adds or removes the key, and (once a site is in view) reads why the preview has photos
// or has none. A key is write-only: it is never shown again, only "set". Both services ask to say where the photos are from where the
// search runs, so the line at the foot names the source and links to it; nothing of the kind goes into a site.

export type PhotoDemo = {
  /** The key form is open from the start. */
  form?: boolean;
  /** The source looked at before it has a key (a pick that only this window knows). */
  pick?: PhotoSource;
};

type State = { hasKey: boolean; hasPixabayKey: boolean; source: string };

export function PhotoRow({ status, statusSource, onChanged, demo }: { status?: string; /** The source the preview's photos are about. */ statusSource?: string; onChanged?: () => void; demo?: PhotoDemo }) {
  const t = useT();
  const qc = useQueryClient();
  const state = useQuery(photosQuery);
  const [form, setForm] = useState(demo?.form ?? false);
  const [key, setKey] = useState("");
  const [problem, setProblem] = useState("");
  const [pick, setPick] = useState<PhotoSource | undefined>(demo?.pick);

  const known = state.data !== undefined;
  // the source in use; the panel says "pixabay" when nothing is stored, and so does a panel too old to say
  const inUse: PhotoSource = isPhotoSource(state.data?.source) ? state.data.source : "pixabay";
  // the source on the screen: the one in use, or one the owner only looks at because it has no key yet
  const view = pick ?? inUse;
  const has = (s: PhotoSource) => (s === "pixabay" ? !!state.data?.hasPixabayKey : !!state.data?.hasKey);
  const hasKey = has(view);
  const name = photoSources[view].name;

  const close = () => {
    setForm(false);
    setKey("");
    setProblem("");
  };
  const stored = (r: State) => {
    qc.setQueryData(photosQuery.queryKey, { hasKey: r.hasKey, hasPixabayKey: r.hasPixabayKey, source: r.source });
    setPick(undefined);
    close();
    onChanged?.();
  };
  const setKeyOf = useMutation({
    mutationFn: async (k: string) => plain(await decoySites.setDecoyPhotosKey({ key: k, source: view })),
    onSuccess: stored,
    onError: (e) => setProblem(decoyErrorText(e, t)),
  });
  const setSource = useMutation({
    mutationFn: async (s: PhotoSource) => plain(await decoySites.setDecoyPhotosSource({ source: s })),
    onSuccess: stored,
    onError: (e) => setProblem(decoyErrorText(e, t)),
  });
  const busy = setKeyOf.isPending || setSource.isPending;

  const choose = (s: PhotoSource) => {
    close();
    if (s === inUse) setPick(undefined);
    else if (has(s)) setSource.mutate(s);
    else setPick(s);
  };

  // the preview's own words name the source it was made from; before any preview the line speaks of the source on the screen
  const shownSource = isPhotoSource(statusSource) ? statusSource : view;
  const word = status ? photoStateKey(status) : undefined;

  return (
    <div className="dz-ph">
      <span className="g" aria-hidden>
        <Icon name="image" size={16} />
      </span>
      <div className="tx">
        <b>{t("dz.ph.title")}</b>
        {known && <span className={hasKey ? "on" : undefined}>{t(hasKey ? "dz.ph.on" : "dz.ph.off")}</span>}
      </div>
      {!form && (
        <div className="act">
          <Button variant="secondary" size="md" disabled={busy} onClick={() => setForm(true)}>
            {t(hasKey ? "dz.ph.change" : "dz.ph.add")}
          </Button>
          {hasKey && (
            <button type="button" className="dz-lk" disabled={busy} onClick={() => setKeyOf.mutate("")}>
              {t("dz.ph.remove")}
            </button>
          )}
        </div>
      )}
      {known && (
        <Segmented
          aria-label={t("dz.ph.src")}
          className="src"
          value={view}
          onValueChange={choose}
          options={photoSourceOrder.map((s) => ({
            value: s,
            label: (
              <span className="inline-flex items-center gap-1.5">
                {has(s) && <StatusDot kind="ok" />}
                {photoSources[s].name}
              </span>
            ),
          }))}
        />
      )}
      {known && view === "pexels" && !hasKey && !form && (
        <p className="hint">
          {t("dz.ph.pexelsHint")}{" "}
          <a href={photoSources.pixabay.keyUrl} target="_blank" rel="noopener noreferrer">
            {t("dz.ph.getPixabay")}
          </a>
        </p>
      )}
      {form && (
        <form
          className="frm"
          noValidate
          onSubmit={(e) => {
            e.preventDefault();
            if (key.trim() && !busy) setKeyOf.mutate(key.trim());
          }}
        >
          <p>
            {t("dz.ph.lead")} {view === "pixabay" && <>{t("dz.ph.howPixabay")} </>}
            <a href={photoSources[view].keyUrl} target="_blank" rel="noopener noreferrer">
              {t("dz.ph.get")}
            </a>
          </p>
          <TextField
            label={t("dz.ph.field", { name })}
            placeholder={t("dz.ph.ph")}
            type="password"
            autoComplete="off"
            spellCheck={false}
            autoFocus
            value={key}
            error={problem || undefined}
            onChange={(e) => {
              setKey(e.target.value);
              setProblem("");
            }}
          />
          <div className="btns">
            <Button size="md" disabled={busy} onClick={close}>
              {t("dz.ph.cancel")}
            </Button>
            <Button type="submit" size="md" variant="primary" disabled={!key.trim() || busy}>
              {setKeyOf.isPending && <span aria-hidden className="dz-spin" />}
              {t(setKeyOf.isPending ? "dz.ph.saving" : "dz.ph.save")}
            </Button>
          </div>
        </form>
      )}
      {!form && problem && <p className="dz-ph-bad">{problem}</p>}
      <p className="ft">
        {word ? <span className={status === "used" ? "st on" : "st"}>{t(word, { name: photoSources[shownSource].name })}</span> : <span />}
        <a href={photoSources[shownSource].url} target="_blank" rel="noopener noreferrer">
          {t("dz.ph.credit", { name: photoSources[shownSource].name })}
        </a>
      </p>
    </div>
  );
}
