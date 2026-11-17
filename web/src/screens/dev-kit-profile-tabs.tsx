import type { QueryClient } from "@tanstack/react-query";
import type { Role } from "@/gen/mistgate/admin/v1/auth_pb";
import type { Frame } from "./dev-kit-profile";

// Development only. What the Servers and People tabs and the windows of the profile page add to the scenes of dev-kit-profile.tsx: the made-up
// answers of their own calls (put on nodes, take off, copy, delete) and the frames that show their states.

/** Seeds what a state of these tabs needs into the scene's query cache (and puts its calls on the shared client). */
export function seedTabs(_qc: QueryClient, _state: string, _role: Role): void {}

/** The frames of the tabs and windows: the gallery appends them to the page's own. */
export const tabFrames: Frame[] = [];
