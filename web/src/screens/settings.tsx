import { Link, Navigate, Outlet, useParams } from "@tanstack/react-router";
import { useCallback, useEffect, useRef, useState } from "react";
import { PageTitle } from "@/components/ui/bits";
import { useT } from "@/i18n";
import type { MessageKey } from "@/i18n/en";
import { AdminsPage } from "./settings-pages/admins";
import { AuditPage } from "./settings-pages/audit";
import { BackupsPage } from "./settings-pages/backups";
import { DomainsPage } from "./settings-pages/domains";
import { InterfacePage } from "./settings-pages/interface";
import { SecurityPage } from "./settings-pages/security";
import { SessionsPage } from "./settings-pages/sessions";
import { SystemPage } from "./settings-pages/system";

// Interface first: it is where /settings lands, and it works for every role. The pages that only explain what is not
// future settings pages with no controls yet come last.
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

type PageId = (typeof pages)[number]["id"];
const isPage = (v: string): v is PageId => pages.some((p) => p.id === v);

// the strip on the phone fades at an edge while there is more to scroll that way
const fades = {
  none: undefined,
  right: "linear-gradient(to right, #000 calc(100% - 32px), transparent)",
  left: "linear-gradient(to left, #000 calc(100% - 32px), transparent)",
  both: "linear-gradient(to right, transparent, #000 32px, #000 calc(100% - 32px), transparent)",
};

export function SettingsLayout() {
  const t = useT();
  const { section } = useParams({ strict: false });
  const nav = useRef<HTMLElement>(null);
  const [fade, setFade] = useState<keyof typeof fades>("none");

  const measure = useCallback(() => {
    const n = nav.current;
    if (!n) return;
    const left = n.scrollLeft > 1;
    const right = n.scrollLeft + n.clientWidth < n.scrollWidth - 1;
    setFade(left && right ? "both" : left ? "left" : right ? "right" : "none");
  }, []);

  // the active page sits in the middle of the strip (on the phone it may start off screen); the page itself never jumps
  useEffect(() => {
    const n = nav.current;
    const a = [...(n?.querySelectorAll<HTMLElement>("[data-page]") ?? [])].find((x) => x.dataset.page === section);
    if (n && a && n.scrollWidth > n.clientWidth) n.scrollTo({ left: a.offsetLeft - (n.clientWidth - a.offsetWidth) / 2, behavior: "smooth" });
    measure();
  }, [section, measure]);
  useEffect(() => {
    window.addEventListener("resize", measure);
    return () => window.removeEventListener("resize", measure);
  }, [measure]);

  return (
    <div className="flex flex-col gap-3.5">
      <PageTitle>{t("nav.settings")}</PageTitle>
      <div className="flex flex-col gap-3.5 md:flex-row md:items-start">
        <nav
          ref={nav}
          aria-label={t("settings.nav")}
          onScroll={measure}
          style={{ maskImage: fades[fade] }}
          className="no-scrollbar relative flex max-w-full flex-none gap-0.5 overflow-x-auto md:w-[180px] md:flex-col"
        >
          {pages.map((p) => (
            <Link
              key={p.id}
              data-page={p.id}
              to="/settings/$section"
              params={{ section: p.id }}
              className="flex h-[34px] flex-none items-center rounded-ctl px-3 text-[13px] font-semibold whitespace-nowrap text-muted transition-colors data-[status=active]:bg-surface-2 data-[status=active]:font-bold data-[status=active]:text-fg"
            >
              {t(p.label)}
            </Link>
          ))}
        </nav>
        <div className="flex w-full min-w-0 flex-1 flex-col gap-3.5">
          <Outlet />
        </div>
      </div>
    </div>
  );
}

/** /settings has no page of its own: land on the first one that works for everybody. */
export function SettingsIndex() {
  return <Navigate to="/settings/$section" params={{ section: "interface" }} replace />;
}

export function SettingsPage() {
  const { section } = useParams({ strict: false });
  if (!section || !isPage(section)) return <Navigate to="/settings/$section" params={{ section: "interface" }} replace />;
  switch (section) {
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
