// Renders a video in the given languages with HyperFrames and makes its delivery files (encode.mjs).
//
//   node make.mjs <name> [en ru zh]        e.g. node make.mjs hero en ru zh
//
// The masters go to out/, the delivery files to $VIDEO_OUT (default out/). The footage must be recorded first
// (record.mjs, see README.md). HyperFrames runs with its telemetry, update check and installs switched off.
import { execFileSync } from "node:child_process";
import { mkdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const here = dirname(fileURLToPath(import.meta.url));
// the payoff frame of each video (seconds) and the size caps of its MP4 and WebP (MB)
const videos = {
  hero: { poster: 24.4, mp4: 8, webp: 4 },
  link: { poster: 13.6, mp4: 8, webp: 4 },
  health: { poster: 14.2, mp4: 8, webp: 4 },
  telegram: { poster: 17.6, mp4: 8, webp: 4 },
};
const [name, ...langs] = process.argv.slice(2);
const v = videos[name];
if (!v) throw new Error(`unknown video "${name}"; known: ${Object.keys(videos).join(", ")}`);
const out = process.env.VIDEO_OUT || join(here, "out");
mkdirSync(join(here, "out"), { recursive: true });
const env = { ...process.env, HYPERFRAMES_NO_TELEMETRY: "1", HYPERFRAMES_NO_UPDATE_CHECK: "1", HYPERFRAMES_SKIP_SKILLS: "1", HYPERFRAMES_NO_AUTO_INSTALL: "1", HYPERFRAMES_NO_OPTIONAL_INSTALL: "1" };
const cli = join(here, "node_modules", "hyperframes", "dist", "cli.js");
for (const lang of langs.length ? langs : ["en"]) {
  const master = join(here, "out", `${name}-${lang}.master.mp4`);
  execFileSync(process.execPath, [cli, "render", "-c", `./${name}.html`, "-o", master, "-q", "high", "--crf", "12", "--variables", JSON.stringify({ lang }), "--quiet"], { cwd: here, env, stdio: "inherit" });
  execFileSync(process.execPath, [join(here, "encode.mjs"), master, out, name, lang, "--poster", String(v.poster), "--mp4-mb", String(v.mp4), "--webp-mb", String(v.webp), "--sheet"], { stdio: "inherit" });
}
