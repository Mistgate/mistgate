import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Card } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Icon } from "@/components/ui/icons";
import { Modal } from "@/components/ui/modal";
import { Pending, QueryError } from "@/components/ui/query-error";
import { useToast } from "@/components/ui/toast";
import { useT } from "@/i18n";
import { assistant } from "@/lib/api";
import { ConversationHead, ConversationMeta } from "@/components/assistant/chat";
import { accessQuery, assistantErrorKey, conversationsQuery, looksSecret, memoryQuery, type Note } from "@/lib/assistant";
import { errorText } from "@/lib/errors";

// What the assistant remembers (each admin's own notes) and the admin's conversations (ADR 0014).

/** Opens a conversation in the floating chat (components/assistant/chat.tsx listens). */
export const openInChat = (id: string) => window.dispatchEvent(new CustomEvent("mistgate:assistant-open", { detail: { id } }));

export function MemoryTab() {
  return (
    <div className="grid items-start gap-3.5 xl:grid-cols-2">
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
  const [text, setText] = useState("");
  const [editing, setEditing] = useState<{ id: string; text: string } | null>(null);
  const [forget, setForget] = useState(false);
  const refusal = (e: unknown) => {
    const k = assistantErrorKey(e);
    toast.error(k ? t(k) : errorText(e, t));
  };
  const done = () => void qc.invalidateQueries({ queryKey: ["assistant", "memory"] });
  const save = useMutation({
    mutationFn: (n: { id: string; text: string }) => assistant.saveMemory(n),
    onSuccess: () => {
      setText("");
      setEditing(null);
      done();
    },
    onError: refusal,
  });
  const del = useMutation({ mutationFn: (id: string) => assistant.deleteMemory({ id }), onSuccess: () => (toast(t("ai.mem.deleted")), done()), onError: refusal });
  const all = useMutation({ mutationFn: () => assistant.forgetAll({}), onSuccess: () => (setForget(false), done()), onError: refusal });
  const notes = q.data ?? [];

  return (
    <Card lg className="flex flex-col gap-3 p-4">
      <div className="flex flex-wrap items-start gap-3">
        <div className="flex min-w-[14rem] flex-1 flex-col gap-0.5">
          <b className="text-[14px] font-semibold">{t("ai.mem.title")}</b>
          <span className="text-xs leading-snug text-pretty text-muted">{t("ai.mem.hint")}</span>
        </div>
        {notes.length > 0 && (
          <Button type="button" size="sm" variant="ghostDanger" onClick={() => setForget(true)}>
            {t("ai.mem.forgetAll")}
          </Button>
        )}
      </div>
      <form
        className="flex gap-2"
        onSubmit={(e) => {
          e.preventDefault();
          if (text.trim()) save.mutate({ id: "", text: text.trim() });
        }}
      >
        <input
          aria-label={t("ai.mem.add")}
          placeholder={t("ai.mem.ph")}
          value={text}
          maxLength={400}
          onChange={(e) => setText(e.target.value)}
          className="h-10 min-w-0 flex-1 rounded-field border border-line bg-canvas px-3 text-[13px] outline-none focus:border-accent"
        />
        <Button type="submit" variant="secondary" disabled={!text.trim() || save.isPending}>
          <Icon name="plus" size={14} />
          {t("ai.mem.add")}
        </Button>
      </form>
      {looksSecret(text) && <p className="text-xs text-warn-text">{t("ai.secretHint")}</p>}
      {q.isPending && <Pending compact />}
      {q.isError && <QueryError compact error={q.error} onRetry={() => void q.refetch()} />}
      {q.data && notes.length === 0 && <p className="text-[13px] text-muted">{t("ai.mem.empty")}</p>}
      <ul className="flex flex-col divide-y divide-line">
        {notes.map((n) => (
          <NoteRow key={n.id} note={n} editing={editing?.id === n.id ? editing.text : null} onEdit={(v) => setEditing(v === null ? null : { id: n.id, text: v })} onSave={() => editing && save.mutate(editing)} onDelete={() => del.mutate(n.id)} />
        ))}
      </ul>
      <Modal
        open={forget}
        onOpenChange={setForget}
        title={t("ai.mem.forgetT")}
        description={t("ai.mem.forgetBody")}
        footer={
          <>
            <Button type="button" variant="secondary" onClick={() => setForget(false)}>
              {t("ai.mem.cancel")}
            </Button>
            <Button type="button" variant="danger" disabled={all.isPending} onClick={() => all.mutate()}>
              {t("ai.mem.forgetAll")}
            </Button>
          </>
        }
      />
    </Card>
  );
}

function NoteRow({ note, editing, onEdit, onSave, onDelete }: { note: Note; editing: string | null; onEdit: (v: string | null) => void; onSave: () => void; onDelete: () => void }) {
  const t = useT();
  if (editing !== null) {
    return (
      <li className="flex flex-col gap-2 py-2.5">
        <textarea value={editing} maxLength={400} rows={2} onChange={(e) => onEdit(e.target.value)} className="resize-y rounded-field border border-line bg-canvas px-3 py-2 text-[13px] outline-none focus:border-accent" />
        <div className="flex justify-end gap-2">
          <Button type="button" size="sm" variant="ghost" onClick={() => onEdit(null)}>
            {t("ai.mem.cancel")}
          </Button>
          <Button type="button" size="sm" variant="primary" disabled={!editing.trim()} onClick={onSave}>
            {t("ai.mem.save")}
          </Button>
        </div>
      </li>
    );
  }
  return (
    <li className="flex items-start gap-2.5 py-2.5 first:pt-0">
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <span className="text-[13px] leading-snug break-words">{note.text}</span>
        {note.byAssistant && <span className="text-[11px] text-faint">{t("ai.mem.byAssistant")}</span>}
      </div>
      <Button type="button" size="xs" variant="ghost" onClick={() => onEdit(note.text)}>
        {t("ai.mem.edit")}
      </Button>
      <Button type="button" size="xs" variant="ghostDanger" onClick={onDelete}>
        {t("ai.mem.delete")}
      </Button>
    </li>
  );
}

function Conversations() {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const [query, setQuery] = useState("");
  const q = useQuery(conversationsQuery(query.trim()));
  const tz = useQuery(accessQuery).data?.timezoneOffsetMinutes ?? 0;
  const [wipe, setWipe] = useState(false);
  const done = () => void qc.invalidateQueries({ queryKey: ["assistant", "conversations"] });
  const del = useMutation({ mutationFn: (id: string) => assistant.deleteConversation({ id }), onSuccess: done, onError: (e) => toast.error(errorText(e, t)) });
  const all = useMutation({ mutationFn: () => assistant.deleteAllConversations({ allAdmins: false }), onSuccess: () => (setWipe(false), done()), onError: (e) => toast.error(errorText(e, t)) });
  const list = q.data ?? [];
  return (
    <Card lg className="flex flex-col gap-3 p-4">
      <div className="flex flex-wrap items-center gap-3">
        <b className="flex-1 text-[14px] font-semibold">{t("ai.conv.title")}</b>
        {list.length > 0 && !query && (
          <Button type="button" size="sm" variant="ghostDanger" onClick={() => setWipe(true)}>
            {t("ai.conv.deleteAll")}
          </Button>
        )}
      </div>
      <input
        aria-label={t("ai.conv.search")}
        placeholder={t("ai.conv.search")}
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        className="h-10 rounded-field border border-line bg-canvas px-3 text-[13px] outline-none focus:border-accent"
      />
      {q.isPending && <Pending compact />}
      {q.isError && <QueryError compact error={q.error} onRetry={() => void q.refetch()} />}
      {q.data && list.length === 0 && <p className="text-[13px] text-muted">{t("ai.conv.empty")}</p>}
      <ul className="flex flex-col divide-y divide-line">
        {list.map((c) => (
          <li key={c.id} className="flex items-center gap-2.5 py-2.5 first:pt-0">
            <button type="button" onClick={() => openInChat(c.id)} className="flex min-w-0 flex-1 flex-col items-start gap-0.5 text-start">
              <ConversationHead c={c} />
              <ConversationMeta c={c} tz={tz} />
            </button>
            <Button type="button" size="xs" variant="ghost" onClick={() => openInChat(c.id)}>
              {t("ai.conv.open")}
            </Button>
            <Button type="button" size="xs" variant="ghostDanger" onClick={() => del.mutate(c.id)}>
              {t("ai.conv.delete")}
            </Button>
          </li>
        ))}
      </ul>
      <Modal
        open={wipe}
        onOpenChange={setWipe}
        title={t("ai.conv.deleteAllT")}
        description={t.n("ai.conv.deleteAllBody", list.length)}
        footer={
          <>
            <Button type="button" variant="secondary" onClick={() => setWipe(false)}>
              {t("ai.mem.cancel")}
            </Button>
            <Button type="button" variant="danger" disabled={all.isPending} onClick={() => all.mutate()}>
              {t("ai.conv.deleteAll")}
            </Button>
          </>
        }
      />
    </Card>
  );
}
