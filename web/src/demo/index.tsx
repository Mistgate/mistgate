import { useTheme, type Theme } from "@/lib/theme";
import { Hero } from "./hero";
import { installVirtualClock } from "./kit";
import "./hero.css";

// /dev-demo (development only): the animated hero for the README, the docs and the wiki.
// ?theme=dark|light picks the look; ?film puts the page on a virtual clock that only moves when a recorder calls
// window.__stageshow.clock.step(), so every frame of a capture is the same on every run (?fps= sets its rate, 25 by default).
const params = new URLSearchParams(window.location.search);
const film = params.has("film");
const asked = params.get("theme");
const forced: Theme | null = asked === "light" || asked === "dark" ? asked : null;

if (film) {
  installVirtualClock({ fps: Number(params.get("fps")) || 25, epoch: Date.UTC(2026, 9, 21, 9, 41) });
  document.getElementById("mg-splash")?.remove();
}
if (forced) document.documentElement.dataset.theme = forced;

export default function DemoPage() {
  const theme = useTheme();
  return (
    <div className="mgd-page" data-film={film || undefined}>
      <Hero theme={forced ?? theme} />
    </div>
  );
}
