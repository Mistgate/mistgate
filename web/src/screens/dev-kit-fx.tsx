import { useEffect, useRef, useState, type ReactNode } from "react";
import { Fresh } from "@/components/fx/fresh";
import { contentBox, firstSweepOptions, pageSweepOptions } from "@/components/fx/page";
import { markFresh } from "@/components/fx/registry";
import { paletteNames, paletteStops, resolvePalette, easings, type Direction, type EasingName, type PaletteName } from "@/components/fx/palette";
import { painterKind } from "@/components/fx/renderer";
import { sweep, type SweepOptions } from "@/components/fx/sweep";
import { Sweep } from "@/components/fx/sweep-react";
import { Avatar, Card, SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Segmented } from "@/components/ui/segmented";
import { Switch } from "@/components/ui/switch";
import { ThemeToggle } from "@/components/prefs";
import { setEffectsEnabled, useEffectsEnabled } from "@/lib/effects";
import { setTheme } from "@/lib/theme";

// Development only (/dev-kit?only=fx): the sweep on a table that is being filled (as in the reference), a new user and a new node,
// with every tunable on a slider. ?freeze=0.5 paints the band paused at 50 % (for screenshots); ?theme=light|dark picks the theme;
// ?palette=prism|... and ?dir=rtl|ttb|... preset the controls.

const params = new URLSearchParams(location.search);
const freezeParam = params.get("freeze");
const freeze = freezeParam !== null && !Number.isNaN(Number(freezeParam)) ? Math.min(1, Math.max(0, Number(freezeParam))) : undefined;
const themeParam = params.get("theme");
if (themeParam === "light" || themeParam === "dark") setTheme(themeParam);

const people = [
  ["Mila", "nl1", "42 GB", "active"],
  ["Jonas", "de1", "17 GB", "active"],
  ["Anouk", "nl1", "88 GB", "active"],
  ["Elias", "fi1", "3 GB", "idle"],
  ["Noor", "be", "61 GB", "active"],
  ["Taavi", "fi1", "29 GB", "active"],
  ["Sofie", "de1", "12 GB", "idle"],
  ["Ruben", "nl1", "74 GB", "active"],
] as const;

function Slider({ label, value, onChange, min, max, step }: { label: string; value: number; onChange: (v: number) => void; min: number; max: number; step: number }) {
  return (
    <label className="flex items-center gap-3 text-xs font-bold text-muted">
      <span className="w-24 flex-none">{label}</span>
      <input type="range" min={min} max={max} step={step} value={value} onChange={(e) => onChange(Number(e.target.value))} className="min-w-0 flex-1 accent-accent" />
      <span className="w-12 flex-none text-end font-mono text-fg">{value}</span>
    </label>
  );
}

function Block({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="flex flex-col gap-3">
      <SectionLabel>{title}</SectionLabel>
      {children}
    </section>
  );
}

export function FxKit() {
  const enabled = useEffectsEnabled();
  const [palette, setPalette] = useState<PaletteName>((paletteNames as readonly string[]).includes(params.get("palette") ?? "") ? (params.get("palette") as PaletteName) : "spectrum");
  const [dir, setDir] = useState<Direction>((["ltr", "rtl", "ttb", "btt", "inline"] as const).find((d) => d === params.get("dir")) ?? "ltr");
  const [easing, setEasing] = useState<EasingName>("easeOut");
  const [bandWidth, setBandWidth] = useState(0.42);
  const [duration, setDuration] = useState(1100);
  const [wave, setWave] = useState(0.5);
  const [ripple, setRipple] = useState(0.5);
  const [alpha, setAlpha] = useState(0.5);
  const opts: SweepOptions = { palette, direction: dir, easing, bandWidth, duration, wave, ripple, peakAlpha: alpha, ...(freeze !== undefined && { freeze }) };

  // the table "being filled": rows of dashes that become people behind the band
  const [filled, setFilled] = useState(freeze !== undefined ? 1 : 0);
  const [round, setRound] = useState(freeze !== undefined ? 1 : 0);
  const fill = () => {
    setFilled((f) => (f ? 0 : 1));
    setRound((r) => r + 1);
  };

  // a user card and a node card, made through the same path as the real create flows
  const [users, setUsers] = useState<string[]>([]);
  const [nodes, setNodes] = useState<string[]>([]);
  const seq = users.length + nodes.length;
  const addUser = () => {
    const id = `u${Date.now()}`;
    markFresh("user", id);
    setUsers((l) => [id, ...l].slice(0, 3));
  };
  const addNode = () => {
    const id = `n${Date.now()}`;
    markFresh("node", id);
    setNodes((l) => [id, ...l].slice(0, 2));
  };
  const frozenCard = freeze !== undefined;
  const [rows, setRows] = useState<string[]>(() => {
    if (freeze !== undefined) markFresh("row", "frozen");
    return [];
  });

  return (
    <div className="flex flex-col gap-6">
      <div className="flex flex-wrap items-center gap-3">
        <h1 className="text-xl font-extrabold tracking-[-0.03em]">Sweep</h1>
        <Painter />
        <div className="flex-1" />
        <label className="flex items-center gap-2 text-xs font-bold text-muted">
          Interface effects
          <Switch checked={enabled} onCheckedChange={setEffectsEnabled} aria-label="Interface effects" />
        </label>
        <ThemeToggle />
      </div>

      <Card lg className="flex flex-col gap-3 p-4">
        <div className="flex flex-wrap gap-2">
          {paletteNames.map((p) => (
            <button
              key={p}
              type="button"
              onClick={() => setPalette(p)}
              className={`flex flex-col gap-1 rounded-ctl border p-1.5 text-[11px] font-bold ${palette === p ? "border-accent-line text-fg" : "border-line text-muted"}`}
            >
              <span className="block h-3 w-24 rounded-sm" style={{ background: `linear-gradient(to right, ${paletteStops(resolvePalette(p, "#b8acf2"), 12).join(",")})` }} />
              {p}
            </button>
          ))}
        </div>
        <div className="grid gap-x-6 gap-y-2 md:grid-cols-2">
          <Slider label="Band width" value={bandWidth} onChange={setBandWidth} min={0.15} max={1} step={0.01} />
          <Slider label="Duration ms" value={duration} onChange={setDuration} min={300} max={3000} step={50} />
          <Slider label="Edge wave" value={wave} onChange={setWave} min={0} max={1} step={0.05} />
          <Slider label="Ripple" value={ripple} onChange={setRipple} min={0} max={1} step={0.05} />
          <Slider label="Peak alpha" value={alpha} onChange={setAlpha} min={0.2} max={1} step={0.05} />
          <div className="flex items-center gap-3">
            <Segmented aria-label="Direction" variant="flat" value={dir} onValueChange={setDir} options={(["ltr", "rtl", "ttb", "btt"] as const).map((d) => ({ value: d, label: d }))} />
            <Segmented aria-label="Easing" variant="flat" value={easing} onValueChange={setEasing} options={(Object.keys(easings) as EasingName[]).slice(0, 4).map((e) => ({ value: e, label: e }))} />
          </div>
        </div>
      </Card>

      <PageDemo />
      <Block title="A table being filled">
        <div className="flex gap-2">
          <Button variant="primary" onClick={fill}>
            {filled ? "Empty it" : "Fill it (AI)"}
          </Button>
          <Button onClick={() => setRound((r) => r + 1)}>Replay</Button>
        </div>
        <div className="overflow-hidden rounded-card-lg border border-line bg-surface">
        <Sweep play={round} onMount={freeze !== undefined} {...opts}>
          <div className="grid grid-cols-[1.4fr_1fr_1fr_1fr] gap-4 border-b border-line px-5 py-2.5 text-xs font-semibold text-muted">
            <span>Person</span>
            <span>Node</span>
            <span>Traffic</span>
            <span>State</span>
          </div>
          {people.map(([name, node, traffic, state]) => (
            <div key={name} className="grid h-12 grid-cols-[1.4fr_1fr_1fr_1fr] items-center gap-4 border-b border-line px-5 text-[13px] last:border-b-0">
              {filled ? (
                <>
                  <span className="flex items-center gap-2.5 font-bold">
                    <Avatar name={name} index={name.length} />
                    {name}
                  </span>
                  <span className="font-mono text-xs text-muted">{node}</span>
                  <span className="font-mono text-xs">{traffic}</span>
                  <span className={state === "active" ? "text-ok-text" : "text-muted"}>{state}</span>
                </>
              ) : (
                <span className="col-span-4 text-faint">—</span>
              )}
            </div>
          ))}
        </Sweep>
        </div>
      </Block>

      <div className="grid gap-6 md:grid-cols-2">
        <Block title="A user, created">
          <Button onClick={addUser}>Create user</Button>
          <div className="flex flex-col gap-2">
            {frozenCard && <UserCard name="Ilona" opts={opts} frozen />}
            {users.map((id, i) => (
              <Fresh key={id} kind="user" id={id} options={opts}>
                <UserCardBody name={["Kasper", "Liv", "Oskar"][(seq + i) % 3]!} />
              </Fresh>
            ))}
          </div>
        </Block>
        <Block title="A node, added">
          <Button onClick={addNode}>Add node</Button>
          <div className="flex flex-col gap-2">
            {frozenCard && <NodeCard name="fi1" opts={opts} frozen />}
            {nodes.map((id, i) => (
              <Fresh key={id} kind="node" id={id} options={opts}>
                <NodeCardBody name={["nl1", "de1"][(seq + i) % 2]!} />
              </Fresh>
            ))}
          </div>
        </Block>
      </div>
      <Block title="A row of a real table, created (as the Users list does it)">
        <Button onClick={() => { const id = `r${Date.now()}`; markFresh("row", id); setRows((l) => [id, ...l].slice(0, 3)); }}>Add row</Button>
        <table className="w-full overflow-hidden rounded-card-lg border border-line bg-surface text-[13px]">
          <tbody>
            {frozenCard && <FxRow id="frozen" name="Ilona" opts={opts} />}
            {rows.map((id, i) => <FxRow key={id} id={id} name={["Kasper", "Liv", "Oskar"][i % 3]!} opts={opts} />)}
            <tr className="h-12 border-b border-line">
              <td className="px-5 font-bold">Mila</td>
              <td className="font-mono text-xs text-muted">nl1</td>
            </tr>
          </tbody>
        </table>
      </Block>
      <Button
        variant="ghost"
        onClick={() => {
          // the imperative API on its own: the whole page's cards, one after another
          document.querySelectorAll<HTMLElement>("[data-fx-card]").forEach((el, i) => void sweep(el, { ...opts, delay: i * 120 }));
        }}
      >
        Sweep every card
      </Button>
    </div>
  );
}

/** A page opening: a mock shell (sidebar, top bar, content) whose content column sweeps; the band stays inside the content area. */
function PageDemo() {
  const [round, setRound] = useState(freeze !== undefined ? 1 : 0);
  const [first, setFirst] = useState(false);
  const main = useRef<HTMLDivElement>(null);
  const opts = (first ? firstSweepOptions : pageSweepOptions)({ ...(freeze !== undefined && { freeze }) });
  return (
    <Block title="A page opening (route sweep; the band keeps off the sidebar and the top bar)">
      <div className="flex gap-2">
        <Button onClick={() => { setFirst(false); setRound((r) => r + 1); }}>Open page</Button>
        <Button onClick={() => { setFirst(true); setRound((r) => r + 1); }}>First load</Button>
      </div>
      <div className="flex h-[300px] overflow-hidden rounded-card-lg border border-line bg-canvas">
        <div className="flex w-40 flex-none flex-col gap-1.5 border-e border-line p-3 text-xs font-bold text-muted">
          {["Overview", "Nodes", "Users", "Profiles"].map((n) => (
            <span key={n} className="rounded-ctl px-2.5 py-2">{n}</span>
          ))}
        </div>
        <div className="flex min-w-0 flex-1 flex-col">
          <div data-fx-chrome="top" className="flex h-12 flex-none items-center border-b border-line px-4 text-xs text-muted">Search</div>
          <div ref={main} className="min-h-0 flex-1 overflow-hidden px-6 py-5">
            <Sweep play={round} onMount={freeze !== undefined} {...opts} clipBox={() => (main.current ? contentBox(main.current)() : null)} className="flex flex-col gap-3">
              <h2 className="text-[22px] font-extrabold tracking-[-0.03em]">Users</h2>
              {people.slice(0, 4).map(([name, node, traffic]) => (
                <div key={name} className="flex items-center gap-3 rounded-card border border-line bg-surface px-3.5 py-2.5 text-[13px]">
                  <Avatar name={name} index={name.length} />
                  <b className="flex-1">{name}</b>
                  <span className="font-mono text-xs text-muted">{node}</span>
                  <span className="font-mono text-xs">{traffic}</span>
                </div>
              ))}
            </Sweep>
          </div>
        </div>
      </div>
    </Block>
  );
}

/** Which painter is in use (WebGL or the CSS fallback), refreshed while the page is open. */
function Painter() {
  const [kind, setKind] = useState(painterKind());
  useEffect(() => {
    const t = setInterval(() => setKind((k) => (painterKind() === "none" ? k : painterKind())), 300);
    return () => clearInterval(t);
  }, []);
  return <span className="font-mono text-xs text-muted">painter: {kind}</span>;
}

function UserCard({ name, opts, frozen }: { name: string; opts: SweepOptions; frozen?: boolean }) {
  return (
    <Sweep play={frozen ? 1 : undefined} onMount {...opts}>
      <UserCardBody name={name} />
    </Sweep>
  );
}

function NodeCard({ name, opts, frozen }: { name: string; opts: SweepOptions; frozen?: boolean }) {
  return (
    <Sweep play={frozen ? 1 : undefined} onMount {...opts}>
      <NodeCardBody name={name} />
    </Sweep>
  );
}

function FxRow({ id, name, opts }: { id: string; name: string; opts: SweepOptions }) {
  return (
    <Fresh as="tr" kind="row" id={id} options={opts} className="h-12 border-b border-line">
      <td className="px-5">
        <span className="flex items-center gap-2.5 font-bold">
          <Avatar name={name} index={name.length} />
          {name}
        </span>
      </td>
      <td className="font-mono text-xs text-muted">fi1</td>
    </Fresh>
  );
}

function UserCardBody({ name }: { name: string }) {
  return (
    <div data-fx-card className="flex flex-col gap-2.5 rounded-card border border-line bg-surface px-3.5 py-3">
      <div className="flex items-center gap-2.5">
        <Avatar name={name} index={name.length} />
        <b className="min-w-0 flex-1 truncate text-[15px]">{name}</b>
        <span className="text-xs text-muted">just now</span>
      </div>
      <div className="flex items-center gap-2 text-xs text-muted">
        <span className="rounded-md bg-surface-2 px-1.5 py-0.5 font-bold">Everyone</span>
        <span className="flex-1" />
        <span className="font-mono">0 / 2</span>
      </div>
      <div className="h-1 rounded-[2px] bg-surface-2" />
    </div>
  );
}

function NodeCardBody({ name }: { name: string }) {
  return (
    <div data-fx-card className="flex flex-col gap-2.5 rounded-card border border-line bg-surface p-3.5">
      <div className="flex items-center gap-2">
        <span className="flex h-5 items-center rounded-md bg-surface-2 px-1.5 font-mono text-[10px] font-bold text-muted">{name.slice(0, 2).toUpperCase()}</span>
        <b className="text-[15px]">{name}</b>
        <span className="min-w-0 flex-1 truncate text-xs text-muted">Frankfurt · Hetzner</span>
        <span className="text-xs font-bold text-ok-text">online</span>
      </div>
      <div className="grid grid-cols-4 gap-2 font-mono text-[15px] font-bold">
        <span>0</span>
        <span>0 B</span>
        <span>3%</span>
        <span>12s</span>
      </div>
    </div>
  );
}
