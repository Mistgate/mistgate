#!/usr/bin/env python3
# The overlay sources are kept as .go.txt so the repository build (go build ./...) does not compile them as a package.
"""Write the three `go build -overlay` files used by the profiling pass (the Mistgate checkout itself is never changed).
usage: mkoverlay.py MISTGATE_SRC PATCHED_DIR OUT_DIR
  MISTGATE_SRC  checkout of the v0.1.32 tag
  PATCHED_DIR   holds panel.go and server.go, i.e. internal/panel/app/panel.go and internal/panel/httpserver/server.go with
                ../mistgate-bench-variants.diff applied (only the 'nolimit-nocache' overlay needs them)
Then, for example:  cd MISTGATE_SRC && go build -overlay OUT_DIR/overlay_panel.json -o mistgate-prof ./cmd/mistgate"""
import json, os, sys

src, patched, out = (os.path.abspath(a) for a in sys.argv[1:4])
here = os.path.dirname(os.path.abspath(__file__))
panel_pprof = {os.path.join(src, "cmd", "mistgate", "zz_bench_pprof.go"): os.path.join(here, "pprof_panel.go.txt")}
overlays = {
    "panel": panel_pprof,
    "node": {os.path.join(src, "cmd", "mistgate-node", "zz_bench_pprof.go"): os.path.join(here, "pprof_node.go.txt")},
    "nolimit-nocache": {**panel_pprof,
                        os.path.join(src, "internal", "panel", "app", "panel.go"): os.path.join(patched, "panel.go"),
                        os.path.join(src, "internal", "panel", "httpserver", "server.go"): os.path.join(patched, "server.go")},
}
os.makedirs(out, exist_ok=True)
for name, replace in overlays.items():
    with open(os.path.join(out, "overlay_%s.json" % name), "w") as f:
        json.dump({"Replace": replace}, f, indent=1)
print("ok")
