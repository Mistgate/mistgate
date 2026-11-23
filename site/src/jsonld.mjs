// schema.org JSON-LD: one <script type="application/ld+json"> per page (a data block: the CSP does not touch it).
import { execFileSync } from "node:child_process";
import { SITE, GITHUB } from "./urls.mjs";

export const WEBSITE = { "@type": "WebSite", "@id": `${SITE}/#website`, name: "Mistgate", url: `${SITE}/` };
export const ORG = { "@type": "Organization", "@id": `${SITE}/#org`, name: "Mistgate", url: `${SITE}/`, logo: `${SITE}/mistgate-badge.svg`, sameAs: [GITHUB] };

/** `<` is escaped, so no text of a page can close the script or open a comment; undefined fields are dropped by JSON. */
export const ldScript = (graph) => `<script type="application/ld+json">${JSON.stringify({ "@context": "https://schema.org", "@graph": graph }).replace(/</g, () => String.fromCharCode(92) + "u003c")}</script>`;

/** Date (W3C, as `git log %cI`) of the last commit of every file under `dir`: Map<path relative to dir, date>.
 *  One `git log` for all of them. Empty without git, outside a repository or in a shallow clone (there every file would
 *  get the date of the one commit that was fetched): the callers then write no date. */
export function gitDates(dir) {
  const git = (...a) => execFileSync("git", ["-C", dir, ...a], { encoding: "utf8", maxBuffer: 1 << 28, stdio: ["ignore", "pipe", "ignore"] });
  const dates = new Map();
  try {
    if (git("rev-parse", "--is-shallow-repository").trim() !== "false") return dates;
    let date = "";
    for (const line of git("log", "--format=@%cI", "--name-only", "--no-renames", "--relative", "--", ".").split("\n")) {
      if (line.startsWith("@")) date = line.slice(1);
      else if (line && !dates.has(line)) dates.set(line, date); // newest first: the first one seen is the last change
    }
  } catch { dates.clear(); }
  return dates;
}
/** The newest of a set of dates, or undefined. */
export const newest = (dates) => [...dates].sort((a, b) => new Date(b) - new Date(a))[0];
