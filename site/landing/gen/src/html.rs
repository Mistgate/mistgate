//! A small tag scanner for HTML the build itself produced or was handed (board fragments, finished pages).
//! It is not a browser parser: it finds tags and their attributes, which is all the checks need, and it knows that the
//! text of `<script>`, `<style>`, `<title>` and `<textarea>` is not markup and that comments hide tags.

pub struct Tag {
    /// lower case; no slash
    pub name: String,
    pub closing: bool,
    /// names lower-cased, values as written (entities not decoded); a bare attribute has the value ""
    pub attrs: Vec<(String, String)>,
}

impl Tag {
    pub fn attr(&self, name: &str) -> Option<&str> {
        self.attrs.iter().find(|(k, _)| k == name).map(|(_, v)| v.as_str())
    }
}

const RAW_TEXT: [&str; 4] = ["script", "style", "title", "textarea"];

pub fn tags(html: &str) -> Vec<Tag> {
    let lower = html.to_ascii_lowercase(); // same byte offsets as `html`
    let b = html.as_bytes();
    let mut out = Vec::new();
    let mut i = 0;
    while i < b.len() {
        if b[i] != b'<' {
            i += 1;
            continue;
        }
        let rest = &lower[i..];
        if rest.starts_with("<!--") {
            i = rest.find("-->").map_or(b.len(), |e| i + e + 3);
            continue;
        }
        let closing = rest.starts_with("</");
        let name_at = i + if closing { 2 } else { 1 };
        if !b.get(name_at).is_some_and(|c| c.is_ascii_alphabetic()) {
            // `<!doctype …>`, `<?…>`, or a lone `<` in text
            i = if rest.starts_with("<!") || rest.starts_with("<?") { rest.find('>').map_or(b.len(), |e| i + e + 1) } else { i + 1 };
            continue;
        }
        let mut j = name_at;
        while j < b.len() && !b[j].is_ascii_whitespace() && b[j] != b'>' && b[j] != b'/' {
            j += 1;
        }
        let name = lower[name_at..j].to_string();
        let mut attrs = Vec::new();
        loop {
            while j < b.len() && (b[j].is_ascii_whitespace() || b[j] == b'/') {
                j += 1;
            }
            if j >= b.len() || b[j] == b'>' {
                break;
            }
            let k0 = j;
            while j < b.len() && !b[j].is_ascii_whitespace() && b[j] != b'=' && b[j] != b'>' && b[j] != b'/' {
                j += 1;
            }
            if j == k0 {
                j += 1; // a stray `=` or quote: skip it
                continue;
            }
            let key = lower[k0..j].to_string();
            while j < b.len() && b[j].is_ascii_whitespace() {
                j += 1;
            }
            let mut val = String::new();
            if j < b.len() && b[j] == b'=' {
                j += 1;
                while j < b.len() && b[j].is_ascii_whitespace() {
                    j += 1;
                }
                if j < b.len() && (b[j] == b'"' || b[j] == b'\'') {
                    let q = b[j];
                    let v0 = j + 1;
                    j = v0;
                    while j < b.len() && b[j] != q {
                        j += 1;
                    }
                    val = html[v0..j].to_string();
                    j = (j + 1).min(b.len());
                } else {
                    let v0 = j;
                    while j < b.len() && !b[j].is_ascii_whitespace() && b[j] != b'>' {
                        j += 1;
                    }
                    val = html[v0..j].to_string();
                }
            }
            attrs.push((key, val));
        }
        i = (j + 1).min(b.len());
        if !closing && RAW_TEXT.contains(&name.as_str()) {
            i = lower[i..].find(&format!("</{name}")).map_or(b.len(), |e| i + e);
        }
        out.push(Tag { name, closing, attrs });
    }
    out
}

/// `&amp;` and friends in an attribute value that is a URL or an id.
pub fn decode(s: &str) -> String {
    s.replace("&quot;", "\"").replace("&#39;", "'").replace("&lt;", "<").replace("&gt;", ">").replace("&amp;", "&")
}

/// What may not be in a page, whoever wrote it: an inline script (CSP `script-src 'self'`), an inline handler, a
/// `javascript:` URL. A JSON data block (`<script type="application/json">`) is not run and is allowed.
pub fn script_problems(tags: &[Tag]) -> Vec<String> {
    let mut bad = Vec::new();
    for t in tags.iter().filter(|t| !t.closing) {
        if t.name == "script" && t.attr("src").is_none() {
            let ty = t.attr("type").unwrap_or("").trim().to_ascii_lowercase();
            if ty != "application/json" && ty != "application/ld+json" {
                bad.push(format!("an inline <script{}>", if ty.is_empty() { String::new() } else { format!(" type=\"{ty}\"") }));
            }
        }
        for (k, v) in &t.attrs {
            if k.len() > 2 && k.starts_with("on") && k[2..].bytes().all(|c| c.is_ascii_lowercase()) {
                bad.push(format!("the inline handler {k}= on <{}>", t.name));
            }
            if matches!(k.as_str(), "href" | "src" | "action" | "formaction" | "poster" | "xlink:href") && decode(v).trim_start().to_ascii_lowercase().starts_with("javascript:") {
                bad.push(format!("a javascript: URL in {k}= on <{}>", t.name));
            }
        }
    }
    bad
}

/// The URLs a page points at: href, src, poster, the URLs of srcset and of `url(...)` in style attributes.
pub fn links(tags: &[Tag]) -> Vec<String> {
    let mut out = Vec::new();
    for t in tags.iter().filter(|t| !t.closing) {
        for (k, v) in &t.attrs {
            match k.as_str() {
                "href" | "src" | "poster" | "xlink:href" => out.push(decode(v)),
                "srcset" => out.extend(decode(v).split(',').filter_map(|c| c.split_whitespace().next().map(str::to_string))),
                "style" => {
                    let s = decode(v);
                    let mut rest = s.as_str();
                    while let Some(p) = rest.find("url(") {
                        rest = &rest[p + 4..];
                        let end = rest.find(')').unwrap_or(rest.len());
                        out.push(rest[..end].trim().trim_matches(|c| c == '"' || c == '\'').to_string());
                        rest = &rest[end..];
                    }
                }
                _ => {}
            }
        }
    }
    out.retain(|u| !u.is_empty());
    out
}

/// The `id` of every element and the `name` of every `<a>`: what a `#fragment` can point at.
pub fn ids(tags: &[Tag]) -> Vec<String> {
    tags.iter()
        .filter(|t| !t.closing)
        .flat_map(|t| t.attr("id").into_iter().chain(if t.name == "a" { t.attr("name") } else { None }))
        .map(decode)
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn finds_tags_through_quotes_comments_and_raw_text() {
        let h = r#"<!doctype html><!-- <script>x</script> --><p title="a>b" ONCLICK=go()>t</p>
            <script type="module" src="/a.js"></script><script>if (a<b) document.write("<img onerror=1>")</script>
            <style>a>b{}</style><TextArea><b onclick=1></TextArea><img src='/x.png' srcset="/a.png 1x, /b.png 2x" style="background:url('/c.svg')">"#;
        let t = tags(h);
        let names: Vec<_> = t.iter().filter(|t| !t.closing).map(|t| t.name.as_str()).collect();
        assert_eq!(names, ["p", "script", "script", "style", "textarea", "img"]);
        assert_eq!(t[0].attr("title"), Some("a>b"));
        assert_eq!(script_problems(&t), ["the inline handler onclick= on <p>", "an inline <script>"]);
        assert_eq!(links(&t), ["/a.js", "/x.png", "/a.png", "/b.png", "/c.svg"]);
    }

    #[test]
    fn data_blocks_are_not_scripts() {
        let t = tags(r#"<script type="application/json">{"a":1}</script><script type="importmap">{}</script>"#);
        assert_eq!(script_problems(&t), ["an inline <script type=\"importmap\">"]);
    }

    #[test]
    fn javascript_urls_and_ids() {
        let t = tags(r#"<a href=" JavaScript:alert(1)">x</a><a name="old"></a><h2 id="a&amp;b"></h2>"#);
        assert_eq!(script_problems(&t), ["a javascript: URL in href= on <a>"]);
        assert_eq!(ids(&t), ["old", "a&b"]);
    }
}
