import { useQuery } from "@tanstack/react-query";
import { Link, useRouterState } from "@tanstack/react-router";
import { useEffect, useMemo, useRef, useState } from "react";
import { halo } from "@/components/fx/halo";
import { navKey, sections, type SectionId } from "@/components/nav";
import { Button, buttonClass } from "@/components/ui/button";
import { ChatRole } from "@/gen/mistgate/admin/v1/assistant_pb";
import { useT } from "@/i18n";
import { accessQuery, conversationsQuery, looksSecret, providerName, recentQuestionsQuery, type Context } from "@/lib/assistant";
import { prefersReducedMotion, useEffectsEnabled } from "@/lib/effects";
import { useIsOwner } from "@/lib/updates";
import { usePick } from "./agent-picker";
import { I } from "./as-icons";
import "./assistant.css";
import { CatMark } from "./cat";
import type { ChatApi } from "./chat";
import { Composer } from "./composer";
import { History } from "./history";
import { useScene } from "./scene";
import { Thread } from "./thread";

// The assistant's window as the board draws it (PnAssistant): 420 x 760 on the corner of the page, a card 12px from the edges on a phone. A head
// (the cat, the name and the model, the conversations, a new one, the settings, close), the conversation or its list, the message box. One turn
// streams at a time: the answer's Markdown, a line per tool call, plan cards the admin applies or dismisses, "Remembered" notes.

/** While the window is open the conversation follows what the server does without this chat: a turn running there (a follow-up's, another tab's)
 * is looked at every 4 s until it ends, and a follow-up is looked for just after its time. */
function useServerTurns(chat: ChatApi) {
  const { conversationId, running, live, refresh } = chat;
  const followUps = chat.followUps ?? [];
  const next = followUps.reduce((min, f) => (Number(f.dueUnix) > 0 ? Math.min(min, Number(f.dueUnix)) : min), Infinity);
  useEffect(() => {
    if (!conversationId || live || !refresh) return;
    let delay: number;
    const ahead = next * 1000 - Date.now();
    if (running) delay = 4_000;
    else if (!Number.isFinite(next)) return;
    // due: the scheduler takes it within a tick; long overdue (the assistant was off), looked at now and then
    else delay = ahead > 0 ? Math.min(ahead + 3_000, 10 * 60_000) : ahead > -120_000 ? 5_000 : 30_000;
    const id = setTimeout(refresh, delay);
    return () => clearTimeout(id);
  }, [conversationId, running, live, next, refresh]);
}

const sectionKinds: Record<string, string> = { nodes: "node", users: "user", profiles: "profile", groups: "group" };

/** The page the admin is on, as the chip that goes with the message. */
function usePageContext(): Context | null {
  const t = useT();
  const path = useRouterState({ select: (st) => st.location.pathname });
  const seg = path.split("/").filter(Boolean);
  const section = seg[0] ?? "overview";
  // only the admin's own sections go with a message (not the AI page itself, not the dev kit or a sign-in page)
  if (section === "ai" || !sections.some((x) => x.id === section)) return null;
  const id = seg[1] ?? "";
  const label = t(navKey(section as SectionId));
  return { kind: id ? (sectionKinds[section] ?? section) : section, id, label: id ? `${label} · ${id}` : label, route: path } as Context;
}

/** Example questions: the first five are the buttons of an empty chat, all of them are what the message box offers to finish. */
const EXAMPLES = ["ai.chat.ex1", "ai.chat.ex2", "ai.chat.ex3", "ai.chat.ex4", "ai.chat.ex5", "ai.chat.ex6", "ai.chat.ex7", "ai.chat.ex8", "ai.chat.ex9"] as const;
const SHOWN = 5;

const phone = () => typeof matchMedia === "function" && matchMedia("(max-width: 720px)").matches;

export function ChatPanel({ chat, readOnly, providerLabel, provider, onClose, initialContext, pickerOpen, draft }: { chat: ChatApi; readOnly: boolean; providerLabel: string; provider?: number; onClose: () => void; initialContext?: Context | null; pickerOpen?: boolean; draft?: { text: string; n: number } }) {
  const t = useT();
  const owner = useIsOwner();
  const pageContext = usePageContext();
  const access = useQuery(accessQuery);
  // the assistant is on and allowed but has no provider: the window says so and the box is closed
  const off = access.data?.available === false && access.data.reason === "no_provider";
  const scene = useScene();
  const [attach, setAttach] = useState(!scene.has("nopage"));
  const [text, setText] = useState(draft?.text ?? "");
  const [askSecret, setAskSecret] = useState(false);
  // opened with an answer that came while the admin was away and no conversation in front: the list, where it is marked
  const [history, setHistory] = useState(() => scene.has("hist") || (!chat.conversationId && (access.data?.unread ?? 0) > 0));
  const [hq, setHq] = useState(scene.has("hq") ? "zz" : "");
  useServerTurns(chat);
  // opened again on a conversation that had follow-ups waiting (or an unread answer): it may have moved on meanwhile
  const reopen = useRef({ chat, unread: access.data?.unread ?? 0 });
  useEffect(() => {
    const { chat: c, unread } = reopen.current;
    if (c.conversationId && !c.live && ((c.followUps?.length ?? 0) > 0 || unread > 0)) c.refresh?.();
  }, []);
  const list = useRef<HTMLDivElement>(null);
  const context = initialContext !== undefined ? initialContext : pageContext;
  const pick = usePick({ provider, model: providerLabel });
  const convs = useQuery({ ...conversationsQuery(hq.trim()), enabled: history });
  // what the message box offers to finish: the questions asked in this conversation (newest first), in the others, then the examples
  const recent = useQuery(recentQuestionsQuery).data;
  const suggestions = useMemo(
    () => [...chat.messages.filter((m) => m.role === ChatRole.USER).map((m) => m.text).reverse(), ...(recent ?? []), ...EXAMPLES.map((k) => t(k))].filter((q) => !looksSecret(q)),
    [chat.messages, recent, t],
  );
  // the wave, the glints and the dotted halo of the window belong to the reveal animation (Settings -> Interface)
  const fx = useEffectsEnabled() && !prefersReducedMotion();
  const tz = access.data?.timezoneOffsetMinutes ?? 0;

  // the list follows the answer as it grows: new messages, text, tool lines, plan cards
  const tail = chat.messages[chat.messages.length - 1];
  const growth = chat.messages.length * 100_000 + (tail ? tail.text.length + tail.tools.length + tail.plans.length * 10 : 0);
  useEffect(() => {
    const el = list.current;
    if (el && growth >= 0) el.scrollTop = el.scrollHeight;
  }, [growth]);

  // the answer is revealed word by word after its text has arrived (useSmoothText): stay at the bottom while it grows,
  // unless the admin has scrolled up to read
  useEffect(() => {
    const el = list.current;
    const content = el?.firstElementChild;
    if (!el || !content || history || typeof ResizeObserver === "undefined") return; // the history list does not grow
    let height = content.getBoundingClientRect().height;
    const ro = new ResizeObserver(() => {
      const grew = content.getBoundingClientRect().height - height;
      height += grew;
      if (grew > 0 && el.scrollHeight - el.scrollTop - el.clientHeight - grew < 48) el.scrollTop = el.scrollHeight;
    });
    ro.observe(content);
    return () => ro.disconnect();
  }, [history]);

  // a turn watched to its end: the halo runs out from the end of the answer, inside the window
  const wasLive = useRef(chat.live);
  useEffect(() => {
    const el = list.current;
    if (wasLive.current && !chat.live && el && tail?.end === "end") {
      const r = el.getBoundingClientRect();
      halo("save", { target: el, origin: { x: r.left + r.width / 2, y: r.bottom - 24 } });
    }
    wasLive.current = chat.live;
  }, [chat.live, tail?.end]);

  function submit(force = false) {
    const body = text.trim();
    if (!body || chat.running || off) return;
    if (!force && looksSecret(body)) return setAskSecret(true);
    setAskSecret(false);
    setText("");
    void chat.send(body, attach ? context : null, { provider: pick.provider, effort: pick.effort });
  }
  /** A page opened from the window: on a phone the window gets out of the way. */
  const onNavigate = () => phone() && onClose();

  const provName = provider !== undefined ? t(providerName(provider)) : "";
  const fresh = () => {
    chat.reset();
    setHistory(false);
    setHq("");
  };
  const sub = off ? t("as.off.sub") : pick.label || (provName ? `${provName} · ${providerLabel}` : providerLabel);
  const count = convs.data?.length ?? 0;

  return (
    <div className="as-dock">
      <section data-fx-halo data-fx={fx ? "1" : undefined} role="dialog" aria-label={t("ai.chat.title")} className="as">
        {fx && (
          <>
            <div className="as-wv" aria-hidden>
              <i />
            </div>
            <span className="as-eg a" aria-hidden />
            <span className="as-eg b" aria-hidden />
          </>
        )}
        <header className="as-h">
          {history ? (
            <button type="button" className="as-ic as-bk" aria-label={t("as.back")} title={t("as.back")} onClick={() => setHistory(false)}>
              <I g="back" flip />
            </button>
          ) : (
            <span className="as-lg" aria-hidden>
              <CatMark size={22} />
            </span>
          )}
          <div className="as-ht">
            <b>{history ? t("ai.chat.history") : t("ai.chat.title")}</b>
            {history ? <span className="as-sb">{convs.data ? (count > 0 ? t.n("as.hist.count", count) : t("as.hist.none")) : " "}</span> : sub && <span className="as-mn">{sub}</span>}
          </div>
          {!history && (
            <button type="button" className="as-ic" aria-label={t("ai.chat.history")} title={t("ai.chat.history")} aria-pressed={history} onClick={() => setHistory(true)}>
              <I g="list" />
            </button>
          )}
          <button type="button" className="as-ic" aria-label={t("ai.chat.new")} title={t("ai.chat.new")} onClick={fresh}>
            <I g="plus" />
          </button>
          {!history && owner && (
            <Link to="/ai" className="as-ic" aria-label={t("ai.chat.settings")} title={t("ai.chat.settings")} onClick={onNavigate}>
              <I g="gear" />
            </Link>
          )}
          <button type="button" className="as-ic" aria-label={t("ai.chat.close")} title={t("ai.chat.close")} onClick={onClose}>
            <I g="x" />
          </button>
        </header>

        {history ? (
          <History
            list={convs.data ?? []}
            loaded={!!convs.data}
            failed={convs.isError}
            query={hq}
            onQuery={setHq}
            current={chat.conversationId}
            tz={tz}
            onPick={(id) => {
              setHistory(false);
              void chat.open(id);
            }}
            onGone={(id) => {
              if (id === "all" || id === chat.conversationId) chat.reset();
            }}
          />
        ) : (
          <>
            <div ref={list} className="as-b">
              <div className="as-bi">
                {off ? <Off owner={owner} onNavigate={onNavigate} /> : chat.messages.length === 0 && <Hello onPick={(q) => void chat.send(q, attach ? context : null, { provider: pick.provider, effort: pick.effort })} />}
                <Thread chat={chat} owner={owner} onNavigate={onNavigate} />
              </div>
            </div>
            <footer className="as-f">
              {askSecret && (
                <div className="as-wn">
                  <span>{t("ai.chat.secretWarn", { provider: provName || "—" })}</span>
                  <Button type="button" size="md" onClick={() => submit(true)}>
                    {t("ai.chat.sendAnyway")}
                  </Button>
                </div>
              )}
              <Composer
                text={text}
                onText={setText}
                onSubmit={() => submit()}
                running={chat.running}
                onStop={() => void chat.stop()}
                context={context}
                attached={attach}
                onAttach={setAttach}
                pick={pick}
                owner={owner}
                onNavigate={onNavigate}
                pickerOpen={pickerOpen}
                suggestions={suggestions}
                off={off}
                placeholder={off ? t("as.off.ph") : undefined}
                focusOnMount
              />
              <div className="as-fm">
                <span>{readOnly ? t("ai.chat.readOnly") : t("as.foot")}</span>
              </div>
            </footer>
          </>
        )}
      </section>
    </div>
  );
}

/** The start of a conversation (the board's "Начало"): the cat, what to ask about, and the first examples as buttons that send themselves. */
function Hello({ onPick }: { onPick: (q: string) => void }) {
  const t = useT();
  return (
    <div className="as-e">
      <span className="as-eo" aria-hidden>
        <CatMark size={30} />
      </span>
      <b>{t("as.hello.t")}</b>
      <p>{t("as.hello.p")}</p>
      <div className="as-sg">
        {EXAMPLES.slice(0, SHOWN).map((k) => (
          <button key={k} type="button" onClick={() => onPick(t(k))}>
            <span>{t(k)}</span>
            <I g="out" flip />
          </button>
        ))}
      </div>
    </div>
  );
}

/** No provider is connected (the board's "Не подключён"): the owner is sent to the AI section, anyone else is told whom to ask. */
function Off({ owner, onNavigate }: { owner: boolean; onNavigate: () => void }) {
  const t = useT();
  return (
    <div className="as-e" data-slot="off">
      <span className="as-eo off" aria-hidden>
        <I g="plug" />
      </span>
      <b>{t("as.off.t")}</b>
      <p>{t("as.off.p")}</p>
      {owner ? (
        <Link to="/ai" className={`${buttonClass("primary", "lg")} as-cta`} onClick={onNavigate}>
          {t("ai.prov.connect")}
        </Link>
      ) : (
        <p>{t("as.off.ask")}</p>
      )}
    </div>
  );
}
