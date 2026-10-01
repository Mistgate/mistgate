import { Dialog } from "@base-ui/react/dialog";
import { useQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useEffect, useId, useMemo, useState } from "react";
import { useAddNode } from "@/components/add-node";
import { Icon } from "@/components/ui/icons";
import { useT } from "@/i18n";
import { en, type MessageKey } from "@/i18n/en";
import { ru } from "@/i18n/ru";
import { cx } from "@/lib/cx";
import { useFmt } from "@/lib/format";
import { useNodeStatus } from "@/lib/node-status";
import { nodesQuery, userSearchQuery } from "@/lib/queries";
import { useSignOut } from "@/lib/session";

type Item = { id: string; tag: string; label: string; meta?: string; run: () => void };
type Group = { title: MessageKey; items: Item[] };

/** Where a place leads: a route of the router, with its params and search. */
type Target = { to: string; params?: Record<string, string>; search?: Record<string, string> };
/** A place of the panel: its name, where it sits (the section of a page or a tab), extra words to find it by, ru and en. */
type Place = { id: string; label: MessageKey; crumb?: MessageKey; words: string; go: Target };

const page = (id: string, words: string): Place => ({
  id: `settings-${id}`,
  label: `settings.${id}` as MessageKey,
  crumb: "nav.settings",
  words,
  go: { to: "/settings/$section", params: { section: id } },
});
const tab = (section: "subscriptions" | "health", id: string, label: MessageKey, words: string, first: boolean): Place => ({
  id: `${section}-${id}`,
  label,
  crumb: `nav.${section}`,
  words,
  go: { to: `/${section}`, search: first ? {} : { tab: id } },
});

// Every menu section, the Settings pages and the parts of Integrations, Subscriptions and Health.
export const places: readonly Place[] = [
  { id: "overview", label: "nav.overview", words: "главная home dashboard", go: { to: "/" } },
  { id: "nodes", label: "nav.nodes", words: "серверы флот servers fleet", go: { to: "/nodes" } },
  { id: "users", label: "nav.users", words: "люди друзья группы people friends groups", go: { to: "/users" } },
  { id: "profiles", label: "nav.profiles", words: "протоколы protocols", go: { to: "/profiles" } },
  { id: "subscriptions", label: "nav.subscriptions", words: "ссылки links", go: { to: "/subscriptions" } },
  { id: "health", label: "nav.health", words: "", go: { to: "/health" } },
  { id: "updates", label: "nav.updates", words: "раскатка версии rollout versions", go: { to: "/updates" } },
  { id: "integrations", label: "nav.integrations", words: "", go: { to: "/integrations" } },
  { id: "settings", label: "nav.settings", words: "", go: { to: "/settings" } },
  page("interface", "тема язык акцент цвет бренд логотип название theme language accent colour color brand logo"),
  page("security", "passkey пароль код вход cloudflare turnstile капча 2fa totp password code sign-in captcha"),
  page("sessions", "устройства входы devices"),
  page("audit", "журнал история log history"),
  page("admins", "помощник роли helper roles"),
  page("domains", "адрес ссылки address url links"),
  page("backups", "бэкап бэкапы копия backup copy"),
  { id: "tokens", label: "int.tok.title", crumb: "nav.integrations", words: "токен токены ключ скрипт token tokens key script api", go: { to: "/integrations" } },
  { id: "mcp", label: "int.mcp.title", crumb: "nav.integrations", words: "агент ии claude agent ai", go: { to: "/integrations" } },
  { id: "approvals", label: "palette.approvals", crumb: "nav.integrations", words: "одобрить решения approve decisions", go: { to: "/integrations" } },
  tab("subscriptions", "formats", "subs.tab.formats", "приложения форматы mihomo happ apps formats clients", true),
  tab("subscriptions", "rules", "subs.tab.rules", "правила user-agent rules", false),
  tab("subscriptions", "texts", "subs.tab.texts", "тексты названия texts names", false),
  tab("subscriptions", "page", "subs.tab.page", "страница друга page", false),
  tab("subscriptions", "dns", "subs.tab.dns", "днс пресеты presets", false),
  tab("health", "alerts", "hl.tab.alerts", "алерты проблемы alerts problems", true),
  tab("health", "checks", "hl.tab.checks", "проверки checks", false),
  tab("health", "doctor", "hl.tab.doctor", "доктор doctor", false),
];

/** The node page's tabs, each with the words that find it next to a node's name ("de1 логи"). */
const nodeTabs = [
  { id: "profiles", label: "node.tab.profiles", words: "профили profiles" },
  { id: "users", label: "node.tab.users", words: "пользователи users" },
  { id: "logs", label: "node.tab.logs", words: "логи журнал logs log" },
  { id: "events", label: "node.tab.events", words: "события events" },
  { id: "doctor", label: "node.tab.doctor", words: "доктор doctor" },
  { id: "settings", label: "node.tab.settings", words: "настройки settings warp" },
] as const satisfies readonly { id: string; label: MessageKey; words: string }[];

/** Every word of the query is found in the place's names (both languages) or its extra words. */
export function placeMatches(p: Pick<Place, "label" | "crumb" | "words">, q: string): boolean {
  const hay = [en[p.label], ru[p.label], p.crumb ? en[p.crumb] : "", p.crumb ? ru[p.crumb] : "", p.words].join(" ").toLowerCase();
  return q.split(/\s+/).every((w) => hay.includes(w));
}

/**
 * Command palette (⌘K / Ctrl+K). Groups NODES / USERS / SECTIONS / ACTIONS; type to filter, arrows and Enter to run.
 * Nodes come from the list the shell already polls; users are searched on the server (a small page) once something is
 * typed; sections and actions are found by their names and a few words in Russian and English. Loaded lazily by the
 * shell on first open.
 */
export default function Palette({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const t = useT();
  const fmt = useFmt();
  const st = useNodeStatus();
  const navigate = useNavigate();
  const addNode = useAddNode();
  const signOut = useSignOut();
  const listId = useId();
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);

  const q = query.trim().toLowerCase();
  // the user search waits for a pause in typing: one request per word, not per key
  const [asked, setAsked] = useState("");
  useEffect(() => {
    const id = setTimeout(() => setAsked(q), 200);
    return () => clearTimeout(id);
  }, [q]);

  const nodes = useQuery({ ...nodesQuery, enabled: open });
  const users = useQuery({ ...userSearchQuery(asked), enabled: open && asked !== "" });
  // until the answer for what is typed is in, "nothing found" would be a guess
  const searching = q !== "" && (q !== asked || users.isFetching);

  const groups = useMemo<Group[]>(() => {
    const done = () => onOpenChange(false);
    const go = (target: Target) => () => {
      done();
      void navigate(target as never);
    };
    const nodeList = nodes.data?.nodes ?? [];
    const nodeItems: Item[] = nodeList
      .filter((n) => !q || [n.name, n.countryCode, fmt.country(n.countryCode), n.location, n.provider, n.address].join(" ").toLowerCase().includes(q))
      .slice(0, 5)
      .map((n) => ({
        id: `node-${n.id}`,
        tag: n.countryCode || "·",
        label: `${n.name} · ${n.location || fmt.country(n.countryCode)}`,
        meta: st.word(n),
        run: go({ to: "/nodes/$id", params: { id: n.id } }),
      }));
    // "de1 логи": a tab of a node, when the query names both
    const words = q.split(/\s+/).filter(Boolean);
    const nodeTab = words.length > 1 ? nodeTabs.find((x) => words.some((w) => placeMatches({ label: x.label, words: x.words }, w))) : undefined;
    const rest = nodeTab ? words.filter((w) => !placeMatches({ label: nodeTab.label, words: nodeTab.words }, w)) : [];
    const tabItems: Item[] =
      nodeTab && rest.length > 0
        ? nodeList
            .filter((n) => rest.every((w) => n.name.toLowerCase().includes(w)))
            .slice(0, 4)
            .map((n) => ({
              id: `node-${n.id}-${nodeTab.id}`,
              tag: n.countryCode || "·",
              label: `${n.name} · ${t(nodeTab.label)}`,
              meta: t("nav.nodes"),
              run: go({ to: "/nodes/$id", params: { id: n.id }, search: { tab: nodeTab.id } }),
            }))
        : [];
    // only users the server matched for exactly what is typed (the answer for an older word must not show)
    const userItems: Item[] =
      asked !== "" && asked === q
        ? (users.data?.users ?? []).slice(0, 5).map((u) => ({
            id: `user-${u.id}`,
            tag: u.name.charAt(0).toUpperCase() || "?",
            label: u.name,
            meta: u.groupName,
            run: go({ to: "/users/$id", params: { id: u.id } }),
          }))
        : [];
    const placeItems: Item[] = q
      ? places
          .filter((p) => placeMatches(p, q))
          .slice(0, 6)
          .map((p) => ({ id: `place-${p.id}`, tag: "→", label: t(p.label), meta: p.crumb ? t(p.crumb) : undefined, run: go(p.go) }))
      : [];
    const actions: (Item & { words: string })[] = [
      { id: "new-token", tag: "+", label: t("palette.newToken"), words: "новый токен создать ключ api new token create key", run: go({ to: "/integrations", search: { new: "token" } }) },
      { id: "new-profile", tag: "+", label: t("palette.newProfile"), words: "новый профиль создать new profile create", run: go({ to: "/profiles/new" }) },
      { id: "create-user", tag: "+", label: t("palette.createUser"), words: "новый пользователь создать добавить друга new user create add friend", run: go({ to: "/users", search: { create: "true" } }) },
      {
        id: "add-node",
        tag: "+",
        label: t("palette.addNode"),
        words: "добавить ноду новая сервер add node new server",
        run: () => {
          done();
          addNode();
        },
      },
      {
        id: "sign-out",
        tag: "←",
        label: t("header.signOut"),
        words: "выйти выход logout sign out exit",
        run: () => {
          done();
          void signOut();
        },
      },
    ];
    const all: Group[] = [
      { title: "palette.nodes", items: [...nodeItems, ...tabItems] },
      { title: "palette.users", items: userItems },
      { title: "palette.places", items: placeItems },
      { title: "palette.actions", items: actions.filter((a) => !q || `${a.label} ${a.words}`.toLowerCase().includes(q)) },
    ];
    return all.filter((g) => g.items.length > 0);
  }, [q, asked, nodes.data, users.data, fmt, st, t, navigate, onOpenChange, addNode, signOut]);

  const flat = groups.flatMap((g) => g.items);
  const current = Math.min(active, flat.length - 1);

  function change(next: boolean) {
    onOpenChange(next);
    if (!next) {
      setQuery("");
      setActive(0);
    }
  }

  function onKeyDown(e: React.KeyboardEvent) {
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      const step = e.key === "ArrowDown" ? 1 : -1;
      setActive(flat.length ? (current + step + flat.length) % flat.length : 0);
    } else if (e.key === "Enter" && flat[current]) {
      e.preventDefault();
      flat[current].run();
    }
  }

  return (
    <Dialog.Root open={open} onOpenChange={change}>
      <Dialog.Portal>
        <Dialog.Backdrop className="fixed inset-0 z-30 bg-black/45 transition-opacity duration-200 data-ending-style:opacity-0 data-starting-style:opacity-0" />
        <Dialog.Viewport className="fixed inset-0 z-30 flex justify-center overflow-y-auto px-3 pt-16 md:px-5 md:pt-[90px]">
          <Dialog.Popup className="flex h-fit max-h-full w-full max-w-[560px] flex-col gap-1 overflow-y-auto rounded-card-lg border border-line bg-surface p-2.5 shadow-(--shadow-toast) outline-none transition-[transform,opacity] duration-300 ease-out-soft data-ending-style:-translate-y-2 data-ending-style:opacity-0 data-starting-style:translate-y-2 data-starting-style:opacity-0">
            <Dialog.Title className="sr-only">{t("palette.title")}</Dialog.Title>
            <div className="relative">
              <Icon name="search" className="pointer-events-none absolute top-3.5 left-3 text-muted" />
              <input
                autoFocus
                role="combobox"
                aria-expanded
                aria-controls={listId}
                aria-activedescendant={flat[current] ? `${listId}-${flat[current].id}` : undefined}
                aria-label={t("palette.placeholder")}
                value={query}
                onChange={(e) => {
                  setQuery(e.target.value);
                  setActive(0);
                }}
                onKeyDown={onKeyDown}
                placeholder={t("palette.placeholder")}
                className="h-11 w-full rounded-field border border-line bg-surface pr-3.5 pl-9 text-sm outline-none transition-colors duration-200 focus:border-accent"
              />
            </div>
            <div id={listId} role="listbox" className="flex flex-col">
              {groups.map((g) => (
                <div key={g.title} role="group" aria-label={t(g.title)} className="flex flex-col gap-0.5 pt-2">
                  <div className="px-2.5 py-1 text-[11px] font-bold tracking-[0.1em] text-muted">{t(g.title)}</div>
                  {g.items.map((it) => (
                    <div
                      key={it.id}
                      id={`${listId}-${it.id}`}
                      role="option"
                      aria-selected={flat[current] === it}
                      onClick={it.run}
                      onMouseMove={() => setActive(flat.indexOf(it))}
                      className={cx(
                        "flex h-10 cursor-pointer items-center gap-2.5 rounded-ctl px-2.5",
                        flat[current] === it && "bg-surface-2",
                      )}
                    >
                      <span className="w-6 font-mono text-[11px] font-bold text-muted">{it.tag}</span>
                      <span className="flex-1 truncate text-[13px] font-semibold">{it.label}</span>
                      {it.meta && <span className="text-xs text-muted">{it.meta}</span>}
                    </div>
                  ))}
                </div>
              ))}
              {flat.length === 0 && (
                <div role="status" className="px-2.5 py-6 text-center text-[13px] text-muted">
                  {searching ? t("palette.searching") : t("palette.empty")}
                </div>
              )}
            </div>
          </Dialog.Popup>
        </Dialog.Viewport>
      </Dialog.Portal>
    </Dialog.Root>
  );
}
