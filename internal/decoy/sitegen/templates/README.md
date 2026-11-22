# Writing a kind of generated site

Short notes for whoever redesigns a kind. The big picture is the comment at the top of `generate.go`; this file is what changed
with the redesign. Each kind now has its **own** design (header, footer, grid, components); the shared part only gives tokens,
helpers and a few pages. Sites of one kind must still differ from each other, which is what the *axes* below are for.

Start from the mock-ups (outside the repository: the design canvas folder `canvas/project/`): `<Kind>.dc.html` is the home page,
the inner pages are next to it (StatusHistory, StatusIncident, DocsConfig, DocsFaq, DocsTerms, ProjectChangelog, ProjectRelease,
Project404, BlogPost, BlogArchive, ClubEvents, ClubJoin, PortfolioPiece, PortfolioAbout, BusinessServices, BusinessContact,
CardProjects, CardNow, LoginHelp, LoginPrivacy, FilesHow, FilesLimits), and `<kind>.css` is the whole style of the kind (media
queries and vw only): **take it as the base of `style.css.tmpl`**, then replace every colour, font, radius and size by a variable
of section 2 (the mock-up colours are the first palette of the kind's `style`, already in `meta.json`).

Which helper fits which page: the status history calendar is `calmonths`, a heat map of the year is `cal`, the incident timeline
times are `times` (+ a text for "UTC"), the response and uptime graphs are `series` + `chart` and `avail`, the release table with
sha256 is `hexs` + `fsize` (labels "Name" and "Size" are new texts of the project kind), the contact page map is `contactmap`,
contributors are `ident` + `person`, topics with counts are `tags`, the round badge of the bakery is `stamp`, install and
config samples are `hl`.

To look at your kind: `go test ./internal/decoy/sitegen -run TestDumpSites -decoy-dump=<dir> -decoy-dump-n=1 -decoy-dump-cc=us`
(add `-decoy-dump-new` to see the legacy kinds in their new palettes, fonts and ranges); `-run TestDumpDrawings -decoy-draw=<dir>`
writes one page with every drawing and code sample. Open them in a browser in the light and the dark scheme, at 1440 and 390 px.

## 1. The style of a kind (`meta.json` -> `"style"`)

```json
"style": {
  "legacy": true,                      // delete this line when the templates are redesigned
  "scheme": "light",                   // light | dark | both
  "palettes": [ {"name":"cream-terracotta", "l": {"bg":"#fbefd3","fg":"#3a2417","ac":"#b5472a","a2":"#3a6a4a"}}, ... ],
  "fonts":    [ {"head":"old","body":"sans","fw":700,"ls":"-.045em","lh":".95"}, ... ],
  "radius":[24,40], "radius_s":[14,24], "radius_b":[36,48],   // one number t moves the three together
  "density":[.95,1.1], "width":[1200,1280], "measure":[640,720],
  "base":[17,18], "scale":[1.33,1.45], "lh":[1.55,1.65], "stroke":[1.7,2.1], "border":[1,2], "shadows":["none"]
}
```

* `legacy: true` keeps the old random look (random hue, random radius and font) and ignores the rest; the palettes are still
  checked by tests. Every kind has it now. Remove it as you switch a kind to the new templates.
* **Scheme**: `light` has no dark media query and `color-scheme: light`; `dark` the opposite; `both` has both and then every palette
  needs `l` and `d`. Pick the scheme the mock-up has; use `both` only where a dark version is natural.
* **Palettes**: 5 to 8, in the spirit of the mock-up (a bakery: cream + terracotta, cream + olive, milk + blue...). `bg`, `fg`, `ac`
  are required; what is left out is derived (`sf` surface, `band`, `sf2`, `mu` muted, `ln`, `ln2`, `ai` ink on the accent,
  `a2`, `so` soft accent, `ok wa bd` and their soft fills `oks was bds`). Give a role explicitly when the mock-up has it.
  `TestPalettesReadable` checks every palette: text 7:1 on page and surface, muted 4.5:1 on page, surface and band, ink on accent
  4.5:1, state colours 3:1, and in dark schemes a dark page that is not tinted brown or olive. Note: the accent is **not** checked
  against the page, so do not use `var(--ac)` as the colour of long text unless your palettes allow it.
* **Fonts**: 2 or 3 sets of the system stacks in `fontStacks` (`generate_gfx.go`: sans, humanist, grotesque, charter, old, georgia,
  bookish, rounded, geometric, didone, verdana, mono). Never a font file. A site in Cyrillic (ru, kz) only gets sets whose stacks all
  have Cyrillic (`latinOnly` in `generate_style.go` lists those that do not), so at least one set must be Cyrillic-safe.
  `fw ls lh` set the headings, `label tt lsl fsl` the small caps labels, `mono` the code font.
* **Layouts**: `"layouts": 2 or 3` at the top of meta.json is the number of first-screen variants; templates read `.Layout`
  (0..n-1), as before. Section order is `home_blocks` / `home_fixed` / `.Order`, as before.
* **Small ranges** stay inside the character of the kind. Nothing but these ranges is random in the look.
* A variant (data.json bundle) may have `"art": "topo"`: the subject of its pictures (see `art` below).

## 2. Variables of the stylesheet

`{{template "base_css" .}}` (or the pieces below) writes `:root{...}`; use only these in CSS:

| group | variables |
|---|---|
| colour | `--bg --band --sf --sf2 --fg --mu --ln --ln2 --ac --ai --a2 --so --ok --wa --bd --oks --was --bds` |
| fonts | `--fb` body, `--fh` headings, `--fl` labels, `--fm` code, `--fwh --lsh --lhh` heading weight/spacing/line height, `--ttl --lsl --fsl` label transform/spacing/size |
| type scale | `--t0` body, `--t1 --t2` plain steps, `--t3 --t4 --t5` display sizes (`clamp()` that follows the window width and reaches the size at 1440 px), `--ts` small |
| shape | `--r --rs --rb` radii, `--bw` border width, `--sh` shadow |
| rhythm | `--s` (8px x density), `--sec` section padding, `--gut` gutter, `--w` container, `--wt` text column |

Also `.S.R .S.RS .S.W .S.FS .S.LH .S.Stroke ...` and `.P.L.* .P.D.* .Pm.*` (the tone the page is drawn in) in templates,
`.Scheme` (`light`/`dark`/`both`), `.Theme`, `.Art`. `.Pm` is the light tone unless the scheme is dark.

`base_css` is the sum of `base_vars` (the `:root`), `base_core` (reset, headings, `pre`, `.wrap`, `.ic`), `base_chrome` (the default
header, footer, `.btn .card .pill .grid`, tables), `base_pages` (mobile menu, legal page with contents, 404, contact map and form) and
`draw_css` (default colours of the drawings below). A redesigned kind includes `base_vars base_core base_pages draw_css` and writes
its own chrome; it may also redefine any of them by defining a template of the same name in its folder. Class names are global
to the site: do not reuse the names of `base_chrome` for something else (`__btn__ __card__ __grid__ __pill__ __hd__ __ft__ ...`).

## 3. Header and footer

A kind replaces the shared header or footer by defining `header` / `footer` in any of its templates (docs does it in
`home.html.tmpl`; a define is global to the kind, so one is enough). The shared ones stay for kinds that do not. A header should
include the phone menu:

```
{{define "header"}}<header class="__top__">... <nav class="__nav__">{{range .Nav}}<a href="{{.Href}}"{{if .Cur}} aria-current="page"{{end}}>{{.Label}}</a>{{end}}</nav>{{template "menu" .}}</header>{{end}}
```

`menu` is a `<details>` (no script): a button with the `menu` and `close` icons and a list of the same links. Its classes
`__mn__ __mnb__ __mnl__` are styled in `base_pages` (shown below 720 px; hide your own nav there). Restyle them in your CSS.
Exactly one `<h1>` per page: a masthead or a big brand name in the header is not an `h1`.

## 4. Functions

Keyed draws take a key as the first argument: the same key gives the same answer on every page. Colours are classes, never values;
the default colours of the classes are in `draw_css` (`base.css.tmpl`), restyle them in your CSS. A drawing has no `<title>`,
is at most 8 KB, and takes its numbers from the site's rng.

```
{{$s := series "resp" 60 110 190}}                      {v, last, min, max, avg, n}   60 numbers between 110 and 190
{{chart $s "area" 900 220}}                             kind spark | area | bars; classes dchart dline darea dgrid dbars dpt
{{$a := avail "api" 90 15 4 7}}                         {days, pct, bp, warn, bad}  90 days, 15 and 4 per mille, last 7 up
{{range $a.days}}<i class="{{if eq . 1}}__w__{{else if eq . 2}}__x__{{end}}"></i>{{end}} {{$a.pct}}%   0 up 1 degraded 2 down
{{cal .Lists.incidents "2026-11-14" 26}}                heat map of weeks x 7 days: classes dcal dc0 (quiet) dc1 dc2 (1, 2+ incidents)
{{range calmonths .Lists.incidents "2026-11-14" 3}}     {label, pad, days: [{n, k}]}  k 0 quiet 1 minor 2 major 3 maint 4 after the end
{{times "inc" 3}}                                       3 ascending times "14:24"; the time zone ("UTC") must be a text key
{{mapsvg "contact" 640 420}}                            a quarter: 6-9 streets, blocks, park, river, pin, 2-3 street names of the country
{{template "contactmap" .}}                             the map with the address under it, for a contact page
{{ident "alice"}}                                       5x5 mirrored pattern, fill classes di0..di3
{{$p := person "a1"}}{{$p.name}} {{$p.ini}}             {first, last, name, ini} of the site's country
{{range tags .Lists.posts "tag"}}{{.tag}} {{.n}}{{end}} [{tag, n}] of a field that is a word or a list of words, most used first
{{stamp "badge" .L.stamp 160}}                          a round stamp, the text once around the rim; classes dstamp dsb dsd dst dsm
{{hl .L.quick "sh"}}                                    code as escaped HTML in spans: dhc comment dhs string dhf flag dhk key dhn number dhp prompt dhm command
{{fsize "f1" 800 6000}}                                 a file size "4.2 MB" in the site's units and decimal mark
{{hexs "sha" 64}}                                       (existing) n hex digits: the sha256 column of a release
{{art "k" "" 480 360}}                                  (existing) a picture; "" = the variant's `art`, else one of landscape blobs tiles waves
{{icon "menu"}}                                         (existing) see the list in `icons` (generate_gfx.go)
{{$h := hrsp 1 5 "08:00" "18:00"}}{{$h.days}} {{$h.time}}   opening hours in two parts, {days, time}; `hrs` with the same arguments is the two parts in one line
{{$w := lastword .L.hero_title}}{{$w.head}}<em>{{$w.last}}</em>{{$w.end}}   {head, last, end}: the line before its last word (with the space), the last word, and the . ! ? … » " ) it ends with; one word = empty head, empty line = three empty parts
{{price .price}}                                         like `money`, but a sum under 30 of the country's units keeps its pence or cents, to the nearest 5: "£4.80", "4,80 €"; `money` rounds to whole units
```

`hl` languages: `sh`, `json`, `yaml` (also `toml`, `ini`), `out` (numbers only), anything else is just escaped. Put the result inside
`<pre><code>...</code></pre>`; the text with the tags taken off is the input.
`art` subjects: the old `landscape blobs tiles waves` and `topo stars botanical still poster ui parcel checker staff`.
A drawing that is bigger than the part of the page it sits in is clipped by its CSS, so size it with CSS (`width:100%`).
Icons added for the mock-ups: menu close chev chevd back search plus minus grid chart checkc key face wheat basket cup moon nouser tag
link arrowc sparkle external cake (and the 28 old ones).

The interpreter has no `and`, `or` or pipes; use nested `if`. Numbers in templates are whole numbers.

## 5. Shared pages

`privacy` and `terms` (a `.L.<name>` list of `{h, p}`) are a text column with a contents box (`toc_h`, new key in all 13 shared texts),
`404` has a big code and a picture, `contactmap` and `contactform` are for contact pages. Their CSS is in `base_pages`.

## 6. Class names of the shared part

Class names are global to the site (a kind's CSS and the shared pages `privacy`, `terms`, `404`, `contact` are one stylesheet), so a
kind must not declare its own class with a name below, or the shared page would pick up the kind's rules (or the kind the shared
ones). These are taken, written here without the underscores that the generator renames per site:

* chrome: `hd hdin logo mark lg lgt nav mn mnb mnl ft ftin ftcol ftnav`
* components: `wrap btn ghost card pill grid tbl mu sub code ic art`
* shared pages: `nf nfa toc cmap cform cfs fld`
* drawings: `dchart dline darea dbars dgrid dpt dcal dc0 dc1 dc2 di0 di1 di2 di3 did dpi dmap dmb dmg dmh dml dmm dmp dmr dms dmt dpin
  dstamp dsb dsd dsm dst dhc dhf dhk dhm dhn dhp dhs`

Use a prefix of your own for the kind's classes (`bz-`, `cl-`...), not a short common word. The list is what `base.css.tmpl` and the
shared page templates use today; when you add a class to a shared template, add it here.

## 7. What the tests will check

* `TestTemplatesHaveNoWords`: no word in a template, not even "UTC" or "Name": every word is a key of `text.<lang>.json`, the
  same keys in all 13 languages (new keys: add them to every language file of the kind, in `ui` or in each variant).
* Exactly one `h1`, one `title`; every element closed; every local link exists; no id used twice.
* `<form>` and `<script>` only in login, files, business, club, portfolio; no `action`, `method`, external URL, `@import`, storage, `fetch`.
  No forms to subscribe, no copy buttons: use `<details>` for menus and accordions.
* business, club and portfolio: no run of digits like a phone number (`\d[\d ()-]{6,}\d`) outside the phone.
* No `datetime` in the future; no word that gives away what the site is for (see `internal/decoy/check.go`, all languages).
* Two sites of a kind: no file the same byte for byte and no run of 200 bytes of markup/CSS in common (class and id names are renamed
  per seed, white space is varied). A static SVG or a long literal in your CSS that is the same on every site is where this fails:
  a drawing longer than ~150 bytes must take numbers from the rng. Keep CSS literals short; write long values as variables.
* Size at most 600 KiB, aim for 250 KB. Text labels of numbers are written without agreement ("Members: 42").
* `TestStylesEmitTokens` builds every kind in its new style (even while `legacy` is on) and checks the scheme, the tokens and the markup.
