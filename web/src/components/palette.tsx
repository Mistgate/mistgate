import { Dialog } from "@base-ui/react/dialog";
import { useQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useEffect, useId, useMemo, useRef, useState, type ReactNode } from "react";
import { useAddNode } from "@/components/add-node";
import { sections } from "@/components/nav";
import { originOf, playWave } from "@/components/fx/wave";
import { Flag } from "@/components/ui/flag";
import { Icon, NavIcon, navPaths, type NavIconName } from "@/components/ui/icons";
import { StatusDot, type StatusKind } from "@/components/ui/status";
import { Glyph, glyphs } from "@/components/windows";
import { useT } from "@/i18n";
import { en, type MessageKey } from "@/i18n/en";
import { ru } from "@/i18n/ru";
import { zh } from "@/i18n/zh";
import { cx } from "@/lib/cx";
import { useEdition } from "@/lib/edition";
import { useFmt } from "@/lib/format";
import { nodeKind, useNodeStatus } from "@/lib/node-status";
import { nodesQuery, userSearchQuery } from "@/lib/queries";
import { useSignOut } from "@/lib/session";
import { setTheme, themeNow } from "@/lib/theme";
import "./windows.css";

/** What a row shows in its first box: a flag, a person's initial, a section's own icon, or a glyph of an action. */
type Lead = { kind: "flag"; code: string } | { kind: "ava"; letter: string; color: string } | { kind: "nav"; icon: NavIconName } | { kind: "act"; glyph: ReactNode };
type Item = {
  id: string;
  lead: Lead;
  title: string;
  /** A name of a node or a person is set in mono. */
  mono?: boolean;
  sub?: string;
  status?: { text: string; kind: StatusKind | "plain" };
  run: () => void;
};
type Group = { title: MessageKey; items: Item[]; /** All that matched, before the list was cut to what fits. */ total: number };

// The round avatars of people: the board's pastels in turn (the same cycle as the people's list).
const avatarColors = ["#c9b8e8", "#8ec8ea", "#dcc68c", "#9fd6cf", "#e8b98f", "#e6a6b0", "#b0cc90"];
const navOf = (id: string): NavIconName => {
  const section = id.split("-")[0]!;
  if (section in navPaths) return section as NavIconName;
  return section === "tokens" || section === "mcp" || section === "approvals" ? "ai" : "settings";
};

/** Where a place leads: a route of the router, with its params, search and hash. */
type Target = { to: string; params?: Record<string, string>; search?: Record<string, string>; hash?: string };
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
  { id: "overview", label: "nav.overview", words: "главная home dashboard 首页 总览 仪表盘", go: { to: "/" } },
  { id: "nodes", label: "nav.nodes", words: "серверы флот servers fleet 节点 服务器 集群 主机", go: { to: "/nodes" } },
  { id: "users", label: "nav.users", words: "люди друзья группы people friends groups 用户 人员 朋友 分组 群组", go: { to: "/users" } },
  { id: "profiles", label: "nav.profiles", words: "протоколы protocols 协议 协议配置 配置 入站", go: { to: "/profiles" } },
  { id: "subscriptions", label: "nav.subscriptions", words: "ссылки links 订阅 链接 订阅链接", go: { to: "/subscriptions" } },
  { id: "health", label: "nav.health", words: "健康 状态 监控 告警 检测", go: { to: "/health" } },
  {
    id: "reachability",
    label: "nav.reachability",
    words: "россия ркн тспу блокировки списки реестр globalping зонд russia rkn tspu blocks lists registry probe 俄罗斯 封锁 名单 探测 访问",
    go: { to: "/reachability" },
  },
  { id: "updates", label: "nav.updates", words: "раскатка версии rollout versions 更新 升级 版本 滚动更新 发布", go: { to: "/updates" } },
  { id: "ai", label: "nav.ai", words: "ии ассистент память чат ai assistant memory chat 助手 人工智能 记忆", go: { to: "/ai" } },
  { id: "integrations", label: "nav.integrations", words: "телеграм оповещения telegram alerts 集成 提醒", go: { to: "/integrations" } },
  { id: "settings", label: "nav.settings", words: "设置 配置 选项", go: { to: "/settings" } },
  page("interface", "тема язык акцент цвет бренд логотип название theme language accent colour color brand logo 界面 主题 语言 强调色 颜色 品牌 标志 名称"),
  page("security", "passkey пароль код вход cloudflare turnstile капча 2fa totp password code sign-in captcha 安全 通行密钥 密码 验证码 登录 人机验证 两步验证"),
  page("sessions", "устройства входы devices 会话 登录 设备 活动会话"),
  page("audit", "журнал история log history 审计 日志 记录 历史"),
  page("admins", "помощник роли helper roles 管理员 协作者 角色 权限"),
  page("domains", "адрес ссылки address url links 域名 地址 链接 网址"),
  page("backups", "бэкап бэкапы копия backup copy 备份 恢复 快照"),
  { id: "tokens", label: "int.tok.title", crumb: "nav.ai", words: "токен токены ключ скрипт token tokens key script api 令牌 密钥 脚本 接口", go: { to: "/ai", search: { tab: "mcp" } } },
  { id: "mcp", label: "int.mcp.title", crumb: "nav.ai", words: "агент ии claude agent ai 智能体 人工智能 MCP", go: { to: "/ai", search: { tab: "mcp" } } },
  { id: "approvals", label: "palette.approvals", crumb: "nav.ai", words: "одобрить решения approve decisions 审批 批准 拒绝 决定", go: { to: "/ai", search: { tab: "approvals" } } },
  tab("subscriptions", "formats", "subs.tab.formats", "приложения форматы mihomo happ apps formats clients 应用 格式 客户端", true),
  tab("subscriptions", "rules", "subs.tab.rules", "правила user-agent rules 规则 下发规则", false),
  tab("subscriptions", "texts", "subs.tab.texts", "тексты названия texts names 文本 名称 显示名称", false),
  tab("subscriptions", "page", "subs.tab.page", "страница друга page 页面 用户页面 订阅页面", false),
  tab("subscriptions", "dns", "subs.tab.dns", "днс пресеты presets 预设 解析 分流", false),
  tab("health", "alerts", "hl.tab.alerts", "алерты проблемы alerts problems 告警 警报 问题", true),
  tab("health", "checks", "hl.tab.checks", "проверки checks 检测 检查 探测", false),
  tab("health", "doctor", "hl.tab.doctor", "доктор doctor 诊断 医生", false),
];

/**
 * The node page's tabs and History's parts, each with the words that find it next to a node's name ("de1 журнал"), and
 * where it opens. The people on the node live on its Overview.
 */
const nodeTabs = [
  { id: "people", label: "node.onlineHere", words: "пользователи люди онлайн users people online 用户 在线", search: {}, hash: "people" },
  {
    id: "ways",
    label: "node.tab.ways",
    words: "входы вход ретрансляция ретрансляции обход обойти мост маршрут ways way in entry relay relays bypass bridge route 入口 中转 绕过 线路",
    search: { tab: "ways" },
  },
  { id: "ports", label: "ports.title", words: "порты порт ports port 端口", search: { tab: "ways", way: "direct" } },
  { id: "profiles", label: "node.tab.profiles", words: "профили profiles 协议配置 协议", search: { tab: "profiles" } },
  { id: "exit", label: "node.tab.exit", words: "выход warp ipv6 dns днс торрент exit egress torrent 出口 种子", search: { tab: "exit" } },
  { id: "history", label: "node.tab.history", words: "история события history events 历史 事件", search: { tab: "history" } },
  { id: "journal", label: "node.tab.journal", words: "журнал journal 运行记录", search: { tab: "history", view: "journal" } },
  { id: "logs", label: "node.tab.logs", words: "логи logs log 日志", search: { tab: "history", view: "logs" } },
  { id: "doctor", label: "node.tab.doctor", words: "доктор doctor 诊断", search: { tab: "doctor" } },
  {
    id: "server",
    label: "node.tab.server",
    words: "сервер настройки ssh пароль ширма агент хост канал settings server password decoy agent host capacity 服务器 设置 密码 伪装",
    search: { tab: "server" },
  },
] as const satisfies readonly { id: string; label: MessageKey; words: string; search: Record<string, string>; hash?: string }[];

/** Every word of the query is found in the place's names (every language) or its extra words. */
export function placeMatches(p: Pick<Place, "label" | "crumb" | "words">, q: string): boolean {
  const hay = [en[p.label], ru[p.label], zh[p.label], p.crumb ? en[p.crumb] : "", p.crumb ? ru[p.crumb] : "", p.crumb ? zh[p.crumb] : "", p.words].join(" ").toLowerCase();
  return q.split(/\s+/).every((w) => hay.includes(w));
}

/** The group a section sits in under the menu's labels ("Setup", "Operations", "System"), by section id. */
const groupOf: Record<string, MessageKey | undefined> = (() => {
  const map: Record<string, MessageKey | undefined> = {};
  let current: MessageKey | undefined;
  for (const s of sections) {
    if ("group" in s) current = s.group;
    map[s.id] = current;
  }
  return map;
})();

/** The window's own look (the board's PnSearch, .sr): the keycap. */
const kbd = "rounded-chip bg-surface-2 px-1.5 py-0.5 font-mono text-[11px] leading-4 text-sec";

/** Rows that match, cut to what the list shows: five when something is typed, four for the first look. */
const rowsShown = (typed: boolean) => (typed ? 5 : 4);

/**
 * The search window (Ctrl K, the board's PnSearch). Groups Nodes / People / Sections / Actions; type to filter, the arrows
 * and Enter to run. Without a query the first look is the actions and the nodes. Nodes come from the list the shell already
 * polls; people are searched on the server (a small page) once something is typed; sections and actions are found by their
 * names and a few words in Russian and English. Loaded lazily by the shell on first open.
 */
export default function Palette({ open, onOpenChange, initialQuery = "" }: { open: boolean; onOpenChange: (open: boolean) => void; /** A word the window opens with (the dev kit looks at each state of the search with it). */ initialQuery?: string }) {
  const t = useT();
  const fmt = useFmt();
  const st = useNodeStatus();
  const navigate = useNavigate();
  const addNode = useAddNode();
  const noToken = useEdition().off("mcp"); // no way to a new token there, so no shortcut to it
  const signOut = useSignOut();
  const listId = useId();
  const list = useRef<HTMLDivElement>(null);
  const [query, setQuery] = useState(initialQuery);
  const [active, setActive] = useState(0);

  const q = query.trim().toLowerCase();
  // the user search waits for a pause in typing: one request per word, not per key
  const [asked, setAsked] = useState(initialQuery.trim().toLowerCase());
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
    const matching = nodeList.filter((n) => !q || [n.name, n.countryCode, fmt.country(n.countryCode), n.location, n.provider, n.address].join(" ").toLowerCase().includes(q));
    const nodeItems: Item[] = matching.map((n) => {
      const kind = nodeKind(n);
      return {
        id: `node-${n.id}`,
        lead: { kind: "flag", code: n.countryCode || "" },
        title: n.name,
        mono: true,
        sub: n.location || fmt.country(n.countryCode),
        status: { text: st.word(n), kind },
        run: go({ to: "/nodes/$id", params: { id: n.id } }),
      };
    });
    // "de1 журнал": a tab of a node, when the query names both; a ways word alone ("обход", "relay") offers the ways in of
    // the first nodes, the other tabs need the node's name (else "settings" would bury the Settings section)
    const words = q.split(/\s+/).filter(Boolean);
    const ways = nodeTabs.find((x) => x.id === "ways")!;
    const waysAlone = words.length === 1 && words[0]!.length >= 3 && placeMatches({ label: ways.label, words: ways.words }, words[0]!);
    const nodeTab = words.length > 1 ? nodeTabs.find((x) => words.some((w) => placeMatches({ label: x.label, words: x.words }, w))) : waysAlone ? ways : undefined;
    const rest = nodeTab ? words.filter((w) => !placeMatches({ label: nodeTab.label, words: nodeTab.words }, w)) : [];
    const tabItems: Item[] =
      nodeTab && (rest.length > 0 || waysAlone)
        ? nodeList
            .filter((n) => rest.every((w) => n.name.toLowerCase().includes(w)))
            .slice(0, 4)
            .map((n) => ({
              id: `node-${n.id}-${nodeTab.id}`,
              lead: { kind: "flag", code: n.countryCode || "" },
              title: `${n.name} · ${t(nodeTab.label)}`,
              sub: t("nav.nodes"),
              run: go({ to: "/nodes/$id", params: { id: n.id }, search: nodeTab.search, ...("hash" in nodeTab ? { hash: nodeTab.hash } : {}) }),
            }))
        : [];
    // only users the server matched for exactly what is typed (the answer for an older word must not show)
    const userItems: Item[] =
      asked !== "" && asked === q
        ? (users.data?.users ?? []).map((u, i) => ({
            id: `user-${u.id}`,
            lead: { kind: "ava", letter: u.name.charAt(0).toUpperCase() || "?", color: avatarColors[i % avatarColors.length]! },
            title: u.name,
            sub: u.currentNodeName || u.groupName,
            status: u.online ? { text: t("win.search.online"), kind: "plain" } : undefined,
            run: go({ to: "/users/$id", params: { id: u.id } }),
          }))
        : [];
    const placeItems: Item[] = q
      ? places
          .filter((p) => placeMatches(p, q))
          .map((p) => ({
            id: `place-${p.id}`,
            lead: { kind: "nav", icon: navOf(p.id) },
            title: t(p.label),
            sub: p.crumb ? t(p.crumb) : groupOf[p.id] ? t(groupOf[p.id]!) : undefined,
            run: go(p.go),
          }))
      : [];
    const light = themeNow() === "light";
    const act = (id: string, label: MessageKey, keys: string, glyph: ReactNode, run: () => void): Item & { words: string } => ({
      id,
      lead: { kind: "act", glyph },
      title: t(label),
      words: keys,
      run,
    });
    const plus = <Glyph d={glyphs.plus} />;
    const actions: (Item & { words: string })[] = [
      act("add-node", "win.search.addSsh", "добавить ноду новая сервер ssh add node new server 添加 节点 服务器 新增", plus, () => {
        done();
        addNode(undefined, "ssh");
      }),
      act("add-node-cmd", "win.search.addCmd", "добавить ноду сервер команда add node server command 添加 节点 命令", plus, () => {
        done();
        addNode(undefined, "cmd");
      }),
      act("create-user", "palette.createUser", "новый пользователь создать добавить друга new user create add friend 新建 用户 创建 添加 朋友", plus, go({ to: "/users", search: { create: "true" } })),
      act(
        "theme",
        light ? "win.search.themeDark" : "win.search.themeLight",
        "тема светлая тёмная theme light dark 主题 浅色 深色",
        <Icon name={light ? "moon" : "sun"} />,
        () => {
          done();
          playWave(originOf(), { light: !light, theme: true });
          setTheme(light ? "dark" : "light");
        },
      ),
      act("new-profile", "palette.newProfile", "новый профиль создать new profile create 新建 协议配置 创建", plus, go({ to: "/profiles/new" })),
      act("new-token", "palette.newToken", "новый токен создать ключ api new token create key 新建 令牌 创建 密钥", plus, go({ to: "/ai", search: { tab: "mcp", new: "token" } })),
      act("sign-out", "header.signOut", "выйти выход logout sign out exit 退出 登出 退出登录", <Icon name="back" />, () => {
        done();
        void signOut();
      }),
    ];
    const actionItems = actions.filter((a) => !(noToken && a.id === "new-token") && (!q || `${a.title} ${a.words}`.toLowerCase().includes(q)));
    const cut = rowsShown(q !== "");
    const group = (title: MessageKey, all: Item[]): Group => ({ title, items: all.slice(0, cut), total: all.length });
    // the first look is the actions and the nodes; with a query, everything that matched
    const all: Group[] = q
      ? [
          group("palette.nodes", [...nodeItems, ...tabItems]),
          group("palette.users", userItems),
          group("palette.places", placeItems),
          group("palette.actions", actionItems),
        ]
      : [group("palette.actions", actionItems), group("palette.nodes", [...nodeItems, ...tabItems])];
    return all.filter((g) => g.items.length > 0);
  }, [q, asked, nodes.data, users.data, fmt, st, t, navigate, onOpenChange, addNode, signOut, noToken]);

  const flat = groups.flatMap((g) => g.items);
  const current = Math.max(0, Math.min(active, flat.length - 1));
  const total = groups.reduce((a, g) => a + g.total, 0);

  // the row the arrows moved to stays in sight
  useEffect(() => {
    const id = flat[current]?.id;
    if (id) list.current?.querySelector(`[id="${listId}-${id}"]`)?.scrollIntoView?.({ block: "nearest" });
  }, [current, flat, listId]);

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
      setActive(Math.max(0, Math.min(flat.length - 1, current + (e.key === "ArrowDown" ? 1 : -1))));
    } else if (e.key === "Enter" && flat[current]) {
      e.preventDefault();
      flat[current].run();
    }
  }

  return (
    <Dialog.Root open={open} onOpenChange={change}>
      <Dialog.Portal>
        <Dialog.Backdrop className="fixed inset-0 z-50 animate-[wn-fade_0.25s_var(--e)_backwards] bg-(--scrim) transition-opacity duration-200 data-ending-style:opacity-0" />
        <Dialog.Viewport className="fixed inset-0 z-50 flex justify-center overflow-y-auto px-6 pt-[72px] pb-6 max-[760px]:px-3 max-[760px]:pb-3 max-[520px]:pt-3">
          <Dialog.Popup className="flex h-fit max-h-full w-full max-w-[640px] animate-[wn-dlg_0.25s_var(--e)_backwards] flex-col overflow-hidden rounded-card border border-line bg-surface text-start shadow-(--shadow-dlg) outline-none transition-[opacity,transform] duration-200 ease-out data-ending-style:translate-y-2 data-ending-style:opacity-0">
            <Dialog.Title className="sr-only">{t("palette.title")}</Dialog.Title>
            <label className="flex h-14 items-center gap-3 border-b border-line ps-5 pe-3 text-muted">
              <Glyph d={glyphs.search} size={18} />
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
                autoComplete="off"
                spellCheck={false}
                className="h-full min-w-0 flex-1 border-0 bg-transparent text-base text-fg outline-none placeholder:text-muted"
              />
              <button type="button" onClick={() => change(false)} className={cx(kbd, "cursor-pointer px-2 py-1 transition-colors hover:text-fg")}>
                Esc
              </button>
            </label>

            <div ref={list} id={listId} role="listbox" aria-label={t("palette.title")} className="wn-scroll flex max-h-[416px] min-h-[240px] flex-col gap-1 overflow-y-auto p-2">
              {groups.map((g) => (
                <div key={g.title} role="group" aria-label={t(g.title)}>
                  <div className="flex h-7 items-center gap-2 px-3 text-xs text-muted">
                    <span>{t(g.title)}</span>
                    {q && <span className="font-mono text-[11px] font-normal tabular-nums">{g.items.length}</span>}
                  </div>
                  {g.items.map((it) => (
                    <SearchRow key={it.id} item={it} id={`${listId}-${it.id}`} on={flat[current] === it} q={q} onHover={() => setActive(flat.indexOf(it))} />
                  ))}
                </div>
              ))}
              {flat.length === 0 && (
                <div role="status" className="flex flex-1 animate-[mg-up_0.25s_var(--e)_both] flex-col items-center justify-center gap-1 p-8 text-center text-[13px] text-muted">
                  <b className="text-base leading-6 font-medium text-fg">{searching ? t("palette.searching") : t("palette.empty")}</b>
                  {!searching && <span>{t("win.search.emptyHint")}</span>}
                </div>
              )}
            </div>

            <div className="flex h-10 items-center gap-4 border-t border-line px-5 text-xs text-muted max-[520px]:hidden">
              <span className="inline-flex items-center gap-1.5">
                <span className={kbd}>↑</span>
                <span className={kbd}>↓</span>
                {t("win.search.pick")}
              </span>
              <span className="inline-flex items-center gap-1.5">
                <span className={kbd}>Enter</span>
                {t("win.search.open")}
              </span>
              <span className="ms-auto font-mono text-[11px] font-normal tabular-nums">{q && flat.length ? t("win.search.count", { n: flat.length, total }) : ""}</span>
            </div>
          </Dialog.Popup>
        </Dialog.Viewport>
      </Dialog.Portal>
    </Dialog.Root>
  );
}

/** One result (.sr-r): a 32 box with what it is, the name, where it is, the state, and the key that opens it on the chosen row. */
function SearchRow({ item, id, on, q, onHover }: { item: Item; id: string; on: boolean; q: string; onHover: () => void }) {
  const at = q ? item.title.toLowerCase().indexOf(q) : -1;
  const lead = item.lead;
  return (
    <div
      id={id}
      role="option"
      aria-selected={on}
      onClick={item.run}
      onMouseMove={onHover}
      className={cx(
        "grid h-12 w-full cursor-pointer grid-cols-[32px_auto_minmax(0,1fr)_auto_auto] items-center gap-3 rounded-field ps-2 pe-3 text-start transition-colors duration-[120ms] ease-(--e) max-[520px]:grid-cols-[32px_auto_minmax(0,1fr)_auto]",
        on && "bg-step",
      )}
    >
      <span
        aria-hidden
        className={cx(
          "grid size-8 place-items-center text-[13px] font-medium",
          lead.kind === "flag" ? "" : lead.kind === "ava" ? "rounded-full text-[#17171a]" : "rounded-ctl",
          lead.kind === "nav" && "text-(--tc)",
          lead.kind === "act" && "bg-ok-soft text-accent-text",
          lead.kind === "nav" && "bg-[color-mix(in_srgb,var(--tc)_16%,var(--surface))]",
        )}
        style={lead.kind === "ava" ? { background: lead.color } : lead.kind === "nav" ? ({ ["--tc" as string]: navPaths[lead.icon][1] === "muted" ? "var(--muted)" : `var(--tone-${navPaths[lead.icon][1]})` } as React.CSSProperties) : undefined}
      >
        {lead.kind === "flag" && <Flag code={lead.code} size={16} />}
        {lead.kind === "ava" && lead.letter}
        {lead.kind === "nav" && <NavIcon name={lead.icon} />}
        {lead.kind === "act" && lead.glyph}
      </span>
      <span className={cx("font-medium whitespace-nowrap", item.mono && "font-mono tabular-nums")}>
        {at >= 0 ? (
          <>
            {item.title.slice(0, at)}
            <mark className="bg-transparent font-[inherit] text-accent-text">{item.title.slice(at, at + q.length)}</mark>
            {item.title.slice(at + q.length)}
          </>
        ) : (
          item.title
        )}
      </span>
      <span className="min-w-0 truncate text-[13px] text-muted">{item.sub}</span>
      <span
        className={cx(
          "inline-flex items-center gap-2 text-[13px] whitespace-nowrap",
          !item.status && "hidden",
          item.status?.kind === "warn" ? "text-warn-text" : item.status?.kind === "bad" ? "text-danger-text" : item.status?.kind === "busy" ? "text-tone-sky" : item.status?.kind === "off" || item.status?.kind === "blip" ? "text-muted" : "text-sec",
        )}
      >
        {item.status && item.status.kind !== "plain" && <StatusDot kind={item.status.kind} />}
        {item.status?.text}
      </span>
      <span aria-hidden className={cx(kbd, "transition-opacity duration-[120ms] ease-(--e) max-[520px]:hidden", on ? "opacity-100" : "opacity-0")}>
        Enter
      </span>
    </div>
  );
}
