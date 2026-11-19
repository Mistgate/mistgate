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
    pub css: Vec<String>,
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

#[component]
fn HeadTags(head: Head) -> impl IntoView {
    let og_image = format!("{SITE}/og.png");
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
        {head.fonts.into_iter().map(|f| view! { <link rel="preload" href=f r#as="font" type="font/woff2" crossorigin=""/> }).collect_view()}
        {head.module_preloads.into_iter().map(|m| view! { <link rel="modulepreload" href=m/> }).collect_view()}
        {head.css.into_iter().map(|c| view! { <link rel="stylesheet" href=c/> }).collect_view()}
        {head.modules.into_iter().map(|m| view! { <script type="module" src=m></script> }).collect_view()}
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
