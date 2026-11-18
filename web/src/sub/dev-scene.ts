import { dict, isLang } from "./i18n";
import type { LockState } from "./lock";
import { LINK } from "./logic";
import type { Actions, State, Tab } from "./state";
import { pickProxy } from "./telegram";
import type { MgData } from "./types";

// The scenes of the page, dev only (`vite -c vite.sub.config.ts`): main.ts calls devScene inside `if (import.meta.env.DEV)` and the
// import is dynamic there, so a build never sees this file. Every state of the page and every window opens on a page of its own by
// the parameters of the URL, drawn by the real parts. The case (`?case=`, dev-data.ts) is the data, these are the person's state.
//
//   lang=en|ru|zh|fa|tr    the language (before the one remembered on this device)      theme=light|dark, as=<platform>: main.ts
//   tab=servers|telegram|trusttunnel|devices      more=1 the long list of devices unfolded      proxy=<n> the n-th proxy of the Telegram and TrustTunnel panels
//   sheet=pick|link|conn|key|full      the window "New device" at that step (full: no place left)       sheet=made  a key made in the window ("ready")
//   key=<device id>[&country=<n>]      the key of a device (the n-th config)        renew=<device id>    the new key of a stale device
//   menu=<id>   the menu of a device       rename=<id>       ask=remove:<id>|rotate:<id>       stale=<id>  the question of a stale key
//   dns=<server id>|link  [&dnsall=0|1 the scope]  [&dnsdone=1 the result  [&dnsskip=<server ids> the servers that did not offer it]]
//   (the password form: lockerr=wrong|short|latin|later|net, see devLockScene)

const tabs: readonly string[] = ["servers", "telegram", "trusttunnel", "devices"];

/** Puts the page in the state the parameters of the URL ask for. `a` is the page's own actions (the calls they make answer from dev-api.ts). */
export function devScene(query: URLSearchParams, data: MgData, st: State, a: Actions): void {
  const q = (k: string) => query.get(k);
  const lang = q("lang");
  if (isLang(lang)) st.lang = lang;
  const tab = q("tab");
  if (tab && tabs.includes(tab)) st.tab = tab as Tab;
  if (q("more") === "1") st.devicesOpen = true;
  const proxy = Number(q("proxy"));
  if (proxy > 0) for (const k of ["tg", "tt"] as const) pickProxy(k, proxy);

  const sheet = q("sheet");
  if (sheet === "pick" || sheet === "link" || sheet === "conn" || sheet === "key") {
    st.amz.adding = true;
    st.amz.pane = sheet;
  } else if (sheet === "full") {
    st.amz.adding = true;
    st.amz.pane = "pick";
    st.amz.error = "device_limit";
    st.amz.errorAt = "add";
  } else if (sheet === "made") {
    // the real way: the form's defaults, "Create" (the call answers from dev-api.ts), the window turns into the new key
    a.amz.add(true, "key");
    a.amz.create();
  }

  st.amz.menu = q("menu");
  const key = q("key");
  if (key) {
    st.amz.node[key] = Number(q("country")) || 0;
    a.amz.show(key);
  }
  const renew = q("renew");
  if (renew) a.amz.renew(renew);
  const rename = q("rename");
  const label = data.amnezia?.devices.find((x) => x.id === rename)?.label ?? data.devices.find((x) => x.id === rename)?.model;
  if (rename && label !== undefined) st.amz.rename = { id: rename, value: label };
  const [kind, id] = (q("ask") ?? "").split(":");
  if ((kind === "remove" || kind === "rotate") && id) st.amz.confirm = { id, kind };
  st.amz.stale = q("stale");

  const dns = q("dns");
  if (dns) {
    a.dns.open(dns === "link" ? LINK : dns);
    if (q("dnsall") === "0" || q("dnsall") === "1") a.dns.scope(q("dnsall") === "1");
    if (q("dnsdone") === "1") st.dns.done[st.dns.open ?? ""] = true;
    st.dns.skipped = (q("dnsskip") ?? "").split(",").filter(Boolean);
  }
}

/** The password form with its error on show: `lockerr=wrong` (the password is not right, two tries left), `short` (three characters typed), `latin` (a letter of another keyboard), `later` (too many tries), `net`. */
export function devLockScene(query: URLSearchParams, st: LockState): void {
  const lang = query.get("lang");
  if (isLang(lang)) st.lang = lang;
  const t = dict[st.lang];
  switch (query.get("lockerr")) {
    case "wrong":
      st.pw = "abcd-9999";
      st.error = t.dgLockWrong(2);
      break;
    case "short":
      st.pw = "abc";
      st.error = t.lockShort(3);
      break;
    case "latin":
      st.pw = "abcd-";
      st.note = t.lockLatin;
      break;
    case "later":
      st.pw = "abcd-9999";
      st.error = t.lockLater(10);
      break;
    case "net":
      st.pw = "abcd-2345";
      st.error = t.lockNet;
      break;
  }
}
