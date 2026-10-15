import { ApiError, type DnsAnswer } from "./api";
import { LINK } from "./logic";
import type { DnsActions, DnsState } from "./state";
import type { MgData } from "./types";

// What the DNS pickers do: post the choice, then change the page's own copy (of the server and the key devices that now hold
// an old DNS, or of the link apps' DNS) and draw again. The target is a server id, or LINK for the apps that use the link. The
// call is injected so a test can use a fake.

export type DnsBody = { server: string; preset: string } | { link: true; preset: string };
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

  /** Saves a choice: the picker closes, the row says "One moment", then what happens next. Without an address nothing is sent. */
  async function save(id: string, preset: string) {
    const endpoint = data.dns?.endpoint ?? "";
    if (!endpoint || choiceOf(id) === undefined || dns().busy) return;
    dns().busy = id;
    dns().error = null;
    dns().open = null;
    render();
    try {
      if (id === LINK) {
        const ans = await api.setDns(endpoint, { link: true, preset });
        const was = data.dns!.link;
        data.dns!.link = ans.link ?? { ...was, choice: preset, effective: preset || was.default };
      } else {
        const ans = await api.setDns(endpoint, { server: id, preset });
        const srv = server(id)!;
        const now = ans.server ?? { ...srv, dns: srv.dns && { ...srv.dns, choice: preset } };
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
      dns().done[id] = true;
      dns().busy = "";
      render();
    } catch (e) {
      if (e instanceof ApiError && e.code === "locked") return locked?.();
      dns().busy = "";
      dns().error = { server: id, code: e instanceof ApiError ? e.code : "failed", retryMin: e instanceof ApiError ? Math.ceil(e.retryAfter / 60) : 0, preset };
      render();
    }
    focus?.(keyOf(id));
  }

  function open(id: string | null) {
    const was = dns().open;
    dns().open = id;
    if (id) {
      dns().pick = choiceOf(id) ?? "";
      if (dns().error?.server === id) dns().error = null;
      delete dns().done[id];
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
    apply() {
      const id = dns().open;
      if (!id) return;
      if (dns().pick === (choiceOf(id) ?? "")) return open(null);
      void save(id, dns().pick);
    },
    retry() {
      const e = dns().error;
      if (e) void save(e.server, e.preset);
    },
  };
}
