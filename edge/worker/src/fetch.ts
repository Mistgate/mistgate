import { fetchWith, scheduledWith } from "./handler";
import { getPanel } from "./panel";

// The single script's entry points: src/handler.ts over the panel of dist/panel.wasm.
export const fetchPanelRequest = fetchWith(getPanel);
export const scheduledPanel = scheduledWith(getPanel);
