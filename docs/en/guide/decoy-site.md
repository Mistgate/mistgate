---
title: Your own decoy site
description: Why to replace the built-in decoy page with a small site of your own, what a good one looks like, how to install it with --decoy-dir, and a ready prompt that has an AI agent invent a unique site.
---

The public address of the panel shows a decoy site to everyone who is not the admin, a subscription client or a node agent. This page is about replacing the built-in page with a small site of your own, so that the address looks like any other small site. The flag and the serving rules are in [Install the panel](../getting-started/install-panel.md#5-the-decoy-site); [Security](../operations/security.md#the-decoy-site-and-the-hidden-admin) explains how the decoy hides the admin.

## Why your own

Without `--decoy-dir` the panel shows a built-in "Coming soon" page. Every installation ships the same page, so anyone who has seen it once can recognise it, and a prober can tell that the address belongs to a Mistgate panel. A site of your own, with its own text and look, makes the address look like an ordinary small site that nobody has a reason to look at twice.

Nodes are a separate matter. Today a node serves one fixed built-in site (see [Masquerade](hysteria2.md#masquerade) in the Hysteria2 page), the same on every installation. A custom node site is **Planned**: the panel will push the same bundle of files to the nodes. It is not available yet, and `--decoy-dir` only changes what the panel serves.

## What a good decoy looks like

- **A few static pages**, three to five, on a plausible and boring topic that fits the domain name: a small local business or a hobby project. A home page, an "about" page, a page with services or prices, a contact page with an address and opening hours.
- **Text written for this site.** Do not copy a template or text found elsewhere: a search engine can find copied text, and then the site stands out. No "lorem ipsum", no "under construction".
- **Everything local and small.** No external scripts, fonts, CDNs, analytics, maps, embeds or social buttons. The whole site is well under 2 MB; a handful of pages with one style sheet is a few tens of kilobytes.
- **Nothing that needs a backend.** No forms, no logins, no contact form, no shop. Give an email address instead.
- **Light images.** Small local SVG drawings or optimised files, each a few tens of kilobytes at most.
- **A realistic `404.html`** in the same style as the site. The panel answers every unknown path with it, so a visitor who guesses a wrong address sees what a small site would show. Add a `429.html` the same way if you like.
- **`robots.txt` only if you want one.** Without it the panel serves the built-in file that turns away known AI crawlers.
- **No giveaway words.** Nothing about VPN, proxy, tunnels, Mistgate or a panel, on any page.
- **A language that fits the domain's country.** A `.de` domain gets German text, a `.fr` domain French.
- **Plausible but invented contact details.** Use a made-up business, street and name. Never use a real person's data or a real phone number: leave the phone out, or use a number from a range reserved for fiction.

A site copied from a free template, or one that matches the sites of other installations, defeats the purpose. The easiest way to get a unique one is to ask an AI agent, as shown below.

## Install it

The panel only reads the directory. Put the site in a directory outside `/home` and `/root` (the systemd unit in [Install the panel](../getting-started/install-panel.md#7-run-it-under-systemd) hides them from the service), for example `/srv/mistgate-decoy`:

```sh
mkdir -p /srv/mistgate-decoy
cp -r ./my-site/. /srv/mistgate-decoy/
find /srv/mistgate-decoy -type d -exec chmod 0755 {} +
find /srv/mistgate-decoy -type f -exec chmod 0644 {} +
```

The files only need to be readable by the user the panel runs as; with the documented unit that is root. Then:

1. Add `--decoy-dir /srv/mistgate-decoy` to `ExecStart` in `/etc/systemd/system/mistgate.service` (or set `MISTGATE_DECOY_DIR`; see [Configuration](../reference/configuration.md)).
2. Reload and restart: `systemctl daemon-reload && systemctl restart mistgate`.
3. Check the result from your computer. The home page and a random path must answer as your site, and the admin must still work:

   ```sh
   curl -si https://panel.example.com/ | head -n 20
   curl -si https://panel.example.com/no-such-page-7f3a | head -n 20
   curl -si https://panel.example.com/<secret>/ | head -n 5
   ```

   The first answers `200` with your home page, the second `404` with your `404.html`, and the third `200` with the admin page (use your own admin URL; in the secret host and separate listener modes open it the way you normally do). Open the home page in a browser too: the pages, the style sheet and the images must all load.

To update the site, replace the files. The panel reads the directory on every request, so a change shows at once and needs no restart; a restart is only needed when you add or change the `--decoy-dir` flag itself.

## Let an AI agent make it

An agent that can write files can invent a whole site in a few minutes. The prompt below makes it choose a random topic and a business name that fits your domain, write every text from scratch, and produce plain HTML and one style sheet. Copy the block, fill in the three lines under "Fill in" and send it to your agent (Claude Code, Codex CLI or any agent that writes files).

- `DOMAIN` is the domain of the panel.
- `LANGUAGE` is the language of the site's text.
- `DIR` is a new or empty directory. If the agent runs on your computer, use a local folder and copy it to the server yourself (`scp -r`); if the agent works on the server over SSH, use `/srv/mistgate-decoy`.

```text
You are building a small static web site for a small business or a hobby project. It will be
the public face of a web server, so it must look like an ordinary, quiet, believable site.
Make it from scratch; it must not look like any template or like any other site.

== Fill in ==
DOMAIN:    example.com          # the site's domain name; the business name must fit it
LANGUAGE:  English              # the language of all the texts (it should fit DOMAIN's country)
DIR:       /srv/mistgate-decoy  # a new or empty directory for the files

== Subject ==
Choose the subject yourself, at random. It is a small local business or a hobby project that
fits DOMAIN. First write down 30 different ideas, then pick one by a random number (for
example "shuf -i 1-30 -n 1" if you have a shell); do not simply take the first idea. The
examples below only show the range: piano tuner, bakery in a small town, translation bureau,
bicycle repair, beekeeper, tailor, bookbinder, aquarium shop, chess club, ceramics studio,
maths tutoring, tea shop, locksmith, photo studio, guitar lessons. Invent your own instead of
taking one of them as it is. Choose a business name that is consistent with DOMAIN (derived
from it, or a name that plausibly belongs to it), and a made-up town or district in the
country that DOMAIN belongs to.

== Content ==
1. Write every text yourself, from scratch, in LANGUAGE: natural, specific and varied. No
   lorem ipsum, no placeholders, no "coming soon" or "under construction", no text copied from
   anywhere. Give the business a small character: a short history, a few services or products
   with invented prices, opening hours, a few honest details.
2. Pages: 3 to 5 (for example home, about, services or prices, contact, and perhaps a short
   news or FAQ page), plus 404.html in the same style, with a short friendly message and a
   link home.
3. Contact details are fictional: an invented street and number, an invented person's name
   (not a known one), an email at DOMAIN such as info@DOMAIN. No real person's data, no real
   company, no tax or registration numbers. A phone number only if it is from a range reserved
   for fiction (for example US 555-0100 to 555-0199, UK 020 7946 0xxx); otherwise leave it out.
4. Never mention VPN, proxy, tunnels, servers, hosting, a panel or Mistgate, and say nothing
   technical about the site itself.

== Technical rules ==
1. Plain HTML5 and ONE style sheet, /style.css. No JavaScript. No forms, logins or anything
   that needs a backend.
2. No external resources at all: no CDNs, web fonts (use a system font stack), analytics,
   maps, embeds, iframes, social buttons, remote images, or links to other sites. Every URL
   in the files is local.
3. Layout: each page in its own folder with an index.html (/index.html, /about/index.html,
   /contact/index.html, ...), and 404.html in the root. Write every link and the style sheet
   path as an absolute path from the root (/style.css, /about/), because 404.html is shown at
   any address.
4. Images: few, and drawn by you as small local SVG files (or none). No stock photos. Each
   file under 50 KB. A small /favicon.svg is welcome.
5. Every page has lang, charset, a viewport meta, a <title> and a meta description that fit
   the page. Use proper headings and alt text.
6. Total size of all files under 500 KB.
7. Do not copy a well-known template or framework look. Choose your own layout (header and
   navigation style, grid, spacing), your own colour palette and your own mix of system fonts,
   so that the site is visibly different from the usual ones. It must work on a phone.
8. Do not create robots.txt or anything whose name starts with a dot.

== Safety ==
1. Write files only inside DIR, and create only DIR and its sub-folders. If DIR already
   exists and is not empty, stop and ask me.
2. Touch nothing else: no other directories, no services or configuration, no packages, no
   network requests, no commands except the ones that write and check these files.

== When done ==
1. Check your work: every page links to existing local files only, there is no "http://" or
   "https://" in any file except the SVG namespace, and none of the words listed under
   Content, rule 4 appears.
2. Print the list of files with their sizes and the total size (for example
   "find DIR -type f -exec ls -l {} +" and "du -sb DIR"), then one line with the subject and
   the business name you chose. Nothing else.
```

When the agent is done, [install](#install-it) the directory. If an agent installs the panel for you, the [AI agent guide](../getting-started/ai-agents.md) already has this as an optional step.
