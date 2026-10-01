import type { AmzActions, AmzState } from "./amnezia";
import { ApiError, type Answer } from "./api";
import { dict } from "./i18n";
import { defaultLabel, mainProfile } from "./logic";
import type { AwgDevice, Lang, MgData } from "./types";

// What the buttons of the AmneziaVPN way do: call the panel, then change the page's own copy of the data (the device
// list, the count) and the section's state, and draw again. The calls are injected so a test can use a fake.

export type Api = {
  addDevice(endpoints: string, body: { profile_id: string; platform: string; label: string }): Promise<Answer>;
  getConfigs(endpoints: string, id: string): Promise<Answer>;
  rotateKey(endpoints: string, id: string): Promise<Answer>;
  revokeDevice(endpoints: string, id: string): Promise<void>;
};

type Deps = {
  data: MgData;
  st: { lang: Lang; amz: AmzState };
  render: () => void;
  api: Api;
  /** Scrolls the element with this id into view (after a draw). */
  reveal: (id: string) => void;
  /** Puts the keyboard on the control with this data-k (after a draw). */
  focus?: (key: string) => void;
  /** The server wants the page password (the cookie is gone or the link was renewed): the page reloads into the lock. */
  locked?: () => void;
};

export function amzActions({ data, st, render, api, reveal, focus, locked }: Deps): AmzActions {
  const am = () => st.amz;
  const endpoints = () => data.amnezia?.endpoints ?? "";
  const find = (id: string) => data.amnezia?.devices.find((x) => x.id === id);

  const fail = (busy: string, e: unknown) => {
    if (e instanceof ApiError && e.code === "locked") return locked?.();
    am().busy = "";
    am().error = e instanceof ApiError ? e.code : "failed";
    am().errorAt = busy;
    am().retryMin = e instanceof ApiError ? Math.ceil(e.retryAfter / 60) : 0;
    render();
  };
  /**
   * Runs one call for a device; one at a time (a second click while it is in flight does nothing). Without an address
   * (self-service off, the admin's preview) nothing is sent at all: a call to "" would hit the decoy and count as a guess.
   */
  async function run<T>(busy: string, call: () => Promise<T>, done: (r: T) => void): Promise<boolean> {
    if (am().busy || !endpoints()) return false;
    am().busy = busy;
    am().error = "";
    am().errorAt = "";
    render();
    try {
      done(await call());
      am().busy = "";
      render();
      return true;
    } catch (e) {
      fail(busy, e);
      return false;
    }
  }
  /** The device the server describes replaces the page's copy of it. */
  const merge = (id: string, fresh?: AwgDevice) => {
    const list = data.amnezia?.devices;
    const i = list?.findIndex((x) => x.id === id) ?? -1;
    if (list && i >= 0 && fresh) list[i] = fresh;
  };
  /** Configs of a device came: the panel counts them as delivered, so the device is no longer stale. */
  const received = (id: string, r: Answer) => {
    am().configs[id] = r.configs;
    const x = find(id);
    if (x) x.stale = false;
    merge(id, r.device && { ...r.device, stale: false });
  };

  return {
    // the dialog: it shows the form, after "Create" the new device's key, or the new key of a stale device; closing it
    // leaves the device in the list and scrolls to it
    add(open) {
      const was = am().created ?? am().renew;
      am().adding = open;
      am().created = null;
      am().renew = null;
      am().error = "";
      am().errorAt = "";
      const profiles = data.amnezia?.profiles ?? [];
      if (open && !profiles.some((p) => p.id === am().form.profile)) am().form.profile = mainProfile(profiles);
      render();
      if (!open && was) reveal(`amz-row-${was}`);
    },
    form(patch) {
      Object.assign(am().form, patch); // no draw: it would drop what is being typed
    },
    create() {
      const f = am().form;
      if (!f.profile) return;
      // a device nobody named gets the platform's word ("iPhone", "iPhone 2"), not the server's "ios 1"
      const name = f.label.trim() || defaultLabel(f.platform, data.amnezia?.devices.map((x) => x.label) ?? [], dict[st.lang]);
      void run(
        "add",
        () => api.addDevice(endpoints(), { profile_id: f.profile, platform: f.platform, label: name }),
        (r) => {
          if (!r.device || !data.amnezia) throw new ApiError(0, "failed");
          data.amnezia.devices.push(r.device);
          data.user.devices_used += 1;
          am().configs[r.device.id] = r.configs;
          am().created = r.device.id; // the dialog turns into the key of this device
          f.label = "";
        },
      );
    },
    show(id) {
      am().error = "";
      am().errorAt = "";
      am().confirm = null;
      am().open = id;
      if (id === null || am().configs[id]) return render();
      void run(id, () => api.getConfigs(endpoints(), id), (r) => received(id, r)).then(() => {
        if (!am().configs[id]) am().open = null; // the call failed: nothing to show
        render();
      });
    },
    renew(id) {
      const open = () => {
        am().renew = id;
        am().adding = false;
        render();
      };
      if (am().configs[id] && !find(id)?.stale) return open();
      void run(`renew:${id}`, () => api.getConfigs(endpoints(), id), (r) => received(id, r)).then((ok) => ok && open());
    },
    node(id, i) {
      am().node[id] = i;
      render();
    },
    ask(c) {
      const was = am().confirm;
      am().confirm = c;
      render();
      // the question takes the keyboard to its safe answer; cancelling gives it back to the button that asked
      if (c) focus?.(`amz-no-${c.id}`);
      else if (was) focus?.(`amz-${was.kind === "remove" ? "del" : "rot"}-${was.id}`);
    },
    rotate(id) {
      void run(
        id,
        () => api.rotateKey(endpoints(), id),
        (r) => {
          am().configs[id] = r.configs;
          am().node[id] = 0;
          am().confirm = null;
          am().open = id;
          merge(id, r.device);
        },
      );
    },
    remove(id) {
      void run(
        id,
        () => api.revokeDevice(endpoints(), id),
        () => {
          if (data.amnezia) data.amnezia.devices = data.amnezia.devices.filter((x) => x.id !== id);
          data.devices = data.devices.filter((x) => x.id !== id);
          data.user.devices_used = Math.max(0, data.user.devices_used - 1);
          delete am().configs[id];
          am().confirm = null;
          if (am().open === id) am().open = null;
        },
      );
    },
  };
}
