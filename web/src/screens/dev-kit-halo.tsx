import { useRef, useState } from "react";
import { halo } from "@/components/fx/halo";
import { Fresh } from "@/components/fx/fresh";
import { markFresh } from "@/components/fx/registry";
import { ThemeToggle } from "@/components/prefs";
import { Button } from "@/components/ui/button";
import { Card, Chip } from "@/components/ui/bits";
import { StatusPill, type StatusKind } from "@/components/ui/status";
import { accentPresets, setAccent, useAccent } from "@/lib/accent";
import { setEffectsEnabled, useEffectsEnabled } from "@/lib/effects";

// The halo on a panel made of fictional data (/dev-kit?only=halo): save, create, and the assistant's panel.
const countries = ["Germany", "Finland", "Netherlands", "Sweden", "France", "Poland", "Austria", "Canada", "Japan", "Singapore", "United Kingdom", "United States"];
const seed = countries.flatMap((country, i) => [1, 2].map((n) => ({
  id: `${country.slice(0, 2).toLowerCase()}${i + 1}-${n}`,
  country,
  status: (i === 3 && n === 2 ? "bad" : i === 7 && n === 1 ? "warn" : "ok") as StatusKind,
  people: 8 + (i * 7 + n * 3) % 40,
  load: 12 + (i * 11 + n * 7) % 67,
})));

export function HaloKit() {
  const on = useEffectsEnabled();
  const accent = useAccent();
  const [nodes, setNodes] = useState(seed);
  const [chat, setChat] = useState(false);
  const thread = useRef<HTMLDivElement>(null);
  const create = () => {
    const id = `sg${nodes.length + 1}-1`;
    setNodes((list) => [{ id, country: "Singapore", status: "ok", people: 0, load: 3 }, ...list]);
    markFresh("node", id);
  };
  const answer = () => {
    const r = thread.current?.getBoundingClientRect();
    if (r) halo("save", { target: thread.current, origin: { x: r.left + r.width / 2, y: r.bottom - 24 } });
  };
  return (
    <div className="min-h-screen bg-canvas text-fg">
      <header data-fx-chrome="top" className="sticky top-0 z-10 flex h-14 items-center justify-between gap-3 border-b border-line bg-canvas px-6">
        <strong>Mistgate <span className="font-normal text-muted">/ halo</span></strong>
        <div className="flex items-center gap-3">
          <label className="flex items-center gap-1.5 text-xs text-muted"><input type="checkbox" checked={on} onChange={(e) => setEffectsEnabled(e.target.checked)} />Effects</label>
          <select aria-label="Accent" value={accent} onChange={(e) => setAccent(e.target.value)} className="rounded-ctl border border-line bg-canvas px-2 py-1 text-xs">{accentPresets.map((p) => <option key={p.id} value={p.color}>{p.id}</option>)}</select>
          <Button size="sm" variant="secondary" onClick={() => setChat((v) => !v)}>Assistant</Button>
          <ThemeToggle />
        </div>
      </header>
      <div className="mx-auto grid max-w-[1440px] gap-6 px-4 py-6 md:grid-cols-[180px_minmax(0,1fr)] md:px-8">
        <aside className="hidden flex-col gap-4 text-sm text-muted md:flex" aria-label="Demo navigation">
          <strong className="text-fg">Overview</strong><span>Nodes</span><span>People</span><span>Profiles</span><span>Health</span><span>AI</span><span>Settings</span>
        </aside>
        <main id="main" className="min-w-0 space-y-6">
          <div className="flex flex-wrap items-end justify-between gap-3">
            <div><p className="text-xs text-muted">YOUR NETWORK</p><h1 className="text-2xl font-semibold">Fleet overview</h1><p className="text-sm text-muted">A test panel with fictional data</p></div>
            <div className="flex gap-2"><Button variant="secondary" onClick={() => halo("save")}>Save</Button><Button onClick={create}>Add node</Button></div>
          </div>
          <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">{[["Nodes", String(nodes.length), "22 online"], ["People online", "347", "across 12 regions"], ["Traffic today", "1.28 TB", "+12% this week"], ["Active alerts", "2", "1 requires attention"]].map(([label, value, hint]) => <Card key={label} className="flex flex-col gap-2 p-5"><span className="text-xs text-muted">{label}</span><strong className="text-2xl">{value}</strong><span className="text-xs text-muted">{hint}</span></Card>)}</div>
          <Card className="overflow-hidden">
            <div className="border-b border-line p-5"><h2 className="font-semibold">Servers</h2><p className="text-xs text-muted">{nodes.length} nodes · mock data</p></div>
            <div className="overflow-x-auto"><table className="w-full text-left text-sm"><thead className="text-xs text-muted"><tr><th className="px-5 py-3">Node</th><th>Location</th><th>Status</th><th>Online</th><th>CPU</th><th>Protocols</th></tr></thead>
              <tbody>{nodes.map((node) => <Fresh as="tr" kind="node" id={node.id} key={node.id} className="border-t border-line"><td className="px-5 py-3 font-medium">{node.id}</td><td>{node.country}</td><td><StatusPill kind={node.status} label={node.status === "ok" ? "Online" : node.status === "warn" ? "Warning" : "Offline"} /></td><td>{node.people}</td><td>{node.load}%</td><td><Chip>Hysteria2</Chip> <Chip>AWG</Chip></td></Fresh>)}</tbody>
            </table></div>
          </Card>
        </main>
      </div>
      {chat && (
        <section data-fx-halo className="fixed end-4 top-4 bottom-4 z-30 flex w-[400px] flex-col overflow-hidden rounded-card-lg border border-line bg-surface shadow-(--shadow-dock)">
          <header className="flex items-center justify-between border-b border-line px-5 py-3"><b>Assistant</b><Button size="sm" onClick={answer}>Answer done</Button></header>
          <div ref={thread} className="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto p-5 text-sm">
            <p className="self-end rounded-2xl bg-accent-soft px-3 py-2">Which nodes are overloaded?</p>
            <p className="rounded-2xl bg-fg/[0.04] px-3 py-2">Two nodes are above 70% CPU: ca8-1 and fr5-2. Both have spare capacity on their neighbours.</p>
            <p className="rounded-2xl bg-fg/[0.04] px-3 py-2">I can move half of the people from ca8-1 to ca8-2. Want me to plan it?</p>
          </div>
        </section>
      )}
    </div>
  );
}
