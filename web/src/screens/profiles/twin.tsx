export type Egress = "direct" | "warp";

/** The plan of a twin (a dry run of TwinProfile): the same key the dev kit fills, so the dialog can be looked at without a server. */
export const twinPlanKey = (profileId: string, egress: Egress, version: number) => ["profiles", "twin", profileId, egress, version] as const;
