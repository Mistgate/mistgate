import { useQuery } from "@tanstack/react-query";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { useCallback, useRef, useState } from "react";
import { Button } from "@/components/ui/button";
import { Modal } from "@/components/ui/modal";
import { Pending, QueryError } from "@/components/ui/query-error";
import { ClientCompatActual, ClientCompatState, outdatedDevicesQuery } from "@/lib/clientcompat";
import { useTx } from "@/screens/users/t";
import { ClientsTab } from "./clients";
import { DnsTab } from "./dns";
import { FormatsTab } from "./formats";
import { LeaveGuard } from "./guard";
import { HeadersTab } from "./headers";
import { MihomoTab } from "./mihomo";
import { PageTab } from "./page";
import { SbTabs } from "./parts";
import { settingsQuery } from "./queries";
import { RulesTab } from "./rules";
import { subsTabs, type SubsTab } from "./tabs";
import { TextsTab } from "./texts";

/** The dev kit's scene opens the screen on a tab of its own and keeps it there: clicks on the strip do not leave the scene's page. */
export type SubsDemo = { tab?: SubsTab; user?: string };

/** /subscriptions: formats and clients, serving rules, headers and texts, the user page and the DNS presets. */
export function SubscriptionsScreen({ demo }: { demo?: SubsDemo }) {
  const t = useTx();
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as { tab?: SubsTab; user?: string };
  const [local, setLocal] = useState<SubsTab>(demo?.tab ?? "formats");
  const tab = demo ? local : (search.tab ?? "formats");
  const user = demo ? demo.user : search.user;
  const q = useQuery(settingsQuery);
  // the badge of "App versions": the devices that need a look (the same first page the tab draws, so one request serves both)
  const devices = useQuery(outdatedDevicesQuery({ state: ClientCompatState.UNSPECIFIED, actual: ClientCompatActual.UNSPECIFIED, userId: "", page: 1, size: 50 }));
  const attention = (devices.data?.counts ?? []).filter((c) => c.state !== ClientCompatState.OK).reduce((n, c) => n + c.devices, 0);

  const dirty = useRef(false);
  const [leaving, setLeaving] = useState<SubsTab | null>(null);
  const setDirty = useCallback((d: boolean) => void (dirty.current = d), []);

  const move = (next: SubsTab) => {
    dirty.current = false;
    if (demo) setLocal(next);
    else void navigate({ to: "/subscriptions", search: next === "formats" ? {} : { tab: next }, replace: true } as never);
  };
  // the links inside the tabs ("Open Who gets what") and the strip: a tab with unsaved changes asks first
  const go = (next: string) => {
    if (next === tab) return;
    if (dirty.current) setLeaving(next as SubsTab);
    else move(next as SubsTab);
  };

  const body = (content: (s: NonNullable<typeof q.data>) => React.ReactNode) => (q.isPending ? <Pending /> : q.isError ? <QueryError error={q.error} onRetry={() => void q.refetch()} /> : content(q.data));

  return (
    <LeaveGuard.Provider value={setDirty}>
      <div className="sbp" data-shot="subs-page">
        <header className="sb-h">
          <h1>{t("subs.title")}</h1>
          <p>{t("subs.sub")}</p>
        </header>
        <SbTabs
          label={t("subs.tabs")}
          value={tab}
          onValueChange={go}
          items={subsTabs.map((id) => ({ value: id, label: t(`subs.tab.${id}`), ...(id === "clients" && attention > 0 ? { badge: attention } : {}) }))}
        />
        <div className="sb-tab" key={tab}>
          {tab === "dns" ? (
            <DnsTab />
          ) : tab === "clients" ? (
            <ClientsTab userId={user} onClearUser={() => (demo ? undefined : void navigate({ to: "/subscriptions", search: { tab: "clients" }, replace: true } as never))} />
          ) : (
            body((d) =>
              tab === "formats" ? (
                <FormatsTab go={go} />
              ) : tab === "rules" ? (
                <RulesTab settings={d.settings} />
              ) : tab === "texts" ? (
                <TextsTab data={d} />
              ) : tab === "headers" ? (
                <HeadersTab data={d} />
              ) : tab === "mihomo" ? (
                <MihomoTab data={d} />
              ) : (
                <PageTab settings={d.settings} />
              ),
            )
          )}
        </div>
      </div>
      <Modal
        open={leaving !== null}
        onOpenChange={(o) => !o && setLeaving(null)}
        title={t("sx.leave.title")}
        description={t("sx.leave.text")}
        closeLabel={t("common.close")}
        footer={
          <>
            <Button
              onClick={() => {
                const next = leaving;
                setLeaving(null);
                if (next) move(next);
              }}
            >
              {t("sx.leave.go")}
            </Button>
            <Button variant="primary" onClick={() => setLeaving(null)}>
              {t("sx.leave.stay")}
            </Button>
          </>
        }
      />
    </LeaveGuard.Provider>
  );
}
