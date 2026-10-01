// The avatar's letter: the first letter (or digit) of a name as one grapheme, upper-case for the UI language.
// A name that starts with a symbol or an emoji falls back to its first grapheme; an empty name gives "?".

const letterLike = /[\p{L}\p{N}]/u;
let segmenter: Intl.Segmenter | undefined;

export function initialOf(name: string, lang?: string): string {
  const s = name.trim();
  if (!s) return "?";
  segmenter ??= new Intl.Segmenter(undefined, { granularity: "grapheme" });
  let first = "";
  for (const { segment } of segmenter.segment(s)) {
    if (!first) first = segment;
    if (letterLike.test(segment)) return segment.toLocaleUpperCase(lang);
  }
  return first.toLocaleUpperCase(lang);
}
