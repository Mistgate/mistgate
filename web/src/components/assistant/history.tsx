import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Button } from "@/components/ui/button";
import { useT } from "@/i18n";
import { assistant } from "@/lib/api";
import type { ListConversationsResponse } from "@/gen/mistgate/admin/v1/assistant_pb";
import { assistantErrorKey } from "@/lib/assistant";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { useFmt } from "@/lib/format";
import type { Plain } from "@/lib/plain";
import { I } from "./as-icons";
import { useScene } from "./scene";

// The conversations as the board draws them (.as-hs): a search over a list in three groups (today, yesterday, earlier), the open one framed, a
// trash can that shows on a row and asks once, and under the list what is kept and "Delete all". Opening a row hands the conversation to the
// window; deleting the one that is open starts a new one.

export type Conv = Plain<ListConversationsResponse>["conversations"][number];

const startOfDay = (d: Date) => new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime();

/** The three groups of the board: today, yesterday, everything before. */
function groups(list: readonly Conv[], now = Date.now()): { key: "today" | "yest" | "earlier"; rows: Conv[] }[] {
  const today = startOfDay(new Date(now));
  const yesterday = today - 86_400_000;
  const out = { today: [] as Conv[], yest: [] as Conv[], earlier: [] as Conv[] };
  for (const c of list) {
    const at = Number(c.updatedUnix) * 1000;
    (at >= today ? out.today : at >= yesterday ? out.yest : out.earlier).push(c);
  }
  return (["today", "yest", "earlier"] as const).filter((k) => out[k].length > 0).map((k) => ({ key: k, rows: out[k] }));
}

export function History({ list, loaded, failed, query, onQuery, current, tz, onPick, onGone }: { list: readonly Conv[]; loaded: boolean; failed: boolean; query: string; onQuery: (q: string) => void; current: string; tz: number; onPick: (id: string) => void; onGone: (id: string | "all") => void }) {
  const t = useT();
  const fmt = useFmt();
  const qc = useQueryClient();
  const scene = useScene();
  const [asking, setAsking] = useState(() => (scene.has("ask") ? (list.find((c) => c.id !== current)?.id ?? "") : ""));
  const [wipe, setWipe] = useState(scene.has("wipe"));
  const [error, setError] = useState("");
  const done = (e: unknown) => setError(assistantErrorKey(e) ? t(assistantErrorKey(e)!) : errorText(e, t));
  const refresh = () => void qc.invalidateQueries({ queryKey: ["assistant", "conversations"] });
  const del = useMutation({
    mutationFn: (id: string) => assistant.deleteConversation({ id }),
    onSuccess: (_r, id) => {
      setAsking("");
      onGone(id);
      refresh();
    },
    onError: done,
  });
  const all = useMutation({
    mutationFn: () => assistant.deleteAllConversations({ allAdmins: false }),
    onSuccess: () => {
      setWipe(false);
      onGone("all");
      refresh();
    },
    onError: done,
  });
  const none = loaded && list.length === 0;
  const due = (c: Conv) => Number(c.followUpUnix ?? 0);
  return (
    <div className="as-hs">
      <div className="as-ht2">
        <label className="as-hq">
          <I g="search" />
          <input placeholder={t("as.hist.search")} aria-label={t("as.hist.search")} autoComplete="off" value={query} onChange={(e) => onQuery(e.target.value)} />
        </label>
      </div>
      <div className="as-b as-hl">
        {groups(list).map((g) => (
          <div key={g.key} className="as-hg">
            <div className="as-hd">
              <span>{t(`as.day.${g.key}` as never)}</span>
            </div>
            {g.rows.map((c) =>
              asking === c.id ? (
                <div key={c.id} className="as-hr as-ask" role="group" aria-label={c.title || t("ai.conv.untitled")}>
                  <span>{t("aip.conv.ask")}</span>
                  <Button size="md" onClick={() => setAsking("")}>
                    {t("ai.mem.cancel")}
                  </Button>
                  <Button size="md" variant="danger" disabled={del.isPending} onClick={() => del.mutate(c.id)}>
                    {t("ai.conv.delete")}
                  </Button>
                </div>
              ) : (
                <div key={c.id} className={cx("as-hr", c.id === current && "on")}>
                  <button type="button" className="as-ho" onClick={() => onPick(c.id)}>
                    <span className="as-h1">
                      <b>
                        {c.unread && <span role="img" aria-label={t("ai.conv.unread")} title={t("ai.conv.unread")} data-slot="unread" className="as-ud" />}
                        <span>{c.title || t("ai.conv.untitled")}</span>
                        {c.taskId && <span className="as-tm">{t("ai.conv.scheduled")}</span>}
                      </b>
                      <span>{c.id === current ? t("as.hist.now") : fmt.ago(c.updatedUnix)}</span>
                    </span>
                    <span className="as-h2">
                      {t.n("ai.conv.messages", c.messages)}
                      {due(c) > 0 && <> · {t("ai.conv.followUp", { time: fmt.stampAt(due(c), tz) })}</>}
                    </span>
                  </button>
                  <button type="button" className="as-ic as-hx" aria-label={t("as.hist.del")} title={t("as.hist.del")} onClick={() => setAsking(c.id)}>
                    <I g="trash" />
                  </button>
                </div>
              ),
            )}
          </div>
        ))}
        {failed && !loaded && (
          <div className="as-e">
            <b>{t("aip.err.title")}</b>
            <p>{t("aip.err.body")}</p>
          </div>
        )}
        {none && (
          <div className="as-e">
            <b>{query.trim() ? t("as.hist.noMatch") : t("as.hist.empty")}</b>
            <p>{t("as.hist.enc")}</p>
          </div>
        )}
        {error && (
          <p role="alert" className="as-note">
            {error}
          </p>
        )}
      </div>
      {list.length > 0 &&
        (wipe ? (
          <div className="as-hf as-ask" role="group" aria-label={t("as.hist.askAll")}>
            <span>{t("as.hist.askAll")}</span>
            <Button size="md" onClick={() => setWipe(false)}>
              {t("ai.mem.cancel")}
            </Button>
            <Button size="md" variant="danger" disabled={all.isPending} onClick={() => all.mutate()}>
              {t("ai.conv.deleteAll")}
            </Button>
          </div>
        ) : (
          <div className="as-hf">
            <span>{t("as.hist.encShort")}</span>
            {!query.trim() && (
              <button type="button" className="as-lk" onClick={() => setWipe(true)}>
                {t("ai.conv.deleteAll")}
              </button>
            )}
          </div>
        ))}
    </div>
  );
}
