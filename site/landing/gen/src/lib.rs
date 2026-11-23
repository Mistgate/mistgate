//! Page generator for the Mistgate landing site.
//!
//! In: `build/` made by the board pipeline (`manifest.json`, `pages/<page>.<lang>.html` body fragments, `assets/`).
//! Out: `dist/<address>/index.html` for every page and language, `dist/assets/`, `dist/_headers`.
//! The docs stay with the old generator (`site/build.mjs`); `--docs-dist` points at what it built, to check the links.

pub mod check;
pub mod doc;
pub mod html;
pub mod manifest;

use manifest::{Manifest, LANGS};
use std::collections::BTreeSet;
use std::fs;
use std::path::{Path, PathBuf};

pub struct Options {
    pub build: PathBuf,
    pub out: PathBuf,
    /// the `dist/` of the docs generator; without it links into the docs are not checked (they are counted)
    pub docs_dist: Option<PathBuf>,
}

#[derive(Debug, Default)]
pub struct Report {
    pub pages: usize,
    pub files: usize,
    /// `main/fa`: pages the manifest marks `todo`, built with what they have
    pub todo: Vec<String>,
    pub unchecked_docs_links: usize,
}

/// The CSP of the old site, without the hash of its inline theme script (the landing has no inline script) and without
/// anything for wasm (nothing from Rust reaches the browser). `style-src 'unsafe-inline'`: the design's markup carries
/// `style=""` attributes, as the docs pages do.
const CSP: &str = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self' https://api.github.com; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'";

fn headers(urls: &BTreeSet<String>) -> String {
    // Cache-Control is not under `/*`: Cloudflare Pages joins the values of every rule that matches a path
    let mut s = format!("/*\n  X-Content-Type-Options: nosniff\n  Referrer-Policy: strict-origin-when-cross-origin\n  X-Frame-Options: DENY\n  Permissions-Policy: camera=(), microphone=(), geolocation=(), interest-cohort=()\n  Content-Security-Policy: {CSP}\n\n/assets/*\n  Cache-Control: public, max-age=31536000, immutable\n");
    for u in urls {
        s.push_str(&format!("\n{u}\n  Cache-Control: public, max-age=0, must-revalidate\n"));
    }
    s
}

fn copy_dir(from: &Path, to: &Path, files: &mut usize) -> Result<(), String> {
    fs::create_dir_all(to).map_err(|e| format!("{}: {e}", to.display()))?;
    for e in fs::read_dir(from).map_err(|e| format!("{}: {e}", from.display()))? {
        let p = e.map_err(|e| e.to_string())?.path();
        let dest = to.join(p.file_name().unwrap());
        if p.is_dir() {
            copy_dir(&p, &dest, files)?;
        } else {
            fs::copy(&p, &dest).map_err(|e| format!("{}: {e}", p.display()))?;
            *files += 1;
        }
    }
    Ok(())
}

fn write(path: &Path, data: &str, files: &mut usize) -> Result<(), String> {
    fs::create_dir_all(path.parent().unwrap()).map_err(|e| format!("{}: {e}", path.display()))?;
    fs::write(path, data).map_err(|e| format!("{}: {e}", path.display()))?;
    *files += 1;
    Ok(())
}

fn fail(what: &str, mut problems: Vec<String>) -> String {
    problems.dedup();
    format!("{what}: {} problem(s)\n  - {}", problems.len(), problems.join("\n  - "))
}

pub fn run(o: &Options) -> Result<Report, String> {
    if !o.build.is_dir() {
        return Err(format!("{}: no such directory (run the board pipeline first)", o.build.display()));
    }
    let m: Manifest = manifest::load(&o.build)?;
    let bad = manifest::validate(&m, &o.build);
    if !bad.is_empty() {
        return Err(fail("manifest", bad));
    }
    let docs = o.docs_dist.as_deref().map(check::DocsIndex::load).transpose()?;
    // every language the manifest lists, in the table's order, so the output does not depend on the manifest's
    let langs: Vec<&manifest::Lang> = LANGS.iter().filter(|l| m.langs.iter().any(|x| x == l.code)).collect();
    let urls: BTreeSet<String> = m.pages.keys().flat_map(|p| langs.iter().map(move |l| manifest::page_url(l.code, p))).collect();

    let mut report = Report::default();
    let mut bad = Vec::new();
    let mut pages = Vec::new(); // (url, finished document)
    let links = check::Links { build: &o.build, pages: &urls, docs: docs.as_ref() };
    for (page, by_lang) in &m.pages {
        for l in &langs {
            let e = &by_lang[l.code];
            let url = manifest::page_url(l.code, page);
            let fragment = match fs::read_to_string(o.build.join(&e.body)) {
                Ok(f) => f,
                Err(err) => {
                    bad.push(format!("{}: {err}", e.body));
                    continue;
                }
            };
            bad.extend(check::fragment_problems(&fragment, &links, &mut report.unchecked_docs_links).into_iter().map(|p| format!("{}: {p}", e.body)));
            if e.todo {
                report.todo.push(format!("{page}/{}", l.code));
            }
            // a page with no title yet (todo) borrows the English one, so that a preview still reads sensibly
            let en = &by_lang["en"];
            let pick = |own: &str, other: &str, last: &str| if !own.trim().is_empty() { own.to_string() } else if e.todo && !other.trim().is_empty() { other.to_string() } else { last.to_string() };
            let lg = l.code.to_uppercase();
            for (k, v) in &e.html_attrs {
                if k != "data-lg" || *v != lg {
                    bad.push(format!("manifest.json: pages.{page}.{}: \"htmlAttrs\" {k}={v:?}: only data-lg={lg:?} is known", l.code));
                }
            }
            let (mut fonts, mut module_preloads, mut fetches) = (Vec::new(), Vec::new(), Vec::new());
            for p in e.preload.iter().chain(&e.fonts).chain(&e.fetch) {
                match p.rsplit('.').next() {
                    Some("woff2") => fonts.push(format!("/{p}")),
                    Some("js" | "mjs") => module_preloads.push(format!("/{p}")),
                    Some("json") => fetches.push(format!("/{p}")),
                    _ => bad.push(format!("manifest.json: pages.{page}.{}: preloaded {p} is not a .woff2 font, a .js module or a .json file", l.code)),
                }
            }
            let mut alternates: Vec<_> = langs.iter().map(|a| doc::Alternate { hreflang: a.hreflang.into(), href: format!("{}{}", manifest::SITE, manifest::page_url(a.code, page)) }).collect();
            alternates.push(doc::Alternate { hreflang: "x-default".into(), href: format!("{}{}", manifest::SITE, manifest::page_url("en", page)) });
            let rendered = doc::render(doc::Page {
                html_lang: l.html_lang.into(),
                dir: if l.rtl { "rtl" } else { "ltr" }.into(),
                data_lg: lg,
                head: doc::Head {
                    title: pick(&e.title, &en.title, "Mistgate"),
                    description: pick(&e.description, &en.description, ""),
                    canonical: format!("{}{url}", manifest::SITE),
                    og_locale: l.og_locale.into(),
                    alternates,
                    fonts,
                    module_preloads,
                    fetches,
                    css: e.css.iter().map(|c| format!("/{c}")).collect(),
                    css_async: e.css_async.iter().map(|c| format!("/{c}")).collect(),
                    loader: e.loader.as_ref().map(|l| format!("/{l}")),
                    modules: e.modules.iter().map(|c| format!("/{c}")).collect(),
                },
                body: fragment,
            });
            pages.push((url, rendered));
        }
    }
    if !bad.is_empty() {
        return Err(fail("fragments", bad));
    }

    // the output directory is emptied: it must not be, or hold, the input
    let (b, out) = (std::path::absolute(&o.build).map_err(|e| e.to_string())?, std::path::absolute(&o.out).map_err(|e| e.to_string())?);
    if out.parent().is_none() || b.starts_with(&out) || out.starts_with(&b) {
        return Err(format!("--out {} must be a directory of its own, apart from --build {}", out.display(), b.display()));
    }
    if out.exists() {
        fs::remove_dir_all(&out).map_err(|e| format!("{}: {e}", out.display()))?;
    }
    fs::create_dir_all(&out).map_err(|e| format!("{}: {e}", out.display()))?;
    if o.build.join("assets").is_dir() {
        copy_dir(&o.build.join("assets"), &out.join("assets"), &mut report.files)?;
    }
    for (url, html) in &pages {
        write(&out.join(url.trim_start_matches('/')).join("index.html"), html, &mut report.files)?;
    }
    write(&out.join("_headers"), &headers(&urls), &mut report.files)?;
    report.pages = pages.len();

    let bad = check::verify_dist(&out, &m);
    if !bad.is_empty() {
        return Err(fail("dist", bad));
    }
    Ok(report)
}
