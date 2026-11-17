import { Navigate, Outlet, useNavigate, useParams } from "@tanstack/react-router";
import type { ReactNode } from "react";
import { Tabs } from "@/components/ui/tabs";
import { useT } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { AdminsPage } from "./settings-pages/admins";
import { AuditPage } from "./settings-pages/audit";
import { BackupsPage } from "./settings-pages/backups";
import { DomainsPage } from "./settings-pages/domains";
import { InterfacePage } from "./settings-pages/interface";
import { SecurityPage } from "./settings-pages/security";
import { SessionsPage } from "./settings-pages/sessions";
import { SxRoot } from "./settings-pages/sx-bits";
import { SystemPage } from "./settings-pages/system";

// Interface first: it is where /settings lands, and it works for every role. The order is the board's (PnSettings: if sys sec ses aud adm dom bk).
export const pages = [
  { id: "interface", label: "settings.interface" },
  { id: "system", label: "settings.system" },
  { id: "security", label: "settings.security" },
  { id: "sessions", label: "settings.sessions" },
  { id: "audit", label: "settings.audit" },
  { id: "admins", label: "settings.admins" },
  { id: "domains", label: "settings.domains" },
  { id: "backups", label: "settings.backups" },
] as const satisfies readonly { id: string; label: MessageKey }[];

export type PageId = (typeof pages)[number]["id"];
export const isPage = (v: string): v is PageId => pages.some((p) => p.id === v);

/**
 * The frame of Settings (the board's sb-h and nd-tabs): the title and its line, the eight tabs, and under the strip whatever `children`
 * is (the page of the tab). The route carries the tab (/settings/$section); a scene that has no route hands `onPick` of its own.
 * Each tab states its own load and failure under the strip (SxGate), so the frame stays put while a tab waits.
 */
export function SettingsFrame({ section, onPick, children }: { section: PageId; onPick: (id: PageId) => void; children: ReactNode }) {
  const t = useT();
  return (
    <SxRoot>
      <header className="sb-h">
        <h1>{t("nav.settings")}</h1>
        <p>{t("sx.sum")}</p>
      </header>
      <Tabs
        className="sx-tabs"
        aria-label={t("settings.nav")}
        items={pages.map((p) => ({ value: p.id, label: t(p.label), content: p.id === section ? children : null }))}
        value={section}
        onValueChange={(v) => isPage(v) && onPick(v)}
      />
    </SxRoot>
  );
}

export function SettingsLayout() {
  const { section } = useParams({ strict: false });
  const navigate = useNavigate();
  return (
    <SettingsFrame section={section && isPage(section) ? section : "interface"} onPick={(id) => void navigate({ to: "/settings/$section", params: { section: id } })}>
      <Outlet />
    </SettingsFrame>
  );
}

/** /settings has no page of its own: land on the first one that works for everybody. */
export function SettingsIndex() {
  return <Navigate to="/settings/$section" params={{ section: "interface" }} replace />;
}

/** The page of a tab, by id (the route's component and the scenes both come here). */
export function SettingsTab({ id }: { id: PageId }) {
  switch (id) {
    case "interface":
      return <InterfacePage />;
    case "system":
      return <SystemPage />;
    case "security":
      return <SecurityPage />;
    case "sessions":
      return <SessionsPage />;
    case "audit":
      return <AuditPage />;
    case "admins":
      return <AdminsPage />;
    case "domains":
      return <DomainsPage />;
    case "backups":
      return <BackupsPage />;
  }
}

export function SettingsPage() {
  const { section } = useParams({ strict: false });
  if (!section || !isPage(section)) return <Navigate to="/settings/$section" params={{ section: "interface" }} replace />;
  return <SettingsTab id={section} />;
}
