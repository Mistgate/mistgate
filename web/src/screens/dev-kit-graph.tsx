import { useLayoutEffect, useMemo, useState, type ReactNode } from "react";
import { Graph, GraphPortRow, GraphSlider, type GraphEdge, type GraphNode } from "@/components/graph";
import { LangToggle, ThemeToggle } from "@/components/prefs";
import { Card, PageTitle } from "@/components/ui/bits";
import { Icon } from "@/components/ui/icons";
import { Switch } from "@/components/ui/switch";
import { setTheme } from "@/lib/theme";
import { WaysShowcase } from "./dev-kit-ways-graph";

// Development only, /dev-kit?only=graph: the node graph editor (components/graph) in two demos on made-up data.
// Plain English on purpose: this page never ships.

let uid = 100;

function Section({ title, hint, children, right }: { title: string; hint: string; children: ReactNode; right?: ReactNode }) {
  return (
    <section className="flex flex-col gap-3">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h2 className="text-base font-semibold tracking-tight">{title}</h2>
          <p className="mt-0.5 max-w-2xl text-[13px] text-muted">{hint}</p>
        </div>
        {right}
      </div>
      {children}
    </section>
  );
}

/* ---------------------------------------------------------------- 1. a shader-like editor */

const shaderColors = { float: "var(--accent-sage)", vec: "var(--accent-lavender)", color: "var(--accent-sand)", any: "var(--muted)" };

function ShaderDemo() {
  const [scale, setScale] = useState(5.2);
  const [detail, setDetail] = useState(2);
  const [rough, setRough] = useState(0.62);
  const [normalize, setNormalize] = useState(true);
  const [clamp, setClamp] = useState(false);
  const [edges, setEdges] = useState<GraphEdge[]>([
    { id: "e1", from: { node: "coord", port: "uv" }, to: { node: "noise", port: "vector" } },
    { id: "e2", from: { node: "noise", port: "fac" }, to: { node: "ramp", port: "fac" } },
    { id: "e3", from: { node: "ramp", port: "color" }, to: { node: "mix", port: "a" } },
    { id: "e4", from: { node: "coord", port: "normal" }, to: { node: "mix", port: "b" } },
    { id: "e5", from: { node: "mix", port: "color" }, to: { node: "out", port: "surface" } },
  ]);

  const nodes = useMemo<GraphNode[]>(
    () => [
      {
        id: "coord",
        title: "Texture Coordinate",
        icon: <Icon name="map" size={13} />,
        tone: "sky",
        rank: 0,
        ports: [
          { id: "uv", side: "out", type: "vec", label: "UV" },
          { id: "normal", side: "out", type: "vec", label: "Normal" },
          { id: "time", side: "out", type: "float", label: "Time" },
        ],
      },
      {
        id: "noise",
        title: "Noise Texture",
        icon: <Icon name="pulse" size={13} />,
        tone: "lavender",
        rank: 1,
        width: 232,
        ports: [
          { id: "vector", side: "in", type: "vec", label: "Vector" },
          { id: "scale", side: "in", type: "float", label: "Scale" },
          { id: "fac", side: "out", type: "float", label: "Fac" },
          { id: "color", side: "out", type: "color", label: "Color" },
        ],
        body: (
          <>
            <GraphPortRow port={["vector", "fac"]} />
            <GraphPortRow port={["scale", "color"]} />
            <GraphSlider label="Scale" value={scale} min={0} max={20} step={0.1} format={(v) => v.toFixed(1)} onChange={setScale} />
            <GraphSlider label="Detail" value={detail} min={0} max={8} step={0.5} format={(v) => v.toFixed(1)} onChange={setDetail} />
            <div className="flex items-center justify-between px-3.5 py-1.5 text-[12.5px]" data-nodrag>
              <span className="text-muted">Normalize</span>
              <Switch checked={normalize} onCheckedChange={setNormalize} aria-label="Normalize" />
            </div>
          </>
        ),
      },
      {
        id: "ramp",
        title: "Color Ramp",
        icon: <Icon name="palette" size={13} />,
        tone: "sand",
        rank: 2,
        ports: [
          { id: "fac", side: "in", type: "float", label: "Fac" },
          { id: "color", side: "out", type: "color", label: "Color" },
          { id: "alpha", side: "out", type: "float", label: "Alpha" },
        ],
        body: (
          <div className="px-3.5 pb-1.5 pt-0.5">
            <div
              className="h-5 rounded-md border border-line"
              style={{ background: "linear-gradient(90deg, #1b1b30, #6b4fd1 38%, #e7b86d 72%, #fff3d6)" }}
              aria-hidden
            />
          </div>
        ),
      },
      {
        id: "mix",
        title: "Mix",
        icon: <Icon name="layers" size={13} />,
        tone: "rose",
        rank: 3,
        ports: [
          { id: "a", side: "in", type: "color", label: "A" },
          { id: "b", side: "in", type: "any", label: "B" },
          { id: "color", side: "out", type: "color", label: "Result" },
        ],
        body: (
          <div className="flex items-center justify-between px-3.5 py-1.5 text-[12.5px]" data-nodrag>
            <span className="text-muted">Clamp result</span>
            <Switch checked={clamp} onCheckedChange={setClamp} aria-label="Clamp result" />
          </div>
        ),
      },
      {
        id: "out",
        title: "Material Output",
        icon: <Icon name="sparkle" size={13} />,
        tone: "mint",
        rank: 4,
        ports: [
          { id: "surface", side: "in", type: "color", label: "Surface" },
          { id: "alpha", side: "in", type: "float", label: "Alpha" },
        ],
        body: <GraphSlider label="Roughness" value={rough} min={0} max={1} step={0.01} format={(v) => v.toFixed(2)} onChange={setRough} />,
      },
    ],
    [scale, detail, rough, normalize, clamp],
  );

  return (
    <Graph
      nodes={nodes}
      edges={edges}
      typeColors={shaderColors}
      ariaLabel="Shader graph demo"
      height={520}
      onConnect={(from, to) => setEdges((es) => [...es, { id: `e${++uid}`, from, to }])}
      onEdgeDelete={(e) => setEdges((es) => es.filter((x) => x.id !== e.id))}
    />
  );
}

export function GraphKit() {
  // ?demo=ways|shader shows one demo alone, ?theme=light|dark picks the theme (for screenshots)
  const q = new URLSearchParams(location.search);
  const demo = q.get("demo");
  const theme = q.get("theme");
  useLayoutEffect(() => {
    if (theme === "light" || theme === "dark") setTheme(theme);
  }, [theme]);
  if (demo === "ways") return <WaysShowcase />;
  if (demo === "shader") return <ShaderDemo />;
  return (
    <div className="flex flex-col gap-10">
      <div className="flex items-center gap-3">
        <PageTitle>Graph editor</PageTitle>
        <div className="flex-1" />
        <LangToggle />
        <ThemeToggle />
      </div>
      <Section
        title="Ways in to be (the intended use: the real WaysGraph screen)"
        hint="People, entries and relays, the node's servers, then the exit, with every state. Drag from an entry's port to a server to add a relay, click a wire for its menu. ?demo=ways shows it alone, &v6=on|can|none|broken|down and &role=helper set the fixtures."
      >
        <WaysShowcase />
      </Section>
      <Section title="Shader-style demo" hint="The engine on its own: typed ports, sliders and switches inside the cards, wires coloured by type.">
        <ShaderDemo />
      </Section>
      <Card className="p-4 text-[13px] text-muted">
        Pan: drag the canvas, Space+drag, middle button, two-finger scroll. Zoom: Ctrl/Cmd+scroll, pinch, + / - / 0. F fits. Shift+drag selects several nodes; arrows nudge (Shift = 1px), Alt disables snapping.
        Keyboard wiring: focus a port, Enter, Tab or arrows to pick the target, Enter to connect, Esc to cancel.
      </Card>
    </div>
  );
}
