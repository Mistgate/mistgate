import { coreEn, type CoreMessages, type Messages } from "./en";
import { zh as opsZh } from "./ops";
import { zh as profilesZh } from "./profiles";
import { zh as usersZh } from "./users";
import { zh as securityZh } from "./security";
import { zh as subsZh } from "./subs";
import { zh as healthZh } from "./health";
import { zh as updatesZh } from "./updates";
import { zh as awgZh } from "./awg";
import { zh as warpZh } from "./warp";
import { zh as integrationsZh } from "./integrations";
import { zh as telegramZh } from "./telegram";
import { zh as portsZh } from "./ports";
import { zh as editionZh } from "./edition";
import { zh as decoyZh } from "./decoy";

// Simplified Chinese (zh-CN). Typed against the English shape: a missing or extra key fails `pnpm typecheck`. Plurals: one form, no "|".
// zh: filled by the translation pass. `core` is the shell, auth and navigation copy (what ru.ts keeps in its own `core`);
// until it is translated the spread shows the English text.
const core: CoreMessages = { ...coreEn };

export const zh: Messages = { ...core, ...opsZh, ...usersZh, ...securityZh, ...profilesZh, ...subsZh, ...healthZh, ...updatesZh, ...awgZh, ...warpZh, ...integrationsZh, ...telegramZh, ...portsZh, ...editionZh, ...decoyZh };
