import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { TextField } from "@/components/ui/text-field";
import { useT } from "@/i18n";
import { decoySites } from "@/lib/api";
import { decoyErrorText, PEXELS_KEY_URL, PEXELS_URL, photoStateKey, photosQuery } from "@/lib/decoysites";
import { plain } from "@/lib/plain";

// The photos of generated sites come from Pexels with the owner's own free key. This row is where the owner sees whether there is a
// key, adds or removes it, and (once a site is in view) reads why the preview has photos or has none. The key is write-only: it is
// never shown again, only "set". The link to Pexels is part of Pexels' rules for the use of its API.

export type PhotoDemo = { /** The key form is open from the start. */ form?: boolean };

export function PhotoRow({ status, onChanged, demo }: { status?: string; onChanged?: () => void; demo?: PhotoDemo }) {
  const t = useT();
  const qc = useQueryClient();
  const has = useQuery(photosQuery);
  const [form, setForm] = useState(demo?.form ?? false);
  const [key, setKey] = useState("");
  const [problem, setProblem] = useState("");

  const set = useMutation({
    mutationFn: async (k: string) => plain(await decoySites.setDecoyPhotosKey({ key: k })),
    onSuccess: (r) => {
      qc.setQueryData(photosQuery.queryKey, { hasKey: r.hasKey });
      setForm(false);
      setKey("");
      setProblem("");
      onChanged?.();
    },
    onError: (e) => setProblem(decoyErrorText(e, t)),
  });

  const known = has.data !== undefined;
  const hasKey = has.data?.hasKey ?? false;
  const word = status ? photoStateKey(status) : undefined;
  const close = () => {
    setForm(false);
    setKey("");
    setProblem("");
  };

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
          <Button variant="secondary" size="md" disabled={set.isPending} onClick={() => setForm(true)}>
            {t("dz.ph.add")}
          </Button>
          {hasKey && (
            <button type="button" className="dz-lk" disabled={set.isPending} onClick={() => set.mutate("")}>
              {t("dz.ph.remove")}
            </button>
          )}
        </div>
      )}
      {form && (
        <form
          className="frm"
          noValidate
          onSubmit={(e) => {
            e.preventDefault();
            if (key.trim() && !set.isPending) set.mutate(key.trim());
          }}
        >
          <p>
            {t("dz.ph.lead")}{" "}
            <a href={PEXELS_KEY_URL} target="_blank" rel="noopener noreferrer">
              {t("dz.ph.get")}
            </a>
          </p>
          <TextField
            label={t("dz.ph.field")}
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
            <Button size="md" disabled={set.isPending} onClick={close}>
              {t("dz.ph.cancel")}
            </Button>
            <Button type="submit" size="md" variant="primary" disabled={!key.trim() || set.isPending}>
              {set.isPending && <span aria-hidden className="dz-spin" />}
              {t(set.isPending ? "dz.ph.saving" : "dz.ph.save")}
            </Button>
          </div>
        </form>
      )}
      {!form && problem && <p className="dz-ph-bad">{problem}</p>}
      <p className="ft">
        {word ? <span className={status === "used" ? "st on" : "st"}>{t(word)}</span> : <span />}
        <a href={PEXELS_URL} target="_blank" rel="noopener noreferrer">
          {t("dz.ph.credit")}
        </a>
      </p>
    </div>
  );
}
