// D8 "The life of an alert": docs/<lang>/diagrams/alert-lifecycle.svg
import { writeFileSync, mkdirSync } from "node:fs";
import { fileURLToPath } from "node:url";
const docs = fileURLToPath(new URL("../../docs", import.meta.url)); // the repository's docs/ next to site/
import { svg, rect, text, chip, flow } from "./svgkit.mjs";

const S = {
  en: {
    title: "The life of an alert",
    desc: "An alert opens while its condition holds and counts in the badge. Mute hides it from the badge for up to 7 days; it comes back when the mute ends. It closes when the condition is gone, muted or not, reopens if the condition returns within an hour, and stays in the history for 90 days.",
    active: ["Active", "in the badge"], muted: ["Muted", "up to 7 days"], closed: ["Closed", "kept 90 days"],
    holds: "the condition holds", gone: "the condition is gone", back: "back within an hour", mute: "Mute", ends: "the mute ends",
  },
  ru: {
    title: "Жизнь алерта",
    desc: "Алерт открывается, пока выполняется его условие, и считается в значке. Заглушка убирает его из значка до 7 дней, после неё он возвращается. Алерт закрывается, когда условия больше нет, заглушён он или нет, открывается снова, если условие вернулось в течение часа, и хранится в истории 90 дней.",
    active: ["Активен", "в значке"], muted: ["Заглушён", "до 7 дней"], closed: ["Закрыт", "хранится 90 дней"],
    holds: "условие выполняется", gone: "условия больше нет", back: "вернулся в течение часа", mute: "Заглушить", ends: "заглушка кончилась",
  },
  zh: {
    title: "告警的生命周期",
    desc: "条件成立期间，告警保持打开，并计入角标。静默会让它在最长 7 天内不计入角标，静默结束后再次计入。条件消失后告警关闭，无论是否静默；条件在一小时内再次出现则重新打开；告警在历史记录中保留 90 天。",
    active: ["活动", "计入角标"], muted: ["已静默", "最多 7 天"], closed: ["已关闭", "保留 90 天"],
    holds: "条件成立", gone: "条件消失", back: "一小时内再次出现", mute: "静默", ends: "静默结束",
  },
};

const W = 720, H = 300;
const state = (tone, icon, x, y, [t, sub]) => [rect("card", x, y, 170, 60, 14), chip(tone, icon, x + 14, y + 18, 24), text("t", x + 48, y + 27, t), text("m", x + 48, y + 43, sub)];
for (const [lang, s] of Object.entries(S)) {
  const body = [
    state("rose", "bell", 190, 80, s.active),
    state("sand", "clock", 190, 210, s.muted),
    state("grey", "check", 530, 80, s.closed),
    `<circle cx="44" cy="110" r="8" fill="var(--d-muted)"/>`,
    flow("grey", [[52, 110], [190, 110]]), text("s", 121, 101, s.holds, "middle"),
    flow("grey", [[360, 100], [530, 100]]), text("s", 445, 91, s.gone, "middle"),
    flow("grey", [[530, 122], [360, 122]]), text("s", 445, 139, s.back, "middle"),
    flow("grey", [[240, 140], [240, 210]]), text("s", 232, 179, s.mute, "end"),
    flow("grey", [[310, 210], [310, 140]]), text("s", 318, 179, s.ends),
    flow("grey", [[360, 240], [615, 240], [615, 140]]), text("s", 487, 231, s.gone, "middle"),
  ];
  mkdirSync(`${docs}/${lang}/diagrams`, { recursive: true });
  writeFileSync(`${docs}/${lang}/diagrams/alert-lifecycle.svg`, svg({ w: W, h: H, title: s.title, desc: s.desc, body }));
}
console.log("D8 written");
