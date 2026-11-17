import type { Lang } from "./types";

// add: words of the redesigned page. Keys are added to `ru` and every other language must have them (the type is `ru`'s).
// Functions take plain values. The user is addressed formally («вы»), as on the boards and in i18n.ts.

const ru = {
};

export type AddDict = typeof ru;

const en: AddDict = {
};

const zh: AddDict = {
};

const fa: AddDict = {
};

const tr: AddDict = {
};

export const add: Record<Lang, AddDict> = { ru, en, zh, fa, tr };
