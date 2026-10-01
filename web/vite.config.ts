import { readFileSync, writeFileSync } from "node:fs";
import tailwindcss from "@tailwindcss/vite";
import react from "@vitejs/plugin-react";
import { defineConfig, type Plugin } from "vitest/config";

// The splash in index.html is inline markup (it paints before the bundle runs), but the badge and its motion have one
// source: src/assets. This puts them into the page, for the dev server and the build alike.
function splashAssets(): Plugin {
  const asset = (name: string) => readFileSync(new URL(`./src/assets/${name}`, import.meta.url), "utf8");
  return {
    name: "mg-splash-assets",
    transformIndexHtml: {
      order: "pre",
      handler(html) {
        const badge = asset("mistgate-badge.svg")
          .replace(/\b(mg-clip|mg-gap)\b/g, "sp-$1")
          .replace(/ width="512" height="512"/, "")
          .replace(/ role="img" aria-label="Mistgate"/, ' aria-hidden="true" focusable="false"')
          // path data is absolute: one decimal at 512 units is plenty (the style rules keep their numbers)
          .replace(/ d="([^"]+)"/g, (_, d: string) => ` d="${d.replace(/\d+\.\d{2,}/g, (n) => String(Number(Number(n).toFixed(1))))}"`)
          .replace(/\s*\n\s*/g, "");
        return html.replace("<!--MG_SPLASH_BADGE-->", () => badge).replace("/*MG_MARK_MOTION*/", () => asset("mark-motion.css"));
      },
    },
  };
}

// The dev proxy target: the panel's admin listener (README.md, Development). MISTGATE_PANEL points it elsewhere when 8081 is taken.
const panel = process.env.MISTGATE_PANEL ?? "http://127.0.0.1:8081";

// base "./" keeps asset URLs relative to <base href>, which the panel rewrites
// to the admin prefix at serve time.
export default defineConfig({
  base: "./",
  plugins: [
    react(),
    tailwindcss(),
    splashAssets(),
    {
      // vite empties dist/ on build; web/embed.go needs the directory to exist in a clean clone.
      name: "keep-dist-gitkeep",
      writeBundle() {
        writeFileSync("dist/.gitkeep", "");
      },
    },
  ],
  // The panel CSP allows fonts from 'self' only (no data: URIs), so never inline the woff2 files.
  build: { assetsInlineLimit: 0 },
  resolve: { tsconfigPaths: true },
  test: { environment: "jsdom" },
  server: {
    port: 5173,
    // /brand/logo.svg is served by the panel next to the API (the sign-in page shows the custom logo)
    // /preview/user-page/<id> is the admin preview of the public user page (Subscriptions, phone frame)
    // /mcp is the agent endpoint (Streamable HTTP) on the admin listener, for pointing an MCP client at the Vite port
    proxy: { "/api": panel, "/mcp": panel, "/brand": panel, "/preview": panel },
  },
});
