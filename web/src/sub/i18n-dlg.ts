import type { Lang } from "./types";

// dlg: words of the small windows of the redesigned page (the device menu, rename, delete, the question of a stale key, the DNS
// choice and its result, the password form). Keys are added to `ru` and every other language must have them (the type is `ru`'s).
// The user is addressed formally («вы»), as on the boards.

const ru = {
};

export type DlgDict = typeof ru;

const en: DlgDict = {
};

const zh: DlgDict = {
};

const fa: DlgDict = {
};

const tr: DlgDict = {
};

export const dlg: Record<Lang, DlgDict> = { ru, en, zh, fa, tr };
