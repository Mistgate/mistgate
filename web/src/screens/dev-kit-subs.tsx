import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { useState } from "react";
import { SectionLabel } from "@/components/ui/bits";
import { HappPing } from "@/gen/mistgate/admin/v1/subscription_pb";
import type { Settings } from "@/screens/subscriptions/model";
import { TextsTab } from "@/screens/subscriptions/texts";

// Development only (the dev kit): Subscriptions → "Names & texts" with how Happ pings the servers, on made-up settings.
// The block carries data-shot for the screenshot script.

const settings = {
  title: "",
  announcement: "",
  supportUrl: "",
  updateIntervalHours: 12,
  serverNameTemplate: "",
  rules: [],
  apps: [],
  defaultDnsPresetId: "",
  userPage: { showAnnouncement: true, showSupport: true, showQr: true },
  happPing: HappPing.TCP,
  happPingOnOpen: true,
} as unknown as Settings;

export function SubsKit() {
  const [qc] = useState(() => new QueryClient({ defaultOptions: { queries: { staleTime: Infinity, retry: false } } }));
  return (
    <QueryClientProvider client={qc}>
      <section data-shot="subs-texts" className="relative left-1/2 flex w-[min(calc(100vw-2rem),1080px)] -translate-x-1/2 flex-col gap-3">
        <SectionLabel>Subscriptions → Names &amp; texts: ping in Happ</SectionLabel>
        <TextsTab data={{ settings, effectiveTitle: "Mistgate" }} />
      </section>
    </QueryClientProvider>
  );
}
