import badge from "../assets/mistgate-badge.svg?raw";

// The mark in the middle of a QR code is tonal grey, as the owner chose it from the board's mark: the disc and the cat in the ink of
// the code, the arch, the swirl and the mist in greys, the eye and the wink white. The built-in badge takes its colours from CSS
// variables (--mg-ink, --mg-deep, --mg-accent, --mg-paper, --mg-mist, --mg-eye); an <img> sees none of the page's, so they are
// written into the badge. The circle's clip and the mask of the gap stay as they are; the @supports block that works the tones out
// of the accent is dropped, so nothing is recomputed.
const mono = "--mg-a:#c9c9d0;--mg-ink:#0c0c0e;--mg-deep:#8a8a93;--mg-paper:#f4f4f6;--mg-mist:#fff;--mg-eye:#fff";

let src: string | undefined;

/** The tonal grey built-in badge as an image URL (a data: URL, nothing to fetch). */
export function monoBadgeSrc(): string {
  return (src ??= "data:image/svg+xml," + encodeURIComponent(badge.replace("--mg-a:var(--mg-accent,#c3b2fd)", mono).replace(/@supports \(color:oklch\(from red l c h\)\)\{.*?\}\}/s, "")));
}
