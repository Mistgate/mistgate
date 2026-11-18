import { ApiError, type DnsAnswer } from "./api";
import { dnsDefault, LINK, serversOf, showsKeyDns } from "./logic";
import type { DnsActions, DnsState } from "./state";
import type { MgData, ServerEntry } from "./types";

// What the DNS window does: post the choice, then change the page's own copy (of the server and the key devices that now hold an
// old DNS, or of the link apps' DNS) and draw again. The window stays open: it turns into "DNS changed" (done) and is closed by
// the person. The target is a server id, or LINK for the apps that use the link; for the keys the choice may be made for all
// servers that have a DNS of their keys at once. The call is injected so a test can use a fake.

export type DnsBody = { server: string; preset: string } | { link: true; preset: string };
/** The servers a choice made "for all" reaches: the working ones that have a DNS of their keys, the open one (`id`) among them, first or not. */
export function keyDnsServers(data: MgData, id: string): string[] {
  const ids = serversOf(data).filter((x) => x.id !== "" && x.online && showsKeyDns(data, x)).map((x) => x.id);
  return ids.includes(id) ? ids : [id, ...ids];
}

export type DnsApi = { setDns(endpoint: string, body: DnsBody): Promise<DnsAnswer> };

type Deps = {
  data: MgData;
  st: { dns: DnsState };
  render: () => void;
  api: DnsApi;
  /** Puts the keyboard on the control with this data-k (after a draw). */
  focus?: (key: string) => void;
  locked?: () => void;
};

export function dnsActions({ data, st, render, api, focus, locked }: Deps): DnsActions {
  const dns = () => st.dns;
  const server = (id: string) => data.servers.find((x) => x.id === id);
  /** What is picked now at a target ("" = its default); undefined for a target the page does not have. */
  const choiceOf = (id: string): string | undefined => (id === LINK ? data.dns?.link.choice : server(id)?.dns?.choice);
  const keyOf = (id: string) => (id === LINK ? "dns-link" : `dns-${id}`);
  const everyone = (id: string) => keyDnsServers(data, id);
  /** The server offers this preset ("" = its own default: always). */
  const offers = (x: ServerEntry, preset: string) => preset === "" || !!x.dns?.options.includes(preset) || (!!x.dns && dnsDefault(x.dns) === preset);

  /** The choice of one target is posted and the page's copy changes with the answer. */
  async function post(endpoint: string, id: string, preset: string) {
    if (id === LINK) {
      const ans = await api.setDns(endpoint, { link: true, preset });
      const was = data.dns!.link;
      data.dns!.link = ans.link ?? { ...was, choice: preset, effective: preset || was.default };
      return;
    }
    const ans = await api.setDns(endpoint, { server: id, preset });
    const srv = server(id)!;
    const now = ans.server ?? { ...srv, dns: srv.dns && { ...srv.dns, choice: preset, effective: preset || dnsDefault(srv.dns) } };
    const i = data.servers.findIndex((x) => x.id === id);
    const kept = now.dns;
    if (kept) kept.keys_to_refresh = [...new Set([...kept.keys_to_refresh, ...ans.stale_devices])];
    if (i >= 0) data.servers[i] = now;
    // a key that holds the old DNS needs a new one (the key itself stays): the "new key needed" mark, for this reason
    for (const x of data.amnezia?.devices ?? []) {
      if (ans.stale_devices.includes(x.id) && !x.stale) {
        x.stale = true;
        x.stale_reason = "dns";
      }
    }
  }

  /**
   * Saves the choice of the open window: for the open target, or (scope "all") for every server that has a DNS of its keys and
   * offers the preset, one after the other; the ones that do not offer it stay as they were. The window then says what happens
   * next. Without an address nothing is sent. A failure stops the rest and keeps the preset for "Retry".
   */
  async function save(preset: string) {
    const id = dns().open;
    const endpoint = data.dns?.endpoint ?? "";
    if (!id || !endpoint || choiceOf(id) === undefined || dns().busy) return;
    const all = id !== LINK && dns().all;
    const targets = all ? everyone(id) : [id];
    const skipped = all ? targets.filter((x) => !offers(server(x)!, preset)) : [];
    // a target that has it already is not asked again (a retry goes on where the failure was)
    const todo = targets.filter((x) => !skipped.includes(x) && choiceOf(x) !== preset);
    if (todo.length === 0) return open(null);
    dns().busy = id;
    dns().error = null;
    dns().skipped = skipped;
    render();
    let at = id;
    try {
      for (const x of todo) {
        at = x;
        await post(endpoint, x, preset);
      }
      dns().done[id] = true;
      dns().busy = "";
      render();
    } catch (e) {
      if (e instanceof ApiError && e.code === "locked") return locked?.();
      dns().busy = "";
      dns().error = { server: at, code: e instanceof ApiError ? e.code : "failed", retryMin: e instanceof ApiError ? Math.ceil(e.retryAfter / 60) : 0, preset };
      render();
    }
  }

  function open(id: string | null) {
    const was = dns().open;
    dns().open = id;
    dns().done = {};
    dns().skipped = [];
    dns().error = null;
    if (id) {
      dns().pick = choiceOf(id) ?? "";
      // a person with several servers that have a DNS of their keys starts on "all servers" (the board's first chip)
      dns().all = id !== LINK && everyone(id).length > 1;
    }
    render();
    if (!id && was) focus?.(keyOf(was));
  }

  return {
    open,
    pick(preset) {
      dns().pick = preset;
      render();
    },
    scope(all) {
      dns().all = all;
      render();
    },
    apply() {
      if (dns().open) void save(dns().pick);
    },
    retry() {
      const e = dns().error;
      if (e) void save(e.preset);
    },
  };
}
