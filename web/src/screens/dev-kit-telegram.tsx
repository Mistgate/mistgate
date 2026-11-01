import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState } from "react";
import { users } from "@/lib/api";
import { profileListQuery, protocolsQuery } from "@/screens/users/rpc";
import { TelegramScreen } from "@/screens/telegram";

// Development only (the dev kit): the Telegram tab on made-up profiles, people and links. The
// screen itself is the production component; only its Connect client and profile cache are replaced.
const mockProfiles = [
  { id: "prf_tg_de", name: "Telegram de1", protocol: "mtproto", summary: "TCP 443 - Fake TLS", nodeCount: 1, userCount: 2, version: 1, formats: [], warnings: [] },
  { id: "prf_tg_fi", name: "Telegram fi1", protocol: "mtproto", summary: "TCP 8443 - Fake TLS", nodeCount: 1, userCount: 1, version: 1, formats: [], warnings: [] },
] as never;

const mockPeople = [
  { id: "usr_marina", name: "Marina", apps: { happ: true, amnezia: true, telegram: true }, accessTelegram: true },
  { id: "usr_oleg", name: "Oleg", apps: { happ: true, amnezia: false, telegram: true }, accessTelegram: true },
] as never[];

const mockLinks: Record<string, unknown[]> = {
  usr_marina: [
    {
      nodeId: "nod_de1", nodeName: "de1", profileId: "prf_tg_de", profileName: "Telegram de1",
      url: "tg://proxy?server=de1.example.com&port=443&secret=ee00112233445566778899aabbccddeeff7777772e676f6f676c652e636f6d",
    },
    {
      nodeId: "nod_fi1", nodeName: "fi1", profileId: "prf_tg_fi", profileName: "Telegram fi1",
      url: "tg://proxy?server=fi1.example.com&port=8443&secret=ee00112233445566778899aabbccddeeff7777772e676f6f676c652e636f6d",
    },
  ],
  usr_oleg: [
    {
      nodeId: "nod_de1", nodeName: "de1", profileId: "prf_tg_de", profileName: "Telegram de1",
      url: "tg://proxy?server=de1.example.com&port=443&secret=eeffeeddccbbaa998877665544332211007777772e676f6f676c652e636f6d",
    },
  ],
};

let patched = false;

function patchApi() {
  if (patched) return;
  patched = true;
  const client = users as unknown as {
    listUsers: (req: unknown) => Promise<{ users: unknown[] }>;
    getTelegramLinks: (req: { userId: string }) => Promise<{ links: unknown[] }>;
    getSubscriptionLink: (req: { userId: string }) => Promise<{ url: string; pagePassword: string }>;
  };
  client.listUsers = async () => ({ users: mockPeople });
  client.getTelegramLinks = async (req) => ({ links: mockLinks[req.userId] ?? [] });
  client.getSubscriptionLink = async (req) => ({
    url: `https://vpn.example.com/demo/${req.userId}`,
    pagePassword: "demo-pass",
  });
}

export function TelegramKit() {
  const [qc] = useState(() => {
    patchApi();
    const c = new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false } } });
    c.setQueryData(profileListQuery.queryKey, mockProfiles);
    c.setQueryData(protocolsQuery.queryKey, [{ id: "mtproto", displayName: "MTProto", apps: [] }] as never);
    return c;
  });
  return (
    <QueryClientProvider client={qc}>
      <TelegramScreen initialOpen />
    </QueryClientProvider>
  );
}
