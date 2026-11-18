// Telegram alerts card on the Integrations screen (telegram.proto): the bot the owner sets up and each admin's own chat.
// English is the source of truth: `ru` is type-checked against it. The admin speaks to people informally («ты»). The
// panel's error codes are "telegram_*" (see lib/telegram.ts); the words of the messages that reach the chat live on the
// server (internal/panel/telegram), not here.
export const en = {
  "tg.noUrl": "The panel does not know its public admin address, so messages carry no links.",

  // ---- the bot (owner)
  "tg.bot.token.ph": "123456789:AA…",
  "tg.bot.replace.hint": "The same bot keeps the linked chats; a different bot unlinks all of them.",
  "tg.bot.removed": "Bot removed",
  "tg.bot.error.unauthorized": "Telegram refused the token: it was revoked or is wrong. Paste a new one.",
  "tg.bot.error.unreachable": "The panel cannot reach Telegram right now. It keeps trying.",
  "tg.bot.error.conflict": "Another program reads this bot's messages, or a webhook is set on it. Give the panel a bot of its own.",

  // ---- your chat (every admin)
  "tg.link.preparing": "Preparing…",
  "tg.link.code.title": "Open the bot and press Start",
  "tg.link.open": "Open in Telegram",
  "tg.link.or": "Or send this to @{bot} yourself:",
  "tg.link.expires": "The code works once and expires in {time}.",
  "tg.link.expired": "The code has expired. Make a new one.",
  "tg.link.waiting": "Waiting for the bot to hear you…",
  "tg.link.again": "New code",
  "tg.link.since": "since {date}",
  "tg.link.test.sent": "Test message sent",
  "tg.link.unlinked": "Chat unlinked",

  // ---- everyone who linked (owner)
  "tg.role.owner": "owner",
  "tg.role.helper": "helper",
  "tg.role.readonly": "read only",

  // ---- refusals
  "tg.err.token": "That does not look like a bot token. Copy the whole line from @BotFather.",
  "tg.err.unauthorized": "Telegram refused the token.",
  "tg.err.unreachable": "Telegram did not answer. Try again in a moment.",
  "tg.err.blocked": "Telegram would not deliver it: open the bot and press Start (you may have blocked it).",
  "tg.err.notConfigured": "The Telegram bot is not set up.",
  "tg.err.notLinked": "No chat is linked to your account.",
  "tg.err.tooSoon": "One test every few seconds is enough. Wait a moment.",
};

export const ru: Record<keyof typeof en, string> = {
  "tg.noUrl": "Панель не знает свой публичный адрес админки, поэтому в сообщениях не будет ссылок.",

  "tg.bot.token.ph": "123456789:AA…",
  "tg.bot.replace.hint": "Тот же бот сохранит привязанные чаты; другой бот отвяжет их все.",
  "tg.bot.removed": "Бот убран",
  "tg.bot.error.unauthorized": "Telegram отклонил токен: его отозвали или он неверный. Вставь новый.",
  "tg.bot.error.unreachable": "Панель сейчас не достаёт до Telegram. Она продолжает пробовать.",
  "tg.bot.error.conflict": "Сообщения этого бота читает другая программа, либо у него задан webhook. Заведи панели отдельного бота.",

  "tg.link.preparing": "Готовлю…",
  "tg.link.code.title": "Открой бота и нажми Start",
  "tg.link.open": "Открыть в Telegram",
  "tg.link.or": "Или отправь это боту @{bot} сам:",
  "tg.link.expires": "Код сработает один раз и истечёт через {time}.",
  "tg.link.expired": "Код истёк. Сделай новый.",
  "tg.link.waiting": "Жду, пока бот тебя услышит…",
  "tg.link.again": "Новый код",
  "tg.link.since": "с {date}",
  "tg.link.test.sent": "Проверочное сообщение отправлено",
  "tg.link.unlinked": "Чат отвязан",

  "tg.role.owner": "владелец",
  "tg.role.helper": "помощник",
  "tg.role.readonly": "только чтение",

  "tg.err.token": "Это не похоже на токен бота. Скопируй всю строку от @BotFather.",
  "tg.err.unauthorized": "Telegram отклонил токен.",
  "tg.err.unreachable": "Telegram не ответил. Попробуй ещё раз чуть позже.",
  "tg.err.blocked": "Telegram не доставил сообщение: открой бота и нажми Start (возможно, ты его заблокировал).",
  "tg.err.notConfigured": "Telegram-бот не настроен.",
  "tg.err.notLinked": "К твоему аккаунту не привязан чат.",
  "tg.err.tooSoon": "Одной проверки раз в несколько секунд достаточно. Подожди немного.",
};

// Simplified Chinese has one plural form: write a message without "|" (a leftover "a|b" shows the last form).
export const zh: typeof en = {
  "tg.noUrl": "面板不知道自己的管理后台公开地址，因此消息中不带链接。",

  "tg.bot.token.ph": "123456789:AA…",
  "tg.bot.replace.hint": "换成同一个机器人会保留已绑定的聊天；换成其他机器人会解绑全部聊天。",
  "tg.bot.removed": "机器人已移除",
  "tg.bot.error.unauthorized": "Telegram 拒绝了此令牌：它已被吊销或有误。请粘贴新的令牌。",
  "tg.bot.error.unreachable": "面板暂时无法连接 Telegram，会持续重试。",
  "tg.bot.error.conflict": "有其他程序在读取此机器人的消息，或它设置了 webhook。请为面板准备一个专用机器人。",

  "tg.link.preparing": "正在准备…",
  "tg.link.code.title": "打开机器人并点击 Start",
  "tg.link.open": "在 Telegram 中打开",
  "tg.link.or": "或者自己把这段内容发给 @{bot}：",
  "tg.link.expires": "绑定码只能使用一次，{time} 后过期。",
  "tg.link.expired": "绑定码已过期。请重新生成。",
  "tg.link.waiting": "正在等待机器人收到你的消息…",
  "tg.link.again": "新绑定码",
  "tg.link.since": "自 {date}",
  "tg.link.test.sent": "测试消息已发送",
  "tg.link.unlinked": "聊天已解绑",

  "tg.role.owner": "所有者",
  "tg.role.helper": "协作者",
  "tg.role.readonly": "只读",

  "tg.err.token": "这看起来不是机器人令牌。请复制 @BotFather 发来的整行内容。",
  "tg.err.unauthorized": "Telegram 拒绝了此令牌。",
  "tg.err.unreachable": "Telegram 没有响应。请稍后再试。",
  "tg.err.blocked": "Telegram 未能送达消息：请打开机器人并点击 Start（你可能屏蔽了它）。",
  "tg.err.notConfigured": "尚未设置 Telegram 机器人。",
  "tg.err.notLinked": "你的账户尚未绑定聊天。",
  "tg.err.tooSoon": "每隔几秒测试一次就够了。请稍等。",
};

export const fa: Record<keyof typeof en, string> = {
  "tg.noUrl": "پنل آدرس عمومی مدیریت خودش را نمی‌داند، برای همین پیام‌ها لینک ندارند.",
  "tg.bot.token.ph": "123456789:AA…",
  "tg.bot.replace.hint": "همان بات چت‌های متصل را نگه می‌دارد؛ بات دیگر همهٔ آن‌ها را جدا می‌کند.",
  "tg.bot.removed": "بات حذف شد",
  "tg.bot.error.unauthorized": "Telegram توکن را رد کرد: باطل شده یا اشتباه است. توکن تازه بچسبان.",
  "tg.bot.error.unreachable": "پنل الان به Telegram نمی‌رسد. دارد دوباره تلاش می‌کند.",
  "tg.bot.error.conflict": "برنامهٔ دیگری پیام‌های این بات را می‌خواند یا برایش webhook تنظیم شده. برای پنل یک بات جدا بساز.",
  "tg.link.preparing": "در حال آماده‌سازی…",
  "tg.link.code.title": "بات را باز کن و Start را بزن",
  "tg.link.open": "باز کردن در Telegram",
  "tg.link.or": "یا خودت این را برای @{bot} بفرست:",
  "tg.link.expires": "کد فقط یک بار کار می‌کند و {time} دیگر منقضی می‌شود.",
  "tg.link.expired": "کد منقضی شد. کد تازه بساز.",
  "tg.link.waiting": "منتظر است بات پیام تو را ببیند…",
  "tg.link.again": "کد جدید",
  "tg.link.since": "از {date}",
  "tg.link.test.sent": "پیام آزمایشی فرستاده شد",
  "tg.link.unlinked": "چت جدا شد",
  "tg.role.owner": "مالک",
  "tg.role.helper": "دستیار",
  "tg.role.readonly": "فقط خواندنی",
  "tg.err.token": "این شبیه توکن بات نیست. کل خط را از \u200E@BotFather کپی کن.",
  "tg.err.unauthorized": "Telegram توکن را رد کرد.",
  "tg.err.unreachable": "Telegram جواب نداد. چند لحظهٔ دیگر دوباره تلاش کن.",
  "tg.err.blocked": "Telegram پیام را تحویل نداد: بات را باز کن و Start را بزن (شاید آن را مسدود کرده‌ای).",
  "tg.err.notConfigured": "بات Telegram تنظیم نشده است.",
  "tg.err.notLinked": "هیچ چتی به حساب تو متصل نیست.",
  "tg.err.tooSoon": "هر چند ثانیه یک آزمایش کافی است. کمی صبر کن.",
};

export const tr: Record<keyof typeof en, string> = {
  "tg.noUrl": "Panel herkese açık yönetici adresini bilmiyor, bu yüzden mesajlarda bağlantı olmaz.",
  "tg.bot.token.ph": "123456789:AA…",
  "tg.bot.replace.hint": "Aynı bot bağlı sohbetleri korur; farklı bir bot hepsinin bağlantısını kaldırır.",
  "tg.bot.removed": "Bot kaldırıldı",
  "tg.bot.error.unauthorized": "Telegram token'ı reddetti: iptal edilmiş ya da yanlış. Yenisini yapıştır.",
  "tg.bot.error.unreachable": "Panel şu an Telegram'a ulaşamıyor. Denemeye devam ediyor.",
  "tg.bot.error.conflict": "Bu botun mesajlarını başka bir program okuyor ya da üzerinde bir webhook tanımlı. Panele ona özel bir bot ver.",
  "tg.link.preparing": "Hazırlanıyor…",
  "tg.link.code.title": "Botu aç ve Başlat'a bas",
  "tg.link.open": "Telegram'da aç",
  "tg.link.or": "Ya da bunu kendin @{bot} botuna gönder:",
  "tg.link.expires": "Kod bir kez çalışır ve {time} içinde geçerliliğini yitirir.",
  "tg.link.expired": "Kodun süresi doldu. Yenisini oluştur.",
  "tg.link.waiting": "Botun seni duyması bekleniyor…",
  "tg.link.again": "Yeni kod",
  "tg.link.since": "{date} tarihinden beri",
  "tg.link.test.sent": "Test mesajı gönderildi",
  "tg.link.unlinked": "Sohbetin bağlantısı kaldırıldı",
  "tg.role.owner": "sahip",
  "tg.role.helper": "yardımcı",
  "tg.role.readonly": "salt okunur",
  "tg.err.token": "Bu bir bot token'ına benzemiyor. @BotFather'dan satırın tamamını kopyala.",
  "tg.err.unauthorized": "Telegram token'ı reddetti.",
  "tg.err.unreachable": "Telegram yanıt vermedi. Biraz sonra yeniden dene.",
  "tg.err.blocked": "Telegram mesajı iletmedi: botu aç ve Başlat'a bas (botu engellemiş olabilirsin).",
  "tg.err.notConfigured": "Telegram botu kurulu değil.",
  "tg.err.notLinked": "Hesabına bağlı bir sohbet yok.",
  "tg.err.tooSoon": "Birkaç saniyede bir test yeter. Biraz bekle.",
};
