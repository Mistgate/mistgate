import type { Lang } from "./types";

// tabs: words of the redesigned page. Keys are added to `ru` and every other language must have them (the type is `ru`'s).
// Functions take plain values. The user is addressed formally («вы»), as on the boards and in i18n.ts.

const ru = {
};

export type TabsDict = typeof ru;

const en: TabsDict = {
};

const zh: TabsDict = {
};

const fa: TabsDict = {
};

const tr: TabsDict = {
};

export const tabs: Record<Lang, TabsDict> = { ru, en, zh, fa, tr };
