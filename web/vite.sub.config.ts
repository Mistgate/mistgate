import { defineConfig, type Plugin, type ViteDevServer } from "vite";

// The public user page as ONE self-contained file, web/dist/sub.html:
// the JS and CSS (and the font subsets, as data: URIs) are inlined, the server puts the page data in place of
// the <!--MG_DATA--> marker and hashes the single inline module script for its CSP. Nothing else is emitted.
//
//   build:  pnpm exec vite build -c vite.sub.config.ts    (`pnpm build` runs it after the admin build)
//   dev:    pnpm exec vite -c vite.sub.config.ts          then open /sub.html?case=<name>&lang=en&as=windows&theme=light  (the cases are in
//                                                         src/sub/dev-data.ts: first, return, soon, expired, quota, disabled, nolinux, locked,
//                                                         limit, stale-dns, per-server, old, ...; the self-service and DNS calls answer themselves)

const marker = "<!--MG_DATA-->";

function singleFile(): Plugin {
  return {
    name: "mg-single-file",
    enforce: "post",
    apply: "build",
    generateBundle(_, bundle) {
      const page = bundle["sub.html"];
      if (!page || page.type !== "asset") throw new Error("sub.html missing from the bundle");
      let html = String(page.source);

      const chunks = Object.values(bundle).filter((f) => f.type === "chunk");
      const styles = Object.values(bundle).filter((f) => f.type === "asset" && f.fileName.endsWith(".css"));
      if (chunks.length !== 1) throw new Error(`expected one JS chunk, got ${chunks.length}`);
      const js = chunks[0]!.code;
      const css = styles.map((f) => String(f.source)).join("\n");

      // what the server's CSP and marker replacement rely on: fail the build, not the deploy
      if (/<\/script/i.test(js) || js.includes("<!--")) throw new Error("the bundle contains </script or <!--");
      if (/<\/style/i.test(css)) throw new Error("the stylesheet contains </style");
      if (/\beval\s*\(|new\s+Function\b/.test(js)) throw new Error("the bundle uses eval / new Function (CSP)");

      // the module script goes to the end of <body>; the stylesheet link becomes an inline <style>
      html = html
        .replace(/\s*<script\b[^>]*\bsrc="[^"]*"[^>]*><\/script>/, "")
        .replace(/\s*<link\b[^>]*rel="stylesheet"[^>]*>/, () => `\n    <style>${css}</style>`)
        .replace(/<\/body>/, () => `<script type="module">${js}</script>\n  </body>`);

      const scripts = html.match(/<script\b/gi)?.length ?? 0;
      if (scripts !== 1 || !/<script type="module">/.test(html)) throw new Error(`expected exactly one inline module script, got ${scripts}`);
      if (html.split(marker).length !== 2) throw new Error(`expected exactly one ${marker}`);
      if (/<[a-z][^>]*\son[a-z]+\s*=/i.test(html.replace(/<script type="module">[\s\S]*<\/script>/, ""))) {
        throw new Error("inline event handler in the page");
      }
      if (/\s(src|href)="(?!#|data:image\/svg\+xml,)[^"]*"/.test(html.replace(/<script type="module">[\s\S]*<\/script>/, "").replace(/<style>[\s\S]*<\/style>/, ""))) {
        throw new Error("the page references an external URL");
      }

      page.source = html;
      for (const f of [...chunks, ...styles]) delete bundle[f.fileName];
    },
  };
}

// Dev only: puts sample data (src/sub/dev-data.ts) where the server would put the real one.
function devData(): Plugin {
  let server: ViteDevServer;
  return {
    name: "mg-dev-data",
    apply: "serve",
    configureServer(s) {
      server = s;
    },
    transformIndexHtml: {
      order: "pre",
      async handler(html, ctx) {
        // loaded through the dev server, so it is never part of the config or of a build
        const { devPage } = (await server.ssrLoadModule("/src/sub/dev-data.ts")) as typeof import("./src/sub/dev-data");
        const json = JSON.stringify(devPage((ctx.originalUrl ?? "").split("?")[1] ?? "")).replace(/</g, "\\u003c");
        return html.replace(marker, () => `<script type="application/json" id="mg-data">${json}</script>`);
      },
    },
  };
}

export default defineConfig({
  base: "./",
  publicDir: false,
  plugins: [singleFile(), devData()],
  resolve: { tsconfigPaths: true },
  build: {
    outDir: "dist",
    emptyOutDir: false, // the admin build owns dist/ (and dist/.gitkeep); this one only adds sub.html
    rollupOptions: { input: "sub.html" },
    assetsInlineLimit: 100_000_000, // fonts become data: URIs (the CSP allows font-src data:)
    cssCodeSplit: false,
    modulePreload: false,
    reportCompressedSize: false,
  },
});
