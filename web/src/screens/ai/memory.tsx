import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Button } from "@/components/ui/button";
import { useToast } from "@/components/ui/toast";
import { useT } from "@/i18n";
import { assistant } from "@/lib/api";
import { accessQuery, assistantErrorKey, conversationsQuery, looksSecret, memoryQuery, type Note } from "@/lib/assistant";
import { cx } from "@/lib/cx";
import { errorText } from "@/lib/errors";
import { useFmt } from "@/lib/format";
import { Dlg, ErrCard, Gl, ICONS, Sec, Skeleton } from "./bits";

// What the assistant remembers (each admin's own notes) and the admin's conversations (ADR 0014), as the board draws them (PnAI "Память").

/** Opens a conversation in the floating chat (components/assistant/chat.tsx listens). */
export const openInChat = (id: string) => window.dispatchEvent(new CustomEvent("mistgate:assistant-open", { detail: { id } }));

export function MemoryTab() {
  const notes = useQuery(memoryQuery);
  const convs = useQuery(conversationsQuery(""));
  if ((notes.isPending && !notes.data) || (convs.isPending && !convs.data)) return <Skeleton />;
  if ((notes.isError && !notes.data) || (convs.isError && !convs.data)) return <ErrCard onRetry={() => void (notes.isError ? notes.refetch() : convs.refetch())} />;
  return (
    <div className="sb-tab">
      <Notes />
      <Conversations />
    </div>
  );
}

function Notes() {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const q = useQuery(memoryQuery);
  const [draft, setDraft] = useState("");
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<{ id: string; text: string } | null>(null);
  const [asking, setAsking] = useState("");
  const [fresh, setFresh] = useState("");
  const [forget, setForget] = useState(false);
  const refusal = (e: unknown) => {
    const k = assistantErrorKey(e);
    toast.error(k ? t(k) : errorText(e, t));
  };
  const done = () => void qc.invalidateQueries({ queryKey: ["assistant", "memory"] });
  const save = useMutation({
    mutationFn: (n: { id: string; text: string }) => assistant.saveMemory(n),
    onSuccess: (res) => {
      setDraft("");
      setAdding(false);
      setEditing(null);
      const id = res.note?.id ?? "";
      setFresh(id);
      if (id) setTimeout(() => setFresh((f) => (f === id ? "" : f)), 1600);
      done();
    },
    onError: refusal,
  });
  const del = useMutation({
    mutationFn: (id: string) => assistant.deleteMemory({ id }),
    onSuccess: () => (setAsking(""), toast(t("ai.mem.deleted")), done()),
    onError: refusal,
  });
  const all = useMutation({ mutationFn: () => assistant.forgetAll({}), onSuccess: () => (setForget(false), toast(t("aip.mem.forgotToast")), done()), onError: refusal });
  const notes = q.data ?? [];

  return (
    <Sec
      tone="lav"
      d={ICONS.note}
      title={t("aip.mem.title")}
      lead={t("aip.mem.lead")}
      aside={
        <>
          {notes.length > 0 && (
            <Button size="md" onClick={() => setForget(true)}>
              {t("ai.mem.forgetAll")}
            </Button>
          )}
          <Button size="md" variant="primary" onClick={() => (setAdding(true), setDraft(""))}>
            <Gl d={ICONS.plus} />
            {t("aip.mem.add")}
          </Button>
        </>
      }
    >
      {adding && (
        <div className="ai-note ed new">
          <textarea className="an-i sb-ta" rows={2} maxLength={400} autoFocus aria-label={t("aip.mem.add")} placeholder={t("aip.mem.ph")} value={draft} onChange={(e) => setDraft(e.target.value)} />
          {looksSecret(draft) && <span className="ai-err">{t("ai.secretHint")}</span>}
          <div className="ai-na">
            <Button size="md" onClick={() => setAdding(false)}>
              {t("ai.mem.cancel")}
            </Button>
            <Button size="md" variant="primary" aria-disabled={!draft.trim() || undefined} disabled={save.isPending} onClick={() => draft.trim() && save.mutate({ id: "", text: draft.trim() })}>
              {t("aip.mem.addGo")}
            </Button>
          </div>
        </div>
      )}
      {notes.length === 0 && !adding && <p className="sb-emp ai-emp">{t("aip.mem.empty")}</p>}
      <div className="ai-notes">
        {notes.map((n) => (
          <NoteRow
            key={n.id}
            note={n}
            fresh={fresh === n.id}
            editing={editing?.id === n.id ? editing.text : null}
            asking={asking === n.id}
            onEdit={(v) => setEditing(v === null ? null : { id: n.id, text: v })}
            onAsk={(v) => setAsking(v ? n.id : "")}
            onSave={() => editing && editing.text.trim() && save.mutate({ id: editing.id, text: editing.text.trim() })}
            onDelete={() => del.mutate(n.id)}
          />
        ))}
      </div>
      {forget && (
        <Dlg
          title={t("ai.mem.forgetT")}
          onClose={() => setForget(false)}
          busy={all.isPending}
          footer={
            <>
              <Button size="lg" onClick={() => setForget(false)}>
                {t("ai.mem.cancel")}
              </Button>
              <Button size="lg" variant="primary" className="ai-dngp" disabled={all.isPending} onClick={() => all.mutate()}>
                {t("ai.mem.forgetAll")}
              </Button>
            </>
          }
        >
          <p>{t("ai.mem.forgetBody")}</p>
        </Dlg>
      )}
    </Sec>
  );
}

function NoteRow({ note, fresh, editing, asking, onEdit, onAsk, onSave, onDelete }: { note: Note; fresh: boolean; editing: string | null; asking: boolean; onEdit: (v: string | null) => void; onAsk: (v: boolean) => void; onSave: () => void; onDelete: () => void }) {
  const t = useT();
  if (editing !== null) {
    return (
      <div className="ai-note ed">
        <textarea className="an-i sb-ta" rows={2} maxLength={400} autoFocus aria-label={t("ai.mem.edit")} value={editing} onChange={(e) => onEdit(e.target.value)} />
        {looksSecret(editing) && <span className="ai-err">{t("ai.secretHint")}</span>}
        <div className="ai-na">
          <Button size="md" onClick={() => onEdit(null)}>
            {t("ai.mem.cancel")}
          </Button>
          <Button size="md" variant="primary" aria-disabled={!editing.trim() || undefined} onClick={onSave}>
            {t("ai.mem.save")}
          </Button>
        </div>
      </div>
    );
  }
  return (
    <div className={cx("ai-note", fresh && "fresh")}>
      <p>
        {note.text}
        {note.byAssistant && <i className="uq-otg">{t("aip.mem.by")}</i>}
      </p>
      {asking ? (
        <span className="ai-ask">
          <em>{t("aip.mem.ask")}</em>
          <Button size="md" onClick={() => onAsk(false)}>
            {t("ai.mem.cancel")}
          </Button>
          <Button size="md" className="rw-dng" onClick={onDelete}>
            {t("ai.mem.delete")}
          </Button>
        </span>
      ) : (
        <span className="ai-na">
          <Button size="md" onClick={() => onEdit(note.text)}>
            {t("ai.mem.edit")}
          </Button>
          <Button size="md" onClick={() => onAsk(true)}>
            {t("ai.mem.delete")}
          </Button>
        </span>
      )}
    </div>
  );
}

function Conversations() {
  const t = useT();
  const fmt = useFmt();
  const toast = useToast();
  const qc = useQueryClient();
  const [query, setQuery] = useState("");
  const q = useQuery(conversationsQuery(query.trim()));
  const tz = useQuery(accessQuery).data?.timezoneOffsetMinutes ?? 0;
  const [wipe, setWipe] = useState(false);
  const [asking, setAsking] = useState("");
  const done = () => void qc.invalidateQueries({ queryKey: ["assistant", "conversations"] });
  const del = useMutation({ mutationFn: (id: string) => assistant.deleteConversation({ id }), onSuccess: () => (setAsking(""), done()), onError: (e) => toast.error(errorText(e, t)) });
  const all = useMutation({ mutationFn: () => assistant.deleteAllConversations({ allAdmins: false }), onSuccess: () => (setWipe(false), toast(t("aip.conv.wipedToast")), done()), onError: (e) => toast.error(errorText(e, t)) });
  const list = q.data ?? [];
  return (
    <Sec
      tone="sky"
      d={ICONS.chat}
      title={t("aip.conv.title")}
      foot={t("aip.conv.foot")}
      aside={
        list.length > 0 && !query ? (
          <Button size="md" onClick={() => setWipe(true)}>
            {t("ai.conv.deleteAll")}
          </Button>
        ) : undefined
      }
    >
      <input className="an-i ai-search" aria-label={t("ai.conv.search")} placeholder={t("ai.conv.search")} value={query} onChange={(e) => setQuery(e.target.value)} />
      {q.isError && !q.data && <p className="sb-emp ai-emp">{t("aip.err.body")}</p>}
      {q.data && list.length === 0 && <p className="sb-emp ai-emp">{t("aip.conv.empty")}</p>}
      <div className="ai-convs">
        {list.map((c) => {
          const due = Number(c.followUpUnix ?? 0);
          return (
            <div key={c.id} className="ai-conv">
              <div>
                <b>
                  {c.unread && <i role="img" aria-label={t("ai.conv.unread")} title={t("ai.conv.unread")} className="ai-dot" />}
                  <span>{c.title || t("ai.conv.untitled")}</span>
                  {c.taskId && <i className="uq-otg">{t("aip.conv.sched")}</i>}
                </b>
                <em>
                  {fmt.ago(c.updatedUnix)} · {t.n("ai.conv.messages", c.messages)}
                  {due > 0 && <> · {t("ai.conv.followUp", { time: fmt.stampAt(due, tz) })}</>}
                </em>
              </div>
              {asking === c.id ? (
                <span className="ai-ask">
                  <em>{t("aip.conv.ask")}</em>
                  <Button size="md" onClick={() => setAsking("")}>
                    {t("ai.mem.cancel")}
                  </Button>
                  <Button size="md" className="rw-dng" onClick={() => del.mutate(c.id)}>
                    {t("ai.conv.delete")}
                  </Button>
                </span>
              ) : (
                <span className="ai-na">
                  <Button size="md" onClick={() => openInChat(c.id)}>
                    {t("ai.conv.open")}
                  </Button>
                  <Button size="md" onClick={() => setAsking(c.id)}>
                    {t("ai.conv.delete")}
                  </Button>
                </span>
              )}
            </div>
          );
        })}
      </div>
      {wipe && (
        <Dlg
          title={t("ai.conv.deleteAllT")}
          onClose={() => setWipe(false)}
          busy={all.isPending}
          footer={
            <>
              <Button size="lg" onClick={() => setWipe(false)}>
                {t("ai.mem.cancel")}
              </Button>
              <Button size="lg" variant="primary" className="ai-dngp" disabled={all.isPending} onClick={() => all.mutate()}>
                {t("ai.conv.deleteAll")}
              </Button>
            </>
          }
        >
          <p>{t.n("ai.conv.deleteAllBody", list.length)}</p>
        </Dlg>
      )}
    </Sec>
  );
}
