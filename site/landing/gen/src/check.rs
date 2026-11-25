//! The checks: a fragment is safe and its links lead somewhere; a finished `dist/` is what the contract promises.

use crate::html::{self, Tag};
use crate::manifest::{self, Manifest, SITE};
use std::collections::{BTreeMap, BTreeSet};
use std::fs;
use std::path::{Path, PathBuf};

/// What the current docs generator (`site/build.mjs`) built: every file of its `dist/`, by URL. Links of the landing
/// into the docs are checked against this, so a link to a page that does not exist fails the build.
pub struct DocsIndex {
    root: PathBuf,
    /// `/getting-started/overview/` for `getting-started/overview/index.html`, `/favicon.svg` for a plain file
    urls: BTreeSet<String>,
}

impl DocsIndex {
    pub fn load(root: &Path) -> Result<Self, String> {
        fn walk(dir: &Path, root: &Path, out: &mut BTreeSet<String>) -> Result<(), String> {
            for e in fs::read_dir(dir).map_err(|e| format!("{}: {e}", dir.display()))? {
                let p = e.map_err(|e| e.to_string())?.path();
                if p.is_dir() {
                    walk(&p, root, out)?;
                } else {
                    let rel = p.strip_prefix(root).unwrap().to_string_lossy().replace('\\', "/");
                    out.insert(match rel.strip_suffix("index.html") {
                        Some(dir) if dir.is_empty() || dir.ends_with('/') => format!("/{dir}"),
                        _ => format!("/{rel}"),
                    });
                }
            }
            Ok(())
        }
        let mut urls = BTreeSet::new();
        walk(root, root, &mut urls)?;
        if urls.is_empty() {
            return Err(format!("{}: the docs dist is empty (build it first: node site/build.mjs)", root.display()));
        }
        Ok(DocsIndex { root: root.to_path_buf(), urls })
    }

    pub fn has(&self, url: &str) -> bool {
        self.urls.contains(url)
    }

    /// Is `frag` an id on the page `url`? (`content` and `nav` are the skip-link targets, as in the docs link check.)
    pub fn has_id(&self, url: &str, frag: &str) -> bool {
        if frag == "content" || frag == "nav" {
            return true;
        }
        let file = self.root.join(url.trim_start_matches('/')).join(if url.ends_with('/') { "index.html" } else { "" });
        fs::read_to_string(file).is_ok_and(|h| html::ids(&html::tags(&h)).iter().any(|i| i == frag))
    }
}

fn percent_decode(s: &str) -> String {
    let b = s.as_bytes();
    let mut out = Vec::with_capacity(b.len());
    let mut i = 0;
    while i < b.len() {
        if b[i] == b'%' && i + 2 < b.len() {
            if let Some(v) = std::str::from_utf8(&b[i + 1..i + 3]).ok().and_then(|h| u8::from_str_radix(h, 16).ok()) {
                out.push(v);
                i += 3;
                continue;
            }
        }
        out.push(b[i]);
        i += 1;
    }
    String::from_utf8_lossy(&out).into_owned()
}

/// What the links of a fragment may point at.
pub struct Links<'a> {
    /// the `build/` directory: `/assets/...` must exist there
    pub build: &'a Path,
    /// the URLs of the pages this generator writes
    pub pages: &'a BTreeSet<String>,
    pub docs: Option<&'a DocsIndex>,
}

/// The problems with a fragment: not a body fragment, not safe under the CSP, or a link that leads nowhere.
/// `unchecked` counts the links into the docs that could not be checked because no docs dist was given.
pub fn fragment_problems(html: &str, links: &Links, unchecked: &mut usize) -> Vec<String> {
    let tags = html::tags(html);
    let mut bad = html::script_problems(&tags);
    for t in tags.iter().filter(|t| !t.closing && matches!(t.name.as_str(), "html" | "head" | "body" | "base")) {
        bad.push(format!("a <{}> tag (a fragment is the inside of <body>)", t.name));
    }
    for raw in html::links(&tags) {
        if let Some(p) = link_problem(&raw, links, unchecked) {
            bad.push(p);
        }
    }
    bad.sort();
    bad.dedup();
    bad
}

fn link_problem(raw: &str, cx: &Links, unchecked: &mut usize) -> Option<String> {
    let lower = raw.to_ascii_lowercase();
    if raw.starts_with('#') || raw.starts_with("//") || ["http:", "https:", "mailto:", "tel:", "data:", "blob:"].iter().any(|p| lower.starts_with(p)) {
        return None; // the page itself, another site, or no file at all
    }
    if lower.starts_with("javascript:") {
        return None; // reported by script_problems
    }
    if !raw.starts_with('/') {
        return Some(format!("the relative link {raw:?} (links are root-relative: /assets/…, /ru/…)"));
    }
    let (path, frag) = match raw.split_once('#') {
        Some((p, f)) => (p, Some(percent_decode(f))),
        None => (raw, None),
    };
    let path = path.split('?').next().unwrap_or(path);
    if path.starts_with("/assets/") {
        return (!cx.build.join(path.trim_start_matches('/')).is_file()).then(|| format!("{raw}: no such file in build/assets"));
    }
    if cx.pages.contains(path) {
        return None; // ponytail: anchors into the landing's own pages are not checked (ids made by board scripts at run time would be false alarms)
    }
    let Some(docs) = cx.docs else {
        *unchecked += 1;
        return None;
    };
    if !docs.has(path) {
        return Some(format!("{raw}: no such page or file in the docs the current site builds"));
    }
    match frag {
        Some(f) if !f.is_empty() && path.ends_with('/') && !docs.has_id(path, &f) => Some(format!("{raw}: the page has no #{f}")),
        _ => None,
    }
}

/// What a finished page says about itself, read back from the file.
struct Written {
    tags: Vec<Tag>,
}

impl Written {
    fn first(&self, name: &str) -> Option<&Tag> {
        self.tags.iter().find(|t| !t.closing && t.name == name)
    }
}

/// Check the finished `out/` against the manifest, reading the files back: a file for every page and language,
/// `lang`/`dir`/`data-lg`, mutual and complete hreflang with x-default, a self-canonical, no inline script or handler,
/// every `/assets/` reference present, `_headers`.
pub fn verify_dist(out: &Path, m: &Manifest) -> Vec<String> {
    let mut bad = Vec::new();
    for page in m.pages.keys() {
        // the alternates every language of this page must carry: the same set, so that the links are mutual
        let mut want: BTreeMap<String, String> = m.langs.iter().filter_map(|l| manifest::lang(l).map(|i| (i.hreflang.to_string(), format!("{SITE}{}", manifest::page_url(l, page))))).collect();
        want.insert("x-default".into(), format!("{SITE}{}", manifest::page_url("en", page)));
        for l in &m.langs {
            let Some(info) = manifest::lang(l) else { continue };
            let url = manifest::page_url(l, page);
            let file = out.join(url.trim_start_matches('/')).join("index.html");
            let at = format!("{url} ({})", file.strip_prefix(out).unwrap_or(&file).display());
            let Ok(text) = fs::read_to_string(&file) else {
                bad.push(format!("{at}: the file was not written"));
                continue;
            };
            let w = Written { tags: html::tags(&text) };
            match w.first("html") {
                None => bad.push(format!("{at}: no <html>")),
                Some(h) => {
                    let (dir, lg) = (if info.rtl { "rtl" } else { "ltr" }, l.to_uppercase());
                    for (k, v) in [("lang", info.html_lang), ("dir", dir), ("data-lg", lg.as_str())] {
                        if h.attr(k) != Some(v) {
                            bad.push(format!("{at}: <html {k}> is {:?}, expected {v:?}", h.attr(k)));
                        }
                    }
                }
            }
            let got: BTreeMap<String, String> = w.tags.iter().filter(|t| t.name == "link" && t.attr("rel") == Some("alternate")).filter_map(|t| Some((t.attr("hreflang")?.to_string(), t.attr("href")?.to_string()))).collect();
            if got != want {
                bad.push(format!("{at}: hreflang set {got:?} differs from the mutual set {want:?}"));
            }
            let canonical: Vec<_> = w.tags.iter().filter(|t| t.name == "link" && t.attr("rel") == Some("canonical")).filter_map(|t| t.attr("href")).collect();
            if canonical != [format!("{SITE}{url}")] {
                bad.push(format!("{at}: canonical is {canonical:?}, expected {SITE}{url}"));
            }
            bad.extend(html::script_problems(&w.tags).into_iter().map(|p| format!("{at}: {p}")));
            for u in html::links(&w.tags).iter().filter(|u| u.starts_with("/assets/")) {
                let path = u.split(['?', '#']).next().unwrap_or(u);
                if !out.join(path.trim_start_matches('/')).is_file() {
                    bad.push(format!("{at}: {u} is not in dist"));
                }
            }
        }
    }
    if !out.join("_headers").is_file() {
        bad.push("_headers was not written".into());
    }
    bad
}
