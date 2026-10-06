import { createMemoryHistory, createRootRoute, createRouter, RouterProvider } from "@tanstack/react-router";
import type { ReactElement } from "react";
import { validatePaging } from "@/lib/paging";

/**
 * A real router on a memory history, for the tests of what lives in the URL: the page of a list. `ui` is the one route's
 * page; `url` is where the router starts ("/?page=3"). Wrap the element in a QueryClientProvider yourself if `ui` needs one.
 */
export function memoryRouter(ui: ReactElement, url = "/") {
  // the router restores the scroll position after a navigation; jsdom has no scrollTo
  window.scrollTo = (() => {}) as typeof window.scrollTo;
  const root = createRootRoute({ component: () => ui, validateSearch: (s: Record<string, unknown>) => ({ ...validatePaging(s), ...(typeof s.tab === "string" ? { tab: s.tab } : {}) }) });
  const router = createRouter({ routeTree: root, history: createMemoryHistory({ initialEntries: [url] }) });
  return { router, element: <RouterProvider router={router} /> };
}
