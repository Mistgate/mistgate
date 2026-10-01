# Mistgate documentation sources

This directory is the source of the Mistgate documentation. It reads fine on GitHub as plain Markdown and is also the input of the documentation site. This file is for whoever builds that site; it is not a page of the documentation itself.

## Layout

```
docs/
  en/
    index.md                 the start page and the table of contents
    getting-started/*.md
    guide/*.md
    operations/*.md
    reference/*.md
  ru/                        the same tree, page for page, in Russian
```

- `en/` and `ru/` are mirrors: every page exists in both languages at the same relative path and with the same headings, so a language switch maps `en/<path>` to `ru/<path>`.
- Sections are directories, pages are files. A section has no index page of its own; `index.md` of each language is the only start page.

## Page format

Every page starts with YAML front matter:

```yaml
---
title: Short page title
description: One sentence that says what the page answers.
---
```

- `title` is the page heading and the sidebar label. The body has **no H1**: render `title` as the H1. The body starts with one or two sentences of text, then H2 and H3 sections.
- `description` is a single sentence, usable as the meta description and as the summary in search results and link previews.
- No other front matter fields are used.

## Links

- Links between pages are relative Markdown links to `.md` files, for example `../guide/nodes.md` or `getting-started/overview.md`. The site rewrites them to its own URLs; on GitHub they work as they are.
- Links point only to pages of the tree above, within the same language.

## Callouts

A callout is a blockquote whose first word is a bold label:

```markdown
> **Note:** a useful fact that is easy to miss.

> **Warning:** something that can break a node, lock you out or lose data.
```

| English | Russian | Meaning |
|:--|:--|:--|
| `**Note:**` | `**Важно:**` | information |
| `**Warning:**` | `**Внимание:**` | risk of breakage or data loss |

Render them as styled boxes; on GitHub they stay plain blockquotes. Features that are being built but are not in the code yet are marked with the bold word **Planned** (Russian: **Планируется**) in the text.

Other conventions: code blocks always carry a language tag (`sh`, `yaml`, `json`, `ini`), there are no images or screenshots, and examples use reserved names only (`panel.example.com`, `203.0.113.0/24`, `2001:db8::/32`).

## Sidebar order

`index.md` comes first. After it the sidebar follows the list under "All pages" in `<lang>/index.md`: the H3 headings there are the sidebar groups, and the links under each heading are the pages in their order. When a page is added, removed or moved, change that list in both `en/index.md` and `ru/index.md`; the site takes its order from there and from nowhere else.
