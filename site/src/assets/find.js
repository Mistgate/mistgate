// The search matcher, kept apart from app.js so a test can run it (build.mjs inlines this file into app.js at /*@find*/).
// Plain substring matching on a normalised text: a Chinese query needs no word splitting, "节点" finds "添加节点",
// because the index holds the readable text and `includes` does not care where a word starts. NFKC folds full-width
// letters and digits (ＡＢＣ１) and compatibility forms to what is typed on a normal keyboard.
const norm = (s) => s.normalize("NFKC").toLowerCase().replace(/ё/g, "е");
const tool = (x) => x.replace(/_(plan|apply)$/, "");
const find = (data, text) => {
  const whole = norm(text).trim(), terms = whole.split(/\s+/).filter(Boolean), hits = [];
  if (!terms.length) return hits;
  for (const p of data) {
    const t = norm(p.t), all = `${t} ${norm(p.d)} ${norm(p.g)}`;
    if (terms.every((x) => all.includes(x))) hits.push({ s: terms.reduce((n, x) => n + (t.startsWith(x) ? 5 : t.includes(x) ? 4 : 1), 0), label: p.t, group: p.g, url: p.u });
    for (const [h, id] of p.h) if (terms.every((x) => norm(h).includes(x))) hits.push({ s: 2.5, label: h, sub: p.t, group: p.g, url: `${p.u}#${id}` });
    // names in code; an MCP change tool is documented once as `node_install`, so `node_install_plan` finds it too.
    // Where a name is defined (a table row, a heading) ranks above a mention.
    for (const [c, id, def] of p.k ?? []) {
      const n = norm(c), has = (x) => n.includes(x) || n.includes(tool(x));
      if (terms.every(has)) hits.push({ s: (n === whole || n === tool(whole) ? 8 : n.startsWith(tool(terms[0])) ? 2.2 : 1.5) + (def ? 0.5 : 0), label: c, code: true, sub: p.t, group: p.g, url: id ? `${p.u}#${id}` : p.u });
    }
  }
  return hits.sort((a, b) => b.s - a.s).slice(0, 8);
};
