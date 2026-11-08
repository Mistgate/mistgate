// The assistant's follow-ups (assistant.proto, FOLLOW-UPS): the quiet line of a follow-up that waits in a conversation, the
// label of a turn a follow-up started, the note of one that did not run, and the marks of an answer that came while the admin
// was away ("ai.fu.*", "ai.conv.*", "ai.chat.unread"). Times are the panel's time zone. English is the source of truth.
export const en = {
  "ai.fu.pending": "I'll come back with the result at {time} (in {left})",
  "ai.fu.afterPlan": "I'll come back with the result {left} after you apply the plan",
  "ai.fu.now": "Coming back with the result…",
  "ai.fu.cancel": "Cancel",
  "ai.fu.label": "On a timer",
  "ai.fu.line": "set at {time}",
  "ai.fu.failed": "The follow-up for {time} did not run: {reason}",
  "ai.fu.missed": "The follow-up for {time} was dropped: the panel was off for more than a day after it.",
  "ai.fu.reason.interrupted": "the panel restarted while it ran.",
  "ai.fu.reason.internal": "the panel could not read it.",
  "ai.err.follow_up_not_pending": "This follow-up has already run or was cancelled.",
  "ai.conv.unread": "New answer",
  "ai.conv.followUp": "back at {time}",
  "ai.chat.unread": "{n} new answer while you were away|{n} new answers while you were away",
};

export const ru: Record<keyof typeof en, string> = {
  "ai.fu.pending": "Вернусь с результатом в {time} (через {left})",
  "ai.fu.afterPlan": "Вернусь с результатом через {left} после того, как вы примените план",
  "ai.fu.now": "Возвращаюсь с результатом…",
  "ai.fu.cancel": "Отменить",
  "ai.fu.label": "По таймеру",
  "ai.fu.line": "заведён в {time}",
  "ai.fu.failed": "Таймер на {time} не сработал: {reason}",
  "ai.fu.missed": "Таймер на {time} пропущен: панель была выключена больше суток после этого времени.",
  "ai.fu.reason.interrupted": "панель перезапускалась, пока он работал.",
  "ai.fu.reason.internal": "панель не смогла его прочитать.",
  "ai.err.follow_up_not_pending": "Этот таймер уже сработал или отменён.",
  "ai.conv.unread": "Новый ответ",
  "ai.conv.followUp": "вернётся в {time}",
  "ai.chat.unread": "{n} новый ответ, пока вас не было|{n} новых ответа, пока вас не было|{n} новых ответов, пока вас не было",
};

export const zh: Record<keyof typeof en, string> = {
  "ai.fu.pending": "我会在 {time}（{left}后）回来告诉你结果",
  "ai.fu.afterPlan": "你应用计划 {left}后，我会回来告诉你结果",
  "ai.fu.now": "正在回来告诉你结果…",
  "ai.fu.cancel": "取消",
  "ai.fu.label": "定时",
  "ai.fu.line": "设于 {time}",
  "ai.fu.failed": "{time} 的定时回访没有运行：{reason}",
  "ai.fu.missed": "{time} 的定时回访已放弃：在那之后面板关闭了一天以上。",
  "ai.fu.reason.interrupted": "运行期间面板重启了。",
  "ai.fu.reason.internal": "面板无法读取它。",
  "ai.err.follow_up_not_pending": "此定时回访已经运行或已取消。",
  "ai.conv.unread": "新回答",
  "ai.conv.followUp": "{time} 回访",
  "ai.chat.unread": "你不在时有 {n} 条新回答",
};

export const fa: Record<keyof typeof en, string> = {
  "ai.fu.pending": "ساعت {time} (تا {left} دیگر) با نتیجه برمی‌گردم",
  "ai.fu.afterPlan": "{left} پس از اینکه برنامه را اعمال کنید با نتیجه برمی‌گردم",
  "ai.fu.now": "در حال بازگشت با نتیجه…",
  "ai.fu.cancel": "لغو",
  "ai.fu.label": "زمان‌دار",
  "ai.fu.line": "تنظیم‌شده در {time}",
  "ai.fu.failed": "پیگیری ساعت {time} اجرا نشد: {reason}",
  "ai.fu.missed": "پیگیری ساعت {time} کنار گذاشته شد: پنل پس از آن بیش از یک روز خاموش بود.",
  "ai.fu.reason.interrupted": "پنل هنگام اجرای آن دوباره راه‌اندازی شد.",
  "ai.fu.reason.internal": "پنل نتوانست آن را بخواند.",
  "ai.err.follow_up_not_pending": "این پیگیری قبلاً اجرا یا لغو شده است.",
  "ai.conv.unread": "پاسخ تازه",
  "ai.conv.followUp": "بازگشت در {time}",
  "ai.chat.unread": "{n} پاسخ تازه در نبود شما",
};

export const tr: Record<keyof typeof en, string> = {
  "ai.fu.pending": "Sonuçla döneceğim: saat {time} ({left} sonra)",
  "ai.fu.afterPlan": "Planı uyguladıktan {left} sonra sonuçla döneceğim",
  "ai.fu.now": "Sonuçla dönüyorum…",
  "ai.fu.cancel": "İptal et",
  "ai.fu.label": "Zamanlayıcıyla",
  "ai.fu.line": "kurulma saati {time}",
  "ai.fu.failed": "{time} takibi çalışmadı: {reason}",
  "ai.fu.missed": "{time} takibi bırakıldı: panel o saatten sonra bir günden fazla kapalıydı.",
  "ai.fu.reason.interrupted": "çalışırken panel yeniden başladı.",
  "ai.fu.reason.internal": "panel onu okuyamadı.",
  "ai.err.follow_up_not_pending": "Bu takip zaten çalıştı ya da iptal edildi.",
  "ai.conv.unread": "Yeni yanıt",
  "ai.conv.followUp": "dönüş {time}",
  "ai.chat.unread": "Sen yokken {n} yeni yanıt",
};
