//! The page document as Leptos components, rendered to a string at build time. Nothing of Leptos reaches the browser:
//! no wasm, no hydration markers, no runtime. The body fragment from the board pipeline goes in as it is.

use crate::manifest::SITE;
use leptos::prelude::*;
use leptos::tachys::html::attribute::custom::custom_attribute;
use leptos::reactive::owner::Owner;

/// One `<link rel="alternate" hreflang>`.
#[derive(Clone)]
pub struct Alternate {
    pub hreflang: String,
    pub href: String,
}

/// Everything a page's `<head>` says. URLs are absolute (`https://…`) where a crawler reads them, root-relative otherwise.
#[derive(Clone)]
pub struct Head {
    pub title: String,
    pub description: String,
    pub canonical: String,
    pub og_locale: String,
    pub alternates: Vec<Alternate>,
    /// root-relative URLs of `.woff2` files to preload
    pub fonts: Vec<String>,
    /// root-relative URLs of modules to `modulepreload`
    pub module_preloads: Vec<String>,
    /// root-relative URLs of `.json` files the page fetches at once
    pub fetches: Vec<String>,
    pub css: Vec<String>,
    /// root-relative URLs of stylesheets that do not block the first paint
    pub css_async: Vec<String>,
    /// root-relative URL of the loader script: with one, the page's own modules, their preloads, the data it fetches and the async stylesheets are not tags
    /// of `<head>` but a JSON block the loader starts from after the first paint
    pub loader: Option<String>,
    pub modules: Vec<String>,
}

#[derive(Clone)]
pub struct Page {
    pub html_lang: String,
    pub dir: String,
    /// the upper-case language code on `<html data-lg>`: the selectors of the design (`html[data-lg="FA"] .lp`)
    pub data_lg: String,
    pub head: Head,
    /// the inside of `<body>`, trusted build input
    pub body: String,
}

// the landing is dark only (the design's --bg); the docs pages have a light theme, these do not
const THEME_COLOR: &str = "#0c0c0e";

/// `<meta property=… content=…>`. The `view!` macro has no typed `property` attribute (a literal one is let through
/// only while the whole element is static), so the attribute goes in by its name.
fn og(property: &'static str, content: String) -> impl IntoView {
    leptos::html::meta().add_any_attr(custom_attribute("property", property)).content(content)
}

/// What `<noscript>` holds: the async stylesheets plain, and the rule that shows what the stylesheet of the page would have shown (the design hides the hero window until the sheet is on)
fn noscript_css(css: &[String]) -> String {
    let links: String = css.iter().map(|c| format!(r#"<link rel="stylesheet" href="{c}">"#)).collect();
    format!("{links}<style>.lpe.hero-e{{visibility:visible}}</style>")
}

/// The JSON block the loader reads (`<script type="application/json" id="mg-boot">`): what the page starts after its first paint.
fn boot_json(head: &Head) -> String {
    let j = serde_json::json!({ "css": head.css_async, "preload": head.module_preloads, "fetch": head.fetches, "modules": head.modules });
    j.to_string().replace('<', "\\u003c") // no `</script>` can end the block
}

#[component]
fn HeadTags(head: Head) -> impl IntoView {
    let og_image = format!("{SITE}/og.png");
    let boot = head.loader.clone().map(|l| view! { <script type="application/json" id="mg-boot" inner_html=boot_json(&head)></script><script src=l defer=""></script> });
    let direct = head.loader.is_none();
    view! {
        <head>
        <meta charset="utf-8"/>
        <meta name="viewport" content="width=device-width, initial-scale=1"/>
        <title>{head.title.clone()}</title>
        <meta name="description" content=head.description.clone()/>
        <link rel="canonical" href=head.canonical.clone()/>
        {head.alternates.into_iter().map(|a| view! { <link rel="alternate" hreflang=a.hreflang href=a.href/> }).collect_view()}
        {og("og:site_name", "Mistgate".into())}
        {og("og:type", "website".into())}
        {og("og:title", head.title.clone())}
        {og("og:description", head.description.clone())}
        {og("og:url", head.canonical.clone())}
        {og("og:locale", head.og_locale.clone())}
        {og("og:image", og_image.clone())}
        <meta name="twitter:card" content="summary_large_image"/>
        <meta name="color-scheme" content="dark"/>
        <meta name="theme-color" content=THEME_COLOR/>
        <link rel="icon" href="/favicon.svg" type="image/svg+xml"/>
        <link rel="icon" href="/favicon-32.png" sizes="32x32" type="image/png"/>
        <link rel="apple-touch-icon" href="/apple-touch-icon.png"/>
        {head.css.into_iter().map(|c| view! { <link rel="stylesheet" href=c/> }).collect_view()}
        {direct.then(|| head.css_async.iter().map(|c| view! { <link rel="stylesheet" href=c.clone() media="print" data-async=""/> }).collect_view())}
        {(!head.css_async.is_empty()).then(|| view! { <noscript inner_html=noscript_css(&head.css_async)></noscript> })}
        {head.fonts.into_iter().map(|f| view! { <link rel="preload" href=f r#as="font" type="font/woff2" crossorigin=""/> }).collect_view()}
        {boot}
        {direct.then(|| head.module_preloads.iter().map(|m| view! { <link rel="modulepreload" href=m.clone() fetchpriority="low"/> }).collect_view())}
        {direct.then(|| head.fetches.iter().map(|f| view! { <link rel="preload" href=f.clone() r#as="fetch" crossorigin="" fetchpriority="low"/> }).collect_view())}
        {direct.then(|| head.modules.iter().map(|m| view! { <script type="module" src=m.clone()></script> }).collect_view())}
        </head>
    }
}

#[component]
pub fn Document(page: Page) -> impl IntoView {
    view! {
        <html lang=page.html_lang dir=page.dir data-lg=page.data_lg>
            <HeadTags head=page.head/>
            <body inner_html=page.body></body>
        </html>
    }
}

/// The finished file: `<!doctype html>` and the document.
///
/// Leptos writes the separators hydration needs: a `<!>` after every list of nodes (`collect_view`). Nothing hydrates
/// here, so they are cut from the `<head>`. (`to_html_with_buf(.., escape = false, ..)` would not do: an element
/// passes its own `escape` to its children.) The body is not touched: it is the pipeline's fragment, byte for byte.
pub fn render(page: Page) -> String {
    let html = Owner::new().with(|| view! { <Document page=page/> }.to_html());
    let (head, rest) = html.split_once("</head>").expect("the document has a <head>");
    format!("<!doctype html>
{}</head>{rest}
", head.replace("<!>", ""))
}
