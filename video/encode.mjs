// The delivery files of one rendered video, from the HyperFrames master MP4:
//   <name>-<lang>.mp4         H.264 yuv420p, faststart, two-pass to stay under --mp4-mb
//   <name>-<lang>.webp        animated, looping, ≤ 1280 wide, quality/fps stepped down until under --webp-mb
//   <name>-poster-<lang>.webp the payoff frame (--poster seconds)
//   <name>-sheet-<lang>.png   a contact sheet of 12 frames
//
//   node encode.mjs <master.mp4> <outDir> <name> <lang> --poster 24.6 [--mp4-mb 8] [--webp-mb 4] [--sheet]
import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, rmSync, statSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const [master, outDir, name, lang, ...rest] = process.argv.slice(2);
const opt = (k, d) => (rest.includes(`--${k}`) ? rest[rest.indexOf(`--${k}`) + 1] : d);
const posterAt = Number(opt("poster", "0"));
const mp4Mb = Number(opt("mp4-mb", "8"));
const webpMb = Number(opt("webp-mb", "4"));
const sheet = rest.includes("--sheet");
const ff = (...a) => execFileSync("ffmpeg", ["-v", "error", "-y", ...a], { stdio: "inherit" });
const mb = (f) => statSync(f).size / 1e6;
const duration = Number(execFileSync("ffprobe", ["-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", master]).toString());
mkdirSync(outDir, { recursive: true });
const tmp = mkdtempSync(join(tmpdir(), "mg-encode-"));

// MP4: two passes at the bitrate that fills 94% of the cap
const mp4 = join(outDir, `${name}-${lang}.mp4`);
const kbps = Math.floor((mp4Mb * 0.94 * 8000) / duration);
const x264 = ["-c:v", "libx264", "-preset", "veryslow", "-profile:v", "high", "-pix_fmt", "yuv420p", "-b:v", `${kbps}k`, "-maxrate", `${kbps * 2}k`, "-bufsize", `${kbps * 4}k`, "-an"];
const log = join(tmp, "pass");
ff("-i", master, ...x264, "-pass", "1", "-passlogfile", log, "-f", "mp4", process.platform === "win32" ? "NUL" : "/dev/null");
ff("-i", master, ...x264, "-pass", "2", "-passlogfile", log, "-movflags", "+faststart", mp4);
console.log(`${mp4}: ${mb(mp4).toFixed(2)} MB, ${duration.toFixed(1)} s, ${kbps} kbit/s`);

// WebP: 1280 wide, 15 fps; lower the quality, then the rate, until it fits
const webp = join(outDir, `${name}-${lang}.webp`);
for (const [fps, q, w] of [[15, 70, 1280], [15, 60, 1280], [15, 50, 1280], [12, 50, 1280], [12, 42, 1280], [12, 42, 1120], [10, 40, 1120], [10, 36, 960]]) {
  ff("-i", master, "-vf", `fps=${fps},scale=${w}:-2:flags=lanczos`, "-c:v", "libwebp_anim", "-lossless", "0", "-q:v", String(q), "-compression_level", "6", "-loop", "0", "-an", webp);
  console.log(`${webp}: ${mb(webp).toFixed(2)} MB at ${w} px, ${fps} fps, q ${q}`);
  if (mb(webp) <= webpMb) break;
}

// the poster: the payoff frame at full size
const poster = join(outDir, `${name}-poster-${lang}.webp`);
ff("-ss", String(posterAt), "-i", master, "-frames:v", "1", "-c:v", "libwebp", "-q:v", "88", poster);
console.log(`${poster}: ${(mb(poster) * 1000).toFixed(0)} kB`);

// the contact sheet: 12 frames evenly over the film (not the loop's last half second), 4 × 3 at 480 px
if (sheet) {
  const png = join(outDir, `${name}-sheet-${lang}.png`);
  const step = (duration - 0.9) / 11;
  const frames = Array.from({ length: 12 }, (_, i) => join(tmp, `s${i}.png`));
  frames.forEach((f, i) => ff("-ss", (0.3 + i * step).toFixed(2), "-i", master, "-frames:v", "1", "-vf", "scale=480:-2", f));
  ff(...frames.flatMap((f) => ["-i", f]), "-filter_complex", `${frames.map((_, i) => `[${i}:v]`).join("")}xstack=inputs=12:layout=${Array.from({ length: 12 }, (_, i) => `${(i % 4) * 480}_${Math.floor(i / 4) * 270}`).join("|")}`, png);
  console.log(`${png}`);
}
rmSync(tmp, { recursive: true, force: true });
