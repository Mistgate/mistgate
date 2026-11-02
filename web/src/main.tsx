import { QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { DirectionProvider } from "@base-ui/react/direction-provider";
import { StrictMode, type ReactNode } from "react";
import { createRoot } from "react-dom/client";
import "./index.css";
import "@/lib/theme"; // apply the saved theme and accent before the first paint
import "@/lib/accent";
import { dirOf, useLang } from "@/i18n";
import { queryClient } from "@/lib/session";
import { hideSplash } from "@/lib/splash";
import { router } from "./router";

// The splash (index.html) stays until the first real screen is ready: the session check, the brand and the screen's
// chunk are done. No timer: a slow panel keeps the splash up, a fast one never shows it (see lib/splash.ts).
const stop = router.subscribe("onResolved", () => {
  stop();
  requestAnimationFrame(() => requestAnimationFrame(hideSplash)); // after React has painted that screen
});

// Base UI's popups (align="end"), tabs and radio groups follow the language's direction: Persian is right to left.
function Direction({ children }: { children: ReactNode }) {
  return <DirectionProvider direction={dirOf(useLang())}>{children}</DirectionProvider>;
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <Direction>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </Direction>
  </StrictMode>,
);
