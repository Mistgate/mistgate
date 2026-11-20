# The new mistgate.app: landing and app page

The landing (`Main`) and the page of the Windows app (`AppPage`) are **design boards**: files `*.dc.html` of the design export, each a
template plus the class that drives it. The pipeline turns them into what a browser runs, and a page generator
(`gen/`, a separate package) wraps the result in documents. Nothing here replaces `site/` (the docs generator).

```
design/      the part of the design export these two pages need (made by `sync`, kept in git, never edited by hand)
pipeline/    Node: sync, compile, translate, css, prerender, build, the stand (check)
build/       OUTPUT of the pipeline, INPUT of the generator (not in git): manifest.json, pages/*.html, assets/*
dist/        OUTPUT of the generator (not in git)
```

## Commands (in `pipeline/`, `pnpm install` once)

| | |
|---|---|
| `pnpm sync <design export folder>` | copy the needed boards, stylesheets and the dictionary into `design/`, after a privacy scan (`MG_DESIGN_SRC` instead of the argument) |
| `pnpm build` | design -> `build/`: compile, bundle, translate, prune the css, pre-render the first screen of every page in every language, check the size budgets |
| `pnpm stand` | serve `build/` on http://127.0.0.1:8790 with the CSP of the site (`/`, `/ru/`, `/app/`, `/ru/app/`, ...) |
| `pnpm check` | headless Chrome over the stand: 390, 820 and 1440 px in ru, en and fa; console, CSP, boards, tours, shots (`--help` in the file header) |
| `pnpm check:quick` | the same for ru and en at 1440 px, no shots |
| `pnpm all` | `build` and `check` |
| `pnpm harvest` | `check` on the Russian pages, writing `harvest.json`: the strings the live screens show, which the dictionary subset is cut by |
| `pnpm test` | unit tests (`node --test`) |

Chrome is taken from `CHROME`, or from the usual places. The shots of `check` go to `MG_SHOTS` (outside the repository).

## What the pipeline does

* **Boards** are compiled to ES modules over Preact (`pipeline/runtime/rt.js`, about 4 KB br). A page's boards are lazy frames: a frame gets its
  board when the screen comes near (`IntersectionObserver`); the board of the first screen is there at once.
  Lookups a board makes with `document.querySelector*` are rewritten to run over its own frame, so a board can stand on a page twice.
* **Patches** over the scripts of the boards are listed in `patches.mjs`; each must apply exactly once, so a redesign that moves the code
  breaks the build instead of losing a patch silently.
* **Languages**: the design translates Russian text in the DOM by a dictionary; the pipeline runs the design's own translator at build
  time (the pre-render) and in the browser (what scripts assemble on the fly), over the part of the dictionary the page can show, in one language.
* **CSS** of the design is cut to the classes the page's boards can use; fonts are the ones of `web/src/fonts`.
* **Links** of the design (`Docs.dc.html#…`) are resolved by `links.json` to the pages the docs generator builds.
* **Images** the design refers to as `/_blob/<hash>` are not in the export; the build draws stand-ins (flags, logos) and lists them in `build/report.json`.

## What the generator gets beyond the contract

`build/manifest.json` is what the page generator reads: for every page and language the fragment (`body`), `title`, `description`, `css`, `modules`, `preload` and `dir`. Each page entry also has three keys the generator may use: `htmlAttrs` (attributes of `<html>`: `data-lg`, which the stylesheets of the design select by), `fonts` (woff2 files to preload) and `fetch` (the language bundle: preload it `as=fetch crossorigin`). `pipeline/serve.mjs` shows the document the generator is expected to write (`renderDocument`).
