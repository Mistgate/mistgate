// Development only: real sites of the generator (internal/decoy/sitegen) as the panel's preview sends them, one document each,
// for the "From a template" window. The files are written by `go run ./tools/decoyfixtures` (see dev-kit-fixtures/README.md) and
// read raw: this module is reached only from the dev-kit, which vite leaves out of production builds.
const docs = import.meta.glob<string>("./dev-kit-fixtures/sites/*/*.html", { query: "?raw", import: "default", eager: true });

export type RealSite = { html: string; name: string };

/** The n-th variant (1 or 2) of a kind in a country; a country without fixtures answers with the United Kingdom's, a kind without one with null. */
export function realSite(kind: string, country: string, n: number): RealSite | null {
  const v = ((Math.abs(n) + 1) % 2) + 1;
  const html = docs[`./dev-kit-fixtures/sites/${country}/${kind}-${v}.html`] ?? docs[`./dev-kit-fixtures/sites/gb/${kind}-${v}.html`];
  if (!html) return null;
  const title = /<title>([^<]*)<\/title>/i.exec(html)?.[1] ?? kind;
  return { html, name: title.replace(/&amp;/g, "&") };
}

/** The blog the window's fixed states show. */
export const demoBlog = realSite("blog", "de", 1)!;
