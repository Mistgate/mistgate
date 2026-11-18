import type { WizardDemo } from "./relay-wizard";

/**
 * Development only: what a scene of the dev kit starts the Ways in tab with, as if a click had brought it there (the editor of a
 * server's port or method open, the wizard at a step with its choice and its run). The real page never sets it.
 */
export const waysDemo: { current?: { edit?: "port" | "mode"; wizard?: WizardDemo } } = {};
