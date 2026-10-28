import { Dialog } from "@base-ui/react/dialog";
import { useQuery } from "@tanstack/react-query";
import { useNavigate } from "@tanstack/react-router";
import { useEffect, useId, useMemo, useState } from "react";
import { useAddNode } from "@/components/add-node";
import { Icon } from "@/components/ui/icons";
import { useT } from "@/i18n";
import { en, type MessageKey } from "@/i18n/en";
import { ru } from "@/i18n/ru";
import { zh } from "@/i18n/zh";
import { cx } from "@/lib/cx";
import { useEdition } from "@/lib/edition";
import { useFmt } from "@/lib/format";
import { useNodeStatus } from "@/lib/node-status";
import { nodesQuery, userSearchQuery } from "@/lib/queries";
import { useSignOut } from "@/lib/session";

type Item = { id: string; tag: string; label: string; meta?: string; run: () => void };
type Group = { title: MessageKey; items: Item[] };

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
  { id: "integrations", label: "nav.integrations", words: "集成 接口 令牌 智能体 审批", go: { to: "/integrations" } },
  { id: "settings", label: "nav.settings", words: "设置 配置 选项", go: { to: "/settings" } },
  page("interface", "тема язык акцент цвет бренд логотип название theme language accent colour color brand logo 界面 主题 语言 强调色 颜色 品牌 标志 名称"),
  page("security", "passkey пароль код вход cloudflare turnstile капча 2fa totp password code sign-in captcha 安全 通行密钥 密码 验证码 登录 人机验证 两步验证"),
  page("sessions", "устройства входы devices 会话 登录 设备 活动会话"),
  page("audit", "журнал история log history 审计 日志 记录 历史"),
  page("admins", "помощник роли helper roles 管理员 协作者 角色 权限"),
  page("domains", "адрес ссылки address url links 域名 地址 链接 网址"),
  page("backups", "бэкап бэкапы копия backup copy 备份 恢复 快照"),
  { id: "tokens", label: "int.tok.title", crumb: "nav.integrations", words: "токен токены ключ скрипт token tokens key script api 令牌 密钥 脚本 接口", go: { to: "/integrations" } },
  { id: "mcp", label: "int.mcp.title", crumb: "nav.integrations", words: "агент ии claude agent ai 智能体 人工智能 MCP", go: { to: "/integrations" } },
  { id: "approvals", label: "palette.approvals", crumb: "nav.integrations", words: "одобрить решения approve decisions 审批 批准 拒绝 决定", go: { to: "/integrations" } },
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
  const noToken = useEdition().off("mcp"); // no way to a new token there, so no shortcut to it
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
    // "de1 журнал": a tab of a node, when the query names both
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
              run: go({ to: "/nodes/$id", params: { id: n.id }, search: nodeTab.search, ...("hash" in nodeTab ? { hash: nodeTab.hash } : {}) }),
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
      { id: "new-token", tag: "+", label: t("palette.newToken"), words: "новый токен создать ключ api new token create key 新建 令牌 创建 密钥", run: go({ to: "/integrations", search: { new: "token" } }) },
      { id: "new-profile", tag: "+", label: t("palette.newProfile"), words: "новый профиль создать new profile create 新建 协议配置 创建", run: go({ to: "/profiles/new" }) },
      { id: "create-user", tag: "+", label: t("palette.createUser"), words: "новый пользователь создать добавить друга new user create add friend 新建 用户 创建 添加 朋友", run: go({ to: "/users", search: { create: "true" } }) },
      {
        id: "add-node",
        tag: "+",
        label: t("palette.addNode"),
        words: "добавить ноду новая сервер add node new server 添加 节点 服务器 新增",
        run: () => {
          done();
          addNode();
        },
      },
      {
        id: "sign-out",
        tag: "←",
        label: t("header.signOut"),
        words: "выйти выход logout sign out exit 退出 登出 退出登录",
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
      { title: "palette.actions", items: actions.filter((a) => !(noToken && a.id === "new-token") && (!q || `${a.label} ${a.words}`.toLowerCase().includes(q))) },
    ];
    return all.filter((g) => g.items.length > 0);
  }, [q, asked, nodes.data, users.data, fmt, st, t, navigate, onOpenChange, addNode, signOut, noToken]);

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
