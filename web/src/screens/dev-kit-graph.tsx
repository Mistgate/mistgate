import { useCallback, useLayoutEffect, useMemo, useState, type ReactNode } from "react";
import { Graph, GraphMenuItem, GraphPortRow, GraphSlider, GraphValue, type GraphEdge, type GraphNode, type PortRef } from "@/components/graph";
import { LangToggle, ThemeToggle } from "@/components/prefs";
import { Card, PageTitle } from "@/components/ui/bits";
import { Icon } from "@/components/ui/icons";
import { Switch } from "@/components/ui/switch";
import { setTheme } from "@/lib/theme";

// Development only, /dev-kit?only=graph: the node graph editor (components/graph) in two demos on made-up data.
// Plain English on purpose: this page never ships.

const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));
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

/* ---------------------------------------------------------------- 2. the node's ways in */

const wayColors = { udp: "var(--accent-sky)", tcp: "var(--accent-lavender)", any: "var(--muted)" };

interface Server {
  id: string;
  name: string;
  type: "udp" | "tcp";
  port: number;
}
const servers: Server[] = [
  { id: "hy2", name: "HY2", type: "udp", port: 443 },
  { id: "awg", name: "AWG", type: "udp", port: 14491 },
  { id: "xhttp", name: "VLESS XHTTP", type: "tcp", port: 4443 },
  { id: "vless", name: "VLESS TCP", type: "tcp", port: 8443 },
  { id: "mtproto", name: "MTProto", type: "tcp", port: 443 },
];

const initialWays: GraphEdge[] = [
  { id: "w1", from: { node: "people", port: "connect" }, to: { node: "be", port: "hy2-in" }, state: "bad", label: "blocked in Russia" },
  { id: "w2", from: { node: "people", port: "connect" }, to: { node: "be", port: "awg-in" }, state: "bad" },
  { id: "w3", from: { node: "people", port: "connect" }, to: { node: "be", port: "xhttp-in" }, state: "bad" },
  { id: "w4", from: { node: "people", port: "connect" }, to: { node: "ru3", port: "from" }, state: "ok", animated: true },
  { id: "w5", from: { node: "ru3", port: "relay" }, to: { node: "be", port: "hy2-in" }, state: "ok", animated: true, label: "42 ms" },
  { id: "w6", from: { node: "people", port: "connect" }, to: { node: "ru2", port: "from" }, state: "warn" },
  { id: "w7", from: { node: "ru2", port: "relay" }, to: { node: "be", port: "vless-in" }, state: "warn", label: "unstable" },
  { id: "w8", from: { node: "be", port: "hy2-out" }, to: { node: "direct", port: "in" }, state: "ok", animated: true },
  { id: "w9", from: { node: "be", port: "awg-out" }, to: { node: "direct", port: "in" }, state: "ok" },
  { id: "w10", from: { node: "be", port: "xhttp-out" }, to: { node: "warp", port: "in" }, state: "ok" },
  { id: "w11", from: { node: "be", port: "vless-out" }, to: { node: "direct", port: "in" }, state: "warn" },
  { id: "w12", from: { node: "be", port: "mtproto-out" }, to: { node: "direct", port: "in" }, state: "off" },
];

const NodeChip = ({ children, tone }: { children: ReactNode; tone?: "ok" | "warn" | "bad" }) => <GraphValue tone={tone} className="ms-auto font-sans">{children}</GraphValue>;

function WaysDemo({ readOnly }: { readOnly: boolean }) {
  const [edges, setEdges] = useState<GraphEdge[]>(initialWays);
  const patch = useCallback((id: string, p: Partial<GraphEdge>) => setEdges((es) => es.map((e) => (e.id === id ? { ...e, ...p } : e))), []);

  const nodes = useMemo<GraphNode[]>(
    () => [
      {
        id: "people",
        title: "People · Russia",
        icon: <Icon name="people" size={13} />,
        tone: "sand",
        rank: 0,
        width: 214,
        ports: [{ id: "connect", side: "out", type: "any", label: "Connect" }],
        body: (
          <>
            <GraphPortRow port="connect">
              <span className="font-medium">Connect</span>
            </GraphPortRow>
            <div className="flex items-center gap-1.5 px-3.5 pb-1 pt-0.5 text-xs text-muted">1 240 people · 7 networks</div>
          </>
        ),
      },
      ...(["ru3", "ru2"] as const).map<GraphNode>((id) => {
        const udp = id === "ru3";
        return {
          id,
          label: id,
          title: (
            <span className="flex items-center gap-2">
              {id}
              <NodeChip>{udp ? "Moscow" : "Saint Petersburg"}</NodeChip>
            </span>
          ),
          icon: <Icon name="server" size={13} />,
          tone: "sky",
          rank: 1,
          width: 214,
          ports: [
            { id: "from", side: "in", type: "any", label: "From people", multi: true },
            { id: "relay", side: "out", type: udp ? "udp" : "tcp", label: "Relay" },
          ],
          body: (
            <>
              <GraphPortRow port="from">
                <span className="text-muted">From people</span>
              </GraphPortRow>
              <GraphPortRow port="relay">
                <span className="inline-flex items-center gap-2">
                  <span className="font-medium">Relay</span>
                  <GraphValue>{udp ? "udp/443" : "tcp/8443"}</GraphValue>
                </span>
              </GraphPortRow>
            </>
          ),
        };
      }),
      {
        id: "be",
        label: "be",
        title: (
          <span className="flex items-center gap-2">
            be
            <NodeChip tone="ok">online</NodeChip>
          </span>
        ),
        icon: <Icon name="shield" size={13} />,
        tone: "mint",
        rank: 2,
        width: 262,
        ports: servers.flatMap((s) => [
          { id: `${s.id}-in`, side: "in" as const, type: s.type, label: s.name, multi: true },
          { id: `${s.id}-out`, side: "out" as const, type: s.type, label: s.name },
        ]),
        body: (
          <>
            {servers.map((s) => (
              <GraphPortRow key={s.id} port={[`${s.id}-in`, `${s.id}-out`]}>
                <span className="flex items-center justify-between gap-2">
                  <span className="truncate font-medium">{s.name}</span>
                  <GraphValue>
                    {s.type}/{s.port}
                  </GraphValue>
                </span>
              </GraphPortRow>
            ))}
          </>
        ),
      },
      {
        id: "direct",
        title: "Internet (direct)",
        icon: <Icon name="globe" size={13} />,
        tone: "lavender",
        rank: 3,
        width: 206,
        ports: [{ id: "in", side: "in", type: "any", label: "Traffic", multi: true }],
        body: (
          <GraphPortRow port="in">
            <span className="text-muted">Traffic leaves from this server</span>
          </GraphPortRow>
        ),
      },
      {
        id: "warp",
        title: (
          <span className="flex items-center gap-2">
            Cloudflare WARP
          </span>
        ),
        icon: <Icon name="cloud" size={13} />,
        tone: "sage",
        rank: 3,
        width: 206,
        ports: [{ id: "in", side: "in", type: "any", label: "Traffic", multi: true }],
        body: (
          <GraphPortRow port="in">
            <span className="text-muted">Traffic exits through Cloudflare</span>
          </GraphPortRow>
        ),
      },
    ],
    [],
  );

  const canConnect = useCallback((from: PortRef, to: PortRef) => {
    const exit = to.node === "direct" || to.node === "warp";
    if (from.node === "people" && exit) return "People come in through an entry or a server, not through an exit";
    if (from.node === "people" && to.node === "people") return "Nothing flows into people";
    if (from.port === "relay" && to.node === from.node) return "A relay forwards to a server on a different node";
    if (from.port === "relay" && to.node !== "be") return "A relay forwards to a server, not to an entry or an exit";
    if (from.node === "be" && !exit) return "A server leaves through an exit: direct or WARP";
    return true;
  }, []);

  const onConnect = useCallback(async (from: PortRef, to: PortRef) => {
    await sleep(900);
    setEdges((es) => [...es, { id: `w${++uid}`, from, to, state: "ok" }]);
  }, []);

  const renderMenu = useCallback(
    (edge: GraphEdge, close: () => void) => (
      <>
        <GraphMenuItem
          icon={<Icon name="pulse" size={14} />}
          onClick={() => {
            close();
            const prev = edge.state;
            patch(edge.id, { state: "pending" });
            void sleep(1100).then(() => patch(edge.id, { state: prev === "bad" ? "bad" : "ok", label: prev === "bad" ? edge.label : "38 ms" }));
          }}
        >
          Check
        </GraphMenuItem>
        <GraphMenuItem
          icon={<Icon name={edge.state === "off" ? "eye" : "eyeOff"} size={14} />}
          onClick={() => {
            close();
            patch(edge.id, { state: edge.state === "off" ? "ok" : "off", animated: false });
          }}
        >
          {edge.state === "off" ? "Turn on" : "Turn off"}
        </GraphMenuItem>
        <div className="my-1 h-px bg-line" />
        <GraphMenuItem
          danger
          icon={<Icon name="trash" size={14} />}
          onClick={() => {
            close();
            setEdges((es) => es.filter((e) => e.id !== edge.id));
          }}
        >
          Remove
        </GraphMenuItem>
      </>
    ),
    [patch],
  );

  return (
    <Graph
      nodes={nodes}
      edges={edges}
      typeColors={wayColors}
      layoutKey="dev-kit-ways"
      ariaLabel="Ways in to be"
      height={640}
      readOnly={readOnly}
      canConnect={canConnect}
      onConnect={onConnect}
      onEdgeDelete={(e) => setEdges((es) => es.filter((x) => x.id !== e.id))}
      renderEdgeMenu={renderMenu}
    />
  );
}

export function GraphKit() {
  const [readOnly, setReadOnly] = useState(false);
  // ?demo=ways|shader shows one demo alone, ?theme=light|dark picks the theme (for screenshots)
  const q = new URLSearchParams(location.search);
  const demo = q.get("demo");
  const theme = q.get("theme");
  useLayoutEffect(() => {
    if (theme === "light" || theme === "dark") setTheme(theme);
  }, [theme]);
  if (demo === "ways") return <WaysDemo readOnly={readOnly} />;
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
        title="Ways in to be (the intended use)"
        hint="People, entries and relays, the node's servers, then the exit. Drag from a port to connect (udp to udp, tcp to tcp), click a wire for its menu, Delete removes it. Arrange resets the layout."
        right={
          <label className="flex items-center gap-2 text-[13px] text-muted">
            Read-only
            <Switch checked={readOnly} onCheckedChange={setReadOnly} aria-label="Read-only" />
          </label>
        }
      >
        <WaysDemo readOnly={readOnly} />
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
