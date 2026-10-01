import { QueryClientProvider } from "@tanstack/react-query";
import { RouterProvider } from "@tanstack/react-router";
import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import "./index.css";
import "@/lib/theme"; // apply the saved theme and accent before the first paint
import "@/lib/accent";
import { queryClient } from "@/lib/session";
import { hideSplash } from "@/lib/splash";
import { router } from "./router";

// The splash (index.html) stays until the first real screen is ready: the session check, the brand and the screen's
// chunk are done. No timer: a slow panel keeps the splash up, a fast one never shows it (see lib/splash.ts).
const stop = router.subscribe("onResolved", () => {
  stop();
  requestAnimationFrame(() => requestAnimationFrame(hideSplash)); // after React has painted that screen
});

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </StrictMode>,
);
