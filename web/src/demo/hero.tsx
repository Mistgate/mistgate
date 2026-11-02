import { Brand } from "@/components/brand";
import { Icon, NavIcon, type NavIconName } from "@/components/ui/icons";
import { Flag } from "@/components/ui/flag";
import { StatusGlyph } from "@/components/ui/status";
import type { Theme } from "@/lib/theme";
import type { ReactNode } from "react";
import { Bar, Collapse, Count, Phone, Spinner, Stage, States, Swap, Window, type Act } from "./kit";

// "Add a server and your people have it." A device pair: the admin's Nodes page, and alice's phone on her own
// subscription page straddling the window's right edge. Beats: the cursor opens Add node, types the address and the
// name, presses Install; the panel checks the server; the new row slides in at the top of the list carrying the rows
// below, Installing turns Online on the same pill and its protocols arrive; Singapore slides into alice's server list.
// The payoff holds while traffic rolls, then what the loop added leaves in one reverse stagger.
//
// Invented data only: documentation addresses (RFC 5737), the user is "alice".

type S = {
  /** the add-node dialog */
  open: boolean;
  focus: "" | "address" | "name";
  typing: boolean;
  address: string;
  name: string;
  checking: boolean;
  /** the new row in the list, its status, its protocols, its traffic */
  added: boolean;
  online: boolean;
  protocols: boolean;
  sgTraffic: number;
  /** Singapore in alice's server list */
  phone: boolean;
  /** leaving: what the loop added closes clipped, so the rows rising into its place never pass over its words */
  leaving: boolean;
  /** each existing row's position in its traffic sequence */
  t: [number, number, number];
};

const initial: S = {
  open: false,
  focus: "",
  typing: false,
  address: "",
  name: "",
  checking: false,
  added: false,
  online: false,
  protocols: false,
  sgTraffic: 0,
  phone: false,
  leaving: false,
  t: [0, 0, 0],
};

const poster: S = { ...initial, added: true, online: true, protocols: true, sgTraffic: 31, phone: true };

// Each sequence has 10 steps and the loop takes exactly 10 per row, so it ends where it began: nothing rewinds.
const NODES = [
  { name: "de1", city: "Frankfurt", cc: "DE", country: "Germany", protocols: ["HY2", "VLESS", "AWG"], traffic: [184, 191, 186, 179, 188, 195, 187, 182, 190, 186] },
  { name: "fi1", city: "Helsinki", cc: "FI", country: "Finland", protocols: ["HY2", "VLESS"], traffic: [96, 102, 99, 93, 101, 97, 104, 98, 95, 100] },
  { name: "nl1", city: "Amsterdam", cc: "NL", country: "Netherlands", protocols: ["VLESS", "AWG"], traffic: [141, 137, 148, 144, 139, 151, 146, 142, 149, 145] },
] as const;

const mbit = (n: number) => `${Math.round(n)} Mbit/s`;

const W = 1100;
const H = 620;
/** where the cursor waits: empty header space, next to Add node */
const REST = { x: 704, y: 126 };

async function script(a: Act<S>) {
  a.mark("start");
  // Live traffic: one row rolls every 430 ms, so each row moves every 1.3 s, ten times a loop
  const traffic = (async () => {
    for (let k = 0; k < 30; k++) {
      await a.wait(430);
      const row = k % 3;
      a.put((s) => ({ t: s.t.map((v, i) => (i === row ? v + 1 : v)) as S["t"] }));
    }
  })();

  await a.wait(700);
  a.mark("add");
  await a.move({ at: "add", x: 0.9, y: 0.66 });
  await a.wait(140);
  await a.click();
  a.put({ open: true });
  await a.wait(460);

  await a.click({ at: "address", x: 0.82, y: 0.56 });
  a.put({ focus: "address", typing: true });
  await a.type("address", "203.0.113.50", { interval: 42 });
  a.put({ typing: false });
  await a.wait(200);
  await a.click({ at: "name", x: 0.74, y: 0.56 });
  a.put({ focus: "name", typing: true });
  await a.type("name", "sg1", { interval: 85 });
  a.put({ typing: false });
  await a.wait(280);

  await a.click({ at: "install", x: 0.86, y: 0.64 });
  a.mark("install");
  a.put({ focus: "", checking: true });
  const away = a.move(REST);
  await a.wait(1300);
  a.put({ open: false });
  await a.wait(240);

  a.mark("row");
  a.put({ added: true });
  await a.wait(1750);
  a.mark("online");
  a.put({ online: true });
  await a.wait(320);
  a.put({ protocols: true, sgTraffic: 12 });
  await a.wait(420);
  a.mark("phone");
  a.put({ phone: true });
  await a.wait(1100);
  a.put({ sgTraffic: 24 });
  await a.wait(1300);
  a.mark("payoff");
  a.put({ sgTraffic: 31 });
  await a.wait(1300);

  // What the loop added leaves in one reverse stagger, the phone's card first; its state goes back unseen
  a.mark("exit");
  a.put({ leaving: true, phone: false });
  await a.wait(70);
  a.put({ added: false });
  await a.wait(600);
  await away;
  await traffic;
  await a.snap({ online: false, protocols: false, sgTraffic: 0, address: "", name: "", checking: false, leaving: false, t: [0, 0, 0] });
}

export function Hero({ theme }: { theme: Theme }) {
  return (
    <Stage<S>
      width={W}
      height={H}
      theme={theme}
      fidelity="standard"
      initial={initial}
      poster={poster}
      script={script}
      cursor={{ at: REST }}
      className="mgd"
    >
      {(s) => (
        <>
          <Window
            className="mgd-win"
            lights="grey"
            sidebarWidth={200}
            sidebar={<Sidebar />}
            lead={<Search />}
            toolbar={<TopRight />}
            body={{ className: "mgd-body" }}
          >
            <Nodes s={s} />
          </Window>
          <div className="mgd-scrim" data-on={s.open || undefined} />
          <Dialog s={s} />
          <Phone width={300} frame="pro" dark={theme === "dark"} wallpaper="var(--bg)" className="mgd-phone">
            <UserPage phone={s.phone} leaving={s.leaving} />
          </Phone>
        </>
      )}
    </Stage>
  );
}

// ---- the admin window ----

const NAV: { id: NavIconName; label: string; group?: string }[] = [
  { id: "overview", label: "Overview" },
  { id: "nodes", label: "Nodes" },
  { id: "users", label: "Users" },
  { id: "profiles", label: "Profiles", group: "SETUP" },
  { id: "subscriptions", label: "Subscriptions" },
  { id: "health", label: "Health", group: "OPERATIONS" },
  { id: "reachability", label: "Reachability" },
  { id: "updates", label: "Updates" },
  { id: "integrations", label: "Integrations" },
  { id: "settings", label: "Settings" },
];

function Sidebar() {
  return (
    <div className="mgd-side">
      <span className="mgd-brand">
        <Brand logoSize={26} textSize={15} />
      </span>
      {NAV.map((n) => (
        <div key={n.id} className="mgd-nav-group">
          {n.group && <span className="mgd-nav-label">{n.group}</span>}
          <span className="mgd-nav" data-active={n.id === "nodes" || undefined}>
            <NavIcon name={n.id} />
            {n.label}
          </span>
        </div>
      ))}
    </div>
  );
}

function Search() {
  return (
    <span className="mgd-search">
      <Icon name="search" size={13} />
      <Bar w={120} h={6} tone={3} />
    </span>
  );
}

function TopRight() {
  return (
    <span className="mgd-top-right">
      <Bar w={88} h={26} r={9} tone={1} />
      <Bar w={26} h={26} r={9} tone={1} />
      <Bar w={26} h={26} r={13} tone={2} />
    </span>
  );
}

function Nodes({ s }: { s: S }) {
  return (
    <div className="mgd-nodes">
      <div className="mgd-head">
        <span className="mgd-title-block">
          <span className="mgd-title">Nodes</span>
          <Bar w={132} h={7} tone={2} />
        </span>
        <span className="mgd-btn" data-target="add" data-cursor="hand">
          <Icon name="plus" size={14} />
          Add node
        </span>
      </div>
      <div className="mgd-table">
        <div className="mgd-tr mgd-th">
          <span>Node</span>
          <span>Status</span>
          <span className="mgd-num">Traffic</span>
        </div>
        <Collapse open={s.added} reveal={s.leaving ? "clip" : "drop"}>
          <NodeRow
            name="sg1"
            city="Singapore"
            flag={<Flag code="SG" size={14} />}
            online={s.online}
            protocols={s.protocols ? ["HY2", "VLESS"] : []}
            traffic={<States align="end" value={s.online ? "on" : "off"} states={{ off: <span className="mgd-dash">—</span>, on: <Count value={s.sgTraffic} format={mbit} /> }} />}
          />
        </Collapse>
        {NODES.map((n, i) => (
          <NodeRow
            key={n.name}
            name={n.name}
            city={n.city}
            flag={<Flag code={n.cc} size={14} />}
            online
            protocols={n.protocols}
            traffic={<Count value={n.traffic[s.t[i]! % n.traffic.length]!} format={mbit} />}
          />
        ))}
        {[58, 44, 66].map((w, i) => (
          <div key={i} className="mgd-tr mgd-filler">
            <span className="mgd-node">
              <Bar w={21} h={14} r={3} tone={2} />
              <span className="mgd-lines">
                <Bar w={w} h={8} tone={3} />
                <Bar w={w + 28} h={6} tone={2} />
              </span>
            </span>
            <Bar w={70} h={24} r={12} tone={1} />
            <Bar w={62} h={7} tone={2} className="mgd-end" />
          </div>
        ))}
      </div>
    </div>
  );
}

function NodeRow({ name, city, flag, online, protocols, traffic }: { name: string; city: string; flag: ReactNode; online: boolean; protocols: readonly string[]; traffic: ReactNode }) {
  return (
    <div className="mgd-tr">
      <span className="mgd-node">
        {flag}
        <span className="mgd-lines" data-two={protocols.length > 0 || undefined}>
          <span className="mgd-name">
            <b>{name}</b>
            <span>{city}</span>
          </span>
          <span className="mgd-protocols">
            {protocols.map((p, i) => (
              <span key={p} className="mgd-proto" style={{ transitionDelay: `${i * 70}ms` }}>
                {p}
              </span>
            ))}
          </span>
        </span>
      </span>
      <Pill online={online} />
      <span className="mgd-num mgd-traffic">{traffic}</span>
    </div>
  );
}

/** Mistgate's status pill (components/ui/status.tsx), with the change of state animated on the one pill: the tone eases,
 * the glyphs crossfade in one cell, the word swaps while the pill's edge eases to fit it. */
function Pill({ online }: { online: boolean }) {
  return (
    <span className={`mgd-pill tone-${online ? "ok" : "busy"}`}>
      <span className="mgd-glyph" data-online={online || undefined}>
        <StatusGlyph kind="busy" />
        <StatusGlyph kind="ok" />
      </span>
      <Swap id={online ? "online" : "installing"} duration={300}>{online ? "Online" : "Installing"}</Swap>
    </span>
  );
}

// ---- the add-node dialog ----

function Dialog({ s }: { s: S }) {
  return (
    <div className="mgd-dialog" data-on={s.open || undefined}>
      <div className="mgd-dialog-head">
        <span className="mgd-dialog-titles">
          <span className="mgd-dialog-title">Add node</span>
          <Bar w={300} h={7} tone={2} />
          <Bar w={220} h={7} tone={2} />
        </span>
        <span className="mgd-close">
          <Icon name="x" size={14} />
        </span>
      </div>
      <div className="mgd-fields">
        <Field label="Address" value={s.address} target="address" focus={s.focus === "address"} typing={s.typing} />
        <Field label="Name" value={s.name} target="name" focus={s.focus === "name"} typing={s.typing} />
      </div>
      <div className="mgd-foot">
        <States
          align="start"
          value={s.checking ? "checking" : "cancel"}
          states={{
            cancel: <span className="mgd-ghost">Cancel</span>,
            checking: (
              <span className="mgd-checking">
                <Spinner size={14} />
                Checking the server
              </span>
            ),
          }}
        />
        <span className="mgd-btn mgd-install" data-target="install" data-cursor="hand" data-busy={s.checking || undefined}>
          Install
        </span>
      </div>
    </div>
  );
}

function Field({ label, value, target, focus, typing }: { label: string; value: string; target: string; focus: boolean; typing: boolean }) {
  return (
    <span className="mgd-field">
      <span className="mgd-label">{label}</span>
      <span className="mgd-input" data-target={target} data-cursor="ibeam" data-focus={focus || undefined}>
        <span data-typing>{value}</span>
        <i className="mgd-caret" data-on={focus || undefined} data-still={(focus && typing) || undefined} />
      </span>
    </span>
  );
}

// ---- alice's phone: the public user page (web/src/sub) ----

// Country only, as the narrow card fits it; Singapore arrives the same way
const SERVERS = NODES.map((n) => ({ cc: n.cc, country: n.country }));

function UserPage({ phone, leaving }: { phone: boolean; leaving: boolean }) {
  return (
    <div className="mgd-sub">
      <div className="mgd-sub-top">
        <span className="mgd-wm">
          mist<b>gate</b>
        </span>
        <Bar w={58} h={30} r={11} tone={1} />
      </div>
      <div className="mgd-sub-hi">
        <span className="mgd-sub-h1">Hi, alice</span>
        <Bar w={168} h={8} tone={2} />
      </div>
      <div className="mgd-sub-head">
        <span className="mgd-sub-h2">Servers</span>
        <Bar w={196} h={7} tone={2} />
      </div>
      <div className="mgd-srv-list">
        <Collapse open={phone} reveal={leaving ? "clip" : "drop"}>
          <Server flag={<Flag code="SG" size={15} />} country="Singapore" />
        </Collapse>
        {SERVERS.map((x) => (
          <Server key={x.cc} flag={<Flag code={x.cc} size={15} />} country={x.country} />
        ))}
        <div className="mgd-srv-gap">
          <div className="mgd-srv mgd-srv-filler">
            <Bar w={22} h={15} r={3} tone={2} />
            <Bar w={128} h={8} tone={2} />
          </div>
        </div>
      </div>
    </div>
  );
}

function Server({ flag, country }: { flag: ReactNode; country: string }) {
  return (
    <div className="mgd-srv-gap">
      <div className="mgd-srv">
        {flag}
        <span className="mgd-srv-name">{country}</span>
        <span className="mgd-state">
          <i className="mgd-live" />
          working
        </span>
      </div>
    </div>
  );
}
