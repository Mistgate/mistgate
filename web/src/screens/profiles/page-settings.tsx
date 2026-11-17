import type { ProfileForm } from "./page-form";
import type { TabCtx } from "./page-types";

/** The Settings tab (in the making). */
export function SettingsTab(_p: { ctx: TabCtx; form: ProfileForm; readOnly: boolean }) {
  return <div className="pf-g pp-grid" />;
}
