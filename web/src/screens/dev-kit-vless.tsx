import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";
import { useState, type ReactNode } from "react";
import { SectionLabel } from "@/components/ui/bits";
import { Button } from "@/components/ui/button";
import { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import { App, NodeStatus, WarpState } from "@/gen/mistgate/admin/v1/common_pb";
import { profiles as profilesApi } from "@/lib/api";
import { nodesQuery } from "@/lib/queries";
import { meQuery } from "@/lib/session";
import { AddInbound } from "@/screens/node/add-inbound";
import { ProfileEditorScreen } from "@/screens/profiles/editor";
import { DeployDialog } from "@/screens/profiles/deploy";
import { hy2, vless } from "@/screens/profiles/schema.fixtures";
import { groupsQuery, profileListQuery, protocolsQuery } from "@/screens/users/rpc";

// Development only, part of /dev-kit: VLESS REALITY (design/vless.md §3, §7.2, §7.5) on made-up data — the new-profile form
// with the site segment (this node's site by default), its string lists, the TLS and CDN fields and the TCP 443 hint, the add dialog's
// hy2_decoy_paused notice and the same sentence in "Put on nodes", and the node's own site on a node with only an IP.
// The calls are answered here and armed on a click or a mount, like the other kits.

const protocols = [
  {
    id: "vless",
    displayName: "VLESS",
    settingsSchemaJson: vless,
    defaultSettingsJson: JSON.stringify({
      port: 443,
      transport: "xhttp",
      security: "reality",
      reality: { target_kind: "self", target: "", server_names: [], short_ids: ["6ba85179", "3c1e9f0a2d4b7e65"], fingerprint: "chrome" },
      xhttp: { path: "/kT3vQ9xLm2", host: "", mode: "auto", x_padding_bytes: "100-1000" },
      egress: "direct",
    }),
    apps: [App.HAPP],
  },
  { id: "hysteria2", displayName: "Hysteria2", settingsSchemaJson: hy2, defaultSettingsJson: JSON.stringify({ port: 443, obfs: { type: "salamander", password: "••••" } }), apps: [App.HAPP] },
];
const profileRows = [
  { id: "prf_hy", name: "hy2 · 443 · Salamander", protocol: "hysteria2", summary: "UDP 443 · Salamander · Let's Encrypt", nodeCount: 2, userCount: 12, version: 4, warnings: [] },
  { id: "prf_vl", name: "VLESS · 443", protocol: "vless", summary: "XHTTP 443 · REALITY", nodeCount: 0, userCount: 0, version: 1, warnings: [] },
];
const kitNodes = [
  { id: "nod_de1", name: "de1", status: NodeStatus.ONLINE, countryCode: "DE", address: "de1.example.com", protocols: ["hysteria2"], awgBackend: "auto" },
  { id: "nod_fi1", name: "fi1", status: NodeStatus.ONLINE, countryCode: "FI", address: "fi1.example.com", protocols: ["hysteria2"], awgBackend: "auto" },
  { id: "nod_se1", name: "se1", status: NodeStatus.ONLINE, countryCode: "SE", address: "se1.example.com", protocols: [], awgBackend: "auto" },
  { id: "nod_ip1", name: "ip1", status: NodeStatus.ONLINE, countryCode: "NL", address: "203.0.113.10", protocols: [], awgBackend: "auto" },
];
const node = { ...kitNodes[0], location: "Frankfurt", provider: "examplehost", online: [], agentVersion: "0.3.1", lastSeenUnix: Math.floor(Date.now() / 1000) - 5, warp: { state: WarpState.NOT_CONFIGURED } };
const decoy = (nodeName: string) => ({ code: "hy2_decoy_paused", params: { node: nodeName, port: "443", hy2: "hy2 · 443 · Salamander", vless: "VLESS · 443", via: "self", target: "", port80: "unchecked" } });

/** A private cache: every query the screens ask is answered from here; the preview is the only call. */
function Cache({ children }: { children: ReactNode }) {
  const [qc] = useState(() => {
    const c = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false, refetchInterval: false } } });
    c.setQueryData(meQuery.queryKey, { admin: { id: "adm_1", role: Role.OWNER } } as never);
    c.setQueryData(protocolsQuery.queryKey, protocols as never);
    c.setQueryData(profileListQuery.queryKey, profileRows as never);
    c.setQueryData(nodesQuery.queryKey, { nodes: kitNodes } as never);
    c.setQueryData(groupsQuery.queryKey, [{ id: "grp_all", name: "Everyone", profileIds: ["prf_hy"], userCount: 14, dnsPresetId: "" }] as never);
    return c;
  });
  return <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
}

function Demo({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="flex flex-col gap-3">
      <SectionLabel>{title}</SectionLabel>
      <div className="rounded-card-lg border border-dashed border-line p-4">{children}</div>
    </section>
  );
}

type VlessSettings = {
  port?: number;
  transport?: string;
  security?: string;
  tls_mode?: string;
  sni?: string;
  cdn?: { host?: string; port?: number };
  ws?: { path?: string };
  reality?: { target_kind?: string; target?: string; server_names?: string[]; short_ids?: string[]; fingerprint?: string };
  xhttp?: { path?: string; mode?: string };
};

/** Dev only: the preview a panel would draw for these settings (the link of design/vless.md §3, shortened). */
function fakeCalls() {
  const p = profilesApi as unknown as Record<string, unknown>;
  p.previewProfile = async (r: { settingsJson: string }) => {
    const s = JSON.parse(r.settingsJson) as VlessSettings;
    if (s.security === "tls") {
      const cdn = (s.cdn?.host ?? "").trim();
      if (s.tls_mode === "self_signed" && !cdn) {
        return { errors: [{ pointer: "/cdn/host", code: "required", message: "a self-signed origin needs a CDN host" }], warnings: [], clientPreview: "", clientLabel: "Subscription link · URI list" };
      }
      const host = cdn || "de1.example.com";
      const ws = s.transport === "ws";
      const mode = cdn && (s.xhttp?.mode ?? "auto") === "auto" ? "packet-up" : (s.xhttp?.mode ?? "auto");
      const q = [
        "encryption=none",
        "fp=chrome",
        `host=${host}`,
        !ws && `mode=${mode}`,
        `path=${encodeURIComponent((ws ? s.ws?.path : s.xhttp?.path) || "/…")}`,
        "security=tls",
        `sni=${host}`,
        `type=${ws ? "ws" : "xhttp"}`,
      ].filter(Boolean);
      const port = cdn ? (s.cdn?.port ?? 443) : (s.port ?? 443);
      return { errors: [], warnings: [], clientPreview: `vless://••••@${host}:${port}?${q.join("&")}#de1 · vless · ${port}`, clientLabel: "Subscription link · URI list" };
    }
    const own = s.reality?.target_kind !== "site"; // the node's own site: its name is the node's domain
    const target = s.reality?.target ?? "";
    if (!own && !target.includes(":")) {
      const [code, message] = target ? ["format", "must be an external host:port"] : ["required", "enter the site's host:port"];
      return { errors: [{ pointer: "/reality/target", code, message }], warnings: [], clientPreview: "", clientLabel: "Subscription link · URI list" };
    }
    const sni = own ? "de1.example.com" : (s.reality?.server_names?.[0] ?? target.split(":")[0]);
    const tcp = s.transport === "tcp";
    const q = [
      "encryption=none",
      tcp && "flow=xtls-rprx-vision",
      `fp=${s.reality?.fingerprint ?? "chrome"}`,
      !tcp && `mode=${s.xhttp?.mode ?? "auto"}`,
      !tcp && `path=${encodeURIComponent(s.xhttp?.path || "/…")}`,
      "pbk=Zm9vYmFyLWV4YW1wbGUta2V5LW5vdC1yZWFs",
      "security=reality",
      `sid=${s.reality?.short_ids?.[0] ?? "…"}`,
      `sni=${sni}`,
      "spx=%2F1f2e3d4c",
      `type=${tcp ? "tcp" : "xhttp"}`,
    ].filter(Boolean);
    return { errors: [], warnings: [], clientPreview: `vless://••••@de1.example.com:${s.port ?? 443}?${q.join("&")}#de1 · vless · ${s.port ?? 443}`, clientLabel: "Subscription link · URI list" };
  };
  p.createInbound = async (r: { nodeId: string; portOverride: number; validateOnly?: boolean; tlsServerNameOverride?: string }) => {
    const name = kitNodes.find((n) => n.id === r.nodeId)?.name ?? "";
    // the node's own site needs a domain for its Let's Encrypt certificate: an IP node has none until one is typed
    if (r.nodeId === "nod_ip1" && !r.tlsServerNameOverride) throw new ConnectError("reality_needs_domain: address=203.0.113.10&node=ip1", Code.InvalidArgument);
    if (r.validateOnly) return { inbound: { port: r.portOverride || 443, nodeName: name }, warnings: (r.portOverride || 443) === 443 ? [decoy(name)] : [], freePort: 0 };
    await new Promise((res) => setTimeout(res, 600));
    return { inbound: {}, warnings: r.nodeId === "nod_se1" ? [] : [decoy(name)] };
  };
}

function EditorDemo() {
  useState(fakeCalls); // before the first preview asks
  return (
    <Cache>
      <ProfileEditorScreen />
    </Cache>
  );
}

function AddDemo() {
  const [open, setOpen] = useState<"de1" | "ip1" | null>(null);
  const ipNode = { ...node, ...kitNodes[3], location: "Amsterdam" };
  return (
    <Cache>
      <div className="flex flex-wrap gap-2">
        <Button
          onClick={() => {
            fakeCalls();
            setOpen("de1");
          }}
        >
          Add “VLESS · 443” to de1 (Hysteria2 there)
        </Button>
        <Button
          onClick={() => {
            fakeCalls();
            setOpen("ip1");
          }}
        >
          Add “VLESS · 443” to ip1 (an IP, no domain)
        </Button>
      </div>
      <AddInbound open={open === "de1"} onOpenChange={(o) => setOpen(o ? "de1" : null)} data={{ node, inbounds: [{ profileId: "prf_hy" }], portChecks: [] } as never} initialProfile="prf_vl" />
      <AddInbound open={open === "ip1"} onOpenChange={(o) => setOpen(o ? "ip1" : null)} data={{ node: ipNode, inbounds: [], portChecks: [] } as never} initialProfile="prf_vl" />
    </Cache>
  );
}

function DeployDemo() {
  const [open, setOpen] = useState(false);
  return (
    <Cache>
      <Button
        onClick={() => {
          fakeCalls();
          setOpen(true);
        }}
      >
        Put “VLESS · 443” on de1 / fi1 / se1
      </Button>
      <DeployDialog open={open} onOpenChange={setOpen} profile={{ id: "prf_vl", name: "VLESS · 443", protocol: "vless" }} inbounds={[]} preselect={["nod_de1", "nod_fi1", "nod_se1"]} />
    </Cache>
  );
}

export function VlessKit() {
  return (
    <div className="relative left-1/2 flex w-[min(64rem,calc(100vw-2rem))] -translate-x-1/2 flex-col gap-6">
      <Demo title="VLESS: a new profile (the site segment, string lists, TLS and CDN fields, XHTTP hidden for TCP, the TCP 443 hint)">
        <EditorDemo />
      </Demo>
      <Demo title="VLESS: the add dialog on a node with Hysteria2 (hy2_decoy_paused)">
        <AddDemo />
      </Demo>
      <Demo title="VLESS: put on nodes — the same sentence in the rows (se1 has no Hysteria2)">
        <DeployDemo />
      </Demo>
    </div>
  );
}
