// Entry points web-ops registers: /profiles, /profiles/new and /profiles/$id.
export { ProfilesScreen } from "./list";
export { ProfileEditorScreen } from "./page";
// /profiles/new keeps the old form until the wizard takes its place
export { ProfileEditorScreen as LegacyNewProfileScreen } from "./editor";
