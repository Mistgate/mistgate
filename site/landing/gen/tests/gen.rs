//! The generator against the fixture in tests/fixture: a small stand-in for the pipeline's `build/`
//! (2 pages x 5 languages, one stylesheet, a module per page, a font) and for the docs generator's `dist/`.

use mistgate_site_gen::{check, html, manifest, run, Options, Report};
use std::collections::{BTreeMap, BTreeSet};
use std::fs;
use std::path::{Path, PathBuf};

const FIXTURE: &str = concat!(env!("CARGO_MANIFEST_DIR"), "/tests/fixture");
const LANGS: [&str; 5] = ["en", "ru", "zh", "fa", "tr"];

fn scratch(name: &str) -> PathBuf {
    let d = Path::new(env!("CARGO_TARGET_TMPDIR")).join(name);
    let _ = fs::remove_dir_all(&d);
    fs::create_dir_all(&d).unwrap();
    d
}

fn copy_dir(from: &Path, to: &Path) {
    fs::create_dir_all(to).unwrap();
    for e in fs::read_dir(from).unwrap() {
        let p = e.unwrap().path();
        let dest = to.join(p.file_name().unwrap());
        if p.is_dir() {
            copy_dir(&p, &dest);
        } else {
            fs::copy(&p, &dest).unwrap();
        }
    }
}

/// A private copy of the fixture's build/ to break on purpose.
fn fixture_build(name: &str) -> PathBuf {
    let d = scratch(name).join("build");
    copy_dir(&Path::new(FIXTURE).join("build"), &d);
    d
}

/// Where the test builds to: next to its private copy of build/, which `scratch` must not be asked for twice.
fn dist_of(build: &Path) -> PathBuf {
    build.parent().unwrap().join("dist")
}

fn edit(build: &Path, file: &str, f: impl FnOnce(String) -> String) {
    let p = build.join(file);
    fs::write(&p, f(fs::read_to_string(&p).unwrap())).unwrap();
}

fn edit_manifest(build: &Path, f: impl FnOnce(&mut serde_json::Value)) {
    let p = build.join("manifest.json");
    let mut v: serde_json::Value = serde_json::from_str(&fs::read_to_string(&p).unwrap()).unwrap();
    f(&mut v);
    fs::write(&p, serde_json::to_string_pretty(&v).unwrap()).unwrap();
}

fn docs() -> PathBuf {
    Path::new(FIXTURE).join("docs-dist")
}

fn generate(build: &Path, out: &Path) -> Result<Report, String> {
    run(&Options { build: build.to_path_buf(), out: out.to_path_buf(), docs_dist: Some(docs()) })
}

/// The pristine fixture, built once per test into its own directory.
fn built(name: &str) -> PathBuf {
    let root = scratch(name);
    let out = root.join("dist");
    let r = generate(&Path::new(FIXTURE).join("build"), &out).unwrap();
    assert_eq!(r.pages, 10);
    out
}

fn urls() -> Vec<(&'static str, &'static str, String)> {
    ["main", "app"].iter().flat_map(|p| LANGS.iter().map(move |l| (*p, *l, manifest::page_url(l, p)))).collect()
}

fn read_page(out: &Path, url: &str) -> String {
    fs::read_to_string(out.join(url.trim_start_matches('/')).join("index.html")).unwrap()
}

fn files(dir: &Path) -> BTreeMap<String, Vec<u8>> {
    fn walk(d: &Path, root: &Path, out: &mut BTreeMap<String, Vec<u8>>) {
        for e in fs::read_dir(d).unwrap() {
            let p = e.unwrap().path();
            if p.is_dir() {
                walk(&p, root, out);
            } else {
                out.insert(p.strip_prefix(root).unwrap().to_string_lossy().replace('\\', "/"), fs::read(&p).unwrap());
            }
        }
    }
    let mut m = BTreeMap::new();
    walk(dir, dir, &mut m);
    m
}

fn err(r: Result<Report, String>) -> String {
    r.expect_err("the build should have failed")
}

// ---- what is written ----

#[test]
fn every_page_in_every_language_is_written_at_its_address() {
    let out = built("files");
    let want: Vec<_> = ["index.html", "ru/index.html", "zh/index.html", "fa/index.html", "tr/index.html", "app/index.html", "ru/app/index.html", "zh/app/index.html", "fa/app/index.html", "tr/app/index.html"].into();
    for f in want {
        assert!(out.join(f).is_file(), "{f} is missing");
    }
    // nothing but the pages, the assets and _headers: no _redirects, no stray files
    let got: BTreeSet<_> = files(&out).into_keys().filter(|f| !f.starts_with("assets/")).collect();
    assert_eq!(got.len(), 11);
    assert!(got.contains("_headers"));
    let build = files(&Path::new(FIXTURE).join("build").join("assets"));
    let dist: BTreeMap<_, _> = files(&out.join("assets"));
    assert_eq!(build, dist, "dist/assets is build/assets");
}

#[test]
fn lang_dir_and_data_lg_are_right() {
    let out = built("lang");
    for (_, l, url) in urls() {
        let tags = html::tags(&read_page(&out, &url));
        let h = tags.iter().find(|t| t.name == "html").unwrap();
        let info = manifest::lang(l).unwrap();
        assert_eq!(h.attr("lang"), Some(info.html_lang), "{url}");
        assert_eq!(h.attr("dir"), Some(if l == "fa" { "rtl" } else { "ltr" }), "{url}");
        assert_eq!(h.attr("data-lg"), Some(l.to_uppercase().as_str()), "{url}");
    }
    assert!(read_page(&out, "/fa/").starts_with("<!doctype html>\n<html lang=\"fa\" dir=\"rtl\" data-lg=\"FA\">"));
    assert!(read_page(&out, "/zh/").contains("<html lang=\"zh-CN\""), "Chinese is zh-CN in <html> and zh-Hans in hreflang");
}

#[test]
fn hreflang_is_mutual_and_complete() {
    let out = built("hreflang");
    for page in ["main", "app"] {
        let mut sets = Vec::new();
        for l in LANGS {
            let url = manifest::page_url(l, page);
            let tags = html::tags(&read_page(&out, &url));
            let alt: BTreeMap<String, String> = tags.iter().filter(|t| t.name == "link" && t.attr("rel") == Some("alternate")).map(|t| (t.attr("hreflang").unwrap().into(), t.attr("href").unwrap().into())).collect();
            // self-referencing and canonical
            let canonical: Vec<_> = tags.iter().filter(|t| t.name == "link" && t.attr("rel") == Some("canonical")).map(|t| t.attr("href").unwrap()).collect();
            assert_eq!(canonical, [format!("https://mistgate.app{url}")]);
            assert_eq!(alt[manifest::lang(l).unwrap().hreflang], format!("https://mistgate.app{url}"), "{url} lists itself");
            sets.push(alt);
        }
        assert!(sets.windows(2).all(|w| w[0] == w[1]), "every language of {page} lists the same alternates");
        let keys: Vec<_> = sets[0].keys().cloned().collect();
        assert_eq!(keys, ["en", "fa", "ru", "tr", "x-default", "zh-Hans"]);
        assert_eq!(sets[0]["x-default"], sets[0]["en"]);
        // every alternate is a page that exists
        for href in sets[0].values() {
            let url = href.strip_prefix("https://mistgate.app").unwrap();
            assert!(out.join(url.trim_start_matches('/')).join("index.html").is_file(), "{href}");
        }
    }
}

#[test]
fn head_has_title_description_open_graph_icons_and_the_manifests_files() {
    let out = built("head");
    let page = read_page(&out, "/ru/");
    let tags = html::tags(&page);
    let meta = |k: &str, v: &str| tags.iter().find(|t| t.name == "meta" && t.attr(k) == Some(v)).and_then(|t| t.attr("content")).map(str::to_string);
    assert!(page.contains("<title>Mistgate: Панель для небольшого VPN на своих серверах</title>"));
    assert_eq!(meta("name", "description").as_deref(), Some("Панель для небольшого VPN на своих серверах."));
    assert_eq!(meta("property", "og:locale").as_deref(), Some("ru_RU"));
    assert_eq!(meta("property", "og:url").as_deref(), Some("https://mistgate.app/ru/"));
    assert_eq!(meta("property", "og:image").as_deref(), Some("https://mistgate.app/og.png"));
    assert_eq!(meta("name", "twitter:card").as_deref(), Some("summary_large_image"));
    assert_eq!(meta("name", "theme-color").as_deref(), Some("#0c0c0e"));
    let link = |rel: &str| -> Vec<&str> { tags.iter().filter(|t| t.name == "link" && t.attr("rel") == Some(rel)).map(|t| t.attr("href").unwrap()).collect() };
    assert_eq!(link("stylesheet"), ["/assets/land.1a2b3c4d.css"]);
    assert_eq!(link("modulepreload"), ["/assets/rt.1a2b3c4d.js"]);
    assert_eq!(link("preload"), ["/assets/onest-latin.1a2b3c4d.woff2"]);
    assert!(page.contains(r#"<link rel="preload" href="/assets/onest-latin.1a2b3c4d.woff2" as="font" type="font/woff2" crossorigin="">"#));
    assert_eq!(link("icon"), ["/favicon.svg", "/favicon-32.png"]);
    assert_eq!(link("apple-touch-icon"), ["/apple-touch-icon.png"]);
    let scripts: Vec<_> = tags.iter().filter(|t| t.name == "script").map(|t| (t.attr("type"), t.attr("src"))).collect();
    assert_eq!(scripts[0], (Some("module"), Some("/assets/main.1a2b3c4d.js")), "the module of the page, external");
    assert!(read_page(&out, "/app/").contains(r#"<script type="module" src="/assets/app.1a2b3c4d.js">"#));
}

#[test]
fn no_hydration_markers_and_the_fragment_is_byte_for_byte() {
    let build = fixture_build("markers");
    // text and markup in the fragment that Leptos must neither escape nor touch, `<!>` included
    let tricky = "<p>a &amp; b <b>c</b> <!> <!-- note --> \"q\" &lt;x&gt;</p>\n";
    edit(&build, "pages/main.en.html", |s| format!("{s}{tricky}"));
    let out = dist_of(&build);
    generate(&build, &out).unwrap();
    for (_, _, url) in urls() {
        let page = read_page(&out, &url);
        let (head, body) = page.split_once("</head>").unwrap();
        assert!(!head.contains("<!>") && !head.contains("<!--"), "{url}: Leptos markers in <head>");
        assert!(body.starts_with("<body>") && body.ends_with("</body></html>\n"));
        if url == "/" {
            assert!(body.contains(tricky));
        }
    }
    let frag = fs::read_to_string(build.join("pages/main.en.html")).unwrap();
    assert!(read_page(&out, "/").contains(&format!("<body>{frag}</body>")));
}

#[test]
fn head_text_is_escaped() {
    let build = fixture_build("escape");
    edit_manifest(&build, |v| {
        v["pages"]["main"]["en"]["title"] = r#"A & <B> "q""#.into();
        v["pages"]["main"]["en"]["description"] = r#"1 < 2 & "x" 'y'"#.into();
    });
    let out = dist_of(&build);
    generate(&build, &out).unwrap();
    let page = read_page(&out, "/");
    assert!(page.contains("<title>A &amp; &lt;B&gt; \"q\"</title>"), "{}", &page[..400]);
    let tags = html::tags(&page);
    let d = tags.iter().find(|t| t.attr("name") == Some("description")).unwrap();
    assert_eq!(html::decode(d.attr("content").unwrap()), r#"1 < 2 & "x" 'y'"#);
    assert!(!page[..page.find("</head>").unwrap()].contains("<B>"));
}

#[test]
fn headers_have_the_csp_and_the_caches() {
    let out = built("headers");
    let h = fs::read_to_string(out.join("_headers")).unwrap();
    let csp = h.lines().find_map(|l| l.trim().strip_prefix("Content-Security-Policy: ")).unwrap();
    let directive = |name: &str| csp.split(';').map(str::trim).find(|d| d.starts_with(name)).unwrap_or_default().to_string();
    assert_eq!(directive("script-src"), "script-src 'self'", "no inline script, no eval, no hash");
    assert_eq!(directive("object-src"), "object-src 'none'");
    assert!(directive("style-src").contains("'unsafe-inline'"), "as the docs site: the design's markup has style=\"\"");
    assert!(!csp.contains("unsafe-eval"));
    // a year, immutable, for the hashed files; HTML revalidates; the `/*` rule has no Cache-Control (Pages would join them)
    let rules: Vec<_> = h.split("\n\n").collect();
    let rule = |path: &str| rules.iter().find(|r| r.lines().next() == Some(path)).unwrap_or_else(|| panic!("no rule for {path}"));
    assert!(rule("/assets/*").contains("Cache-Control: public, max-age=31536000, immutable"));
    assert!(!rule("/*").contains("Cache-Control"));
    for (_, _, url) in urls() {
        assert!(rule(&url).contains("max-age=0, must-revalidate"), "{url}");
    }
}

#[test]
fn two_builds_are_byte_identical() {
    let a = built("det-a");
    let b = built("det-b");
    let (fa, fb) = (files(&a), files(&b));
    assert_eq!(fa.keys().collect::<Vec<_>>(), fb.keys().collect::<Vec<_>>());
    assert!(fa == fb, "the contents differ");
    // and a rebuild over an existing dist leaves no trace of it
    fs::write(a.join("stale.txt"), "x").unwrap();
    generate(&Path::new(FIXTURE).join("build"), &a).unwrap();
    assert!(files(&a) == fb);
}

// ---- what is refused ----

#[test]
fn a_broken_manifest_says_which_file_and_why() {
    let build = fixture_build("bad-json");
    edit(&build, "manifest.json", |s| s.replace("\"langs\"", "\"langs\" ,,"));
    let e = err(generate(&build, &dist_of(&build)));
    assert!(e.contains("manifest.json") && e.contains("line"), "{e}");
    let e = err(generate(&scratch("no-build").join("nothing"), &dist_of(&build)));
    assert!(e.contains("no such directory"), "{e}");
}

#[test]
fn manifest_problems_are_listed_together() {
    let build = fixture_build("bad-manifest");
    edit_manifest(&build, |v| {
        v["v"] = 2.into();
        v["pages"]["main"].as_object_mut().unwrap().remove("tr");
        v["pages"]["main"]["fa"]["dir"] = "ltr".into();
        v["pages"]["main"]["en"]["css"] = serde_json::json!(["assets/nope.css"]);
        v["pages"]["app"]["ru"]["title"] = "".into();
        v["pages"]["app"]["zh"]["preload"] = serde_json::json!(["assets/logo.1a2b3c4d.svg"]);
        v["pages"]["blog"] = v["pages"]["app"].clone();
    });
    let e = err(generate(&build, &dist_of(&build)));
    for needle in ["\"v\" is 2", "page \"main\" has no \"tr\"", "\"dir\" is \"ltr\", the language fa is rtl", "css\" entry assets/nope.css does not exist", "pages.app.ru: empty \"title\"", "unknown page \"blog\""] {
        assert!(e.contains(needle), "missing {needle:?} in:\n{e}");
    }
    // a preload that is neither a font nor a module is found after the manifest passes
    let build = fixture_build("bad-preload");
    edit_manifest(&build, |v| v["pages"]["app"]["zh"]["preload"] = serde_json::json!(["assets/logo.1a2b3c4d.svg"]));
    let e = err(generate(&build, &dist_of(&build)));
    assert!(e.contains("pages.app.zh") && e.contains("is not a .woff2 font, a .js module or a .json file"), "{e}");
}

#[test]
fn a_missing_fragment_is_named() {
    let build = fixture_build("no-fragment");
    fs::remove_file(build.join("pages/main.ru.html")).unwrap();
    let e = err(generate(&build, &dist_of(&build)));
    assert!(e.contains("pages/main.ru.html") && e.contains("does not exist"), "{e}");
}

#[test]
fn inline_scripts_and_handlers_in_a_fragment_fail_the_build_with_its_name() {
    for (name, bad, needle) in [
        ("script", "<script>alert(1)</script>", "an inline <script>"),
        ("handler", "<img src=\"/assets/logo.1a2b3c4d.svg\" alt=\"\" onerror=\"x()\">", "onerror="),
        ("handler-upper", "<a href=\"#top\" ONCLICK=go()>x</a>", "onclick="),
        ("javascript-url", "<a href=\"javascript:void(0)\">x</a>", "a javascript: URL"),
        ("document-tags", "<body class=\"x\"></body>", "a <body> tag"),
    ] {
        let build = fixture_build(&format!("frag-{name}"));
        edit(&build, "pages/app.zh.html", |s| format!("{s}{bad}"));
        let e = err(generate(&build, &dist_of(&build)));
        assert!(e.contains("pages/app.zh.html") && e.contains(needle), "{name}: {e}");
    }
    // text that only looks like it is fine: in a comment, in a JSON data block, in the text of an element
    let build = fixture_build("frag-fine");
    edit(&build, "pages/app.zh.html", |s| format!("{s}<!-- <script>x</script> --><p>&lt;script&gt; onclick=1</p><textarea><script>y</script></textarea>"));
    generate(&build, &dist_of(&build)).unwrap();
}

#[test]
fn links_to_missing_assets_and_missing_pages_fail() {
    for (name, bad, needle) in [
        ("asset", "<img src=\"/assets/gone.1a2b3c4d.svg\" alt=\"\">", "/assets/gone.1a2b3c4d.svg: no such file in build/assets"),
        ("asset-css", "<i style=\"background:url(/assets/gone.png)\"></i>", "/assets/gone.png"),
        ("docs-page", "<a href=\"/ru/guide/no-such-page/\">x</a>", "/ru/guide/no-such-page/: no such page or file in the docs"),
        ("docs-no-slash", "<a href=\"/getting-started/overview\">x</a>", "/getting-started/overview"),
        ("docs-anchor", "<a href=\"/ru/getting-started/install-panel/#нет-такого\">x</a>", "the page has no #нет-такого"),
        ("landing-page", "<a href=\"/tr/pricing/\">x</a>", "/tr/pricing/"),
        ("relative", "<a href=\"guide/vless/\">x</a>", "the relative link"),
    ] {
        let build = fixture_build(&format!("link-{name}"));
        edit(&build, "pages/main.tr.html", |s| format!("{s}{bad}"));
        let e = err(generate(&build, &dist_of(&build)));
        assert!(e.contains("pages/main.tr.html") && e.contains(needle), "{name}: {e}");
    }
    // the right ones pass: a percent-encoded Cyrillic anchor, a query, the landing's own pages, other sites
    let build = fixture_build("link-ok");
    edit(&build, "pages/main.tr.html", |s| format!("{s}<a href=\"/ru/getting-started/install-panel/#%D1%83%D1%81%D1%82%D0%B0%D0%BD%D0%BE%D0%B2%D0%BA%D0%B0-%D0%BE%D0%B4%D0%BD%D0%BE%D0%B9-%D0%BA%D0%BE%D0%BC%D0%B0%D0%BD%D0%B4%D0%BE%D0%B9\">x</a><a href=\"/ru/app/?x=1#y\">y</a><a href=\"//example.com/\">z</a><a href=\"mailto:a@b.c\">m</a>"));
    generate(&build, &dist_of(&build)).unwrap();
}

#[test]
fn without_a_docs_dist_the_docs_links_are_counted_not_checked() {
    let build = fixture_build("no-docs");
    edit(&build, "pages/main.tr.html", |s| format!("{s}<a href=\"/ru/guide/no-such-page/\">x</a>"));
    let r = run(&Options { build: build.clone(), out: dist_of(&build), docs_dist: None }).unwrap();
    assert!(r.unchecked_docs_links >= 10, "{}", r.unchecked_docs_links);
}

#[test]
fn a_page_without_its_data_is_a_todo_warning_not_a_failure() {
    let build = fixture_build("todo");
    edit_manifest(&build, |v| {
        for k in ["title", "description"] {
            v["pages"]["app"]["fa"].as_object_mut().unwrap().remove(k);
        }
        v["pages"]["app"]["fa"]["todo"] = true.into();
        v["pages"]["main"]["tr"]["todo"] = true.into();
        v["pages"]["main"]["tr"]["title"] = "".into();
    });
    let out = dist_of(&build);
    let r = generate(&build, &out).unwrap();
    assert_eq!(r.todo, ["app/fa", "main/tr"]);
    // built with the English title and description, so a preview still reads
    let fa = read_page(&out, "/fa/app/");
    assert!(fa.contains("<title>Mistgate: Application</title>"), "{}", &fa[..300]);
    assert!(fa.contains("<html lang=\"fa\" dir=\"rtl\""));
    // the same gaps without todo are an error
    edit_manifest(&build, |v| {
        v["pages"]["app"]["fa"]["todo"] = false.into();
    });
    let e = err(generate(&build, &out));
    assert!(e.contains("pages.app.fa: empty \"title\""), "{e}");
}

#[test]
fn the_output_directory_may_not_be_or_hold_the_input() {
    let build = fixture_build("out-guard");
    for out in [build.clone(), build.parent().unwrap().to_path_buf(), build.join("assets")] {
        let e = err(generate(&build, &out));
        assert!(e.contains("must be a directory of its own"), "{e}");
    }
    assert!(build.join("manifest.json").is_file(), "the input was not touched");
}

// ---- the check on the finished dist is not blind ----

#[test]
fn verify_dist_finds_what_a_hand_edit_broke() {
    let build = Path::new(FIXTURE).join("build");
    let m = manifest::load(&build).unwrap();
    let out = built("verify");
    assert_eq!(check::verify_dist(&out, &m), Vec::<String>::new());

    let ru = out.join("ru/index.html");
    let good = fs::read_to_string(&ru).unwrap();
    for (what, bad, needle) in [
        ("inline script", good.replace("</head>", "<script>1</script></head>"), "an inline <script>"),
        ("handler", good.replace("<body>", "<body onload=\"x()\">"), "onload="),
        ("hreflang", good.replace("hreflang=\"tr\"", "hreflang=\"xx\""), "hreflang set"),
        ("lang", good.replace("lang=\"ru\"", "lang=\"en\""), "<html lang>"),
        ("dir", good.replace("dir=\"ltr\"", "dir=\"rtl\""), "<html dir>"),
        ("canonical", good.replace("href=\"https://mistgate.app/ru/\">", "href=\"https://mistgate.app/\">"), "canonical"),
        ("asset", good.replace("land.1a2b3c4d.css", "land.00000000.css"), "is not in dist"),
    ] {
        fs::write(&ru, &bad).unwrap();
        let found = check::verify_dist(&out, &m).join("\n");
        assert!(found.contains(needle), "{what}: {found}");
    }
    fs::write(&ru, &good).unwrap();
    fs::remove_file(out.join("fa/app/index.html")).unwrap();
    fs::remove_file(out.join("_headers")).unwrap();
    let found = check::verify_dist(&out, &m).join("\n");
    assert!(found.contains("/fa/app/") && found.contains("was not written") && found.contains("_headers"), "{found}");
}

// ---- against the real docs, when the docs generator has been run ----

/// `MG_DOCS_DIST=<dist of node site/build.mjs> cargo test -- --ignored`: the fixture's links into the docs (an English,
/// a Russian page with a Cyrillic anchor, the other three languages, a root file) are real pages of the real site.
#[test]
#[ignore = "needs MG_DOCS_DIST, the dist of the docs generator"]
fn the_fixtures_links_exist_in_the_real_docs() {
    let dist = std::env::var("MG_DOCS_DIST").expect("set MG_DOCS_DIST");
    let r = run(&Options { build: Path::new(FIXTURE).join("build"), out: scratch("real-docs").join("dist"), docs_dist: Some(dist.clone().into()) }).unwrap();
    assert_eq!(r.unchecked_docs_links, 0);
    // and the real index knows what is not there
    let idx = check::DocsIndex::load(Path::new(&dist)).unwrap();
    assert!(idx.has("/getting-started/overview/") && !idx.has("/getting-started/overview"));
    // the start page of the docs is /docs/; / is the landing's
    assert!(idx.has("/docs/") && idx.has("/ru/docs/") && !idx.has("/"));
    assert!(idx.has_id("/ru/getting-started/install-panel/", "установка-одной-командой"));
    assert!(!idx.has("/ru/guide/no-such-page/"));
}
