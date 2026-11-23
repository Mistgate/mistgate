//! The input contract: `build/manifest.json` made by the board pipeline, and the fixed tables of languages and pages.

use serde::Deserialize;
use std::collections::{BTreeMap, BTreeSet};
use std::path::{Component, Path};

pub const SITE: &str = "https://mistgate.app";

/// A language of the site. The values are the same as in the docs generator (`site/src/layout.mjs`): the hreflang of
/// Chinese is `zh-Hans` in `<html lang>` and in hreflang (the Open Graph locale is `zh_CN`).
pub struct Lang {
    pub code: &'static str,
    pub hreflang: &'static str,
    pub html_lang: &'static str,
    pub og_locale: &'static str,
    pub rtl: bool,
}

pub const LANGS: [Lang; 5] = [
    Lang { code: "en", hreflang: "en", html_lang: "en", og_locale: "en_US", rtl: false },
    Lang { code: "ru", hreflang: "ru", html_lang: "ru", og_locale: "ru_RU", rtl: false },
    Lang { code: "zh", hreflang: "zh-Hans", html_lang: "zh-Hans", og_locale: "zh_CN", rtl: false },
    Lang { code: "fa", hreflang: "fa", html_lang: "fa", og_locale: "fa_IR", rtl: true },
    Lang { code: "tr", hreflang: "tr", html_lang: "tr", og_locale: "tr_TR", rtl: false },
];

pub fn lang(code: &str) -> Option<&'static Lang> {
    LANGS.iter().find(|l| l.code == code)
}

/// The pages and where each lives under a language's root.
pub const PAGES: [(&str, &str); 2] = [("main", ""), ("app", "app/")];

/// `("ru", "app")` -> `/ru/app/`; English has no prefix: `/`, `/app/` (the same addresses as the docs generator).
pub fn page_url(lang: &str, page: &str) -> String {
    let base = if lang == "en" { String::new() } else { format!("/{lang}") };
    let tail = PAGES.iter().find(|p| p.0 == page).map_or("", |p| p.1);
    format!("{base}/{tail}")
}

#[derive(Deserialize, Debug)]
pub struct Manifest {
    pub v: u32,
    #[serde(rename = "designVersion", default)]
    pub design_version: Option<String>,
    pub langs: Vec<String>,
    pub pages: BTreeMap<String, BTreeMap<String, PageEntry>>,
    #[serde(default)]
    pub assets: Vec<String>,
    #[serde(flatten)]
    pub extra: BTreeMap<String, serde_json::Value>,
}

#[derive(Deserialize, Debug)]
pub struct PageEntry {
    pub body: String,
    #[serde(default)]
    pub title: String,
    #[serde(default)]
    pub description: String,
    #[serde(default)]
    pub css: Vec<String>,
    #[serde(default)]
    pub modules: Vec<String>,
    #[serde(default)]
    pub preload: Vec<String>,
    /// `.woff2` files of the first screen, preloaded
    #[serde(default)]
    pub fonts: Vec<String>,
    /// data the page fetches at once (its language pack), preloaded
    #[serde(default)]
    pub fetch: Vec<String>,
    /// what the pipeline expects on `<html>`; only `data-lg`, and it must agree with the language
    #[serde(default, rename = "htmlAttrs")]
    pub html_attrs: BTreeMap<String, String>,
    #[serde(default)]
    pub dir: Option<String>,
    #[serde(default)]
    pub todo: bool,
    #[serde(flatten)]
    pub extra: BTreeMap<String, serde_json::Value>,
}

pub fn load(build: &Path) -> Result<Manifest, String> {
    let file = build.join("manifest.json");
    let text = std::fs::read_to_string(&file).map_err(|e| format!("{}: {e}", file.display()))?;
    serde_json::from_str(text.trim_start_matches('\u{feff}')).map_err(|e| format!("{}: {e}", file.display()))
}

/// A path inside `build/` that the manifest names: relative, no `..`, forward slashes.
fn safe_rel(p: &str) -> bool {
    !p.is_empty() && !p.contains('\\') && Path::new(p).components().all(|c| matches!(c, Component::Normal(_)))
}

/// Everything wrong with the manifest itself and the files it names (all problems at once, not the first only).
/// `todo` pages are not a problem here: the caller lists them as a warning.
pub fn validate(m: &Manifest, build: &Path) -> Vec<String> {
    let mut bad = Vec::new();
    if m.v != 1 {
        bad.push(format!("manifest.json: \"v\" is {}, this generator reads version 1", m.v));
    }
    for k in m.extra.keys() {
        bad.push(format!("manifest.json: unknown field \"{k}\" (the contract has v, designVersion, langs, pages, assets)"));
    }
    let mut seen = BTreeSet::new();
    for l in &m.langs {
        if lang(l).is_none() {
            bad.push(format!("manifest.json: unknown language \"{l}\" (known: {})", LANGS.map(|l| l.code).join(" ")));
        }
        if !seen.insert(l.as_str()) {
            bad.push(format!("manifest.json: language \"{l}\" is listed twice"));
        }
    }
    if !seen.contains("en") {
        bad.push("manifest.json: \"langs\" must contain \"en\" (the x-default of every page)".into());
    }
    if m.pages.is_empty() {
        bad.push("manifest.json: \"pages\" is empty".into());
    }
    for (page, by_lang) in &m.pages {
        if !PAGES.iter().any(|p| p.0 == page) {
            bad.push(format!("manifest.json: unknown page \"{page}\" (known: {})", PAGES.map(|p| p.0).join(" ")));
            continue;
        }
        for l in &m.langs {
            if !by_lang.contains_key(l) {
                bad.push(format!("manifest.json: page \"{page}\" has no \"{l}\" entry (every page needs every language, or hreflang cannot be mutual)"));
            }
        }
        for (l, e) in by_lang {
            let at = format!("manifest.json: pages.{page}.{l}");
            if !seen.contains(l.as_str()) {
                bad.push(format!("{at}: language is not in \"langs\""));
                continue;
            }
            for k in e.extra.keys() {
                bad.push(format!("{at}: unknown field \"{k}\""));
            }
            if !e.todo && e.title.trim().is_empty() {
                bad.push(format!("{at}: empty \"title\" (mark the page \"todo\": true if it has none yet)"));
            }
            if !e.todo && e.description.trim().is_empty() {
                bad.push(format!("{at}: empty \"description\" (mark the page \"todo\": true if it has none yet)"));
            }
            if let (Some(d), Some(info)) = (&e.dir, lang(l)) {
                let want = if info.rtl { "rtl" } else { "ltr" };
                if d != want {
                    bad.push(format!("{at}: \"dir\" is \"{d}\", the language {l} is {want}"));
                }
            }
            if !safe_rel(&e.body) {
                bad.push(format!("{at}: \"body\" {:?} is not a relative path inside build/", e.body));
            } else if !build.join(&e.body).is_file() {
                bad.push(format!("{at}: fragment {} does not exist", e.body));
            }
            for (field, list) in [("css", &e.css), ("modules", &e.modules), ("preload", &e.preload), ("fonts", &e.fonts), ("fetch", &e.fetch)] {
                for a in list {
                    if !safe_rel(a) || !a.starts_with("assets/") {
                        bad.push(format!("{at}: \"{field}\" entry {a:?} is not a path under assets/"));
                    } else if !build.join(a).is_file() {
                        bad.push(format!("{at}: \"{field}\" entry {a} does not exist"));
                    }
                }
            }
        }
    }
    for a in &m.assets {
        if !safe_rel(a) || !a.starts_with("assets/") {
            bad.push(format!("manifest.json: \"assets\" entry {a:?} is not a path under assets/"));
        } else if !build.join(a).is_file() {
            bad.push(format!("manifest.json: \"assets\" entry {a} does not exist"));
        }
    }
    bad
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn urls_follow_the_docs_generator() {
        assert_eq!(page_url("en", "main"), "/");
        assert_eq!(page_url("ru", "main"), "/ru/");
        assert_eq!(page_url("en", "app"), "/app/");
        assert_eq!(page_url("fa", "app"), "/fa/app/");
    }
}
