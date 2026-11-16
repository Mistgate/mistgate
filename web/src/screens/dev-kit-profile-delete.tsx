import { Code, ConnectError } from "@connectrpc/connect";
import { useState } from "react";
import { InboundState, NodeStatus } from "@/gen/mistgate/admin/v1/common_pb";
import { isLang, setLang } from "@/i18n";
import { groups as groupsApi, nodes as nodesApi, profiles as profilesApi, relays as relaysApi } from "@/lib/api";
import { nodesQuery } from "@/lib/queries";
import { relaysQuery } from "@/lib/relay";
import { setTheme } from "@/lib/theme";
import { Mock } from "@/screens/dev-kit-node";
import { DeleteSection } from "@/screens/profiles/editor";
import { WherePanel } from "@/screens/profiles/where";
import { groupsQuery } from "@/screens/users/rpc";

// Development only, /dev-kit?only=profile-delete&scene=where|delete|clean&lang=ru&theme=dark: taking a profile off several nodes,
// on made-up data and a private query cache (no server). The profile "Main 443" is on eleven nodes (de1, nl1, fi1 ...), fi1 is
// not connected. The screenshots click through it by the address: the nodes card ("where"), the delete window of a profile that
// is on nodes ("delete"), and of one that is on none ("clean"). &fail=1 makes the panel refuse the call (the error state);
// a call that works takes the servers out of the list, as the page's own refetch would.

const params = new URLSearchParams(location.search);
if (params.get("only") === "profile-delete") {
  const th = params.get("theme");
  if (th === "light" || th === "dark") setTheme(th);
  const lg = params.get("lang");
  if (isLang(lg)) setLang(lg);
}

const names: [string, string, string][] = [
  ["de1", "DE", "Frankfurt"],
  ["nl1", "NL", "Amsterdam"],
  ["fi1", "FI", "Helsinki"],
  ["se1", "SE", "Stockholm"],
  ["pl1", "PL", "Warsaw"],
  ["dk1", "DK", "Copenhagen"],
  ["lv1", "LV", "Riga"],
  ["cz1", "CZ", "Prague"],
  ["at1", "AT", "Vienna"],
  ["ch1", "CH", "Zurich"],
  ["fr1", "FR", "Paris"],
];
const nodeRows = names.map(([name, countryCode, location]) => ({
  id: `nod_${name}`, name, countryCode, location, address: `${name}.example.com`, protocols: ["hysteria2"], awgBackend: "auto", status: name === "fi1" ? NodeStatus.DOWN : NodeStatus.ONLINE,
}));
const inboundRow = (name: string, i: number) => ({
  id: `inb_${name}`, profileId: "prf_main", profileName: "Main 443", protocol: "hysteria2", nodeId: `nod_${name}`, nodeName: name, port: 443, state: i === 3 ? InboundState.PENDING : InboundState.ACTIVE, lastError: "", egress: "direct",
});
const all = names.map(([n], i) => inboundRow(n, i));
const profile = { id: "prf_main", name: "Main 443", protocol: "hysteria2", summary: "UDP 443 · Salamander", nodeCount: 11, userCount: 14, version: 3, warnings: [], formats: [] };
const group = { id: "grp_all", name: "Family", profileIds: ["prf_main"], userCount: 9, dnsPresetId: "", happNodes: 11, amneziaNodes: 0, color: "" };
const group2 = { id: "grp_two", name: "Friends", profileIds: ["prf_main"], userCount: 5, dnsPresetId: "", happNodes: 11, amneziaNodes: 0, color: "" };

type Rec = Record<string, unknown>;

function Scene({ scene }: { scene: string }) {
  const [inbounds, setInbounds] = useState<typeof all>(scene === "clean" ? [] : all);
  useState(() => {
    // what the panel would answer, so a refetch finds the same rows
    const fail = params.get("fail") === "1";
    (profilesApi as unknown as Rec).removeProfileFromNodes = async (req: { nodeIds: string[]; deleteProfile: boolean }) => {
      await new Promise((r) => setTimeout(r, 250));
      if (fail) throw new ConnectError("profile_deployed: nodes=pl1%2C+dk1", Code.FailedPrecondition);
      setInbounds((cur) => cur.filter((i) => !req.nodeIds.includes(i.nodeId)));
      return { removed: req.nodeIds.length, profileDeleted: req.deleteProfile, relaysRemoved: 0 };
    };
    (profilesApi as unknown as Rec).deleteProfile = async () => ({});
    (nodesApi as unknown as Rec).listNodes = async () => ({ nodes: nodeRows });
    (groupsApi as unknown as Rec).listGroups = async () => ({ groups: [group, group2] });
    (relaysApi as unknown as Rec).listRelays = async () => ({ relays: [] });
  });
  return (
    <Mock
      fill={[
        [groupsQuery.queryKey, [group, group2]],
        [nodesQuery.queryKey, { nodes: nodeRows }],
        [relaysQuery("").queryKey, []],
      ]}
    >
      <div className="mx-auto flex w-full max-w-[1100px] flex-col gap-3.5" data-shot={`profile-delete-${scene}`}>
        {scene === "where" ? (
          <WherePanel profile={profile as never} inbounds={inbounds as never} />
        ) : (
          <DeleteSection profile={profile as never} inbounds={inbounds as never} />
        )}
      </div>
    </Mock>
  );
}

export function ProfileDeleteKit() {
  const scene = params.get("scene") ?? "where";
  return <Scene scene={scene} />;
}
