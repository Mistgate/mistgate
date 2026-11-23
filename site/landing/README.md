# mistgate.app: the landing and application pages

The pages of the new landing site (`/`, `/ru/`, `/zh/`, `/fa/`, `/tr/` and `/app/`, `/ru/app/`, …). The documentation
is not part of this: it is still built by the old generator, `site/build.mjs`, and the landing's "Docs" links lead to
its pages.

**The folder to publish** is made by one command, `pnpm --dir site build:all` (`site/merge.mjs`): the docs, the pipeline,
the generator, then the merge into `site/public/`. The docs' start page is `/docs/` (`/ru/docs/`, …), so the language homes
(`/`, `/ru/`, `/zh/`, `/fa/`, `/tr/`) are the landing's alone; every other address stays the docs'. The merge refuses a path both sides write,
merges `_headers` (the docs' policy under `/*`, the landing's in the rule of each of its addresses, after `! Content-Security-Policy`)
and `sitemap.xml`, and fails on a broken link, a page whose policy forbids its inline script, or a sitemap that does not
match the pages. The merge also puts a schema.org JSON-LD block into the `<head>` of every landing page (built from the page's own title, description and language; `site/merge.mjs`, `landingLd`) and writes `<lastmod>` for the landing pages: the date of the last commit in `landing/design`. Dates come from one `git log`; without git or in a shallow clone there are none (the build still passes).
`node site/merge.mjs --no-build` merges what `site/dist/` and `site/landing/dist/` already hold.

```
site/landing/
  pipeline/   (another package) design boards -> build/
  gen/        Rust crate: build/ -> dist/            <- this README
  build/      output of the pipeline, input of gen   (not in git)
  dist/       output of gen: the files of the site   (not in git)
```

## Build

From the repository root:

```sh
# 1. the docs, to check the landing's links into them (once; any dist of the docs generator will do)
pnpm --dir site install --frozen-lockfile
MG_STARS= MG_DIST=/tmp/docs-dist pnpm --dir site build

# 2. the pages
cargo run --manifest-path site/landing/gen/Cargo.toml --release -- \
  --build site/landing/build --out site/landing/dist --docs-dist /tmp/docs-dist
```

`--out` is emptied first. `--docs-dist` is optional: without it the links into the docs are counted and reported as
unchecked, not checked. Rust 1.100 (`gen/rust-toolchain.toml` pins it when cargo runs inside `gen/`; from the root,
have 1.100 as the default toolchain). No wasm target, no `cargo-leptos`.

## What goes in and out

In, `build/` (the contract with the pipeline):

- `manifest.json`: `v: 1`, `langs`, `pages.<main|app>.<lang>` with `body`, `title`, `description`, `css`, `modules`,
  `preload`, `dir`, `todo`; `assets`.
- `pages/<page>.<lang>.html`: the inside of `<body>`, translated, first screen rendered. No `<html>`, `<head>`, `<body>`,
  no inline script (a `<script type="application/json">` data block is fine), no `on…=` handlers, no `javascript:` URLs.
  Links are root-relative: `/assets/…` must exist in `build/assets`, other internal links must be pages of this site or
  of the docs (with the anchor, if there is one).
- `assets/`: files with a hash of their content in the name.
- `preload` entries: a `.woff2` becomes `<link rel="preload" as="font">`, a `.js` becomes `<link rel="modulepreload">`.

Out, `dist/`:

- `index.html` of every page and language at its address (`/`, `/ru/`, … and `/app/`, `/ru/app/`, …), written by Leptos
  components: `<html lang dir data-lg>`, title, description, canonical, `hreflang` for all five languages and
  `x-default` (English), Open Graph and Twitter card, icons, `theme-color` of the dark theme, the stylesheets and the
  modules of the manifest. Nothing inline.
- `assets/`: a copy of `build/assets`.
- `_headers`: the CSP (`script-src 'self'`), a year of `immutable` for `/assets/*`, revalidation for the pages.

The same input gives the same bytes. A page the manifest marks `"todo": true` is built with what it has (the English
title and description if its own are missing) and listed as a warning. Anything else wrong (a broken manifest, a missing
fragment, an inline script, a link that leads nowhere) stops the build with the file and the reason; every problem is
listed at once.

The favicons, `og.png` and `mistgate-badge.svg` are the docs site's files at the root of its `dist/`; the landing
pages point at them and the merged `dist/` has them.

## Tests

```sh
cargo test --manifest-path site/landing/gen/Cargo.toml
# also against the real docs:
MG_DOCS_DIST=/tmp/docs-dist cargo test --manifest-path site/landing/gen/Cargo.toml -- --include-ignored
```

They run on a small stand-in for `build/` and for the docs `dist/` (`gen/tests/fixture/`): two pages in five languages.

---

# The boards pipeline: The new mistgate.app: landing and app page

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
| `pnpm sync <design export folder> [<blobs folder>]` | copy the needed boards, stylesheets and the dictionary into `design/`, and the images (`<hash>.svg`) into `design/blobs/`, after a privacy scan and a check that no svg has a script or loads from outside (`MG_DESIGN_SRC`, `MG_DESIGN_BLOBS` instead of the arguments) |
| `pnpm build` | design -> `build/`: compile, bundle, translate, prune the css, pre-render the first screen of every page in every language, check the size budgets |
| `pnpm stand` | serve `build/` on http://127.0.0.1:8790 with the CSP of the site (`/`, `/ru/`, `/app/`, `/ru/app/`, ...) |
| `pnpm check` | headless Chrome over the stand: 390, 820 and 1440 px in ru, en and fa; console, CSP, layout shift while loading (processor 4x slower, under 0.1), boards, every shot frame shows its part of the board, the nodes board with 0 and 1 node, tours, shots (`--help` in the file header) |
| `pnpm check:quick` | the same for ru and en at 1440 px, no shots |
| `pnpm all` | `build` and `check` |
| `pnpm harvest` | `check` on the Russian pages at all three widths (a narrow layout shows strings the wide one does not), writing `harvest.json`: the strings the live screens show, which the dictionary subset is cut by |
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
* **Links** of the design (`Docs.dc.html#…`) are resolved by `links.json` to the pages the docs generator builds; a bare `Docs.dc.html` is the start page of the docs, `/docs/`.
* **Title and description** of the home page and of the app page per language are in `pipeline/meta.json` (the home page's `title` is written as it is; without one it is "Mistgate — <h1>"). A docs page can word its `<title>` separately with `seoTitle:` in its front matter.
* **Images** the design refers to as `/_blob/<hash>` are the files of `design/blobs/<hash>.svg` (copied by `sync`); a file is named in `assets/` by its content, so copies of one picture are one file. A hash with no file gets a stand-in drawn by the build (flags, logos), listed in `build/report.json` (`standIns`) and in the build output.

## What the generator gets beyond the contract

`build/manifest.json` is what the page generator reads: for every page and language the fragment (`body`), `title`, `description`, `css`, `modules`, `preload` and `dir`. Each page entry also has three keys the generator may use: `htmlAttrs` (attributes of `<html>`: `data-lg`, which the stylesheets of the design select by), `fonts` (woff2 files to preload) and `fetch` (the language bundle: preload it `as=fetch crossorigin`). `pipeline/serve.mjs` shows the document the generator is expected to write (`renderDocument`).
