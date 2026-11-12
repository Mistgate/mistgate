import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
import { isStepUpCancelled, useStepUp } from "@/components/step-up";
import { useToast } from "@/components/ui/toast";
import { useT } from "@/i18n";
import { clientApi } from "./api";
import { errorText } from "./errors";
import { plain } from "./plain";

// The "Client API" switch (client_api.proto): whether the Mistgate desktop app may find the panel and connect to it directly.
// Everybody who sees Subscriptions reads it; only the owner changes it, with a step-up. Off by default. The words are in
// i18n/subs.ts ("subs.clientApi.").

/** Who uses the Mistgate app: everybody, the people who have it on, and the devices connected with a key of their own. */
export type ClientApiStats = { people: number; peopleOn: number; devices: number };
export type ClientApiState = { enabled: boolean; stats?: ClientApiStats };

export const clientApiQuery = queryOptions({
  queryKey: ["clientApi"],
  queryFn: async ({ signal }): Promise<ClientApiState> => {
    const r = await clientApi.getClientApi({}, { signal });
    const stats = r.stats && { people: r.stats.people, peopleOn: r.stats.peopleOn, devices: r.stats.devices };
    return { ...plain(r.settings ?? { enabled: false }), ...(stats && { stats }) };
  },
});

/** Switches the client API; the toast says what it is now. */
export function useClientApiUpdate() {
  const t = useT();
  const toast = useToast();
  const qc = useQueryClient();
  const guard = useStepUp();
  return useMutation({
    mutationFn: async (enabled: boolean) => plain((await guard(() => clientApi.updateClientApi({ enabled }))).settings ?? { enabled }),
    onSuccess: (s) => {
      void qc.invalidateQueries({ queryKey: clientApiQuery.queryKey });
      toast(t(s.enabled ? "subs.clientApi.toastOn" : "subs.clientApi.toastOff"));
    },
    onError: (e) => {
      if (!isStepUpCancelled(e)) toast.error(errorText(e, t));
    },
  });
}
