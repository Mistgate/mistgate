# mistgate.app: the landing and application pages

The pages of the new landing site (`/`, `/ru/`, `/zh/`, `/fa/`, `/tr/` and `/app/`, `/ru/app/`, …). The documentation
is not part of this: it is still built by the old generator, `site/build.mjs`, and the landing's "Docs" links lead to
its pages.

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
