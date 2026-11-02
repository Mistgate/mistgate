# Videos of Mistgate

Short films of the real admin and the person's page, running on invented data (`mocks.mjs`: nodes de1, fi1, nl1, msk1, sg1;
people alice, bob, carol; documentation addresses and example.com names). Each video is a [HyperFrames](https://github.com/heygen-com/hyperframes)
composition rendered to MP4 in English, Russian and Chinese; the hero also in Persian (right to left) and Turkish.

| Video | File | What it shows |
| --- | --- | --- |
| hero | `hero.html` | the Overview, a node sg1 installed over SSH from the browser, a profile put on it, alice's page with Singapore |
| link | `link.html` | a new person, carol, her one link and QR code, her own page: one-tap add, the QR for another device, servers, devices |
| health | `health.html` | the folded alerts, one opened, the doctor's fix for an oversized journal, the check green |
| telegram | `telegram.html` | a new MTProto profile with Fake TLS, on every node and in the groups at once, alice's Telegram proxy links |

## How it is made

1. **Footage** (`record.mjs`, `takes.mjs`): headless Chrome drives the real UI from the Vite dev server with real input
   (eased mouse moves, clicks, typing). Every `/api` call is answered from `mocks.mjs`; any request that would leave the
   machine gets a 404. The page's clock (`vclock.js`: `Date`, timers, animation frames, CSS animations) moves 1/30 s per
   filmed frame, so the footage is smooth and the same on every run. Each take writes PNG frames and a `track.json`
   (cursor, clicks, named moments, rectangles for the camera) to `assets/takes/<take>-<lang>/`.
2. **Edit** (`<video>.html`, `film.js`, `film.css`): the composition plays the takes at chosen speeds, moves a camera over
   them, draws the cursor from the track, and adds one-line captions, the outro and a loop back to the first frame.
   Everything is a function of the time HyperFrames seeks to.
3. **Render** (`make.mjs`, `encode.mjs`): `hyperframes render` makes a master MP4 per language; `encode.mjs` makes the
   delivery files from it.

## Commands

Once, from the repository root:

```sh
npm ci --prefix video
pnpm --dir web install --frozen-lockfile
```

Footage. Run one Vite per `web` folder at a time. The admin (from the repository root, in its own terminal):

```sh
pnpm --dir web exec vite --host 127.0.0.1 --port 5293 --strictPort
```

```sh
cd video
for lang in en ru zh; do for take in hero-admin link-admin health-admin tg-admin; do node record.mjs $take --lang $lang; done; done
for lang in fa tr; do node record.mjs hero-admin --lang $lang; done
```

Then stop it and start the person's page instead:

```sh
pnpm --dir web exec vite -c vite.sub.config.ts --host 127.0.0.1 --port 5294 --strictPort
```

```sh
cd video
for lang in en ru zh; do for take in hero-phone link-phone tg-phone; do node record.mjs $take --lang $lang; done; done
for lang in fa tr; do node record.mjs hero-phone --lang $lang; done
```

Render and encode (the masters go to `video/out/`, the delivery files to `$VIDEO_OUT`, default `video/out/`):

```sh
cd video
node make.mjs hero en ru zh fa tr
node make.mjs link en ru zh
node make.mjs health en ru zh
node make.mjs telegram en ru zh
```

For each video and language this makes `<name>-<lang>.mp4` (H.264, yuv420p, faststart, two-pass under 8 MB),
`<name>-<lang>.webp` (animated, looping, 1280 px wide or less, under 4 MB), `<name>-poster-<lang>.webp` (the payoff frame)
and `<name>-sheet-<lang>.png` (12 frames). The MP4s in `docs/images/video/` (the site's) are 1280×720, made from the master:
`ffmpeg -i out/<name>-<lang>.master.mp4 -vf scale=1280:-2:flags=lanczos -c:v libx264 -preset slow -crf 24 -pix_fmt yuv420p -movflags +faststart -an ../docs/images/video/<name>-<lang>.mp4`;
the WebP and the poster are copied there as they are.

Stills of a composition without a render, to check a cut: `node stills.mjs hero.html en <dir> 3.5,12,24.4`.
HyperFrames' own preview works too: `npx hyperframes preview`.

## Settings

| Variable | Default | |
| --- | --- | --- |
| `CHROME` | the usual Chrome path of the platform | the Chrome the takes are filmed in |
| `ADMIN_ORIGIN`, `SUB_ORIGIN` | `http://127.0.0.1:5293`, `:5294` | the two Vite servers |
| `CDP_PORT` | `9381` | a different one per take to film several at once |
| `VIDEO_OUT` | `video/out` | where the delivery files go |

`make.mjs` runs HyperFrames with its telemetry, update check and installs switched off. HyperFrames is pinned in
`package.json`; Onest and Vazirmatn (SIL OFL, `assets/fonts/`) and the mark (`assets/brand/`) are copies of the panel's own.

To change a video: the action is in `takes.mjs` (record again), the data in `mocks.mjs`, the speed, camera and captions
in the video's HTML (render again).
