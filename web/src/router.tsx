import {
  createRootRoute,
  createRoute,
  createRouter,
  lazyRouteComponent,
  Navigate,
  Outlet,
  redirect,
} from "@tanstack/react-router";
import { lazy, Suspense } from "react";
import { Button } from "@/components/ui/button";
import { ToastProvider } from "@/components/ui/toast";
import { sections } from "@/components/nav";
import { useT } from "@/i18n";
import { basepath, isUnauthenticated } from "@/lib/api";
import { bootBrand, loginInfoQuery } from "@/lib/instance";
import { validatePaging, type PagingSearch } from "@/lib/paging";
import { goToSignIn, meQuery, queryClient, setSignedOutHandler } from "@/lib/session";
import { validateHealthSearch } from "@/screens/health/tabs";
import { validateNodeSearch } from "@/screens/node/tabs";
import { validateSubsSearch } from "@/screens/subscriptions/tabs";

// Every screen is its own chunk: the first paint only needs the router, the session check and the
// chunk of the screen it lands on (sign-in, or the shell and the section).
// The mark (and the badge inside it) is not worth the first load: the two screens that show it fetch it when they appear.
const AnimatedMark = lazy(() => import("@/components/brand").then((m) => ({ default: m.AnimatedMark })));
const SectionScreen = lazy(() => import("@/screens/section").then((m) => ({ default: m.SectionScreen })));

const root = createRootRoute({
  // The brand (wordmark, logo, default accent and language) is public: apply it before the first screen so
  // the sign-in page never flashes the built-in one. Never throws; an unreachable panel keeps the defaults.
  beforeLoad: () => bootBrand(),
  component: () => (
    <ToastProvider>
      <Outlet />
    </ToastProvider>
  ),
  notFoundComponent: () => <Navigate to="/" replace />,
});

/** Login and setup make no sense with a live session: send the admin home. */
async function leaveIfSignedIn() {
  try {
    await queryClient.ensureQueryData(meQuery);
  } catch {
    return; // not signed in (or panel unreachable): stay on the page
  }
  throw redirect({ to: "/" });
}

const login = createRoute({
  getParentRoute: () => root,
  path: "/login",
  beforeLoad: leaveIfSignedIn,
  component: lazyRouteComponent(() => import("@/screens/login"), "LoginScreen"),
});
const setup = createRoute({
  getParentRoute: () => root,
  path: "/setup",
  beforeLoad: async () => {
    await leaveIfSignedIn();
    // Setup is for the very first admin: once one exists the link is dead, go to sign-in.
    let open = true;
    try {
      open = (await queryClient.ensureQueryData(loginInfoQuery)).setupOpen;
    } catch {
      // panel unreachable: show the page, its own calls report the failure
    }
    if (!open) throw redirect({ to: "/login" });
  },
  component: lazyRouteComponent(() => import("@/screens/setup"), "SetupScreen"),
});

// Development only: every UI primitive on one page (vite drops this branch from production builds).
const devKit = import.meta.env.DEV
  ? createRoute({
      getParentRoute: () => root,
      path: "/dev-kit",
      component: lazyRouteComponent(() => import("@/screens/dev-kit")),
    })
  : null;

// Everything else needs a session; Me() decides.
const app = createRoute({
  getParentRoute: () => root,
  id: "app",
  beforeLoad: async () => {
    try {
      await queryClient.ensureQueryData(meQuery);
    } catch (e) {
      if (isUnauthenticated(e)) throw redirect({ to: "/login" });
      throw e;
    }
  },
  component: lazyRouteComponent(() => import("@/screens/shell"), "Shell"),
  pendingComponent: Loading,
  errorComponent: GuardError,
});

// "?add=1" opens the add-node window once (the end of the setup wizard); the Overview drops it at once.
const overview = createRoute({
  getParentRoute: () => app,
  path: "/",
  validateSearch: (s: Record<string, unknown>): { add?: 1 } => (s.add === 1 || s.add === "1" || s.add === true ? { add: 1 } : {}),
  component: lazyRouteComponent(() => import("@/screens/overview"), "OverviewScreen"),
});

const nodes = createRoute({
  getParentRoute: () => app,
  path: "/nodes",
  component: lazyRouteComponent(() => import("@/screens/nodes"), "NodesScreen"),
});
const node = createRoute({
  getParentRoute: () => app,
  path: "/nodes/$id",
  validateSearch: validateNodeSearch,
  component: lazyRouteComponent(() => import("@/screens/node"), "NodeScreen"),
});

// "?create" opens the create-user dialog (the palette's "Create user" action uses it); "?group=<id>" lists one group's users
// (the profile page links to it). "?tab=groups" is the Groups tab, where "?group=<id>" opens that group.
const users = createRoute({
  getParentRoute: () => app,
  path: "/users",
  validateSearch: (s: Record<string, unknown>): { create?: true; group?: string; tab?: "groups" } & PagingSearch => ({
    ...(s.create === true || s.create === "true" || s.create === "1" ? { create: true as const } : {}),
    ...(typeof s.group === "string" && s.group !== "" ? { group: s.group } : {}),
    ...(s.tab === "groups" ? { tab: "groups" as const } : validatePaging(s)),
  }),
  component: lazyRouteComponent(() => import("@/screens/users"), "UsersScreen"),
});
// "?add=device" opens "Add device" (the link window sends the owner here when self-service is off).
const user = createRoute({
  getParentRoute: () => app,
  path: "/users/$id",
  validateSearch: (s: Record<string, unknown>): { add?: "device" } => (s.add === "device" ? { add: "device" } : {}),
  component: lazyRouteComponent(() => import("@/screens/users"), "UserScreen"),
});

const profiles = createRoute({
  getParentRoute: () => app,
  path: "/profiles",
  component: lazyRouteComponent(() => import("@/screens/profiles"), "ProfilesScreen"),
});
// "?node=<id>": made from that node's page; the new profile goes on it and the editor returns there.
const profileNew = createRoute({
  getParentRoute: () => app,
  path: "/profiles/new",
  validateSearch: (s: Record<string, unknown>): { node?: string } => (typeof s.node === "string" && s.node !== "" ? { node: s.node } : {}),
  component: lazyRouteComponent(() => import("@/screens/profiles"), "ProfileEditorScreen"),
});
const profile = createRoute({
  getParentRoute: () => app,
  path: "/profiles/$id",
  component: lazyRouteComponent(() => import("@/screens/profiles"), "ProfileEditorScreen"),
});

const subscriptions = createRoute({
  getParentRoute: () => app,
  path: "/subscriptions",
  validateSearch: validateSubsSearch,
  component: lazyRouteComponent(() => import("@/screens/subscriptions"), "SubscriptionsScreen"),
});

// Settings has sub-pages (/settings/interface, ...); the sections without a backend yet are one calm screen each.
const settings = createRoute({
  getParentRoute: () => app,
  path: "/settings",
  component: lazyRouteComponent(() => import("@/screens/settings"), "SettingsLayout"),
});
const settingsIndex = createRoute({
  getParentRoute: () => settings,
  path: "/",
  component: lazyRouteComponent(() => import("@/screens/settings"), "SettingsIndex"),
});
const settingsPage = createRoute({
  getParentRoute: () => settings,
  path: "$section",
  // the audit log's place (`before`, `size`) and the backups' page
  validateSearch: validatePaging,
  component: lazyRouteComponent(() => import("@/screens/settings"), "SettingsPage"),
});

const health = createRoute({
  getParentRoute: () => app,
  path: "/health",
  validateSearch: validateHealthSearch,
  component: lazyRouteComponent(() => import("@/screens/health"), "HealthScreen"),
});

const updates = createRoute({
  getParentRoute: () => app,
  path: "/updates",
  component: lazyRouteComponent(() => import("@/screens/updates"), "UpdatesScreen"),
});

const integrations = createRoute({
  getParentRoute: () => app,
  path: "/integrations",
  // the page of the approvals history
  validateSearch: validatePaging,
  component: lazyRouteComponent(() => import("@/screens/integrations"), "IntegrationsScreen"),
});

const built = new Set(["overview", "nodes", "users", "profiles", "subscriptions", "health", "updates", "integrations", "settings"]);
const placeholderRoutes = sections
  .filter((s) => !built.has(s.id))
  .map((s) => createRoute({ getParentRoute: () => app, path: s.to, component: () => <SectionScreen id={s.id} /> }));

const routeTree = root.addChildren([
  login,
  setup,
  ...(devKit ? [devKit] : []),
  app.addChildren([
    overview,
    nodes,
    node,
    users,
    user,
    profiles,
    profileNew,
    profile,
    subscriptions,
    health,
    updates,
    integrations,
    ...placeholderRoutes,
    settings.addChildren([settingsIndex, settingsPage]),
  ]),
]);

export const router = createRouter({ routeTree, basepath });

// A poll that finds the session gone (expired, ended from another device) lands on the sign-in screen.
setSignedOutHandler(() => {
  const path = router.state.location.pathname;
  if (path.endsWith("/login") || path.endsWith("/setup")) return;
  goToSignIn();
});

declare module "@tanstack/react-router" {
  interface Register {
    router: typeof router;
  }
}

/** The lazy mark in a box of its own size, so nothing jumps when it arrives. */
function Mark({ size, mode }: { size: number; mode: "intro" | "loading" }) {
  return (
    <span aria-hidden className="block flex-none" style={{ width: size, height: size }}>
      <Suspense fallback={null}>
        <AnimatedMark size={size} mode={mode} />
      </Suspense>
    </span>
  );
}

function Loading() {
  const t = useT();
  return (
    <div role="status" className="flex flex-col items-center gap-3 p-16 text-muted">
      <Mark size={56} mode="loading" />
      <p>{t("common.loading")}</p>
    </div>
  );
}

function GuardError({ reset }: { reset: () => void }) {
  const t = useT();
  return (
    <div className="mx-auto flex max-w-sm flex-col items-center gap-4 px-4 py-24 text-center">
      <Mark size={72} mode="intro" />
      <p className="text-[22px] font-extrabold tracking-[-0.03em]">{t("err.guard")}</p>
      <p className="text-[13px] text-muted">{t("err.network")}</p>
      <Button variant="primary" size="lg" onClick={reset}>
        {t("common.retry")}
      </Button>
    </div>
  );
}
