/**
 * The board's path: a run of dark modules in a row is one shape, its corners are round (radius half a module) where the module
 * above (below) the end of the run is light. The modules under the logo are left out.
 */
export function roundedPath(n: number, on: (x: number, y: number) => boolean): string {
  const r = 0.5;
  let d = "";
  for (let y = 0; y < n; y++) {
    let x = 0;
    while (x < n) {
      if (!on(x, y)) {
        x++;
        continue;
      }
      let e = x;
      while (e + 1 < n && on(e + 1, y)) e++;
      const x1 = e + 1;
      const tl = !on(x, y - 1), tr = !on(e, y - 1), bl = !on(x, y + 1), br = !on(e, y + 1);
      d += `M${x + (tl ? r : 0)} ${y}H${x1 - (tr ? r : 0)}${tr ? `a${r} ${r} 0 0 1 ${r} ${r}` : ""}V${y + 1 - (br ? r : 0)}${br ? `a${r} ${r} 0 0 1 ${-r} ${r}` : ""}H${x + (bl ? r : 0)}${bl ? `a${r} ${r} 0 0 1 ${-r} ${-r}` : ""}V${y + (tl ? r : 0)}${tl ? `a${r} ${r} 0 0 1 ${r} ${-r}` : ""}z`;
      x = x1;
    }
  }
  return d;
}
